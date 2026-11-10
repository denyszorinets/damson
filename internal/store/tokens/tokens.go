// Package tokens generates and hashes pool tokens. Only the hash is ever
// persisted (ARCHITECTURE section 7.1); the plaintext is shown once at
// creation.
package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

// Prefix marks a Damson pool token; AdminPrefix the admin bootstrap token
// (ARCHITECTURE 12.2), which is never accepted where a pool token is.
const (
	Prefix      = "dmp_"
	AdminPrefix = "dma_"
	// MetricsPrefix marks the read-only token that scrapes /metrics.
	MetricsPrefix = "dmm_"
)

// MetricsSettingKey is the instance_settings key of the metrics token's hash;
// an empty value means it was revoked.
const MetricsSettingKey = "auth.metrics_token_hash"

// AdminSettingKey is the instance_settings key that holds the SHA-256 of the
// admin token; an empty value means the token was revoked.
const AdminSettingKey = "auth.admin_token_hash"

const secretBytes = 32

// Generate returns a new random token read from r (crypto/rand.Reader in
// production; injectable for tests) and its hash.
func Generate(r io.Reader) (plaintext, hash string, err error) {
	return GenerateWithPrefix(r, Prefix)
}

// GenerateWithPrefix is Generate for a token kind with its own prefix.
func GenerateWithPrefix(r io.Reader, prefix string) (plaintext, hash string, err error) {
	b := make([]byte, secretBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", "", fmt.Errorf("generating token: %w", err)
	}
	plaintext = prefix + base64.RawURLEncoding.EncodeToString(b)
	return plaintext, Hash(plaintext), nil
}

// New is Generate with crypto/rand.
func New() (plaintext, hash string, err error) { return Generate(rand.Reader) }

// Hash returns the lowercase hex SHA-256 of a token.
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
