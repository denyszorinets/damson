package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestStepExecuteSecretsAreMasked(t *testing.T) {
	const secret = "hunter2-very-secret"
	msg := StepExecute{
		AttemptID: "a1", StepPath: "root/0", PluginName: "ssh",
		Secrets: SecretBundle{"secret://a/b": {"password": secret, "user": "adminuser9"}},
	}

	var logJSON, logText bytes.Buffer
	slog.New(slog.NewJSONHandler(&logJSON, nil)).Info("dispatch", "msg", msg, "bundle", msg.Secrets, "ptr", &msg)
	slog.New(slog.NewTextHandler(&logText, nil)).Info("dispatch", "msg", msg, "bundle", msg.Secrets, "ptr", &msg)
	js, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{
		"slog json": logJSON.String(),
		"slog text": logText.String(),
		"json":      string(js),
		"%v":        fmt.Sprintf("%v", msg),
		"%+v":       fmt.Sprintf("%+v", msg),
		"%#v":       fmt.Sprintf("%#v", msg),
		"%s":        fmt.Sprintf("%s", msg.Secrets),
		"%v ptr":    fmt.Sprintf("%+v", &msg),
		"String()":  msg.Secrets.String(),
	}
	for name, out := range outputs {
		if strings.Contains(out, secret) || strings.Contains(out, "adminuser9") {
			t.Errorf("%s leaks a secret value: %s", name, out)
		}
	}
	if !strings.Contains(outputs["json"], "[REDACTED]") {
		t.Errorf("json output has no redaction marker: %s", outputs["json"])
	}
}

func TestSecretsSurviveWireEncoding(t *testing.T) {
	// Masking applies to text renderings only; the wire carries the values.
	in := StepExecute{AttemptID: "a", Secrets: SecretBundle{"r": {"k": "v"}}}
	b, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	var out StepExecute
	if err := Decode(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Secrets["r"]["k"] != "v" {
		t.Fatalf("secret lost on the wire: %#v", map[string]map[string]string(out.Secrets))
	}
}
