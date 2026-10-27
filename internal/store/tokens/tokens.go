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

// Prefix marks a Damson pool token.
const Prefix = "dmp_"

const secretBytes = 32

// Generate returns a new random token read from r (crypto/rand.Reader in
// production; injectable for tests) and its hash.
func Generate(r io.Reader) (plaintext, hash string, err error) {
	b := make([]byte, secretBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", "", fmt.Errorf("generating pool token: %w", err)
	}
	plaintext = Prefix + base64.RawURLEncoding.EncodeToString(b)
	return plaintext, Hash(plaintext), nil
}

// New is Generate with crypto/rand.
func New() (plaintext, hash string, err error) { return Generate(rand.Reader) }

// Hash returns the lowercase hex SHA-256 of a token.
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
