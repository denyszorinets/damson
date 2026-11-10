package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
)

// grantTTL is how long a user's role on a target is reused (ARCHITECTURE 12.4):
// a rule edited elsewhere takes effect within this time on every controller.
const grantTTL = 60 * time.Second

// maxGrants bounds the grant cache; it is cleared when full.
const maxGrants = 8192

// errForbidden marks a principal that has a role on the target, but not enough.
var errForbidden = errors.New("the role does not allow this")

// grantCache remembers roles per (session, project, job) for grantTTL. It is
// in memory and per controller; the database stays the source of truth.
type grantCache struct {
	mu   sync.Mutex
	m    map[string]grantEntry
	sets map[string]roleSet
	gen  uint64 // bumped by reset: a grant computed before it is not stored
}

type grantEntry struct {
	role string
	exp  time.Time
}

func newGrantCache() *grantCache {
	return &grantCache{m: map[string]grantEntry{}, sets: map[string]roleSet{}}
}

func (c *grantCache) get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || !now.Before(e.exp) {
		delete(c.m, key)
		return "", false
	}
	return e.role, true
}

// generation is read before the database is, and passed back to put.
func (c *grantCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// put stores a grant unless reset ran since gen was read: the grant may rest on
// rules that have since changed.
func (c *grantCache) put(key, role string, now time.Time, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	if len(c.m) >= maxGrants {
		clear(c.m)
	}
	c.m[key] = grantEntry{role: role, exp: now.Add(grantTTL)}
}

// roles caches a whole project listing's roles, under its own key space.
type roleSet struct {
	roles map[string]string
	exp   time.Time
}

func (c *grantCache) getRoles(key string, now time.Time) (map[string]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.sets[key]
	if !ok || !now.Before(e.exp) {
		delete(c.sets, key)
		return nil, false
	}
	return e.roles, true
}

func (c *grantCache) putRoles(key string, roles map[string]string, now time.Time, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	if len(c.sets) >= maxGrants {
		clear(c.sets)
	}
	c.sets[key] = roleSet{roles: roles, exp: now.Add(grantTTL)}
}

// reset forgets every grant: called after a rule edit on this controller.
func (c *grantCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	clear(c.m)
	clear(c.sets)
}

// identityOf is the user a session belongs to, as access rules see them.
func (d deps) identityOf(s *store.Session) expr.Identity {
	id := expr.Identity{Sub: s.Sub, Email: s.Email, Name: s.Name}
	var claims map[string]any
	if json.Unmarshal(s.Claims, &claims) != nil {
		claims = map[string]any{}
	}
	id.Claims = claims
	if list, ok := claims[d.oidc.groupsClaim].([]any); ok {
		for _, g := range list {
			if str, ok := g.(string); ok {
				id.Groups = append(id.Groups, str)
			}
		}
	}
	return id
}

// roleOn is the role a principal holds on a project (and, when jobID is set,
// on that job): "" means none. An instance admin is admin everywhere; a service
// token holds its role where its scope covers the project; a user holds the
// highest role any matching rule grants on the job, the project or an ancestor.
func (d deps) roleOn(ctx context.Context, p Principal, project, jobID string) (string, error) {
	switch {
	case p.InstanceAdmin:
		return store.RoleAdmin, nil
	case p.Token != nil:
		if covers(p.Token.Project, project) {
			return p.Token.Role, nil
		}
		return "", nil
	case p.Session != nil:
		return d.userRole(ctx, p.Session, project, jobID)
	}
	return "", nil
}

func (d deps) userRole(ctx context.Context, s *store.Session, project, jobID string) (string, error) {
	if d.policy == nil || d.oidc == nil {
		return "", nil
	}
	key := s.ID + "|" + project + "|" + jobID
	if role, ok := d.grants.get(key, d.now()); ok {
		return role, nil
	}
	gen := d.grants.generation()
	chain, err := d.ds.ListProjectChain(ctx, project)
	if err != nil {
		return "", err
	}
	lists := make([][]store.AccessRule, 0, len(chain)+1)
	for _, p := range chain {
		lists = append(lists, p.AccessRules)
	}
	if jobID != "" {
		ja, err := d.ds.GetJobAccess(ctx, jobID)
		if err != nil {
			return "", err
		}
		lists = append(lists, ja.Rules)
	}
	role := d.policy.Role(d.identityOf(s), lists...)
	d.grants.put(key, role, d.now(), gen)
	return role, nil
}

// require checks that the principal of ctx holds at least need on the target.
// No role at all answers as a missing target (ErrNotFound), so a target the
// caller may not see cannot be told from one that does not exist; a lower role
// answers errForbidden.
func (d deps) require(ctx context.Context, project, jobID, need string) error {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return errForbidden
	}
	role, err := d.roleOn(ctx, p, project, jobID)
	if err != nil {
		return err
	}
	switch {
	case role == "":
		return store.ErrNotFound
	case store.RoleRank(role) < store.RoleRank(need):
		return errForbidden
	}
	return nil
}

