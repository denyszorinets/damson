//go:build winrmwindows

package winrm_test

import "go.uber.org/goleak"

// leakOptions tolerates the idle connection of the http transport: it uses the
// library's own transport, whose connection cannot be closed from the plugin
// and ends when the node's idle timeout passes (documented in plugin-winrm).
func leakOptions() []goleak.Option {
	return []goleak.Option{
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
	}
}
