package secrets_test

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/denyszorinets/damson/internal/secrets"
)

func newKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func TestRewrap(t *testing.T) {
	oldK, newK := newKey(t), newKey(t)
	oldRing, _ := secrets.NewKeyring(oldK, "")
	both, _ := secrets.NewKeyring(newK, oldK)
	sealed, err := oldRing.Seal(rand.Reader, "proj", "db", 3, map[string]string{"password": "pw"})
	if err != nil {
		t.Fatal(err)
	}

	id, wrapped, err := both.Rewrap(rand.Reader, "proj", "db", 3, sealed.KEKID, sealed.WrappedDEK)
	if err != nil || id != both.ActiveID() {
		t.Fatalf("Rewrap = %q, %v", id, err)
	}
	only, _ := secrets.NewKeyring(newK, "")
	got, err := only.Open("proj", "db", 3, secrets.Sealed{KEKID: id, WrappedDEK: wrapped, Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext})
	if err != nil || got["password"] != "pw" {
		t.Fatalf("Open after rewrap = %v, %v", got, err)
	}

	// Already under the active key: returned as it is.
	if id2, w2, err := both.Rewrap(rand.Reader, "proj", "db", 3, id, wrapped); err != nil || id2 != id || string(w2) != string(wrapped) {
		t.Fatalf("rewrap of an active row = %q, %v", id2, err)
	}
	for name, c := range map[string]struct {
		scope, name string
		version     int64
		kek         string
		want        error
	}{
		"unknown kek":   {"proj", "db", 3, "ffffffffffffffff", secrets.ErrUnknownKEK},
		"wrong scope":   {"other", "db", 3, sealed.KEKID, secrets.ErrDecrypt},
		"wrong name":    {"proj", "x", 3, sealed.KEKID, secrets.ErrDecrypt},
		"wrong version": {"proj", "db", 4, sealed.KEKID, secrets.ErrDecrypt},
	} {
		if _, _, err := both.Rewrap(rand.Reader, c.scope, c.name, c.version, c.kek, sealed.WrappedDEK); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}
