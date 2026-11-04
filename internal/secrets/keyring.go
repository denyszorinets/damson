package secrets

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const kekLen = 32

// ErrBadKEK is returned for a key that is not 32 bytes of standard base64.
var ErrBadKEK = errors.New("invalid KEK: want 32 bytes, standard base64")

// KeyID identifies a KEK without revealing it.
func KeyID(key []byte) string {
	h := sha256.Sum256(append([]byte("damson-kek-id/v1"), key...))
	return hex.EncodeToString(h[:])[:16]
}

// Keyring holds the active KEK and optionally the previous one, which is
// kept so rows wrapped under it stay readable until rotate-kek ran.
type Keyring struct {
	active, previous     []byte
	activeID, previousID string
}

// ParseKEK decodes a base64 KEK.
func ParseKEK(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != kekLen {
		return nil, ErrBadKEK
	}
	return b, nil
}

// NewKeyring builds a keyring from base64 keys; previous may be empty.
func NewKeyring(active, previous string) (*Keyring, error) {
	a, err := ParseKEK(active)
	if err != nil {
		return nil, fmt.Errorf("active key: %w", err)
	}
	k := &Keyring{active: a, activeID: KeyID(a)}
	if strings.TrimSpace(previous) != "" {
		p, err := ParseKEK(previous)
		if err != nil {
			return nil, fmt.Errorf("previous key: %w", err)
		}
		k.previous, k.previousID = p, KeyID(p)
	}
	return k, nil
}

// ActiveID is the kek_id new versions are wrapped under.
func (k *Keyring) ActiveID() string { return k.activeID }

// PreviousID is the kek_id of the previous key, or "".
func (k *Keyring) PreviousID() string { return k.previousID }

// key returns the key with the given id.
func (k *Keyring) key(id string) ([]byte, bool) {
	switch {
	case id == k.activeID:
		return k.active, true
	case k.previous != nil && id == k.previousID:
		return k.previous, true
	}
	return nil, false
}
