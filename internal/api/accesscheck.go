package api

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/auth/policy"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
)

// Bounds of an access check's input.
const (
	maxCheckGroups = 50
	maxCheckLen    = 256
)

// AccessSource says which rule gave a role. The rule text is shown only to an
// instance admin running a check: expressions can name other users and groups.
type AccessSource struct {
	Kind      string `json:"kind" enum:"direct,inherited,job,instance_admin,token" doc:"direct: a rule of the project itself; inherited: a rule of an ancestor project; job: a rule of the job; instance_admin: the instance admin rule at login; token: the service token's scope."`
	Project   string `json:"project,omitempty" doc:"The project whose rule granted the role."`
	RuleIndex int    `json:"rule_index" doc:"Position of the rule in that project's or job's list, from 0; -1 when no rule applies (an instance admin or a token)."`
	Rule      string `json:"rule,omitempty" doc:"The rule's expression; present in an access check only."`
}

// MyAccessItem is the caller's role on one project or job and where it comes from.
type MyAccessItem struct {
	Project string       `json:"project,omitempty" doc:"Set for a project. A job shared only by its own rule names no project."`
	JobID   string       `json:"job_id,omitempty"`
	Name    string       `json:"name,omitempty" doc:"The job's name, for a job."`
	Role    string       `json:"role"`
	Source  AccessSource `json:"source"`
}

type myAccessOutput struct {
	Body struct {
		Items []MyAccessItem `json:"items" doc:"Projects first, ordered by path, then jobs reached only through a rule of the job."`
	}
}

type accessCheckInput struct {
	Body struct {
		Groups  []string `json:"groups,omitempty" maxItems:"50" doc:"Group names to evaluate the rules for, as the groups claim would list them."`
		Email   string   `json:"email,omitempty" maxLength:"256" doc:"An email address, for rules on user.email."`
		Project string   `json:"project,omitempty" maxLength:"600" doc:"The project to check. Needed unless job_id is given."`
		JobID   string   `json:"job_id,omitempty" maxLength:"128" doc:"A job to check: its own rules count too."`
	}
}

// AccessCheck is why an identity has, or lacks, a role.
type AccessCheck struct {
	Project string        `json:"project"`
	JobID   string        `json:"job_id,omitempty"`
	Role    string        `json:"role,omitempty" doc:"Absent when no rule grants a role."`
	Source  *AccessSource `json:"source,omitempty"`
	Rules   int           `json:"rules" doc:"How many rules were evaluated, along the project path and the job."`
	Note    string        `json:"note"`
}

type accessCheckOutput struct{ Body AccessCheck }

func registerAccessCheck(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: "get-my-access", Method: http.MethodGet, Path: "/access/me", Tags: []string{"auth"}, Security: adminSecurity,
		Summary:     "The caller's own roles and where they come from",
		Description: "Each project the caller holds a role on, and each job reached only through a rule of the job, with the kind of rule and the project it belongs to. Rule texts are not shown here.",
		Errors:      []int{http.StatusUnauthorized, http.StatusServiceUnavailable},
	}, func(ctx context.Context, _ *struct{}) (*myAccessOutput, error) {
		return getMyAccess(ctx, d)
	})
	huma.Register(api, huma.Operation{
		OperationID: "check-access", Method: http.MethodPost, Path: "/access/check", Tags: []string{"auth"}, Security: adminSecurity,
		Middlewares: huma.Middlewares{requireAdmin(api, d)},
		Summary:     "Why an identity has, or lacks, a role (instance admin)",
		Description: "Evaluates the access rules for the given groups and email with the evaluator that enforces them, and names the rule that granted the role. The instance admin rule is evaluated at login and is not part of this. The check is audited as access.check, with counts, never the groups or the email.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *accessCheckInput) (*accessCheckOutput, error) {
		return checkAccess(ctx, d, in)
	})
}

// chainRules lists the rule lists along path, root first, from the projects read.
func chainRules(byPath map[string]store.Project, path string) (lists [][]store.AccessRule, paths []string) {
	var chain []store.Project
	for p := path; p != ""; p = store.ParentPath(p) {
		if pr, ok := byPath[p]; ok {
			chain = append([]store.Project{pr}, chain...)
		}
	}
	for _, pr := range chain {
		lists = append(lists, pr.AccessRules)
		paths = append(paths, pr.Path)
	}
	return lists, paths
}

