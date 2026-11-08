package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/apply"
	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/store"
)

// ApplyFile is one file of a bundle: its path under the bundle root and its JSON content.
type ApplyFile struct {
	Path    string          `json:"path" required:"true" maxLength:"300" doc:"Slash-separated path under the root, such as infra/billing/project.json or infra/billing/deploy.job.json."`
	Content json.RawMessage `json:"content" required:"true" doc:"The JSON document of the file."`
}

type applyInput struct {
	Body struct {
		Source string      `json:"source" required:"true" maxLength:"80" doc:"The managing source, files:<name>. ui and git:* are refused here."`
		Prune  bool        `json:"prune,omitempty" doc:"Archive the source's jobs missing from the bundle and release its projects missing from it."`
		DryRun bool        `json:"dry_run,omitempty" doc:"Plan and validate, write nothing."`
		Adopt  bool        `json:"adopt,omitempty" doc:"Take over projects and jobs nobody manages."`
		Files  []ApplyFile `json:"files" required:"true" maxItems:"2000"`
	}
}

// ApplyChange is one line of an apply result.
type ApplyChange struct {
	Kind        string   `json:"kind" enum:"project,job"`
	Project     string   `json:"project"`
	Name        string   `json:"name,omitempty" doc:"The job name; empty for a project."`
	Action      string   `json:"action" enum:"create,update,unchanged,archive,restore,adopt,release"`
	FromVersion int      `json:"from_version,omitempty"`
	ToVersion   int      `json:"to_version,omitempty"`
	Checksum    string   `json:"checksum,omitempty"`
	Fields      []string `json:"fields,omitempty" doc:"What changed: description, access_rules, node_scope or spec."`
}

// ApplyResult is the outcome of an apply, or of its dry run.
type ApplyResult struct {
	Source  string         `json:"source"`
	DryRun  bool           `json:"dry_run"`
	Changes []ApplyChange  `json:"changes"`
	Summary map[string]int `json:"summary" doc:"Changes counted by action."`
}

type applyOutput struct{ Body ApplyResult }

// specValidator is apply.Validator over the instance: the same job checks as
// create-job, and the same rule and scope checks as the project routes.
type specValidator struct{ d deps }

func (v specValidator) CheckJob(raw json.RawMessage) (string, string, string, []apply.FieldIssue) {
	c := v.d.checkSpec(raw)
	if len(c.errors) > 0 {
		out := make([]apply.FieldIssue, len(c.errors))
		for i, is := range c.errors {
			out[i] = apply.FieldIssue{Field: is.Path, Message: is.Message}
		}
		return "", "", "", out
	}
	sum, err := engine.Checksum(raw)
	if err != nil {
		return "", "", "", []apply.FieldIssue{{Field: "$", Message: "the document cannot be hashed"}}
	}
	return c.job.Metadata.Project, c.job.Metadata.Name, sum, nil
}

func (v specValidator) CheckProject(description string, rules []store.AccessRule, scope string) []apply.FieldIssue {
	var out []apply.FieldIssue
	if len(description) > 512 {
		out = append(out, apply.FieldIssue{Field: "description", Message: "is longer than 512 bytes"})
	}
	if len(rules) > store.MaxAccessRules {
		out = append(out, apply.FieldIssue{Field: "access_rules", Message: fmt.Sprintf("at most %d rules", store.MaxAccessRules)})
		return out
	}
	for i, r := range rules {
		if store.RoleRank(r.Role) > store.RoleRank(store.RoleAdmin) || store.RoleRank(r.Role) == 0 {
			out = append(out, apply.FieldIssue{Field: fmt.Sprintf("access_rules[%d].role", i), Message: "is not a role that can be granted"})
		}
		if r.Rule == "" || len(r.Rule) > store.MaxAccessRuleBytes {
			out = append(out, apply.FieldIssue{Field: fmt.Sprintf("access_rules[%d].rule", i), Message: fmt.Sprintf("must be 1 to %d bytes", store.MaxAccessRuleBytes)})
		}
	}
	if v.d.policy != nil {
		for _, fe := range v.d.policy.Validate(rules) {
			out = append(out, apply.FieldIssue{Field: fmt.Sprintf("access_rules[%d].rule", fe.Index), Message: fe.Message})
		}
	}
	if len(scope) > store.MaxNodeScopeBytes {
		out = append(out, apply.FieldIssue{Field: "node_scope", Message: fmt.Sprintf("is longer than %d bytes", store.MaxNodeScopeBytes)})
	} else if scope != "" && v.d.nodeScopeEnv != nil {
		if _, err := v.d.nodeScopeEnv.CompileRule(scope); err != nil {
			out = append(out, apply.FieldIssue{Field: "node_scope", Message: err.Error()})
		}
	}
	return out
}

