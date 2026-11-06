package plugincli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// readSecrets reads the secrets file: a JSON object of secret:// refs, each a
// string (the "value" field) or an object of string fields:
//
//	{"secret://demo/token": "abc", "secret://demo/db": {"username": "u", "password": "p"}}
//
// The file must be a regular file with mode 0600 or stricter, checked on the
// open file, and at most maxSecretsFile bytes. Errors name refs and fields,
// never values.
func readSecrets(path string) (map[string]map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening the secrets file: %w", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("reading the secrets file: %w", err)
	}
	switch {
	case !fi.Mode().IsRegular():
		return nil, errors.New("the secrets file must be a regular file")
	case fi.Mode().Perm()&0o077 != 0:
		return nil, fmt.Errorf("the secrets file has mode %04o; it must be 0600 or stricter", fi.Mode().Perm())
	case fi.Size() > maxSecretsFile:
		return nil, fmt.Errorf("the secrets file is larger than %d bytes", maxSecretsFile)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSecretsFile+1))
	if err != nil {
		return nil, fmt.Errorf("reading the secrets file: %w", err)
	}
	defer clear(b)
	return parseSecrets(b)
}

func parseSecrets(b []byte) (map[string]map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		// The decoder's message can quote input; say only where the file is wrong.
		return nil, errors.New("the secrets file is not a JSON object of secret:// refs")
	}
	out := make(map[string]map[string]string, len(raw))
	for ref, v := range raw {
		if !strings.HasPrefix(ref, "secret://") || strings.ContainsAny(ref, "# \t\r\n") {
			return nil, fmt.Errorf("secrets file key %q is not a secret:// ref without a #field", ref)
		}
		v = bytes.TrimSpace(v)
		switch {
		case len(v) > 0 && v[0] == '"':
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, fmt.Errorf("secret %s is not valid", ref)
			}
			out[ref] = map[string]string{"value": s}
		case len(v) > 0 && v[0] == '{':
			var fields map[string]string
			if err := json.Unmarshal(v, &fields); err != nil {
				return nil, fmt.Errorf("secret %s must be a string or an object of string fields", ref)
			}
			out[ref] = fields
		default:
			return nil, fmt.Errorf("secret %s must be a string or an object of string fields", ref)
		}
	}
	return out, nil
}
