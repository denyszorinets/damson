package apply

import (
	"fmt"
	"sort"
	"strings"

	"github.com/denyszorinets/damson/internal/store"
)

// Actions of a Change.
const (
	ActionCreate    = "create"
	ActionUpdate    = "update"
	ActionUnchanged = "unchanged"
	ActionArchive   = "archive"
	ActionRestore   = "restore"
	ActionAdopt     = "adopt"
	ActionRelease   = "release"
)

// Kinds of a Change.
const (
	ChangeJob     = "job"
	ChangeProject = "project"
)

// Options of a plan.
type Options struct {
	// Source is the managing source, "files:<name>".
	Source string
	// Prune archives the source's jobs missing from the bundle and releases its
	// projects missing from it.
	Prune bool
	// Adopt allows taking over objects nobody manages.
	Adopt bool
}

// Change is one line of the result: what apply does, or would do, to an object.
type Change struct {
	Kind        string
	Project     string
	Name        string // empty for a project
	Action      string
	FromVersion int
	ToVersion   int
	Checksum    string
	Fields      []string
}

// Plan is the outcome of comparing a bundle with the stored state.
type Plan struct {
	Changes []Change
	Ops     []store.ApplyOp
}

// Summary counts the changes by action.
func (p Plan) Summary() map[string]int {
	out := map[string]int{}
	for _, c := range p.Changes {
		out[c.Action]++
	}
	return out
}

type planner struct {
	o      Options
	st     store.ApplyState
	byPath map[string]store.Project
	known  map[string]bool // project paths that exist after the plan
	jobs   map[string]store.ApplyJob
	issues []Issue
	plan   Plan
	// ops by phase keep the order the store needs: projects, jobs, archives, releases.
	projOps, jobOps, archOps, relOps []store.ApplyOp
}

func (p *planner) issue(file int, project, path, format string, a ...any) {
	p.issues = append(p.issues, Issue{File: file, Project: project, Path: path, Message: fmt.Sprintf(format, a...)})
}

// Compute compares b with st. It returns every conflict it finds; the plan is
// only usable when there are none.
func Compute(b Bundle, st store.ApplyState, o Options, filePath func(int) string) (Plan, []Issue) {
	p := &planner{o: o, st: st, byPath: map[string]store.Project{}, known: map[string]bool{}, jobs: map[string]store.ApplyJob{}}
	for _, pr := range st.Projects {
		p.byPath[pr.Path] = pr
		p.known[pr.Path] = true
	}
	for _, j := range st.Jobs {
		p.jobs[j.ProjectPath+"\x00"+j.Name] = j
	}
	where := func(i int) string {
		if filePath != nil && i >= 0 {
			return filePath(i)
		}
		return ""
	}
	inBundle := map[string]bool{}
	for _, pr := range b.Projects {
		inBundle[pr.Path] = true
		p.project(pr, where(pr.File))
	}
	jobSeen := map[string]bool{}
	for _, j := range b.Jobs {
		jobSeen[j.Project+"\x00"+j.Name] = true
		p.job(j, where(j.File))
	}
	if o.Prune {
		p.prune(b, inBundle, jobSeen)
	}
	if len(p.issues) > 0 {
		return Plan{}, p.issues
	}
	p.plan.Ops = append(append(append(p.projOps, p.jobOps...), p.archOps...), p.relOps...)
	return p.plan, nil
}

func (p *planner) project(pr Project, file string) {
	cur, exists := p.byPath[pr.Path]
	if !exists {
		if parent := store.ParentPath(pr.Path); parent != "" && !p.known[parent] {
			p.issue(pr.File, pr.Path, file, "the parent project %q does not exist and is not in the bundle", parent)
			return
		}
		p.known[pr.Path] = true
		p.projOps = append(p.projOps, store.ApplyOp{
			Kind: store.OpProjectCreate, Source: p.o.Source, Path: pr.Path, Description: pr.Description,
			AccessRules: pr.Rules, NodeScope: pr.NodeScope,
		})
		p.plan.Changes = append(p.plan.Changes, Change{Kind: ChangeProject, Project: pr.Path, Action: ActionCreate})
		return
	}
	action := ActionUpdate
	switch {
	case cur.ManagedBy == p.o.Source:
	case cur.ManagedBy == store.SourceUI && p.o.Adopt:
		action = ActionAdopt
	case cur.ManagedBy == store.SourceUI:
		p.issue(pr.File, pr.Path, file, "project %q exists and nobody manages it; use --adopt to take it over", pr.Path)
		return
	default:
		p.issue(pr.File, pr.Path, file, "project %q is managed by %s", pr.Path, cur.ManagedBy)
		return
	}
	var fields []string
	op := store.ApplyOp{
		Kind: store.OpProjectUpdate, Source: p.o.Source, ID: cur.ID, ExpectVersion: cur.Version, ExpectManaged: cur.ManagedBy,
		Description: pr.Description, AccessRules: pr.Rules, NodeScope: pr.NodeScope,
	}
	if cur.Description != pr.Description {
		fields, op.ChangedDescription = append(fields, "description"), true
	}
	if !sameRules(cur.AccessRules, pr.Rules) {
		fields, op.ChangedRules = append(fields, "access_rules"), true
	}
	if cur.NodeScope != pr.NodeScope {
		fields, op.ChangedScope = append(fields, "node_scope"), true
	}
	if len(fields) == 0 && action == ActionUpdate {
		p.plan.Changes = append(p.plan.Changes, Change{Kind: ChangeProject, Project: pr.Path, Action: ActionUnchanged})
		return
	}
	p.projOps = append(p.projOps, op)
	p.plan.Changes = append(p.plan.Changes, Change{Kind: ChangeProject, Project: pr.Path, Action: action, Fields: fields})
}

