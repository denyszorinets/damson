package agent

import (
	"encoding/json"
	"log/slog"

	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// LocalMasking gives a tool that runs a plugin outside an agent (damson plugin
// run) the same secret handling the agent applies to a step: the secrets are
// served to the plugin from memory, and everything that leaves, log lines,
// outputs and error text, is masked by the agent's own masker and output
// sanitizer (ARCHITECTURE.md section 9.4). Call Wipe when the run is over.
type LocalMasking struct {
	m   *masker
	res *bundleResolver
}

// NewLocalMasking takes ownership of the bundle.
func NewLocalMasking(b map[string]map[string]string) *LocalMasking {
	bundle := protocol.SecretBundle(b)
	return &LocalMasking{m: newMasker(bundle), res: newBundleResolver(bundle)}
}

// Resolver serves the secrets to the plugin.
func (l *LocalMasking) Resolver() pluginsdk.SecretResolver { return l.res }

// Mask hides secret values in text.
func (l *LocalMasking) Mask(s string) string { return l.m.Mask(s) }

// Outputs decodes a plugin's outputs with every secret-bearing string replaced
// by ***. It returns the JSON paths it redacted.
func (l *LocalMasking) Outputs(raw json.RawMessage) (map[string]any, []string, error) {
	return sanitizeOutputs(raw, l.m)
}

// ShortSecrets reports that a secret value is too short to be masked in text.
func (l *LocalMasking) ShortSecrets() bool { return l.m.short }

// Wipe drops the secrets; afterwards every non-empty text masks to ***.
func (l *LocalMasking) Wipe() {
	l.res.wipe()
	l.m.wipe()
}

// Logger wraps h so that secret values are masked in what a plugin logs.
func (l *LocalMasking) Logger(h slog.Handler) *slog.Logger {
	return newMaskedLogger(slog.New(h), l.m)
}
