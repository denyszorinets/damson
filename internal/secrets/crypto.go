package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Sealed is one encrypted secret version as stored.
type Sealed struct {
	KEKID      string
	WrappedDEK []byte // wrap nonce || GCM(KEK, DEK)
	Nonce      []byte
	Ciphertext []byte
}

// Errors of the crypto layer. They carry no secret material.
var (
	ErrUnknownKEK = errors.New("secret encrypted under an unknown KEK")
	ErrDecrypt    = errors.New("secret cannot be decrypted")
)

const aadPrefix = "damson-secret/v1\x00"

func dataAAD(scope, name string, version int64) []byte {
	return []byte(aadPrefix + scope + "\x00" + name + "\x00" + strconv.FormatInt(version, 10))
}

func wrapAAD(scope, name string, version int64, kekID string) []byte {
	return append(dataAAD(scope, name, version), []byte("\x00"+kekID)...)
}

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// Seal encrypts fields as version of scope/name under a fresh DEK wrapped by
// the active KEK. rand supplies the DEK and nonces.
func (k *Keyring) Seal(rand io.Reader, scope, name string, version int64, fields map[string]string) (Sealed, error) {
	plain, err := json.Marshal(fields)
	if err != nil {
		return Sealed{}, fmt.Errorf("encoding secret: %w", err)
	}
	defer clear(plain)
	dek := make([]byte, kekLen)
	defer clear(dek)
	if _, err := io.ReadFull(rand, dek); err != nil {
		return Sealed{}, fmt.Errorf("generating DEK: %w", err)
	}
	data, err := gcm(dek)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, data.NonceSize())
	if _, err := io.ReadFull(rand, nonce); err != nil {
		return Sealed{}, fmt.Errorf("generating nonce: %w", err)
	}
	wrapped, err := k.wrap(rand, k.active, k.activeID, scope, name, version, dek)
	if err != nil {
		return Sealed{}, err
	}
	return Sealed{
		KEKID:      k.activeID,
		WrappedDEK: wrapped,
		Nonce:      nonce,
		Ciphertext: data.Seal(nil, nonce, plain, dataAAD(scope, name, version)),
	}, nil
}

func (k *Keyring) wrap(rand io.Reader, kek []byte, kekID, scope, name string, version int64, dek []byte) ([]byte, error) {
	w, err := gcm(kek)
	if err != nil {
		return nil, err
	}
	n := make([]byte, w.NonceSize())
	if _, err := io.ReadFull(rand, n); err != nil {
		return nil, fmt.Errorf("generating wrap nonce: %w", err)
	}
	return w.Seal(n, n, dek, wrapAAD(scope, name, version, kekID)), nil
}

// Open decrypts s as version of scope/name. A mismatch of scope, name or
// version fails because they are bound in as additional data.
func (k *Keyring) Open(scope, name string, version int64, s Sealed) (map[string]string, error) {
	kek, ok := k.key(s.KEKID)
	if !ok {
		return nil, ErrUnknownKEK
	}
	w, err := gcm(kek)
	if err != nil {
		return nil, err
	}
	if len(s.WrappedDEK) < w.NonceSize() {
		return nil, ErrDecrypt
	}
	wn, wct := s.WrappedDEK[:w.NonceSize()], s.WrappedDEK[w.NonceSize():]
	dek, err := w.Open(nil, wn, wct, wrapAAD(scope, name, version, s.KEKID))
	if err != nil {
		return nil, ErrDecrypt
	}
	defer clear(dek)
	data, err := gcm(dek)
	if err != nil || len(s.Nonce) != data.NonceSize() {
		return nil, ErrDecrypt
	}
	plain, err := data.Open(nil, s.Nonce, s.Ciphertext, dataAAD(scope, name, version))
	if err != nil {
		return nil, ErrDecrypt
	}
	defer clear(plain)
	fields := map[string]string{}
	if err := json.Unmarshal(plain, &fields); err != nil {
		return nil, ErrDecrypt
	}
	return fields, nil
}
