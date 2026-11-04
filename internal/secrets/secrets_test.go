package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	good := map[string]Ref{
		"secret://proj/db":              {Scope: "proj", Name: "db"},
		"secret://a/b/c.d-e_f#password": {Scope: "a/b", Name: "c.d-e_f", Field: "password"},
		"secret://_inventory/web-key":   {Scope: "_inventory", Name: "web-key"},
		"secret://p0/N#private_key":     {Scope: "p0", Name: "N", Field: "private_key"},
		"secret://a/b/c/d/e/f/g/h/name": {Scope: "a/b/c/d/e/f/g/h", Name: "name"},
	}
	for s, want := range good {
		got, err := ParseRef(s)
		if err != nil || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", s, got, err, want)
		}
		if got.String() != s {
			t.Errorf("String() = %q, want %q", got.String(), s)
		}
	}
	bad := []string{
		"", "secret://", "secret://x", "secret:///x", "http://a/b",
		"secret://_proj/x", "secret://_inventory/a/b", "secret://Proj/x",
		"secret://a//b", "secret://a/b/", "secret://a/b#", "secret://a/b#Up",
		"secret://a/b%2Fc", "secret://a/..", "secret://a/b..c", "secret://é/x",
		"secret://a/b/c/d/e/f/g/h/i/name",
		"secret://a/" + strings.Repeat("n", 600),
	}
	for _, s := range bad {
		if _, err := ParseRef(s); !errors.Is(err, ErrBadRef) {
			t.Errorf("ParseRef(%q) err = %v, want ErrBadRef", s, err)
		}
	}
}

func TestValidateFields(t *testing.T) {
	cases := []struct {
		name   string
		typ    string
		fields map[string]string
		want   error
	}{
		{"generic ok", "generic", map[string]string{"anything": "x"}, nil},
		{"password ok", "password", map[string]string{"password": "x"}, nil},
		{"missing required", "password", map[string]string{"username": "u"}, ErrBadFields},
		{"unknown field", "token", map[string]string{"token": "x", "extra": "y"}, ErrBadFields},
		{"bad field name", "generic", map[string]string{"Bad": "x"}, ErrBadFields},
		{"invalid utf8", "generic", map[string]string{"a": "\xff\xfe"}, ErrBadFields},
		{"empty", "generic", map[string]string{}, ErrBadFields},
		{"field too large", "generic", map[string]string{"a": strings.Repeat("x", MaxFieldBytes+1)}, ErrTooLarge},
	}
	for _, c := range cases {
		typ, err := LookupType(c.typ)
		if err != nil {
			t.Fatal(err)
		}
		if err := typ.ValidateFields(c.fields); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := LookupType("nope"); !errors.Is(err, ErrBadType) {
		t.Errorf("unknown type err = %v", err)
	}
	typ, _ := LookupType("generic")
	big := map[string]string{}
	for i := range 5 {
		big["f"+string(rune('a'+i))] = strings.Repeat("x", MaxFieldBytes)
	}
	if err := typ.ValidateFields(big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("total too large err = %v", err)
	}
}

func validErrNoValue(t *testing.T, err error, value string) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), value) {
		t.Errorf("error leaks value: %v", err)
	}
}

func TestValidateFieldsNeverLeaksValue(t *testing.T) {
	typ, _ := LookupType("token")
	err := typ.ValidateFields(map[string]string{"token": "s3cr3t-value", "bogus": "s3cr3t-value"})
	validErrNoValue(t, err, "s3cr3t-value")
}

func newKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestKeyringParsing(t *testing.T) {
	good := newKey(t)
	for name, key := range map[string]string{
		"31 bytes":   base64.StdEncoding.EncodeToString(make([]byte, 31)),
		"33 bytes":   base64.StdEncoding.EncodeToString(make([]byte, 33)),
		"not base64": "!!!notbase64!!!",
		"empty":      "",
	} {
		if _, err := NewKeyring(key, ""); !errors.Is(err, ErrBadKEK) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := NewKeyring(good, "short"); !errors.Is(err, ErrBadKEK) {
		t.Errorf("bad previous: %v", err)
	}
	k, err := NewKeyring(good, newKey(t))
	if err != nil || k.ActiveID() == "" || k.PreviousID() == "" || k.ActiveID() == k.PreviousID() {
		t.Fatalf("keyring: %v %q %q", err, k.ActiveID(), k.PreviousID())
	}
	if len(k.ActiveID()) != 16 {
		t.Errorf("kek id length %d", len(k.ActiveID()))
	}
}

func TestSealOpen(t *testing.T) {
	old := newKey(t)
	k, _ := NewKeyring(old, "")
	fields := map[string]string{"password": "hunter2-value", "username": "root"}
	s, err := k.Seal(rand.Reader, "proj/x", "db", 3, fields)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(s.Ciphertext, []byte("hunter2-value")) {
		t.Fatal("plaintext in ciphertext")
	}
	got, err := k.Open("proj/x", "db", 3, s)
	if err != nil || got["password"] != "hunter2-value" || got["username"] != "root" {
		t.Fatalf("round trip: %v %v", got, err)
	}

	// Rotation: the new keyring has old as previous and still reads the row.
	k2, _ := NewKeyring(newKey(t), old)
	if got, err := k2.Open("proj/x", "db", 3, s); err != nil || got["password"] != "hunter2-value" {
		t.Fatalf("previous key read: %v %v", got, err)
	}
	// Without the previous key the row is unreadable.
	k3, _ := NewKeyring(newKey(t), "")
	if _, err := k3.Open("proj/x", "db", 3, s); !errors.Is(err, ErrUnknownKEK) {
		t.Fatalf("unknown kek: %v", err)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	k, _ := NewKeyring(newKey(t), "")
	s, err := k.Seal(rand.Reader, "proj", "db", 1, map[string]string{"a": "v"})
	if err != nil {
		t.Fatal(err)
	}
	flip := func(b []byte) []byte { c := append([]byte(nil), b...); c[len(c)-1] ^= 1; return c }
	cases := map[string]func() error{
		"scope":       func() error { _, e := k.Open("other", "db", 1, s); return e },
		"name":        func() error { _, e := k.Open("proj", "other", 1, s); return e },
		"version":     func() error { _, e := k.Open("proj", "db", 2, s); return e },
		"wrapped":     func() error { c := s; c.WrappedDEK = flip(s.WrappedDEK); _, e := k.Open("proj", "db", 1, c); return e },
		"ciphertext":  func() error { c := s; c.Ciphertext = flip(s.Ciphertext); _, e := k.Open("proj", "db", 1, c); return e },
		"nonce":       func() error { c := s; c.Nonce = flip(s.Nonce); _, e := k.Open("proj", "db", 1, c); return e },
		"short wrap":  func() error { c := s; c.WrappedDEK = []byte{1}; _, e := k.Open("proj", "db", 1, c); return e },
		"short nonce": func() error { c := s; c.Nonce = []byte{1}; _, e := k.Open("proj", "db", 1, c); return e },
	}
	for name, f := range cases {
		if err := f(); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: err = %v, want ErrDecrypt", name, err)
		}
	}
	// A ciphertext copied from another row (other scope/name) fails too.
	other, _ := k.Seal(rand.Reader, "proj", "other", 1, map[string]string{"a": "w"})
	c := s
	c.Ciphertext, c.Nonce = other.Ciphertext, other.Nonce
	if _, err := k.Open("proj", "db", 1, c); !errors.Is(err, ErrDecrypt) {
		t.Errorf("copied ciphertext: %v", err)
	}
}

func TestCanary(t *testing.T) {
	a, b := newKey(t), newKey(t)
	ka, _ := NewKeyring(a, "")
	raw, err := ka.NewCanary(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := ka.CheckCanary(raw); st != CanaryActive {
		t.Errorf("active: %v", st)
	}
	kb, _ := NewKeyring(b, a)
	if st, id := kb.CheckCanary(raw); st != CanaryPrevious || id != ka.ActiveID() {
		t.Errorf("previous: %v %q", st, id)
	}
	kc, _ := NewKeyring(b, "")
	if st, _ := kc.CheckCanary(raw); st != CanaryMismatch {
		t.Errorf("mismatch: %v", st)
	}
	if st, _ := ka.CheckCanary([]byte("garbage")); st != CanaryMismatch {
		t.Errorf("garbage: %v", st)
	}
}
