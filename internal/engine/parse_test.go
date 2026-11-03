package engine

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

const testdata = "../../testdata/engine"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdata, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseValid(t *testing.T) {
	j, err := Parse(readFixture(t, "valid-linear.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(j.Spec.Steps); got != 3 {
		t.Fatalf("steps = %d", got)
	}
	if j.Spec.Steps[2].When != "always()" || !j.Spec.Steps[2].ContinueOnError {
		t.Fatalf("step 3 = %+v", j.Spec.Steps[2])
	}
}

// mutate returns the valid fixture with one replacement applied.
func mutate(t *testing.T, old, repl string) []byte {
	t.Helper()
	s := string(readFixture(t, "valid-linear.json"))
	if !strings.Contains(s, old) {
		t.Fatalf("fixture lacks %q", old)
	}
	return []byte(strings.Replace(s, old, repl, 1))
}

func TestParseInvalid(t *testing.T) {
	const installStep = `"id": "install",
        "plugin": "ssh",`
	tests := []struct {
		name     string
		doc      []byte
		wantPath string
		wantMsg  string
	}{
		{"not json", []byte(`{`), "$", "not valid JSON"},
		{"wrong apiVersion", mutate(t, `"damson.dev/v1"`, `"damson.dev/v2"`), "apiVersion", "damson.dev/v1"},
		{"wrong kind", mutate(t, `"kind": "Job"`, `"kind": "Pipeline"`), "kind", "Job"},
		{"missing name", mutate(t, `"name": "deploy-report-tool", `, ``), "metadata", "name"},
		{"unknown top field", mutate(t, `"timeout": "30m",`, `"timeout": "30m", "bogus": 1,`), "spec", "bogus"},
		{"unknown step field", mutate(t, `"id": "fetch",`, `"id": "fetch", "bogus": 1,`), "spec.steps[0]", "bogus"},
		{"bad id chars", mutate(t, `"id": "install"`, `"id": "Install_1"`), "spec.steps[1].id", "[a-z0-9-]+"},
		{"duplicate id", mutate(t, `"id": "install"`, `"id": "fetch"`), "spec.steps[1].id", `duplicate id "fetch"`},
		{"missing id", mutate(t, `"id": "install",`, ``), "spec.steps[1]", "id"},
		{"missing plugin", mutate(t, installStep, `"id": "install",`), "spec.steps[1].plugin", "required"},
		{"bad plugin ref", mutate(t, `"transfer@1"`, `"Transfer@x"`), "spec.steps[0].plugin", "name@<major>"},
		{"bad timeout", mutate(t, `"timeout": "10m"`, `"timeout": "ten"`), "spec.steps[0].timeout", "duration"},
		{"negative timeout", mutate(t, `"timeout": "10m"`, `"timeout": "-5m"`), "spec.steps[0].timeout", "positive"},
		{"bad job timeout", mutate(t, `"timeout": "30m"`, `"timeout": "soon"`), "spec.timeout", "duration"},
		{"when syntax", mutate(t, `"always()"`, `"always( &&"`), "spec.steps[2].when", "invalid expression"},
		{"when unknown var", mutate(t, `"always()"`, `"secrets.x == 1"`), "spec.steps[2].when", "invalid expression"},
		{"with expression syntax", mutate(t, `${{ inputs.version }}`, `${{ inputs. }}`), "spec.steps[0].with.key", "invalid expression"},
		{"with unterminated", mutate(t, `${{ inputs.version }}`, `${{ inputs.version`), "spec.steps[0].with.key", "unterminated"},
		{"selector syntax", mutate(t, `'report' in node.tags`, `'report' in`), "spec.targets.selector", "invalid expression"},
		{"options not object", mutate(t, `"type": "object",
      "required"`, `"type": "string",
      "required"`), "spec.options.type", "object"},
		{"options bad schema", mutate(t, `"type": "string", "pattern": "^\\d+\\.\\d+\\.\\d+$"`, `"type": "string", "pattern": "("`), "spec.options", "not a valid JSON Schema"},
		{"no steps", mutate(t, `"steps": [`, `"stepz": [`), "spec", "stepz"},
		{"empty runs_on", mutate(t, `"runs_on": { "pool": "dc1", "labels": { "arch": "amd64" } }`, `"runs_on": {}`), "spec.steps[0].runs_on", "pool or labels"},
		// Reserved for M3: parsed, but rejected with a clear error.
		{"retry without max_attempts", mutate(t, `"timeout": "10m",`, `"retry": {},`), "spec.steps[0].retry", "max_attempts"},
		{"retry max_attempts too high", mutate(t, `"timeout": "10m",`, `"retry": {"max_attempts": 101},`), "spec.steps[0].retry.max_attempts", "maximum"},
		{"retry bad initial", mutate(t, `"timeout": "10m",`, `"retry": {"max_attempts": 3, "backoff": {"initial": "soon"}},`), "spec.steps[0].retry.backoff.initial", "duration"},
		{"retry negative max", mutate(t, `"timeout": "10m",`, `"retry": {"max_attempts": 3, "backoff": {"max": "-1s"}},`), "spec.steps[0].retry.backoff.max", "positive"},
		{"retry delay over a day", mutate(t, `"timeout": "10m",`, `"retry": {"max_attempts": 3, "backoff": {"max": "25h"}},`), "spec.steps[0].retry.backoff.max", "at most"},
		{"retry max below initial", mutate(t, `"timeout": "10m",`, `"retry": {"max_attempts": 3, "backoff": {"initial": "10s", "max": "5s"}},`), "spec.steps[0].retry.backoff.max", "less than"},
		{"retry multiplier below 1", mutate(t, `"timeout": "10m",`, `"retry": {"max_attempts": 3, "backoff": {"multiplier": 0.5}},`), "spec.steps[0].retry.backoff.multiplier", "minimum"},
		{"retry multiplier above 10", mutate(t, `"timeout": "10m",`, `"retry": {"max_attempts": 3, "backoff": {"multiplier": 11}},`), "spec.steps[0].retry.backoff.multiplier", "maximum"},
		{"retry jitter above 1", mutate(t, `"timeout": "10m",`, `"retry": {"max_attempts": 3, "backoff": {"jitter": 1.5}},`), "spec.steps[0].retry.backoff.jitter", "maximum"},
		{"retry_on syntax", mutate(t, `"timeout": "10m",`, `"retry": {"max_attempts": 3, "retry_on": "error.kind =="},`), "spec.steps[0].retry.retry_on", "invalid expression"},
		{"for_each reserved", mutate(t, `"timeout": "10m",`, `"for_each": "targets",`), "spec.steps[0].for_each", "not supported yet"},
		{"catch reserved", mutate(t, `"timeout": "10m",`, `"catch": [{"id":"c","plugin":"ssh"}],`), "spec.steps[0].catch", "not supported yet"},
		{"finally reserved", mutate(t, `"timeout": "10m",`, `"finally": [{"id":"c","plugin":"ssh"}],`), "spec.steps[0].finally", "not supported yet"},
		{"concurrency reserved", mutate(t, `"timeout": "30m",`, `"timeout": "30m", "concurrency": {"key": "x", "max": 1},`), "spec.concurrency", "not supported yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.doc)
			var ve ValidationErrors
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want ValidationErrors", err)
			}
			for _, fe := range ve {
				if fe.Path == tt.wantPath && strings.Contains(fe.Message, tt.wantMsg) {
					return
				}
			}
			t.Fatalf("no error at %q containing %q in %v", tt.wantPath, tt.wantMsg, ve)
		})
	}
}