func sameRules(a, b []store.AccessRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (p *planner) job(j Job, file string) {
	if !p.known[j.Project] {
		p.issue(j.File, j.Project, file, "the project %q does not exist and the bundle has no %s for it", j.Project, ProjectFile)
		return
	}
	cur, exists := p.jobs[j.Project+"\x00"+j.Name]
	if !exists {
		p.jobOps = append(p.jobOps, store.ApplyOp{
			Kind: store.OpJobCreate, Source: p.o.Source, ProjectPath: j.Project, Name: j.Name, Spec: j.Spec, Checksum: j.Checksum,
		})
		p.plan.Changes = append(p.plan.Changes, Change{Kind: ChangeJob, Project: j.Project, Name: j.Name, Action: ActionCreate, ToVersion: 1, Checksum: j.Checksum})
		return
	}
	action := ActionUpdate
	switch {
	case cur.ManagedBy == p.o.Source && cur.ArchivedAt != nil:
		action = ActionRestore
	case cur.ManagedBy == p.o.Source:
	case cur.ManagedBy == store.SourceUI && p.o.Adopt:
		action = ActionAdopt
	case cur.ManagedBy == store.SourceUI:
		p.issue(j.File, j.Project, file, "job %q exists in %q and nobody manages it; use --adopt to take it over", j.Name, j.Project)
		return
	default:
		p.issue(j.File, j.Project, file, "job %q in %q is managed by %s", j.Name, j.Project, cur.ManagedBy)
		return
	}
	changed := cur.Checksum != j.Checksum
	if !changed && action == ActionUpdate {
		p.plan.Changes = append(p.plan.Changes, Change{Kind: ChangeJob, Project: j.Project, Name: j.Name, Action: ActionUnchanged,
			FromVersion: cur.CurrentVersion, ToVersion: cur.CurrentVersion, Checksum: j.Checksum})
		return
	}
	op := store.ApplyOp{
		Kind: store.OpJobUpdate, Source: p.o.Source, ID: cur.ID, ExpectCurrent: cur.CurrentVersion, ExpectManaged: cur.ManagedBy,
		Restore: action == ActionRestore,
	}
	to := cur.CurrentVersion
	var fields []string
	if changed {
		op.Spec, op.Checksum = j.Spec, j.Checksum
		to++
		fields = []string{"spec"}
	}
	p.jobOps = append(p.jobOps, op)
	p.plan.Changes = append(p.plan.Changes, Change{Kind: ChangeJob, Project: j.Project, Name: j.Name, Action: action,
		FromVersion: cur.CurrentVersion, ToVersion: to, Checksum: j.Checksum, Fields: fields})
}

func (p *planner) prune(b Bundle, inBundle map[string]bool, jobSeen map[string]bool) {
	var jobs []store.ApplyJob
	for _, j := range p.st.Jobs {
		if j.ManagedBy == p.o.Source && j.ArchivedAt == nil && !jobSeen[j.ProjectPath+"\x00"+j.Name] {
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(i, k int) bool {
		if jobs[i].ProjectPath != jobs[k].ProjectPath {
			return jobs[i].ProjectPath < jobs[k].ProjectPath
		}
		return jobs[i].Name < jobs[k].Name
	})
	for _, j := range jobs {
		if j.Bindings > 0 {
			p.issue(-1, j.ProjectPath, "", "cannot prune job %q in %q: it still has %d enabled schedules or webhooks; remove them first", j.Name, j.ProjectPath, j.Bindings)
			continue
		}
		p.archOps = append(p.archOps, store.ApplyOp{
			Kind: store.OpJobArchive, Source: p.o.Source, ID: j.ID, ExpectCurrent: j.CurrentVersion, ExpectManaged: j.ManagedBy,
		})
		p.plan.Changes = append(p.plan.Changes, Change{Kind: ChangeJob, Project: j.ProjectPath, Name: j.Name, Action: ActionArchive, FromVersion: j.CurrentVersion})
	}
	var gone []store.Project
	for _, pr := range p.st.Projects {
		if pr.ManagedBy == p.o.Source && !inBundle[pr.Path] {
			gone = append(gone, pr)
		}
	}
	// Children first, so a release never leaves a managed child under a freed parent
	// in the middle of the plan (they are independent, but the order is stable).
	sort.Slice(gone, func(i, k int) bool {
		return strings.Count(gone[i].Path, "/") > strings.Count(gone[k].Path, "/") || (strings.Count(gone[i].Path, "/") == strings.Count(gone[k].Path, "/") && gone[i].Path < gone[k].Path)
	})
	for _, pr := range gone {
		p.relOps = append(p.relOps, store.ApplyOp{
			Kind: store.OpProjectUpdate, Source: store.SourceUI, ID: pr.ID, ExpectVersion: pr.Version, ExpectManaged: pr.ManagedBy,
			Description: pr.Description, AccessRules: pr.AccessRules, NodeScope: pr.NodeScope,
		})
		p.plan.Changes = append(p.plan.Changes, Change{Kind: ChangeProject, Project: pr.Path, Action: ActionRelease})
	}
}