func sourceOf(g policy.Grant, target string, paths []string) *AccessSource {
	if g.Role == "" {
		return nil
	}
	if g.List >= len(paths) { // the job's own list comes after the project chain
		return &AccessSource{Kind: "job", RuleIndex: g.Index}
	}
	kind := "inherited"
	if paths[g.List] == target {
		kind = "direct"
	}
	return &AccessSource{Kind: kind, Project: paths[g.List], RuleIndex: g.Index}
}

func getMyAccess(ctx context.Context, d deps) (*myAccessOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	out := &myAccessOutput{}
	out.Body.Items = []MyAccessItem{}
	p, _ := PrincipalFrom(ctx)
	all, err := d.ds.ListProjects(ctx)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	byPath := make(map[string]store.Project, len(all))
	for _, pr := range all {
		byPath[pr.Path] = pr
	}
	switch {
	case p.InstanceAdmin:
		for _, pr := range all {
			out.Body.Items = append(out.Body.Items, MyAccessItem{Project: pr.Path, Role: store.RoleAdmin,
				Source: AccessSource{Kind: "instance_admin", RuleIndex: -1}})
		}
	case p.Token != nil:
		for _, pr := range all {
			if covers(p.Token.Project, pr.Path) {
				out.Body.Items = append(out.Body.Items, MyAccessItem{Project: pr.Path, Role: p.Token.Role,
					Source: AccessSource{Kind: "token", Project: p.Token.Project, RuleIndex: -1}})
			}
		}
	case p.Session != nil && d.policy != nil:
		id := d.identityOf(p.Session)
		for _, pr := range all { // ordered by path
			lists, paths := chainRules(byPath, pr.Path)
			g := d.policy.Explain(id, lists...)
			if g.Role == "" {
				continue
			}
			out.Body.Items = append(out.Body.Items, MyAccessItem{Project: pr.Path, Role: g.Role, Source: *sourceOf(g, pr.Path, paths)})
		}
		shared, err := d.sharedFor(ctx, p, byPath)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		out.Body.Items = append(out.Body.Items, shared...)
	}
	return out, nil
}

// sharedFor is the jobs the session user reaches only through a rule of the job.
func (d deps) sharedFor(ctx context.Context, p Principal, byPath map[string]store.Project) ([]MyAccessItem, error) {
	v, err := d.visibleScope(ctx, p)
	if err != nil {
		return nil, err
	}
	pathOf := make(map[string]string, len(byPath))
	for path, pr := range byPath {
		pathOf[pr.ID] = path
	}
	rows, err := d.ds.ListJobRules(ctx, maxJobRuleScan)
	if err != nil {
		return nil, err
	}
	id := d.identityOf(p.Session)
	var out []MyAccessItem
	for _, r := range rows {
		if v.sees(pathOf[r.ProjectID]) {
			continue
		}
		if g := d.policy.Explain(id, r.Rules); g.Role != "" {
			out = append(out, MyAccessItem{JobID: r.JobID, Name: r.Name, Role: g.Role, Source: AccessSource{Kind: "job", RuleIndex: g.Index}})
		}
	}
	return out, nil
}

