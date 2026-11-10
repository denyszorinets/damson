package tlsroots

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

func TestPool(t *testing.T) {
	if _, err := Pool(""); err != nil {
		t.Fatalf("system roots only: %v", err)
	}
	if _, err := Pool("not pem"); !errors.Is(err, ErrBadPEM) {
		t.Fatalf("bad pem: %v", err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Pool(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))); err != nil {
		t.Fatalf("good pem: %v", err)
	}
}
