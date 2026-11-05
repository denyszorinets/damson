package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/storefactory"
)

func devCfg(t *testing.T) (config.Config, string) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "damson.db")
	return config.Config{Database: config.Database{Driver: config.DriverSQLite, DSN: db}}, db + ".kek"
}

// devStore opens and migrates the database of cfg, as the dev role does before
// it asks for a key.
func devStore(t *testing.T, cfg config.Config) store.Store {
	t.Helper()
	st, err := storefactory.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

// noTempFiles fails if a key temp file was left next to the database.
func noTempFiles(t *testing.T, keyPath string) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Dir(keyPath))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func logger() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewJSONHandler(&b, nil)), &b
}

func TestDevKEKIsGeneratedAndReused(t *testing.T) {
	cfg, path := devCfg(t)
	ctx, st := context.Background(), devStore(t, cfg)
	log, logs := logger()
	got, err := ensureDevKEK(ctx, st, &cfg, log)
	if err != nil || got != path {
		t.Fatalf("ensureDevKEK = %q, %v", got, err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	raw, err := secrets.ParseKEK(cfg.Secrets.KEK)
	if err != nil {
		t.Fatalf("generated key is not valid: %v", err)
	}
	if strings.Contains(logs.String(), cfg.Secrets.KEK) || !strings.Contains(logs.String(), secrets.KeyID(raw)) {
		t.Fatalf("log must carry the kek_id and never the key: %s", logs.String())
	}
	noTempFiles(t, path)
	first := cfg.Secrets.KEK

	again := config.Config{Database: cfg.Database}
	if _, err := ensureDevKEK(ctx, st, &again, log); err != nil || again.Secrets.KEK != first {
		t.Fatalf("restart must reuse the key: %v", err)
	}
}

func TestDevKEKConfiguredKeyWins(t *testing.T) {
	cfg, path := devCfg(t)
	ctx, st := context.Background(), devStore(t, cfg)
	cfg.Secrets.KEK = base64.StdEncoding.EncodeToString(make([]byte, 32))
	log, logs := logger()
	if got, err := ensureDevKEK(ctx, st, &cfg, log); err != nil || got != "" {
		t.Fatalf("ensureDevKEK = %q, %v", got, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a key file was created although a KEK is configured")
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureDevKEK(ctx, st, &cfg, log); err != nil || !strings.Contains(logs.String(), "ignored") {
		t.Fatalf("an existing file must be ignored with a warning: %v %s", err, logs.String())
	}
}

func TestDevKEKRefusesBadFiles(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(make([]byte, 32)) + "\n"
	for name, tc := range map[string]struct {
		content string
		mode    os.FileMode
		want    string
	}{
		"group readable": {good, 0o640, "chmod 600"},
		"world readable": {good, 0o644, "chmod 600"},
		"empty":          {"", 0o600, "valid key"},
		"damaged":        {"not-base64!!", 0o600, "valid key"},
		"short":          {base64.StdEncoding.EncodeToString(make([]byte, 16)), 0o600, "valid key"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, path := devCfg(t)
			ctx, st := context.Background(), devStore(t, cfg)
			if err := os.WriteFile(path, []byte(tc.content), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			log, _ := logger()
			_, err := ensureDevKEK(ctx, st, &cfg, log)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("err = %v, want one naming the path and %q", err, tc.want)
			}
			if cfg.Secrets.KEK != "" {
				t.Fatal("a refused file still set the key")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		cfg, path := devCfg(t)
		ctx, st := context.Background(), devStore(t, cfg)
		target := filepath.Join(filepath.Dir(path), "elsewhere")
		if err := os.WriteFile(target, []byte(good), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		log, _ := logger()
		if _, err := ensureDevKEK(ctx, st, &cfg, log); err == nil {
			t.Fatal("a symlink was accepted")
		}
	})
}

// Two dev processes starting together end up with the same key.
func TestDevKEKConcurrentStart(t *testing.T) {
	base, path := devCfg(t)
	ctx, st := context.Background(), devStore(t, base)
	db := strings.TrimSuffix(path, ".kek")
	keys := make([]string, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := config.Config{Database: config.Database{Driver: config.DriverSQLite, DSN: db}}
			log, _ := logger()
			if _, err := ensureDevKEK(ctx, st, &cfg, log); err != nil {
				t.Errorf("ensureDevKEK: %v", err)
			}
			keys[i] = cfg.Secrets.KEK
		}()
	}
	wg.Wait()
	for _, k := range keys {
		if k == "" || k != keys[0] {
			t.Fatalf("keys differ: %v", keys)
		}
	}
	noTempFiles(t, path)
}

func startAndStop(t *testing.T, env []string, args ...string) (code int, log string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var errb syncBuf
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, io.Discard, &errb, env) }()
	// Either the server listens (then stop it) or the start fails by itself.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case c := <-done:
			return c, errb.String()
		default:
		}
		if strings.Contains(errb.String(), "http server listening") {
			cancel()
			return <-done, errb.String()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("neither listening nor failed; log: %s", errb.String())
	return 0, ""
}

// The dev role makes a key next to the database, reuses it on restart and,
// when the key is lost while the database holds its canary, refuses to start
// without making a useless new key; restoring the old file works. A controller
// never makes a key.
func TestDevRoleKeyFile(t *testing.T) {
	env := ctrlEnv(t)
	dsn := strings.TrimPrefix(env[0], "DAMSON_DATABASE__DSN=")
	keyFile := dsn + ".kek"

	if code, log := startAndStop(t, env, "dev"); code != exitOK {
		t.Fatalf("first start: exit %d; %s", code, log)
	}
	fi, err := os.Stat(keyFile)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file after first start: %v %v", fi, err)
	}
	key, _ := os.ReadFile(keyFile)
	if code, log := startAndStop(t, env, "dev"); code != exitOK || strings.Contains(log, strings.TrimSpace(string(key))) {
		t.Fatalf("restart: exit %d; %s", code, log)
	}

	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	// A controller cannot read the canary without a key and does not make one.
	code, log := startAndStop(t, env, "controller", "-auto-migrate")
	if code == exitOK || !strings.Contains(log, "no KEK is configured") {
		t.Fatalf("controller without a KEK on a database with a canary: exit %d; %s", code, log)
	}
	if _, err := os.Stat(keyFile); err == nil {
		t.Fatal("a controller generated a key file")
	}
	// The dev role refuses too, names the file and the files to delete, and
	// leaves no new key behind.
	code, log = startAndStop(t, env, "dev")
	if code == exitOK || !strings.Contains(log, keyFile) || !strings.Contains(log, "restore it") || !strings.Contains(log, dsn+"-wal") {
		t.Fatalf("lost key: exit %d; %s", code, log)
	}
	if _, err := os.Stat(keyFile); err == nil {
		t.Fatal("the dev role made a new key for a database that holds secrets")
	}
	// Restoring the file brings it back.
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, log := startAndStop(t, env, "dev"); code != exitOK {
		t.Fatalf("after restoring the key: exit %d; %s", code, log)
	}
}

// A controller on a fresh database starts without secrets and makes no key file.
func TestControllerMakesNoKeyFile(t *testing.T) {
	env := ctrlEnv(t)
	dsn := strings.TrimPrefix(env[0], "DAMSON_DATABASE__DSN=")
	code, log := startAndStop(t, env, "controller", "-auto-migrate", "-insecure-dev")
	if code != exitOK || !strings.Contains(log, "secrets are disabled") {
		t.Fatalf("exit %d; %s", code, log)
	}
	if _, err := os.Stat(dsn + ".kek"); err == nil {
		t.Fatal("a controller generated a key file")
	}
}
