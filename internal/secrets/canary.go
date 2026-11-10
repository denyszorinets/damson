package secrets

import (
	"encoding/json"
	"fmt"
	"io"
)

const canaryAAD = "damson-canary/v1"

var canaryPlain = []byte("damson-kek-canary")

// Canary proves which KEK an instance was initialized with. It is stored as
// JSON in instance_settings so a controller started with a wrong key refuses
// to run instead of writing secrets nobody can read back.
type Canary struct {
	KEKID string `json:"kek_id"`
	Nonce []byte `json:"nonce"`
	CT    []byte `json:"ct"`
}

// NewCanary encrypts the fixed plaintext under the active KEK.
func (k *Keyring) NewCanary(rand io.Reader) ([]byte, error) {
	g, err := gcm(k.active)
	if err != nil {
		return nil, err
	}
	n := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rand, n); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	return json.Marshal(Canary{KEKID: k.activeID, Nonce: n, CT: g.Seal(nil, n, canaryPlain, []byte(canaryAAD))})
}

// CanaryState is the result of checking a stored canary.
type CanaryState int

// Canary check results.
const (
	CanaryActive   CanaryState = iota // decrypts under the active key
	CanaryPrevious                    // decrypts under the previous key: rotate-kek is due
	CanaryMismatch                    // neither key matches
)

// CheckCanary decrypts a stored canary. A malformed value is a mismatch.
func (k *Keyring) CheckCanary(raw []byte) (CanaryState, string) {
	var c Canary
	if json.Unmarshal(raw, &c) != nil {
		return CanaryMismatch, ""
	}
	key, ok := k.key(c.KEKID)
	if !ok {
		return CanaryMismatch, c.KEKID
	}
	g, err := gcm(key)
	if err != nil || len(c.Nonce) != g.NonceSize() {
		return CanaryMismatch, c.KEKID
	}
	if _, err := g.Open(nil, c.Nonce, c.CT, []byte(canaryAAD)); err != nil {
		return CanaryMismatch, c.KEKID
	}
	if c.KEKID == k.activeID {
		return CanaryActive, c.KEKID
	}
	return CanaryPrevious, c.KEKID
}
