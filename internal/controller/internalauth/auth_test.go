package internalauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	secretA = strings.Repeat("a", 40)
	secretB = strings.Repeat("b", 40)
	t0      = time.Unix(1_800_000_000, 0)
)

func req(t *testing.T, method, target string) *http.Request {
	t.Helper()
	return httptest.NewRequest(method, target, nil)
}

func ring(t *testing.T, cur, prev string) *Keyring {
	t.Helper()
	k, err := NewKeyring(cur, prev)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSignAndVerify(t *testing.T) {
	k := ring(t, secretA, "")
	r := req(t, http.MethodGet, "/internal/v1/executions/e1/tail?after_seq=3&attempt_id=a1")
	k.Sign(r, "ctl-A", "ctl-B", t0)
	caller, err := k.Verify(r, "ctl-B", t0.Add(5*time.Second))
	if err != nil || caller != "ctl-A" {
		t.Fatalf("Verify = %q, %v", caller, err)
	}
	// Query order does not matter (canonical form).
	r2 := req(t, http.MethodGet, "/internal/v1/executions/e1/tail?attempt_id=a1&after_seq=3")
	r2.Header = r.Header.Clone()
	if _, err := k.Verify(r2, "ctl-B", t0); err != nil {
		t.Fatalf("reordered query: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	k := ring(t, secretA, "")
	signedReq := func() *http.Request {
		r := req(t, http.MethodGet, "/internal/v1/executions/e1/tail?after_seq=3")
		k.Sign(r, "ctl-A", "ctl-B", t0)
		return r
	}
	tests := []struct {
		name   string
		mutate func(r *http.Request)
		self   string
		now    time.Time
		want   error
	}{
		{"valid", func(*http.Request) {}, "ctl-B", t0, nil},
		{"edge of the window", func(*http.Request) {}, "ctl-B", t0.Add(Window), nil},
		{"too late", func(*http.Request) {}, "ctl-B", t0.Add(Window + time.Second), ErrTimestamp},
		{"from the future", func(*http.Request) {}, "ctl-B", t0.Add(-Window - time.Second), ErrTimestamp},
		{"other target", func(*http.Request) {}, "ctl-C", t0, ErrSignature},
		{"other path", func(r *http.Request) { r.URL.Path = "/internal/v1/executions/e2/tail" }, "ctl-B", t0, ErrSignature},
		{"other query", func(r *http.Request) { r.URL.RawQuery = "after_seq=0" }, "ctl-B", t0, ErrSignature},
		{"other method", func(r *http.Request) { r.Method = http.MethodPost }, "ctl-B", t0, ErrSignature},
		{"other caller", func(r *http.Request) { r.Header.Set(HeaderCaller, "ctl-X") }, "ctl-B", t0, ErrSignature},
		{"other timestamp", func(r *http.Request) { r.Header.Set(HeaderTimestamp, "1800000001") }, "ctl-B", t0, ErrSignature},
		{"garbled signature", func(r *http.Request) { r.Header.Set(HeaderSignature, "AAAA") }, "ctl-B", t0, ErrSignature},
		{"no signature", func(r *http.Request) { r.Header.Del(HeaderSignature) }, "ctl-B", t0, ErrMissing},
		{"no caller", func(r *http.Request) { r.Header.Del(HeaderCaller) }, "ctl-B", t0, ErrMissing},
		{"bad timestamp", func(r *http.Request) { r.Header.Set(HeaderTimestamp, "soon") }, "ctl-B", t0, ErrMissing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := signedReq()
			tc.mutate(r)
			_, err := k.Verify(r, tc.self, tc.now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Verify = %v; want %v", err, tc.want)
			}
		})
	}
}

func TestRotation(t *testing.T) {
	old, next := ring(t, secretA, ""), ring(t, secretB, secretA)
	r := req(t, http.MethodGet, "/internal/v1/executions/e1/tail")
	old.Sign(r, "ctl-A", "ctl-B", t0)
	if _, err := next.Verify(r, "ctl-B", t0); err != nil {
		t.Fatalf("previous key not accepted: %v", err)
	}
	r = req(t, http.MethodGet, "/internal/v1/executions/e1/tail")
	next.Sign(r, "ctl-B", "ctl-A", t0)
	if _, err := old.Verify(r, "ctl-A", t0); !errors.Is(err, ErrSignature) {
		t.Fatalf("a key nobody holds yet verified: %v", err)
	}
	if _, err := ring(t, secretB, "").Verify(r, "ctl-A", t0); err != nil {
		t.Fatalf("current key rejected: %v", err)
	}
}

func TestNewKeyringRejectsShortSecrets(t *testing.T) {
	if _, err := NewKeyring("short", ""); err == nil {
		t.Error("short secret accepted")
	}
	if _, err := NewKeyring(secretA, "short"); err == nil {
		t.Error("short previous secret accepted")
	}
}

func TestErrorsDoNotCarrySecrets(t *testing.T) {
	k := ring(t, secretA, "")
	r := req(t, http.MethodGet, "/x")
	k.Sign(r, "c", "t", t0)
	sig := r.Header.Get(HeaderSignature)
	r.Header.Set(HeaderSignature, "AAAA")
	_, err := k.Verify(r, "t", t0)
	if err == nil || strings.Contains(err.Error(), secretA) || strings.Contains(err.Error(), sig) {
		t.Fatalf("error %v leaks", err)
	}
}
