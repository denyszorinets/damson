package config

import (
	"fmt"
	"os"
	"strings"
)

// Environment variables that carry the key encryption key (ARCHITECTURE 9.2).
// They are not configuration keys: they never appear in a TOML file.
const (
	EnvKEK         = "DAMSON_KEK"
	EnvKEKPrevious = "DAMSON_KEK_PREVIOUS"
)

// Secrets configures the db secret backend. The KEK itself comes from the
// environment or a file, never from a config value.
type Secrets struct {
	KEKFile         string `koanf:"kek_file"`
	KEKPreviousFile string `koanf:"kek_previous_file"`

	// KEK and KEKPrevious are the resolved base64 keys; Load fills them.
	KEK, KEKPrevious string `koanf:"-"`
}

// loadKEKs resolves the keys: the environment variable or the file, not both.
func (c *Config) loadKEKs(environ []string) error {
	var err error
	if c.Secrets.KEK, err = resolveKEK(environ, EnvKEK, c.Secrets.KEKFile, "secrets.kek_file"); err != nil {
		return err
	}
	if c.Secrets.KEKPrevious, err = resolveKEK(environ, EnvKEKPrevious, c.Secrets.KEKPreviousFile, "secrets.kek_previous_file"); err != nil {
		return err
	}
	if c.Secrets.KEKPrevious != "" && c.Secrets.KEK == "" {
		return fmt.Errorf("%s is set without %s or secrets.kek_file", EnvKEKPrevious, EnvKEK)
	}
	return nil
}

func resolveKEK(environ []string, envName, file, fileKey string) (string, error) {
	var fromEnv string
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, envName+"="); ok {
			fromEnv = strings.TrimSpace(v)
		}
	}
	switch {
	case fromEnv != "" && file != "":
		return "", fmt.Errorf("%s and %s are both set; use one", envName, fileKey)
	case fromEnv != "":
		return fromEnv, nil
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", fileKey, err)
		}
		key := strings.TrimSpace(string(b))
		if key == "" {
			return "", fmt.Errorf("%s: the file is empty", fileKey)
		}
		return key, nil
	}
	return "", nil
}