func issueDetails(files []apply.File, issues []apply.Issue) []ProblemDetail {
	out := make([]ProblemDetail, 0, len(issues))
	for _, is := range issues {
		loc := "body"
		if is.File >= 0 && is.File < len(files) {
			loc = fmt.Sprintf("body.files[%d]", is.File)
			if is.Field != "" {
				loc += ".content." + is.Field
			}
		}
		msg := is.Message
		if is.Path != "" {
			msg = is.Path + ": " + msg
		}
		out = append(out, ProblemDetail{Message: msg, Location: loc})
	}
	return out
}

// requireChange checks the caller's role for one change (ADR-0069): editor for
// jobs and unchanged projects, admin for changing a project, admin on the parent
// (instance admin for a root) for creating one.
func (d deps) requireChange(ctx context.Context, c apply.Change) error {
	switch {
	case c.Kind == apply.ChangeJob:
		return d.require(ctx, c.Project, "", store.RoleEditor)
	case c.Action == apply.ActionCreate:
		return d.requireToCreate(ctx, c.Project)
	case c.Action == apply.ActionUnchanged:
		return d.require(ctx, c.Project, "", store.RoleEditor)
	default:
		return d.require(ctx, c.Project, "", store.RoleAdmin)
	}
}

func toApplyChanges(cs []apply.Change) []ApplyChange {
	out := make([]ApplyChange, len(cs))
	for i, c := range cs {
		out[i] = ApplyChange{Kind: c.Kind, Project: c.Project, Name: c.Name, Action: c.Action, FromVersion: c.FromVersion,
			ToVersion: c.ToVersion, Checksum: c.Checksum, Fields: c.Fields}
	}
	return out
}

func applyBundle(ctx context.Context, d deps, in *applyInput) (*applyOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	b := in.Body
	if !apply.ValidSource(b.Source) {
		return nil, apiError(ctx, invalid("the source is invalid", ProblemDetail{
			Message: "must be files:<name> with letters, digits, . _ - (ui and git:* cannot be used here)", Location: "body.source"}))
	}
	if b.Prune && len(b.Files) == 0 {
		return nil, apiError(ctx, invalid("prune needs files", ProblemDetail{
			Message: "an empty bundle with prune would archive everything the source owns", Location: "body.files"}))
	}
	files := make([]apply.File, len(b.Files))
	for i, f := range b.Files {
		files[i] = apply.File{Path: f.Path, Content: f.Content}
	}
	bundle, issues := apply.Parse(files, specValidator{d})
	if len(issues) > 0 {
		return nil, apiError(ctx, invalid("the bundle is invalid", issueDetails(files, issues)...))
	}
	paths := touchedProjects(bundle)
	st, err := d.ds.ApplyState(ctx, b.Source, paths)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	plan, issues := apply.Compute(bundle, st, apply.Options{Source: b.Source, Prune: b.Prune, Adopt: b.Adopt}, func(i int) string { return files[i].Path })
	if len(issues) > 0 {
		// A problem about a project says it exists, who owns it or what it holds:
		// the caller must hold a role there before it learns that (ADR-0064).
		for _, is := range issues {
			if is.Project == "" {
				continue
			}
			if err := d.require(ctx, is.Project, "", store.RoleEditor); err != nil {
				return nil, apiError(ctx, err)
			}
		}
		return nil, apiError(ctx, invalid("the bundle cannot be applied", issueDetails(files, issues)...))
	}
	for _, c := range plan.Changes {
		if err := d.requireChange(ctx, c); err != nil {
			return nil, apiError(ctx, err)
		}
	}
	if !b.DryRun && len(plan.Ops) > 0 {
		if err := d.ds.ApplyPlan(ctx, plan.Ops, d.now()); err != nil {
			var me *store.ManagedError
			if errors.Is(err, store.ErrStale) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) || errors.As(err, &me) {
				return nil, apiError(ctx, fmt.Errorf("a project or job changed while the bundle was applied; nothing was written, retry: %w", store.ErrStale))
			}
			return nil, apiError(ctx, err)
		}
		d.grants.reset()
	}
	return &applyOutput{Body: ApplyResult{Source: b.Source, DryRun: b.DryRun, Changes: toApplyChanges(plan.Changes), Summary: plan.Summary()}}, nil
}

