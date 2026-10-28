package pluginreg_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invopop/jsonschema"

	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func TestLookupVersusResolve(t *testing.T) {
	r := newReg(t)
	// 2.0.0 needs a capability no agent has: Lookup (authoring) still names
	// it, Resolve (dispatch) falls back to what can actually run.
	setAgent(t, r, "a1", "p", nil, man("helm", "2.0.0", "bin:helm"))
	setAgent(t, r, "a2", "p", nil, man("helm", "1.0.0"))
	m, err := r.Lookup("helm")
	if err != nil || m.Version != "2.0.0" {
		t.Fatalf("Lookup = %q, %v", m.Version, err)
	}
	res, err := r.Resolve("helm", pluginreg.Filter{})
	if err != nil || res.Manifest.Version != "1.0.0" {
		t.Fatalf("Resolve = %+v, %v", res.Manifest.Version, err)
	}
}

func TestBuiltinShadowing(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	r, err := pluginreg.New([]pluginsdk.Manifest{man("ssh", "1.0.0")}, log)
	if err != nil {
		t.Fatal(err)
	}
	hostile := man("ssh", "2.0.0")
	hostile.Description = "TOPSECRET-DESCRIPTION"
	differing := altMan("ssh", "1.0.0")
	differing.Description = "TOPSECRET-DESCRIPTION"
	setAgent(t, r, "evil", "p", nil, hostile)
	setAgent(t, r, "odd", "p", nil, differing)
	setAgent(t, r, "good", "p", nil, man("ssh", "1.0.0"))

	// Unpinned never reaches the agent-only 2.0.0.
	res, err := r.Resolve("ssh", pluginreg.Filter{})
	if err != nil || res.Manifest.Version != "1.0.0" || !reflect.DeepEqual(ids(res.Agents), []string{"good"}) {
		t.Fatalf("unpinned = %+v, %v", res, err)
	}
	if m, err := r.Lookup("ssh"); err != nil || m.Version != "1.0.0" {
		t.Errorf("Lookup unpinned = %q, %v", m.Version, err)
	}
	// An explicit pin is the only way in.
	res, err = r.Resolve("ssh@2", pluginreg.Filter{})
	if err != nil || res.Manifest.Version != "2.0.0" || !reflect.DeepEqual(ids(res.Agents), []string{"evil"}) {
		t.Fatalf("pinned = %+v, %v", res, err)
	}
	// The agent whose 1.0.0 differs from the built-in is never chosen.
	res, _ = r.Resolve("ssh@1", pluginreg.Filter{})
	if !reflect.DeepEqual(ids(res.Agents), []string{"good"}) {
		t.Errorf("agents = %v", ids(res.Agents))
	}

	logs := buf.String()
	if !strings.Contains(logs, "higher version") || !strings.Contains(logs, "differs from the built-in") {
		t.Errorf("missing shadow warnings: %s", logs)
	}
	if strings.Contains(logs, "TOPSECRET") {
		t.Errorf("manifest contents logged: %s", logs)
	}
	for _, w := range strings.Split(strings.TrimSpace(logs), "\n") {
		if strings.Contains(w, `"agent_id":"good"`) {
			t.Errorf("matching manifest must not warn: %s", w)
		}
	}
}

func TestUnpinnedBuiltinNameWithOnlyAgentVersions(t *testing.T) {
	r := newReg(t, man("ssh", "1.0.0"))
	setAgent(t, r, "evil", "p", nil, man("ssh", "2.0.0"))
	_, err := r.Resolve("ssh", pluginreg.Filter{})
	if !errors.Is(err, pluginreg.ErrNoCapableAgent) {
		t.Errorf("err = %v", err)
	}
	// A name with no builtin keeps plain semantics.
	setAgent(t, r, "a", "p", nil, man("custom", "1.0.0"), man("custom", "2.0.0"))
	if res, err := r.Resolve("custom", pluginreg.Filter{}); err != nil || res.Manifest.Version != "2.0.0" {
		t.Errorf("custom = %+v, %v", res, err)
	}
}

func nested(depth int) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "string"}
	for range depth {
		s = &jsonschema.Schema{Not: s}
	}
	return s
}

