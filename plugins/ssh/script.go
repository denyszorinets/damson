package ssh

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// quote single-quotes s for a POSIX shell.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// remoteCall is what is sent to the node: the remote command line and the
// data written to its stdin. Without a pty everything sensitive (resolved env
// values, the script) travels on stdin, so it never shows up in the remote
// process list. With a pty stdin is not usable, so the body is an argument.
type remoteCall struct {
	Command string
	Stdin   string
}

// buildCall assembles the shell body for in and env (values already resolved).
func buildCall(in Input, env map[string]string) (remoteCall, error) {
	var b strings.Builder
	interp := interpreterWords(in.Interpreter)
	if in.Script != "" {
		marker, err := heredocMarker(in.Script)
		if err != nil {
			return remoteCall{}, err
		}
		b.WriteString("f=$(mktemp \"${TMPDIR:-/tmp}/damson-XXXXXXXX\") || exit 1\n")
		b.WriteString("trap 'rm -f \"$f\"' EXIT\n")
		fmt.Fprintf(&b, "cat >\"$f\" <<'%s'\n%s\n%s\nchmod 700 \"$f\"\n", marker, strings.TrimSuffix(in.Script, "\n"), marker)
	}
	for _, k := range sortedKeys(env) {
		fmt.Fprintf(&b, "export %s=%s\n", k, quote(env[k]))
	}
	if in.Workdir != "" {
		fmt.Fprintf(&b, "cd %s || exit 1\n", quote(in.Workdir))
	}
	if in.Script != "" {
		fmt.Fprintf(&b, "%s \"$f\"\n", interp)
	} else {
		fmt.Fprintf(&b, "exec %s -c %s\n", interp, quote(in.Command))
	}

	prefix := ""
	if in.Sudo {
		prefix = "sudo -n "
		if in.SudoUser != "" {
			prefix += "-u " + quote(in.SudoUser) + " "
		}
		prefix += "-- "
	}
	if in.Pty {
		return remoteCall{Command: prefix + "/bin/sh -c " + quote(b.String())}, nil
	}
	return remoteCall{Command: prefix + "/bin/sh -s", Stdin: b.String()}, nil
}

func interpreterWords(interp string) string {
	words := strings.Fields(interp)
	for i, w := range words {
		words[i] = quote(w)
	}
	return strings.Join(words, " ")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// heredocMarker returns a random here-document terminator that does not occur
// in script.
func heredocMarker(script string) (string, error) {
	for range 8 {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", fmt.Errorf("generating heredoc marker: %w", err)
		}
		m := "DAMSON_EOF_" + hex.EncodeToString(raw[:])
		if !strings.Contains(script, m) {
			return m, nil
		}
	}
	return "", errors.New("could not find an unused heredoc marker")
}