// touchedProjects lists the projects whose jobs the plan needs to see.
func touchedProjects(b apply.Bundle) []string {
	seen := map[string]bool{}
	for _, p := range b.Projects {
		seen[p.Path] = true
	}
	for _, j := range b.Jobs {
		seen[j.Project] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

type exportInput struct {
	Project string `query:"project" required:"true" maxLength:"600" doc:"The project to export, with every project below it."`
}

// ExportBundle is the files of an export, in the shape apply takes.
type ExportBundle struct {
	Files []ApplyFile `json:"files"`
}

type exportOutput struct{ Body ExportBundle }

func exportProjects(ctx context.Context, d deps, in *exportInput) (*exportOutput, error) {
	if err := d.ready(); err != nil {
		return nil, apiError(ctx, err)
	}
	if err := store.ValidateProjectPath(in.Project); err != nil {
		return nil, apiError(ctx, invalid("the project path is invalid", ProblemDetail{Message: pathProblem(in.Project), Location: "query.project"}))
	}
	if err := d.require(ctx, in.Project, "", store.RoleAdmin); err != nil {
		return nil, apiError(ctx, err)
	}
	projects, err := d.ds.ListProjectSubtree(ctx, in.Project)
	if err != nil {
		return nil, apiError(ctx, err)
	}
	if len(projects) == 0 {
		return nil, apiError(ctx, store.ErrNotFound)
	}
	var files []ApplyFile
	var unsafe []string
	folded := map[string]bool{} // names that differ only by case collide on some file systems
	for _, p := range projects {
		pf, err := projectFile(p)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		files = append(files, ApplyFile{Path: p.Path + "/" + apply.ProjectFile, Content: pf})
		cursor := ""
		for {
			page, err := d.ds.ListJobs(ctx, p.ID, cursor, 200)
			if err != nil {
				return nil, apiError(ctx, err)
			}
			for _, j := range page.Jobs {
				fold := p.Path + "/" + strings.ToLower(j.Name)
				if !safeFileStem(j.Name) || folded[fold] {
					unsafe = append(unsafe, p.Path+"/"+strconv.Quote(j.Name))
					continue
				}
				folded[fold] = true
				ver, err := d.ds.GetJobVersionByNumber(ctx, j.ID, j.CurrentVersion)
				if err != nil {
					return nil, apiError(ctx, err)
				}
				files = append(files, ApplyFile{Path: p.Path + "/" + j.Name + apply.JobSuffix, Content: ver.Spec})
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if len(files) > apply.MaxFiles {
			return nil, apiError(ctx, invalid(fmt.Sprintf("more than %d files; export a smaller project", apply.MaxFiles)))
		}
	}
	if len(unsafe) > 0 {
		return nil, apiError(ctx, invalid("some job names cannot be file names", ProblemDetail{Message: strings.Join(unsafe, ", "), Location: "query.project"}))
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return &exportOutput{Body: ExportBundle{Files: files}}, nil
}

// safeFileStem reports whether a job name survives as a file name: no path
// characters, no control characters, no leading dot (the CLI skips dot files, so
// apply --prune would archive the job).
func safeFileStem(name string) bool {
	if name == "" || name[0] == '.' || len(name)+len(apply.JobSuffix) > apply.MaxNameBytes || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func projectFile(p store.Project) (json.RawMessage, error) {
	type rule struct {
		Role string `json:"role"`
		Rule string `json:"rule"`
	}
	doc := struct {
		APIVersion  string `json:"apiVersion"`
		Kind        string `json:"kind"`
		Description string `json:"description,omitempty"`
		AccessRules []rule `json:"access_rules,omitempty"`
		NodeScope   string `json:"node_scope,omitempty"`
	}{APIVersion: apply.APIVersion, Kind: apply.KindProject, Description: p.Description, NodeScope: p.NodeScope}
	for _, r := range p.AccessRules {
		doc.AccessRules = append(doc.AccessRules, rule{r.Role, r.Rule})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encoding project file: %w", err)
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func registerApply(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: "apply", Method: http.MethodPost, Path: "/apply", Summary: "Apply job and project files",
		Tags: []string{"apply"}, Security: adminSecurity, MaxBodyBytes: 16 << 20, BodyReadTimeout: 30 * time.Second,
		Description: "Makes the projects and jobs of a source equal to the files (ADR-0069). All files are validated first and every problem is reported; the writes run in one transaction. Jobs are matched by (project, name) and compared by checksum, so unchanged jobs get no new version. Objects nobody manages need adopt. With prune, the source's jobs missing from the bundle are archived and its projects released.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *applyInput) (*applyOutput, error) { return applyBundle(ctx, d, in) })
	huma.Register(api, huma.Operation{
		OperationID: "export", Method: http.MethodGet, Path: "/export", Summary: "Export projects and jobs as files",
		Tags: []string{"apply"}, Security: adminSecurity,
		Description: "Returns the same layout apply takes, for a project and the projects below it: a project.json each, and every live job. Applying the result changes nothing once the objects it holds are managed by the source: objects nobody manages need adopt on the first apply.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *exportInput) (*exportOutput, error) { return exportProjects(ctx, d, in) })
}
