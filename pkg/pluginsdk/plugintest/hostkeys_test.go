package plugintest_test

import (
	"context"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
)

type hostKeyPlugin struct{ pluginsdk.Plugin }

func (hostKeyPlugin) Execute(_ context.Context, req pluginsdk.ExecRequest, _ pluginsdk.Emitter) (pluginsdk.Result, error) {
	if req.HostKeys != nil {
		req.HostKeys.ReportHostKey("n1", "ssh-ed25519 AAAA")
	}
	if req.Config["flag"] != "on" {
		return pluginsdk.Result{Error: pluginsdk.ValidationError("config not passed")}, nil
	}
	return pluginsdk.Result{}, nil
}

func TestExecPassesHostKeysAndConfig(t *testing.T) {
	o := plugintest.Exec(context.Background(), t, hostKeyPlugin{}, plugintest.Case{Config: map[string]string{"flag": "on"}})
	if o.Result.Error != nil {
		t.Fatalf("config not passed: %v", o.Result.Error)
	}
	want := plugintest.ObservedKey{Node: "n1", Key: "ssh-ed25519 AAAA"}
	if got := o.HostKeys.Keys(); len(got) != 1 || got[0] != want {
		t.Fatalf("host keys = %v, want %v", got, want)
	}
}
