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
