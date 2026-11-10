package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Canonical re-encodes a JSON document canonically: object keys sorted,
// no insignificant whitespace, numbers kept verbatim, no HTML escaping.
// Two documents with the same content in different key order or formatting
// yield identical bytes.
func Canonical(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decoding JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("decoding JSON: trailing data after document")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil { // maps encode with sorted keys
		return nil, fmt.Errorf("encoding canonical JSON: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Checksum returns the hex SHA-256 of the canonical form of a spec document.
// It is the JobVersion checksum (§4).
func Checksum(data []byte) (string, error) {
	c, err := Canonical(data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}

// Checksum returns the checksum of the job's canonical JSON form.
func (j *Job) Checksum() (string, error) {
	b, err := json.Marshal(j)
	if err != nil {
		return "", fmt.Errorf("marshaling job: %w", err)
	}
	return Checksum(b)
}
