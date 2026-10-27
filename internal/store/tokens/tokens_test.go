package tokens_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/store/tokens"
)

func TestNew(t *testing.T) {
	p1, h1, err := tokens.New()
	if err != nil {
		t.Fatal(err)
	}
	p2, h2, _ := tokens.New()
	if p1 == p2 || h1 == h2 {
		t.Fatal("tokens are not unique")
	}
	if !strings.HasPrefix(p1, tokens.Prefix) {
		t.Fatalf("token %q lacks prefix", p1)
	}
	if h1 != tokens.Hash(p1) || len(h1) != 64 {
		t.Fatalf("hash %q does not match Hash(plaintext)", h1)
	}
	if strings.Contains(h1, p1) || strings.Contains(p1, h1) {
		t.Fatal("hash and plaintext overlap")
	}
}

func TestHashKnownVector(t *testing.T) {
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" // sha256("abc")
	if got := tokens.Hash("abc"); got != want {
		t.Fatalf("Hash = %s; want %s", got, want)
	}
}

func TestGenerateReaderError(t *testing.T) {
	if _, _, err := tokens.Generate(bytes.NewReader([]byte("short"))); err == nil {
		t.Fatal("expected error on short entropy")
	}
}
