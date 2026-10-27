package config

import (
	"fmt"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// EnvPrefix prefixes every configuration environment variable. A double
// underscore separates sections from keys: DAMSON_AGENT__CONTROLLER_URL sets
// agent.controller_url.
const EnvPrefix = "DAMSON_"

// EnvConfigFile names the env var that supplies the config file path when no
// --config flag is given.
const EnvConfigFile = "DAMSON_CONFIG"

// Load reads defaults, the optional TOML file at path (empty means none), and
// the environment, then validates the result for role. environ is typically
// os.Environ().
func Load(role Role, path string, environ []string) (Config, error) {
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(defaults(), "."), nil); err != nil {
		return Config{}, fmt.Errorf("loading defaults: %w", err)
	}
	if path != "" {
		if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
			return Config{}, fmt.Errorf("reading config file %q: %w", path, err)
		}
	}
	if err := k.Load(envProvider(environ), nil); err != nil {
		return Config{}, fmt.Errorf("reading environment: %w", err)
	}

	var cfg Config
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		Tag: "koanf",
		DecoderConfig: &mapstructure.DecoderConfig{
			TagName:          "koanf",
			ErrorUnused:      true, // unknown keys are typos; fail loudly
			WeaklyTypedInput: true, // env values are strings
		},
	}); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	if err := cfg.Validate(role); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// FilePath resolves the config file: the flag value wins, then DAMSON_CONFIG.
func FilePath(flagValue string, environ []string) string {
	if flagValue != "" {
		return flagValue
	}
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, EnvConfigFile+"="); ok {
			return v
		}
	}
	return ""
}

func envProvider(environ []string) *env.Env {
	known := defaults()
	return env.Provider(".", env.Opt{
		Prefix:      EnvPrefix,
		EnvironFunc: func() []string { return environ },
		TransformFunc: func(k, v string) (string, any) {
			if k == EnvConfigFile {
				return "", nil // handled by FilePath; empty key is ignored
			}
			k = strings.TrimPrefix(k, EnvPrefix)
			k = strings.ReplaceAll(strings.ToLower(k), "__", ".")
			if _, ok := known[k]; !ok {
				return "", nil // not a config key (for example DAMSON_KEK)
			}
			return k, v
		},
	})
}
