package all_test

import (
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk/plugintest"
	"github.com/denyszorinets/damson/plugins/all"
)

// contractOptions maps each built-in plugin name to its contract cases. Add an
// entry here when a built-in is added to all.Plugins.
func contractOptions() map[string]plugintest.Options {
	return map[string]plugintest.Options{}
}

func TestBuiltinsPassContract(t *testing.T) {
	plugintest.RunContractAll(t, all.Plugins(), contractOptions())
}

func TestSource(t *testing.T) {
	plugintest.CheckSource(t, ".", plugintest.SkipImportRule())
}
