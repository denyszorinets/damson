package apply

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// fakeValidator accepts job documents of the form {"project":..,"name":..,"v":..}
// and treats the checksum as the document text.
type fakeValidator struct{}

func (fakeValidator) CheckJob(raw json.RawMessage) (string, string, string, []FieldIssue) {
	var d struct {
		Project, Name string
		Bad           bool
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", "", "", []FieldIssue{{Field: "$", Message: "bad json"}}
	}
	if d.Bad {
		return "", "", "", []FieldIssue{{Field: "spec.steps[0]", Message: "invalid step"}}
	}
	return d.Project, d.Name, string(raw), nil
}

func (fakeValidator) CheckProject(desc string, rules []store.AccessRule, scope string) []FieldIssue {
	if strings.Contains(scope, "BAD") {
		return []FieldIssue{{Field: "node_scope", Message: "does not compile"}}
	}
	return nil
}

func f(path, content string) File { return File{Path: path, Content: json.RawMessage(content)} }

func projectJSON(desc string) string {
	return fmt.Sprintf(`{"apiVersion":"damson.dev/v1","kind":"Project","description":%q}`, desc)
}

func jobJSON(project, name, v string) string {
	return fmt.Sprintf(`{"project":%q,"name":%q,"v":%q}`, project, name, v)
}

func TestParseReportsEveryIssue(t *testing.T) {
	files := []File{
		f("infra/project.json", projectJSON("ok")),
		f("infra/deploy.job.json", jobJSON("infra", "deploy", "1")),
		f("infra/wrong.job.json", jobJSON("other", "wrong", "1")),              // project mismatch
		f("infra/named.job.json", jobJSON("infra", "different", "1")),          // name mismatch
		f("infra/bad.job.json", `{"project":"infra","name":"bad","bad":true}`), // validator issue
		f("infra/../x.job.json", jobJSON("x", "x", "1")),                       // traversal
		f("/abs/x.job.json", jobJSON("x", "x", "1")),
		f("top.job.json", jobJSON("x", "top", "1")),
		f("Upper/project.json", projectJSON("x")),
		f("infra/notes.txt", `{}`),
		f("infra/extra/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project","nope":1}`),
		f("infra/scope/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project","node_scope":"BAD"}`),
		f("infra/deploy.job.json", jobJSON("infra", "deploy", "2")), // duplicate path
	}
	_, issues := Parse(files, fakeValidator{})
	wantFiles := map[int]string{
		2: "folder", 3: "file name", 4: "invalid step", 5: "empty, . or ..", 6: "relative", 7: "inside a project folder",
		8: "valid project path", 9: "not project.json", 10: "not a valid project file", 11: "does not compile", 12: "appears twice",
	}
	got := map[int]string{}
	for _, is := range issues {
		got[is.File] += is.Message + " "
	}
	for i, want := range wantFiles {
		if !strings.Contains(got[i], want) {
			t.Errorf("file %d (%s): issues %q lack %q", i, files[i].Path, got[i], want)
		}
	}
	if got[0] != "" || got[1] != "" {
		t.Errorf("good files reported: %q %q", got[0], got[1])
	}
}

func TestParseOrdersParentsFirst(t *testing.T) {
	b, issues := Parse([]File{
		f("a/b/c/project.json", projectJSON("")), f("a/project.json", projectJSON("")), f("a/b/project.json", projectJSON("")),
		f("z/project.json", projectJSON("")),
	}, fakeValidator{})
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	var got []string
	for _, p := range b.Projects {
		got = append(got, p.Path)
	}
	if strings.Join(got, ",") != "a,z,a/b,a/b/c" {
		t.Fatalf("order = %v", got)
	}
}

func TestParseLimits(t *testing.T) {
	var many []File
	for i := range MaxFiles + 1 {
		many = append(many, f(fmt.Sprintf("p/j%d.job.json", i), `{}`))
	}
	if _, issues := Parse(many, fakeValidator{}); len(issues) != 1 || issues[0].File != -1 {
		t.Fatalf("too many files: %v", issues)
	}
	big := File{Path: "p/big.job.json", Content: json.RawMessage(`"` + strings.Repeat("x", MaxFileBytes) + `"`)}
	if _, issues := Parse([]File{big}, fakeValidator{}); len(issues) != 1 || !strings.Contains(issues[0].Message, "larger") {
		t.Fatalf("big file: %v", issues)
	}
}

func state(projects []store.Project, jobs ...store.ApplyJob) store.ApplyState {
	return store.ApplyState{Projects: projects, Jobs: jobs}
}

func job(project, name, managed, sum string, bindings int) store.ApplyJob {
	return store.ApplyJob{
		Job:         store.Job{ID: "j-" + project + "-" + name, Name: name, ManagedBy: managed, CurrentVersion: 2},
		ProjectPath: project, Checksum: sum, Bindings: bindings,
	}
}

func bundle(t *testing.T, files ...File) Bundle {
	t.Helper()
	b, issues := Parse(files, fakeValidator{})
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	return b
}

