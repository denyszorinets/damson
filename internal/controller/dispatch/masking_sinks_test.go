package dispatch_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// ADR-0047: the agent masks, the controller keeps nothing. This runs a real
// agent against the db secret backend and scans every place the controller
// stores or logs what the agent sent. The leak forms are computed here.

const (
	leakToken  = "T0ken-Valu3!/zz"
	leakUser   = "svc-damson-77"
	leakLine1  = "KEYLINEAAAA1111"
	leakLine2  = "KEYLINEBBBB2222"
	leakKey    = "-----BEGIN KEY-----\n" + leakLine1 + "\n" + leakLine2 + "\n-----END KEY-----"
	leakPhrase = "Phr4se-xyz-99"
)

func leakForms() []string {
	var out []string
	for _, v := range []string{leakToken, leakUser, leakKey, leakLine1, leakLine2, leakPhrase} {
		out = append(out, v,
			base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v)),
			base64.URLEncoding.EncodeToString([]byte(v)), base64.RawURLEncoding.EncodeToString([]byte(v)),
			url.QueryEscape(v), url.PathEscape(v))
	}
	return out
}

// leakPlugin is a single-node plugin that tries to put every secret form into
// every channel: logs, outputs, the error.
type leakPlugin struct{}

func (leakPlugin) Manifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: "leakdemo", Version: "1.0.0", Title: "leakdemo", TargetMode: pluginsdk.TargetSingleNode,
		InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func (leakPlugin) Validate(context.Context, json.RawMessage) error { return nil }

func (leakPlugin) Execute(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	var in struct {
		Node, Token string
		Fail        bool
	}
	_ = json.Unmarshal(req.Input, &in)
	node := req.Nodes[in.Node]
	cred, err := req.Secrets.ResolveFields(node.Credential)
	if err != nil {
		return pluginsdk.Result{}, errors.New("credential missing")
	}
	tok, err := req.Secrets.Resolve(in.Token)
	if err != nil || tok != leakToken || cred["private_key"] != leakKey {
		return pluginsdk.Result{}, errors.New("secrets did not arrive")
	}
	for i, f := range leakForms() {
		emit.Log(pluginsdk.Stdout, fmt.Sprintf("form %d %s", i, f))
	}
	emit.Progress(10, "progress "+leakToken)
	outs, _ := json.Marshal(map[string]any{leakToken: "u-" + leakUser, "k": leakKey, "ok": "fine"})
	if in.Fail {
		return pluginsdk.Result{Outputs: outs}, errors.New("failed with " + leakToken + " and " + leakLine2)
	}
	return pluginsdk.Result{Outputs: outs}, nil
}

func TestNoSecretFormReachesControllerSinks(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		b := newSecretBackend(t, st)
		putIn(t, b, "_inventory", "cred", map[string]string{"username": leakUser, "private_key": leakKey, "passphrase": leakPhrase})
		putIn(t, b, "default", "token", map[string]string{"value": leakToken})
		addNode(t, st, "n1", true)
		logs := &lockedBuf{}
		log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		project := func(context.Context, string) (string, error) { return "default", nil }
		c := startClusterWith(t, st, nodeSpec, log, []pluginsdk.Plugin{leakPlugin{}}, nil, nil, func(cfg *dispatch.Config) {
			cfg.Nodes = &node.InventoryNodes{Store: st, Scope: inventory.AllNodes{}, Project: project}
			cfg.Secrets, cfg.Project = node.NewSecretSource(b, log), project
		})

		for _, tc := range []struct {
			name, input string
			want        store.RunStatus
		}{
			{"succeeding run", `{"node":"n1","token":"secret://default/token"}`, store.RunSucceeded},
			{"failing run", `{"node":"n1","token":"secret://default/token","fail":true}`, store.RunFailed},
		} {
			tree := c.waitRun(c.triggerInput("leakdemo", tc.input), tc.want)
			ex := tree.Steps[0].Executions[0]
			waitLog(t, st, ex.ID, "form 0 ***") // the plugin ran and was masked

			sinks := map[string]string{
				"step input": string(tree.Steps[0].Input),
				"outputs":    string(ex.Outputs),
				"error":      ex.Error,
			}
			chunks, err := st.ListLogChunks(context.Background(), ex.ID, 1, 1<<30, 100)
			if err != nil || len(chunks) == 0 {
				t.Fatalf("%s: chunks = %v, %v", tc.name, chunks, err)
			}
			for i, ch := range chunks {
				data, err := st.GetLogData(context.Background(), ch.ObjectKey)
				if err != nil {
					t.Fatal(err)
				}
				sinks[fmt.Sprintf("log chunk %d", i)] = string(data)
			}
			if tc.want == store.RunFailed && !strings.Contains(ex.Error, "***") {
				t.Errorf("%s: error = %q; want it masked, not dropped", tc.name, ex.Error)
			}
			for where, text := range sinks {
				for _, form := range leakForms() {
					if strings.Contains(text, form) {
						t.Fatalf("%s: %s contains a secret form (%d bytes)", tc.name, where, len(form))
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
		for _, form := range leakForms() {
			if strings.Contains(logs.String(), form) {
				t.Fatalf("the controller log contains a secret form (%d bytes)", len(form))
			}
		}
	})
}