// projectRoles is the principal's role on each of projects, by path; projects
// with no role are left out. It reads the rules from the projects given, so a
// full listing costs no further queries. Each project's own rules are evaluated
// once; a project's role is the highest along its path.
func (d deps) projectRoles(ctx context.Context, p Principal, projects []store.Project) map[string]string {
	out := make(map[string]string, len(projects))
	if p.Session == nil || p.InstanceAdmin {
		for _, pr := range projects {
			if role, _ := d.roleOn(ctx, p, pr.Path, ""); role != "" {
				out[pr.Path] = role
			}
		}
		return out
	}
	if d.policy == nil {
		return out
	}
	key := p.Session.ID + "|*"
	if cached, ok := d.grants.getRoles(key, d.now()); ok {
		return cached
	}
	gen := d.grants.generation()
	id := d.identityOf(p.Session)
	own := make(map[string]string, len(projects)) // role granted by the project's own rules
	for _, pr := range projects {
		own[pr.Path] = d.policy.Role(id, pr.AccessRules)
	}
	for _, pr := range projects { // projects are ordered by path, so parents come first
		best := own[pr.Path]
		if parent := store.ParentPath(pr.Path); parent != "" && store.RoleRank(out[parent]) > store.RoleRank(best) {
			best = out[parent]
		}
		if best != "" {
			out[pr.Path] = best
		}
	}
	d.grants.putRoles(key, out, d.now(), gen)
	return out
}

// Bounds of visibleRuns: it reads pages of scanPage runs, at most scanPages.
const (
	scanPage  = 200
	scanPages = 20
)

// visibleRuns is QueryRuns for a caller who may see only the projects in visible (roles by path): it reads
// pages and keeps the runs of projects the user holds a role on, until limit
// are found or the scan bound is hit (then the cursor lets the caller go on).
// A non-empty project keeps only the runs of that project and those below it.
func (d deps) visibleRuns(ctx context.Context, f store.RunFilter, visible map[string]string, project, cursor string, limit int) (store.RunPage, error) {
	limit = store.ClampLimit(limit)
	projectOfJob := map[string]string{} // job id to project path, "" when unknown
	var out store.RunPage
	for i := 0; i < scanPages; i++ {
		page, err := d.ds.QueryRuns(ctx, f, cursor, scanPage)
		if err != nil {
			return store.RunPage{}, err
		}
		for _, r := range page.Runs {
			path, seen := projectOfJob[r.JobID]
			if !seen {
				if path, err = d.projectOfJobRow(ctx, r.JobID); err != nil && !errors.Is(err, errNoParent) {
					return store.RunPage{}, err
				}
				projectOfJob[r.JobID] = path
			}
			if visible[path] == "" || !inScope(path, project) {
				continue
			}
			out.Runs = append(out.Runs, r)
			if len(out.Runs) == limit {
				out.NextCursor = r.ID
				return out, nil
			}
		}
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
	out.NextCursor = cursor // the scan position: the runs before it were all looked at
	return out, nil
}

// auditRunCancel appends the run.cancel event in the transaction that records
// the request.
func (d deps) auditRunCancel(ctx context.Context, tx store.Tx, project string, run store.Run) error {
	meta, err := store.AuditFrom(ctx)
	if err != nil {
		meta = store.AuditMeta{Actor: "anonymous"}
	}
	ev, err := store.NewEventAs(meta, store.ActionRunCancel, store.AuditTargetRun, run.ID, project, struct {
		JobID string `json:"job_id"`
	}{run.JobID}, d.now())
	if err != nil {
		return err
	}
	return tx.AppendAuditEvent(ctx, ev)
}
