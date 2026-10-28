package all_test

import (
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/all"
	"github.com/denyszorinets/damson/plugins/internal/sshtest"
)

// contractOptions maps each built-in plugin name to its contract cases. Add an
// entry here when a built-in is added to all.Plugins.
func contractOptions(t *testing.T) map[string]plugintest.Options {
	f := sshtest.NewFixture(t, "")
	blocking := f.Case("blocking", `{"command":"sleep 30"}`)
	return map[string]plugintest.Options{
		"ssh": {
			Cases:    []plugintest.Case{f.Case("command", `{"command":"echo hello"}`)},
			Blocking: &blocking,
		},
	}
}

func TestBuiltinsPassContract(t *testing.T) {
	plugintest.RunContractAll(t, all.Plugins(), contractOptions(t))
}

func TestSource(t *testing.T) {
	plugintest.CheckSource(t, ".", plugintest.SkipImportRule())
}
