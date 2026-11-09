package tlsreload

import (
	"bytes"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/testutil/selfsigned"
)

func cn(t *testing.T, r *Reloader) string {
	t.Helper()
	c, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	return string(c.Certificate[0])
}

func TestReloadsARenewedCertificate(t *testing.T) {
	dir := t.TempDir()
	a, b := selfsigned.New(t, "a"), selfsigned.New(t, "b")
	certFile, keyFile := a.Write(t, dir)
	logs := &bytes.Buffer{}
	r, err := New(certFile, keyFile, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	r.now = func() time.Time { return clock }
	first := cn(t, r)

	b.Write(t, dir)
	_ = os.Chtimes(certFile, clock.Add(time.Minute), clock.Add(time.Minute))
	if cn(t, r) != first {
		t.Fatal("reloaded before the check interval")
	}
	clock = clock.Add(CheckInterval + time.Second)
	if cn(t, r) == first {
		t.Fatal("the renewed certificate was not picked up")
	}
}

func TestBadRenewalKeepsTheOldCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := selfsigned.New(t, "a").Write(t, dir)
	logs := &bytes.Buffer{}
	r, err := New(certFile, keyFile, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	r.now = func() time.Time { return clock }
	first := cn(t, r)

	if err := os.WriteFile(certFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(CheckInterval + time.Second)
	if cn(t, r) != first {
		t.Fatal("a broken renewal replaced the working certificate")
	}
	if !bytes.Contains(logs.Bytes(), []byte("not reloaded")) {
		t.Errorf("no error logged: %s", logs)
	}
	n := bytes.Count(logs.Bytes(), []byte("not reloaded"))
	clock = clock.Add(CheckInterval + time.Second)
	cn(t, r)
	if bytes.Count(logs.Bytes(), []byte("not reloaded")) != n {
		t.Error("the same broken files were logged again")
	}
}

func TestStartRefusesAMismatchedPair(t *testing.T) {
	dir := t.TempDir()
	certFile, _ := selfsigned.New(t, "a").Write(t, dir)
	_, otherKey := selfsigned.New(t, "b").Write(t, t.TempDir())
	if _, err := New(certFile, otherKey, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); err == nil {
		t.Fatal("a certificate with another key was accepted")
	}
	if _, err := New(dir+"/missing.crt", otherKey, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); err == nil {
		t.Fatal("missing files were accepted")
	}
}

func TestHalfReplacedPairKeepsServingThenReloads(t *testing.T) {
	dir := t.TempDir()
	a, b := selfsigned.New(t, "a"), selfsigned.New(t, "b")
	certFile, keyFile := a.Write(t, dir)
	logs := &bytes.Buffer{}
	r, err := New(certFile, keyFile, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	r.now = func() time.Time { return clock }
	first := cn(t, r)

	// A sequential replacer writes the certificate first: cert B with key A.
	if err := os.WriteFile(certFile, b.CertPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(certFile, clock.Add(time.Minute), clock.Add(time.Minute))
	clock = clock.Add(CheckInterval + time.Second)
	if cn(t, r) != first {
		t.Fatal("a certificate without its key was served")
	}
	if bytes.Count(logs.Bytes(), []byte("not reloaded")) != 1 {
		t.Errorf("want one error for the half-replaced pair: %s", logs)
	}
	// Then the key lands.
	if err := os.WriteFile(keyFile, b.KeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(keyFile, clock.Add(time.Minute), clock.Add(time.Minute))
	clock = clock.Add(CheckInterval + time.Second)
	if cn(t, r) == first {
		t.Fatal("the completed pair was not loaded")
	}
}

func TestMissingFilesAreLoggedOnce(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := selfsigned.New(t, "a").Write(t, dir)
	logs := &bytes.Buffer{}
	r, err := New(certFile, keyFile, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	r.now = func() time.Time { return clock }
	first := cn(t, r)
	if err := os.Remove(certFile); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		clock = clock.Add(CheckInterval + time.Second)
		if cn(t, r) != first {
			t.Fatal("the certificate changed after its file vanished")
		}
	}
	if n := bytes.Count(logs.Bytes(), []byte("cannot be read")); n != 1 {
		t.Errorf("logged %d times, want once: %s", n, logs)
	}
}
