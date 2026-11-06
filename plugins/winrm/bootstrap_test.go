package winrm

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func decodeUTF16LE(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("not UTF-16LE base64: %v", err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func TestBootstrapCommandRoundTrips(t *testing.T) {
	cmd := bootstrapCommand()
	enc, ok := strings.CutPrefix(cmd, powershellArgs)
	if !ok || strings.ContainsAny(enc, " \"'") {
		t.Fatalf("command = %.60s...", cmd)
	}
	if got := decodeUTF16LE(t, enc); got != bootstrapScript {
		t.Fatal("the encoded command is not the bootstrap script")
	}
	// A Windows command line is limited to 8191 characters.
	if len(cmd) > 4000 {
		t.Fatalf("the command line is %d characters", len(cmd))
	}
	// Characters outside the BMP survive the UTF-16 encoding.
	if got := decodeUTF16LE(t, encodeUTF16LE("a\U0001F600b")); got != "a\U0001F600b" {
		t.Fatalf("round trip = %q", got)
	}
}

func TestStdinPayload(t *testing.T) {
	decode := func(b []byte) map[string]any {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	b, err := stdinPayload(Input{Command: `echo "héllo" & dir`}, map[string]string{"A": "1", "SECRET": `p"a'ss$`})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(b)
	if m["command"] != `echo "héllo" & dir` || m["powershell"] != nil || m["env"].(map[string]any)["SECRET"] != `p"a'ss$` {
		t.Fatalf("payload = %v", m)
	}
	b, err = stdinPayload(Input{Powershell: "Get-Date"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m = decode(b)
	if m["powershell"] != "Get-Date" || m["command"] != nil || m["env"] != nil {
		t.Fatalf("payload = %v", m)
	}
	if _, err := stdinPayload(Input{}, nil); err == nil {
		t.Fatal("an empty input produced a payload")
	}
}

func TestClassify(t *testing.T) {
	for in, want := range map[string]pluginsdk.ErrorKind{
		"http response error: 401 - invalid content type":           pluginsdk.KindAuth,
		"http error 401: Unauthorized":                              pluginsdk.KindAuth,
		"winrm request: tls: failed to verify certificate: x509: x": pluginsdk.KindConnection,
		"winrm request: dial tcp 10.0.0.1:5986: i/o timeout":        pluginsdk.KindConnection,
	} {
		if got := pluginsdk.AsStepError(classify(errors.New(in), "h")).Kind; got != want {
			t.Errorf("%q = %s; want %s", in, got, want)
		}
	}
}

func TestFaultSummaryReducesASoapFault(t *testing.T) {
	in := `http error 500: <s:Envelope><s:Body><s:Fault><s:Reason><s:Text xml:lang="en-US">The WS-Management service cannot process the request.</s:Text></s:Reason><s:Detail><f:WSManFault><f:Message>The shell is gone</f:Message></f:WSManFault></s:Detail></s:Fault></s:Body></s:Envelope>`
	got := faultSummary(in)
	if !strings.Contains(got, "cannot process the request") || !strings.Contains(got, "The shell is gone") || strings.Contains(got, "<s:") {
		t.Fatalf("summary = %q", got)
	}
	if long := faultSummary(strings.Repeat("x", 5000)); len(long) > 4100 {
		t.Fatalf("not bounded: %d", len(long))
	}
}
