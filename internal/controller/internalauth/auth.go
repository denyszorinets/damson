// Package internalauth authenticates requests between controllers with an HMAC
// over a cluster secret (ADR-0040). A signature binds the method, path, query,
// caller, target controller, body hash and a timestamp, so a captured request
// cannot be replayed beyond the window, aimed at another controller or turned
// into another request. The endpoints it guards are read-only, so no nonce
// cache is kept.
package internalauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Header names of an authenticated request.
const (
	HeaderTimestamp = "X-Damson-Timestamp"
	HeaderCaller    = "X-Damson-Caller"
	HeaderSignature = "X-Damson-Signature"
)

// Window is how far a request's timestamp may differ from the verifier's clock.
const Window = 30 * time.Second

// MinSecretBytes is the shortest accepted secret.
const MinSecretBytes = 32

const (
	keyContext = "damson internal v1"
	sigPrefix  = "damson-internal-v1\n"
)

// Reasons a verification fails. They are safe to log and to count; nothing
// about the expected signature is ever included.
var (
	ErrMissing   = errors.New("missing authentication headers")
	ErrTimestamp = errors.New("timestamp outside the allowed window")
	ErrTarget    = errors.New("request is for another controller")
	ErrSignature = errors.New("signature mismatch")
)

// Keyring signs with the current secret and verifies against the current and
// the previous one, which allows rotating without downtime.
type Keyring struct {
	current  []byte
	previous []byte
}

// NewKeyring derives the signing keys. previous may be empty.
func NewKeyring(current, previous string) (*Keyring, error) {
	if len(current) < MinSecretBytes {
		return nil, fmt.Errorf("internalauth: the cluster secret must be at least %d bytes", MinSecretBytes)
	}
	k := &Keyring{current: derive(current)}
	if previous != "" {
		if len(previous) < MinSecretBytes {
			return nil, fmt.Errorf("internalauth: the previous cluster secret must be at least %d bytes", MinSecretBytes)
		}
		k.previous = derive(previous)
	}
	return k, nil
}

func derive(secret string) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(keyContext))
	return m.Sum(nil)
}

// Sign adds the authentication headers to a request for the controller target
// from the controller caller. The request must have no body.
func (k *Keyring) Sign(r *http.Request, caller, target string, now time.Time) {
	ts := strconv.FormatInt(now.Unix(), 10)
	r.Header.Set(HeaderTimestamp, ts)
	r.Header.Set(HeaderCaller, caller)
	r.Header.Set(HeaderSignature, mac(k.current, signed(r, ts, caller, target)))
}

// Verify checks the headers of a request addressed to the controller self and
// returns the caller's id. The error is one of the package's sentinels.
func (k *Keyring) Verify(r *http.Request, self string, now time.Time) (caller string, err error) {
	ts, caller, sig := r.Header.Get(HeaderTimestamp), r.Header.Get(HeaderCaller), r.Header.Get(HeaderSignature)
	if ts == "" || caller == "" || sig == "" {
		return "", ErrMissing
	}
	sec, perr := strconv.ParseInt(ts, 10, 64)
	if perr != nil {
		return "", ErrMissing
	}
	if d := now.Sub(time.Unix(sec, 0)); d > Window || d < -Window {
		return "", ErrTimestamp
	}
	msg := signed(r, ts, caller, self)
	for _, key := range [][]byte{k.current, k.previous} {
		if key != nil && hmac.Equal([]byte(sig), []byte(mac(key, msg))) {
			return caller, nil
		}
	}
	return "", ErrSignature
}

// signed is the string a signature covers. The target is the verifier's own
// id when verifying, so a request signed for another controller fails here.
func signed(r *http.Request, ts, caller, target string) string {
	body := sha256.Sum256(nil) // the guarded requests carry no body
	return sigPrefix + r.Method + "\n" + r.URL.EscapedPath() + "\n" + r.URL.Query().Encode() + "\n" +
		ts + "\n" + caller + "\n" + target + "\n" + hex.EncodeToString(body[:])
}

func mac(key []byte, msg string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