func actions(p Plan) string {
	var out []string
	for _, c := range p.Changes {
		out = append(out, c.Action+":"+c.Project+"/"+c.Name)
	}
	return strings.Join(out, " ")
}

const src = "files:main"

func TestPlanCreateUpdateUnchanged(t *testing.T) {
	jsonSame := jobJSON("infra", "same", "1")
	b := bundle(t,
		f("infra/project.json", projectJSON("new desc")),
		f("infra/new.job.json", jobJSON("infra", "new", "1")),
		f("infra/same.job.json", jsonSame),
		f("infra/changed.job.json", jobJSON("infra", "changed", "2")),
	)
	st := state([]store.Project{{ID: "p1", Path: "infra", ManagedBy: src, Version: 3, Description: "old"}},
		job("infra", "same", src, jsonSame, 0), job("infra", "changed", src, "old", 0))
	plan, issues := Compute(b, st, Options{Source: src}, nil)
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	want := "update:infra/ update:infra/changed create:infra/new unchanged:infra/same"
	if got := actions(plan); got != want {
		t.Fatalf("changes = %q\nwant      %q", got, want)
	}
	if plan.Changes[1].FromVersion != 2 || plan.Changes[1].ToVersion != 3 {
		t.Fatalf("versions = %+v", plan.Changes[1])
	}
	if len(plan.Ops) != 3 { // unchanged writes nothing
		t.Fatalf("ops = %d, want 3", len(plan.Ops))
	}
	if plan.Ops[0].Kind != store.OpProjectUpdate || !plan.Ops[0].ChangedDescription || plan.Ops[0].ChangedRules {
		t.Fatalf("project op = %+v", plan.Ops[0])
	}
	if s := plan.Summary(); s[ActionCreate] != 1 || s[ActionUnchanged] != 1 || s[ActionUpdate] != 2 {
		t.Fatalf("summary = %v", s)
	}
}

func TestPlanNewProjectsNeedTheirParents(t *testing.T) {
	b := bundle(t, f("a/b/project.json", projectJSON("")), f("a/b/j.job.json", jobJSON("a/b", "j", "1")))
	if _, issues := Compute(b, state(nil), Options{Source: src}, nil); len(issues) < 1 || !strings.Contains(issues[0].Message, `parent project "a"`) {
		t.Fatalf("issues = %v", issues)
	}
	b = bundle(t, f("a/project.json", projectJSON("")), f("a/b/project.json", projectJSON("")), f("a/b/j.job.json", jobJSON("a/b", "j", "1")))
	plan, issues := Compute(b, state(nil), Options{Source: src}, nil)
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	if len(plan.Ops) != 3 || plan.Ops[0].Path != "a" || plan.Ops[1].Path != "a/b" || plan.Ops[2].Kind != store.OpJobCreate {
		t.Fatalf("ops = %+v", plan.Ops)
	}
}

func TestPlanJobNeedsItsProject(t *testing.T) {
	b := bundle(t, f("nowhere/j.job.json", jobJSON("nowhere", "j", "1")))
	if _, issues := Compute(b, state(nil), Options{Source: src}, nil); len(issues) != 1 || !strings.Contains(issues[0].Message, "does not exist") {
		t.Fatalf("issues = %v", issues)
	}
}

func TestPlanOwnership(t *testing.T) {
	b := bundle(t, f("infra/project.json", projectJSON("")), f("infra/j.job.json", jobJSON("infra", "j", "1")))
	for _, tc := range []struct {
		name    string
		projOwn string
		jobOwn  string
		adopt   bool
		want    string // substring of the first issue, "" for none
		actions string
	}{
		{"ui without adopt", "ui", "ui", false, "use --adopt", ""},
		{"ui with adopt", "ui", "ui", true, "", "adopt:infra/ adopt:infra/j"},
		{"another source", "files:other", "ui", true, "managed by files:other", ""},
		{"job of another source", src, "files:other", true, "managed by files:other", ""},
		{"git source is not ours", "git:main", "ui", true, "managed by git:main", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := state([]store.Project{{ID: "p1", Path: "infra", ManagedBy: tc.projOwn, Version: 1}}, job("infra", "j", tc.jobOwn, "different", 0))
			plan, issues := Compute(b, st, Options{Source: src, Adopt: tc.adopt}, nil)
			if tc.want == "" {
				if len(issues) != 0 || actions(plan) != tc.actions {
					t.Fatalf("issues %v, actions %q; want %q", issues, actions(plan), tc.actions)
				}
				return
			}
			if len(issues) == 0 || !strings.Contains(issues[0].Message, tc.want) {
				t.Fatalf("issues = %v; want %q", issues, tc.want)
			}
		})
	}
}

func TestPlanAdoptWithoutChangeStillWrites(t *testing.T) {
	same := jobJSON("infra", "j", "1")
	b := bundle(t, f("infra/project.json", projectJSON("")), f("infra/j.job.json", same))
	st := state([]store.Project{{ID: "p1", Path: "infra", ManagedBy: "ui", Version: 1}}, job("infra", "j", "ui", same, 0))
	plan, issues := Compute(b, st, Options{Source: src, Adopt: true}, nil)
	if len(issues) != 0 || len(plan.Ops) != 2 {
		t.Fatalf("issues %v ops %d", issues, len(plan.Ops))
	}
	if plan.Ops[1].Spec != nil || plan.Ops[1].ExpectManaged != "ui" || plan.Ops[1].Source != src {
		t.Fatalf("job op = %+v", plan.Ops[1])
	}
}

