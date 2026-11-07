package gitcheckout

import (
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// classify maps a failed git command to a step error from its stderr. Git
// words its errors in English because the environment sets LC_ALL=C. The text
// never holds a credential: tokens are not in the URL or the arguments.
func classify(sub, stderr string, cause error) error {
	low := strings.ToLower(stderr)
	msg := "git " + sub + " failed"
	if stderr != "" {
		msg += ": " + stderr
	}
	notFound := strings.Contains(low, "repository") && strings.Contains(low, "not found")
	switch {
	case notFound:
		return pluginsdk.ValidationError("%s", msg)
	case containsAny(low, "authentication failed", "permission denied", "could not read username", "could not read password",
		"invalid username or password", "http basic: access denied", "the requested url returned error: 401", "the requested url returned error: 403",
		"host key verification failed", "no matching host key"):
		return pluginsdk.NewError(pluginsdk.KindAuth, msg, cause)
	case containsAny(low, "could not resolve host", "connection refused", "connection timed out", "network is unreachable",
		"no route to host", "unable to access", "connection reset", "operation timed out", "ssl certificate", "ssl:", "tls", "unable to connect"):
		return pluginsdk.NewError(pluginsdk.KindConnection, msg, cause)
	case containsAny(low, "couldn't find remote ref", "not our ref", "repository not found", "does not appear to be a git repository",
		"not a valid object name", "invalid refspec", "no such remote ref", "reference is not a tree", "unadvertised object"):
		return pluginsdk.ValidationError("%s", msg)
	}
	return pluginsdk.NewError(pluginsdk.KindExitCode, msg, cause)
}

func containsAny(s string, subs ...string) bool {
	for _, p := range subs {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
