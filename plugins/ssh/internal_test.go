package ssh

import (
	"strings"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func TestDecodeValidation(t *testing.T) {
	tests := []struct {
		name, input string
		wantErr     string // empty: valid
	}{
		{"command", `{"node":"n","command":"id"}`, ""},
		{"script", `{"node":"n","script":"id"}`, ""},
		{"no node", `{"command":"id"}`, "node is required"},
		{"neither", `{"node":"n"}`, "exactly one"},
		{"both", `{"node":"n","command":"a","script":"b"}`, "exactly one"},
		{"unknown field", `{"node":"n","command":"a","bogus":1}`, "invalid input"},
		{"malformed", `{`, "invalid input"},
		{"sudo_user without sudo", `{"node":"n","command":"a","sudo_user":"bob"}`, "requires sudo"},
		{"sudo_user ok", `{"node":"n","command":"a","sudo":true,"sudo_user":"bob"}`, ""},
		{"sudo_user option injection", `{"node":"n","command":"a","sudo":true,"sudo_user":"-s"}`, "must not start"},
		{"bad env name", `{"node":"n","command":"a","env":{"A-B":"x"}}`, "not a valid variable name"},
		{"bad exit code", `{"node":"n","command":"a","success_exit_codes":[256]}`, "outside 0..255"},
		{"newline interpreter", `{"node":"n","command":"a","interpreter":"/bin/sh\nrm"}`, "single-line"},
		{"pty with env", `{"node":"n","command":"a","pty":true,"env":{"A":"b"}}`, "process list"},
		{"pty without env", `{"node":"n","command":"a","pty":true}`, ""},
		{"nul command", `{"node":"n","command":"a\u0000b"}`, "NUL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in, err := decode([]byte(tc.input))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if in.Interpreter != "/bin/sh" || len(in.SuccessExitCodes) != 1 {
					t.Errorf("defaults not applied: %+v", in)
				}
				return
			}
			se := pluginsdk.AsStepError(err)
			if err == nil || se.Kind != pluginsdk.KindValidation || !strings.Contains(se.Message, tc.wantErr) {
				t.Fatalf("error = %v, want validation containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestBuildCall(t *testing.T) {
	tests := []struct {
		name        string
		in          Input
		env         map[string]string
		wantCommand string
		wantStdin   []string // substrings in order
		noStdin     bool
	}{
		{
			name: "command", in: Input{Command: "echo 'hi'", Interpreter: "/bin/sh"},
			wantCommand: "/bin/sh -s",
			wantStdin:   []string{`exec '/bin/sh' -c 'echo '\''hi'\'''`},
		},
		{
			name: "env workdir sudo", in: Input{Command: "id", Interpreter: "/bin/bash -e", Workdir: "/opt/my app", Sudo: true, SudoUser: "bob"},
			env:         map[string]string{"B": "2", "A": "it's"},
			wantCommand: "sudo -n -u 'bob' -- /bin/sh -s",
			wantStdin:   []string{`export A='it'\''s'`, `export B='2'`, `cd -- '/opt/my app' || exit 1`, `exec '/bin/bash' '-e' -c 'id'`},
		},
		{
			name: "script", in: Input{Script: "echo a\necho b\n", Interpreter: "python3"},
			wantCommand: "/bin/sh -s",
			wantStdin:   []string{"mktemp", "trap", "<<'DAMSON_EOF_", "echo a\necho b\nDAMSON_EOF_", "chmod 700", `'python3' "$f"`},
		},
		{
			name: "pty puts body in argv", in: Input{Command: "id", Interpreter: "/bin/sh", Pty: true}, noStdin: true,
			wantCommand: "/bin/sh -c 'exec '\\''/bin/sh'\\'' -c '\\''id'\\''\n'",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildCall(tc.in, tc.env)
			if err != nil {
				t.Fatal(err)
			}
			if got.Command != tc.wantCommand {
				t.Errorf("command = %q, want %q", got.Command, tc.wantCommand)
			}
			if tc.noStdin && got.Stdin != "" {
				t.Errorf("stdin = %q, want none", got.Stdin)
			}
			rest := got.Stdin
			for _, sub := range tc.wantStdin {
				i := strings.Index(rest, sub)
				if i < 0 {
					t.Fatalf("stdin %q lacks %q (in order)", got.Stdin, sub)
				}
				rest = rest[i+len(sub):]
			}
		})
	}
}

func TestEnvValuesStayOutOfArgv(t *testing.T) {
	got, err := buildCall(Input{Command: "id", Interpreter: "/bin/sh", Sudo: true}, map[string]string{"TOKEN": "s3cret-value"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Command, "s3cret-value") || !strings.Contains(got.Stdin, "s3cret-value") {
		t.Errorf("env value must travel on stdin only: %+v", got)
	}
}

func TestHeredocMarkerAvoidsScriptContent(t *testing.T) {
	m, err := heredocMarker("plain script")
	if err != nil || !strings.HasPrefix(m, "DAMSON_EOF_") {
		t.Fatalf("marker = %q, %v", m, err)
	}
}