// User-supplied schemas must never trigger I/O: remote and file $refs fail
// to compile with a validation error.
func TestOptionsRefsDoNotLoad(t *testing.T) {
	for _, ref := range []string{"file:///etc/passwd", "https://example.com/schema.json", "http://127.0.0.1:1/x.json"} {
		t.Run(ref, func(t *testing.T) {
			doc := mutate(t, `"type": "string", "pattern": "^\\d+\\.\\d+\\.\\d+$"`, `"$ref": "`+ref+`"`)
			_, err := Parse(doc)
			var ve ValidationErrors
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want ValidationErrors", err)
			}
			for _, fe := range ve {
				if fe.Path == "spec.options" && strings.Contains(fe.Message, "not a valid JSON Schema") {
					return
				}
			}
			t.Fatalf("no spec.options error in %v", ve)
		})
	}
}

func TestEngineParseUsesCachedSchema(t *testing.T) {
	eng := newEngine(t, exprLimits())
	if _, err := eng.Parse(readFixture(t, "valid-linear.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Parse([]byte(`{"kind":"x"}`)); !errors.As(err, new(ValidationErrors)) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateReportsAllErrors(t *testing.T) {
	doc := mutate(t, `"id": "install"`, `"id": "fetch"`)
	doc = []byte(strings.Replace(string(doc), `"timeout": "10m"`, `"timeout": "ten"`, 1))
	_, err := Parse(doc)
	var ve ValidationErrors
	if !errors.As(err, &ve) || len(ve) != 2 {
		t.Fatalf("want 2 errors, got %v", err)
	}
}

func TestChecksum(t *testing.T) {
	a := []byte(`{"b": 1, "a": {"y": [1, 2.50], "x": "<&>"}}`)
	b := []byte("{\n  \"a\": {\"x\": \"<&>\", \"y\": [1, 2.50]},\n  \"b\": 1\n}\n")
	c := []byte(`{"b": 1, "a": {"y": [1, 2.50], "x": "other"}}`)
	sa, err := Checksum(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := Checksum(b)
	sc, _ := Checksum(c)
	if sa != sb {
		t.Fatalf("formatting/key order changed checksum: %s vs %s", sa, sb)
	}
	if sa == sc {
		t.Fatal("different content gave same checksum")
	}
	canon, _ := Canonical(a)
	if string(canon) != `{"a":{"x":"<&>","y":[1,2.50]},"b":1}` {
		t.Fatalf("canonical = %s", canon)
	}
	if _, err := Checksum([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestJobChecksumStable(t *testing.T) {
	j, err := Parse(readFixture(t, "valid-linear.json"))
	if err != nil {
		t.Fatal(err)
	}
	c1, err := j.Checksum()
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := j.Checksum()
	if c1 != c2 || len(c1) != 64 {
		t.Fatalf("checksum unstable or malformed: %q %q", c1, c2)
	}
}

// The committed schema is generated; `make gen` refreshes it.
func TestSchemaGolden(t *testing.T) {
	got, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(testdata, "job-v1.schema.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("job-v1.schema.json is stale; run `make gen`")
	}
}

func TestParseValidRetry(t *testing.T) {
	doc := mutate(t, `"timeout": "10m",`, `"retry": {"max_attempts": 5, "backoff": {"initial": "2s", "multiplier": 3, "max": "1m", "jitter": 0}, "retry_on": "error.kind == 'connection' || error.message.contains('reset')", "on_lost": false},`)
	job, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := job.Spec.Steps[0].Retry
	if r == nil || r.MaxAttempts != 5 || r.OnLost == nil || *r.OnLost || r.Backoff == nil || *r.Backoff.Jitter != 0 || *r.Backoff.Multiplier != 3 {
		t.Fatalf("retry = %+v", r)
	}
}

func TestParseValidGroup(t *testing.T) {
	job, err := Parse(stepsDoc(`[{"id":"fetch","type":"group","continue_on_error":true,"when":"success()","steps":[{"id":"a","plugin":"ssh"},{"id":"inner","type":"group","steps":[{"id":"b","plugin":"ssh"}]}]}]`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g := job.Spec.Steps[0]
	if !g.IsGroup() || len(g.Steps) != 2 || !g.ContinueOnError {
		t.Fatalf("group = %+v", g)
	}
	if s, ok := job.StepAt("fetch/inner/b"); !ok || s.ID != "b" || s.Plugin != "ssh" {
		t.Fatalf("StepAt(fetch/inner/b) = %+v, %v", s, ok)
	}
}

func TestGroupNestingLimit(t *testing.T) {
	inner := `{"id":"leaf","plugin":"ssh"}`
	for i := range 9 { // 9 nested groups: one over the limit
		inner = fmt.Sprintf(`{"id":"g%d","type":"group","steps":[%s]}`, i, inner)
	}
	_, err := Parse(stepsDoc("[" + inner + "]"))
	var ve ValidationErrors
	if !errors.As(err, &ve) || !strings.Contains(ve.Error(), "nest at most 8") {
		t.Fatalf("err = %v; want the nesting limit", err)
	}
}

// stepsDoc is a minimal job whose spec.steps is the given JSON array.
func stepsDoc(steps string) []byte {
	return []byte(`{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"g"},"spec":{"steps":` + steps + `}}`)
}

func TestParseInvalidGroups(t *testing.T) {
	tests := []struct {
		name, steps, wantPath, wantMsg string
	}{
		{"group needs steps", `[{"id":"g","type":"group"}]`, "spec.steps[0].steps", "at least one step"},
		{"group rejects plugin", `[{"id":"g","type":"group","plugin":"ssh","steps":[{"id":"c","plugin":"ssh"}]}]`, "spec.steps[0].plugin", "not allowed on a group"},
		{"group rejects with", `[{"id":"g","type":"group","with":{"a":1},"steps":[{"id":"c","plugin":"ssh"}]}]`, "spec.steps[0].with", "not allowed on a group"},
		{"group rejects timeout", `[{"id":"g","type":"group","timeout":"1m","steps":[{"id":"c","plugin":"ssh"}]}]`, "spec.steps[0].timeout", "not allowed on a group"},
		{"group rejects retry", `[{"id":"g","type":"group","retry":{"max_attempts":2},"steps":[{"id":"c","plugin":"ssh"}]}]`, "spec.steps[0].retry", "not allowed on a group"},
		{"group rejects runs_on", `[{"id":"g","type":"group","runs_on":{"pool":"p"},"steps":[{"id":"c","plugin":"ssh"}]}]`, "spec.steps[0].runs_on", "not allowed on a group"},
		{"steps on a plugin step", `[{"id":"a","plugin":"ssh","steps":[{"id":"c","plugin":"ssh"}]}]`, "spec.steps[0].steps", "only valid on a step of type"},
		{"reserved id catch in a group", `[{"id":"g","type":"group","steps":[{"id":"catch","plugin":"ssh"}]}]`, "spec.steps[0].steps[0].id", "reserved inside a group"},
		{"reserved id finally", `[{"id":"g","type":"group","steps":[{"id":"finally","plugin":"ssh"}]}]`, "spec.steps[0].steps[0].id", "reserved"},
		{"duplicate id in a group", `[{"id":"g","type":"group","steps":[{"id":"c","plugin":"ssh"},{"id":"c","plugin":"ssh"}]}]`, "spec.steps[0].steps[1].id", "duplicate id"},
		{"nested step is validated", `[{"id":"g","type":"group","steps":[{"id":"c","plugin":"Bad Name"}]}]`, "spec.steps[0].steps[0].plugin", "name@<major>"},
		{"nested when is checked", `[{"id":"g","type":"group","steps":[{"id":"c","plugin":"ssh","when":"always( &&"}]}]`, "spec.steps[0].steps[0].when", "invalid expression"},
		{"group when is checked", `[{"id":"g","type":"group","when":"always( &&","steps":[{"id":"c","plugin":"ssh"}]}]`, "spec.steps[0].when", "invalid expression"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(stepsDoc(tt.steps))
			var ve ValidationErrors
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want ValidationErrors", err)
			}
			for _, fe := range ve {
				if fe.Path == tt.wantPath && strings.Contains(fe.Message, tt.wantMsg) {
					return
				}
			}
			t.Fatalf("no error at %q containing %q in %v", tt.wantPath, tt.wantMsg, ve)
		})
	}
}

// Jobs saved before groups existed may use catch or finally as a top-level step
// id; only ids inside a group are reserved.
func TestTopLevelCatchAndFinallyIDsStayValid(t *testing.T) {
	job, err := Parse(stepsDoc(`[{"id":"catch","plugin":"ssh"},{"id":"finally","plugin":"ssh"},{"id":"g","type":"group","steps":[{"id":"x","plugin":"ssh"}]}]`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for path, want := range map[string]string{"catch": "ssh", "finally": "ssh", "g/x": "ssh"} {
		if s, ok := job.StepAt(path); !ok || s.Plugin != want {
			t.Errorf("StepAt(%q) = %+v, %v", path, s, ok)
		}
	}
}