func TestSchemaLimits(t *testing.T) {
	big := man("big", "1.0.0")
	big.InputSchema = &jsonschema.Schema{Type: "object", Description: strings.Repeat("x", pluginreg.MaxSchemaBytes+1)}
	deep := man("deep", "1.0.0")
	deep.InputSchema = nested(pluginreg.MaxSchemaDepth + 5)

	for _, m := range []pluginsdk.Manifest{big, deep} {
		if err := pluginreg.ValidateInput(m, json.RawMessage(`{}`)); !errors.Is(err, pluginreg.ErrSchemaTooLarge) {
			t.Errorf("%s: ValidateInput err = %v", m.Name, err)
		}
		r := newReg(t)
		err := r.SetAgent(pluginreg.AgentInfo{ID: "a", Pool: "p", Manifests: []pluginsdk.Manifest{m, man("ok", "1.0.0")}})
		if err == nil || !strings.Contains(err.Error(), "skipped") {
			t.Errorf("%s: SetAgent err = %v", m.Name, err)
		}
		if _, e := r.Resolve(m.Name, pluginreg.Filter{}); !errors.Is(e, pluginreg.ErrUnknownPlugin) {
			t.Errorf("%s must not register: %v", m.Name, e)
		}
		if _, e := r.Resolve("ok", pluginreg.Filter{}); e != nil {
			t.Errorf("sibling manifest lost: %v", e)
		}
	}
	r := newReg(t)
	huge := json.RawMessage(`"` + strings.Repeat("a", pluginreg.MaxInputBytes) + `"`)
	if err := r.ValidateInput(man("t", "1.0.0"), huge); !errors.Is(err, pluginreg.ErrInputTooLarge) {
		t.Errorf("input err = %v", err)
	}
}

func TestPathologicalRefSchema(t *testing.T) {
	m := man("loop", "1.0.0")
	m.InputSchema = &jsonschema.Schema{
		Ref: "#/$defs/a",
		Definitions: jsonschema.Definitions{
			"a": {Ref: "#/$defs/b"},
			"b": {Ref: "#/$defs/a"},
		},
	}
	done := make(chan error, 1)
	go func() { done <- pluginreg.ValidateInput(m, json.RawMessage(`{}`)) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a reference cycle must not validate as usable")
		} else if errors.As(err, new(pluginreg.ValidationErrors)) {
			t.Errorf("expected a compile error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cyclic $ref hangs")
	}
	r := newReg(t)
	if err := r.SetAgent(pluginreg.AgentInfo{ID: "a", Pool: "p", Manifests: []pluginsdk.Manifest{m}}); err == nil {
		t.Error("cyclic manifest accepted")
	}
}

type leakIn struct {
	Mode  string  `json:"mode" jsonschema:"const=fixed"`
	Mail  string  `json:"mail,omitempty" jsonschema:"format=email"`
	Count float64 `json:"count,omitempty" jsonschema:"minimum=10,maximum=20,multipleOf=5"`
	Tags  []int   `json:"tags,omitempty"`
}

func TestValidationNeverEchoesInput(t *testing.T) {
	m := man("leak", "1.0.0")
	m.InputSchema = pluginsdk.MustSchemaFor[leakIn]()
	// Set const directly (struct tags do not cover it). format is annotation-only
	// (not asserted) by default; safeMessage still rewrites it if enabled.
	mode, _ := m.InputSchema.Properties.Get("mode")
	mode.Const = "fixed"
	mail, _ := m.InputSchema.Properties.Get("mail")
	mail.Format = "email"
	inputs := []string{
		`{"mode":"hunter2-secret"}`,
		`{"mode":"fixed","hunter2-secret":"v"}`,
		`{"mode":"fixed","count":987654321}`,
		`{"mode":"fixed","count":-987654321}`,
		`{"mode":"fixed","count":13.37}`,
		`{"mode":"fixed","tags":["hunter2-secret"]}`,
		`{"mode":"fixed","count":"hunter2-secret"}`,
	}
	for _, in := range inputs {
		err := pluginreg.ValidateInput(m, json.RawMessage(in))
		var ve pluginreg.ValidationErrors
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v", in, err)
			continue
		}
		for _, bad := range []string{"hunter2", "987654321", "13.37"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("%s: error echoes %q: %v", in, bad, err)
			}
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	r := newReg(t, man("ssh", "1.0.0"))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("a%d", g)
			for i := range 100 {
				switch (g + i) % 4 {
				case 0:
					_ = r.SetAgent(pluginreg.AgentInfo{ID: id, Pool: "p", Manifests: []pluginsdk.Manifest{man("ssh", "1.0.0"), man("x", fmt.Sprintf("1.%d.0", i%3))}})
				case 1:
					r.RemoveAgent(id)
				case 2:
					if res, err := r.Resolve("x", pluginreg.Filter{}); err == nil {
						_ = r.ValidateResolved(res, json.RawMessage(`{"host":"a.b"}`))
					}
					_ = r.List()
				default:
					_ = r.ValidateInput(man("ssh", "1.0.0"), json.RawMessage(`{"host":"a.b"}`))
					_ = r.Agents()
				}
			}
		}()
	}
	wg.Wait()
}