func TestPlanPrune(t *testing.T) {
	keepJSON := jobJSON("infra", "keep", "1")
	b := bundle(t, f("infra/project.json", projectJSON("")), f("infra/keep.job.json", keepJSON))
	archivedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	archived := job("infra", "oldarchived", src, "x", 0)
	archived.ArchivedAt = &archivedAt
	st := state([]store.Project{
		{ID: "p1", Path: "infra", ManagedBy: src, Version: 1},
		{ID: "p2", Path: "gone", ManagedBy: src, Version: 4, Description: "keep my text"},
		{ID: "p3", Path: "other", ManagedBy: "files:other", Version: 1},
	},
		job("infra", "keep", src, keepJSON, 0),
		job("infra", "removed", src, "x", 0),
		archived,
		job("other", "theirs", "files:other", "x", 0),
		job("infra", "uijob", "ui", "x", 0))

	plan, issues := Compute(b, st, Options{Source: src}, nil)
	if len(issues) != 0 || actions(plan) != "unchanged:infra/ unchanged:infra/keep" {
		t.Fatalf("without --prune: issues %v actions %q", issues, actions(plan))
	}
	plan, issues = Compute(b, st, Options{Source: src, Prune: true}, nil)
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	if got, want := actions(plan), "unchanged:infra/ unchanged:infra/keep archive:infra/removed release:gone/"; got != want {
		t.Fatalf("with --prune: %q, want %q", got, want)
	}
	last := plan.Ops[len(plan.Ops)-1]
	if last.Source != store.SourceUI || last.ExpectManaged != src || last.Description != "keep my text" {
		t.Fatalf("release op = %+v", last)
	}
	if plan.Ops[0].Kind != store.OpJobArchive {
		t.Fatalf("ops = %+v", plan.Ops)
	}
}

func TestPlanPruneRefusesLiveBindings(t *testing.T) {
	b := bundle(t, f("infra/project.json", projectJSON("")))
	st := state([]store.Project{{ID: "p1", Path: "infra", ManagedBy: src, Version: 1}}, job("infra", "busy", src, "x", 2))
	if _, issues := Compute(b, st, Options{Source: src, Prune: true}, nil); len(issues) != 1 || !strings.Contains(issues[0].Message, "2 enabled schedules or webhooks") {
		t.Fatalf("issues = %v", issues)
	}
}

func TestPlanRestoresArchivedJob(t *testing.T) {
	same := jobJSON("infra", "j", "1")
	b := bundle(t, f("infra/project.json", projectJSON("")), f("infra/j.job.json", same))
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	j := job("infra", "j", src, same, 0)
	j.ArchivedAt = &at
	plan, issues := Compute(b, state([]store.Project{{ID: "p1", Path: "infra", ManagedBy: src, Version: 1}}, j), Options{Source: src}, nil)
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	op := plan.Ops[len(plan.Ops)-1]
	if !op.Restore || op.Spec != nil || plan.Changes[1].Action != ActionRestore {
		t.Fatalf("restore: op %+v change %+v", op, plan.Changes[1])
	}
	j.Checksum = "other"
	plan, _ = Compute(b, state([]store.Project{{ID: "p1", Path: "infra", ManagedBy: src, Version: 1}}, j), Options{Source: src}, nil)
	if op := plan.Ops[len(plan.Ops)-1]; !op.Restore || op.Spec == nil || plan.Changes[1].ToVersion != 3 {
		t.Fatalf("restore with new spec: op %+v change %+v", op, plan.Changes[1])
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	b := bundle(t,
		f("b/project.json", projectJSON("")), f("a/project.json", projectJSON("")),
		f("a/z.job.json", jobJSON("a", "z", "1")), f("a/y.job.json", jobJSON("a", "y", "1")), f("b/x.job.json", jobJSON("b", "x", "1")))
	first, _ := Compute(b, state(nil), Options{Source: src}, nil)
	for range 5 {
		again, _ := Compute(b, state(nil), Options{Source: src}, nil)
		if actions(again) != actions(first) {
			t.Fatalf("plan changed between runs: %q vs %q", actions(again), actions(first))
		}
	}
	if got := actions(first); got != "create:a/ create:b/ create:a/y create:a/z create:b/x" {
		t.Fatalf("order = %q", got)
	}
}

func TestSourceGrammar(t *testing.T) {
	for src, ok := range map[string]bool{"files:main": true, "files:a.b-c_d": true, "ui": false, "git:main": false, "files:": false, "files:Up": false, "": false, "files:" + strings.Repeat("a", 64): false} {
		if ValidSource(src) != ok {
			t.Errorf("ValidSource(%q) = %v, want %v", src, !ok, ok)
		}
	}
}
