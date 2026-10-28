package agent

import (
	"context"
	"log/slog"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/internal/protocol"
)

// hostKeyReporter maps pluginsdk.HostKeyReporter to node.hostkey_observed.
// The report is best-effort: when the connection is down it is dropped, and
// the next run on that node observes the key again. The controller applies
// it only if the attempt is still current (fencing).
type hostKeyReporter struct {
	ctx     context.Context
	link    *link
	attempt string
	log     *slog.Logger
}

// ReportHostKey implements pluginsdk.HostKeyReporter.
func (h *hostKeyReporter) ReportHostKey(node, key string) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(key)))
	if err != nil {
		h.log.Warn("ignoring unparsable host key report", "node", node, "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(h.ctx, sendTimeout)
	defer cancel()
	err = h.link.notify(ctx, protocol.MethodNodeHostKeyObserved, protocol.HostKeyObserved{
		AttemptID:   h.attempt,
		Node:        node,
		KeyType:     pub.Type(),
		HostKey:     strings.TrimSpace(key),
		Fingerprint: ssh.FingerprintSHA256(pub),
	})
	if err != nil {
		h.log.Debug("host key report not delivered", "node", node, "error", err)
	}
}