func checkAccess(ctx context.Context, d deps, in *accessCheckInput) (*accessCheckOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	if d.policy == nil || d.oidc == nil { // the same guard as the enforcement path
		return nil, apiError(ctx, invalid("access rules are not in use: login is not configured", ProblemDetail{Message: "login is not configured", Location: "body"}))
	}
	b := in.Body
	if len(b.Groups) > maxCheckGroups {
		return nil, apiError(ctx, invalid("too many groups", ProblemDetail{Message: "at most 50 groups", Location: "body.groups"}))
	}
	groups := make([]string, 0, len(b.Groups))
	for _, g := range b.Groups {
		if g = strings.TrimSpace(g); g != "" {
			if len(g) > maxCheckLen {
				return nil, apiError(ctx, invalid("a group name is too long", ProblemDetail{Message: "at most 256 bytes", Location: "body.groups"}))
			}
			groups = append(groups, g)
		}
	}
	project := b.Project
	var jobRules []store.AccessRule
	jobID := b.JobID
	all, err := d.ds.ListProjects(ctx)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	if jobID != "" {
		job, err := d.ds.GetJob(ctx, jobID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		project = "" // a job is checked in its own project, whatever the body says
		for _, pr := range all {
			if pr.ID == job.ProjectID {
				project = pr.Path
			}
		}
		if project == "" {
			return nil, apiError(ctx, store.ErrNotFound)
		}
		ja, err := d.ds.GetJobAccess(ctx, jobID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		jobRules = ja.Rules
	}
	if project == "" {
		return nil, apiError(ctx, invalid("name a project or a job", ProblemDetail{Message: "project or job_id is required", Location: "body.project"}))
	}
	byPath := make(map[string]store.Project, len(all))
	for _, pr := range all {
		byPath[pr.Path] = pr
	}
	if _, ok := byPath[project]; !ok {
		return nil, apiError(ctx, store.ErrNotFound)
	}
	lists, paths := chainRules(byPath, project)
	rules := 0
	for _, l := range lists {
		rules += len(l)
	}
	if jobID != "" {
		lists = append(lists, jobRules)
		rules += len(jobRules)
	}
	id := identityFor(groups, b.Email, d.oidc.groupsClaim)
	g := d.policy.Explain(id, lists...)
	out := &accessCheckOutput{Body: AccessCheck{
		Project: project, JobID: jobID, Role: g.Role, Rules: rules,
		Note: "The identity has only the groups and the email: user.sub, user.name and every other claim are empty or missing, so a rule on them grants nothing here. The instance admin rule, evaluated at login, is not part of this check.",
	}}
	if src := sourceOf(g, project, paths); src != nil {
		if src.Kind == "job" {
			src.Rule = jobRules[g.Index].Rule
		} else {
			src.Rule = byPath[src.Project].AccessRules[g.Index].Rule
		}
		out.Body.Source = src
	}
	if err := d.auditAccessCheck(ctx, project, jobID, len(groups), b.Email != "", g.Role, out.Body.Source); err != nil {
		return nil, apiError(ctx, err)
	}
	return out, nil
}

// identityFor is the identity a check evaluates: the groups under the configured
// claim name, and the email as both user.email and claims.email.
func identityFor(groups []string, email, groupsClaim string) expr.Identity {
	list := make([]any, len(groups))
	for i, g := range groups {
		list[i] = g
	}
	claims := map[string]any{}
	if email != "" {
		claims["email"] = email
	}
	claims[groupsClaim] = list // the groups win if the claim is named like another
	sort.Strings(groups)
	return expr.Identity{Email: email, Groups: groups, Claims: claims}
}

// auditAccessCheck records the check. It keeps counts and the outcome, never the
// groups or the email: they name the people asked about.
func (d deps) auditAccessCheck(ctx context.Context, project, jobID string, groups int, emailSet bool, role string, src *AccessSource) error {
	meta, err := store.AuditFrom(ctx)
	if err != nil {
		meta = store.AuditMeta{Actor: "anonymous"}
	}
	kind := ""
	if src != nil {
		kind = src.Kind
	}
	target, targetType := project, store.AuditTargetProject
	if jobID != "" {
		target, targetType = jobID, store.AuditTargetJob
	}
	ev, err := store.NewEventAs(meta, store.ActionAccessCheck, targetType, target, project, struct {
		Groups   int    `json:"groups"`
		EmailSet bool   `json:"email_set"`
		Role     string `json:"role,omitempty"`
		Kind     string `json:"source_kind,omitempty"`
		Project  string `json:"project"`
		JobID    string `json:"job_id,omitempty"`
	}{groups, emailSet, role, kind, project, jobID}, d.now())
	if err != nil {
		return err
	}
	return d.ds.WithTx(ctx, func(tx store.Tx) error { return tx.AppendAuditEvent(ctx, ev) })
}
