package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKEK(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "kek")
	if err := os.WriteFile(file, []byte("  from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	toml := filepath.Join(dir, "c.toml")
	if err := os.WriteFile(toml, []byte("[secrets]\nkek_file = '"+file+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(RoleController, "", []string{"DAMSON_KEK= from-env ", "DAMSON_KEK_PREVIOUS=prev"})
	if err != nil || c.Secrets.KEK != "from-env" || c.Secrets.KEKPrevious != "prev" {
		t.Fatalf("env: %+v %v", c.Secrets, err)
	}
	c, err = Load(RoleController, toml, nil)
	if err != nil || c.Secrets.KEK != "from-file" {
		t.Fatalf("file: %+v %v", c.Secrets, err)
	}
	if _, err = Load(RoleController, toml, []string{"DAMSON_KEK=x"}); err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("env and file: %v", err)
	}
	if c, err = Load(RoleController, "", nil); err != nil || c.Secrets.KEK != "" {
		t.Fatalf("unset: %+v %v", c.Secrets, err)
	}
	c, _ = Load(RoleController, "", []string{"DAMSON_KEK=topsecret"})
	if r := c.Redacted(); r.Secrets.KEK != "***" {
		t.Fatalf("KEK not redacted: %+v", r.Secrets)
	}
}

func TestLoadKEKMissingFile(t *testing.T) {
	toml := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(toml, []byte("[secrets]\nkek_file = '/nonexistent/kek'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(RoleController, toml, nil); err == nil || !strings.Contains(err.Error(), "secrets.kek_file") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadKEKRejectsEmptyFileAndOrphanPrevious(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	toml := filepath.Join(dir, "c.toml")
	if err := os.WriteFile(toml, []byte("[secrets]\nkek_file = '"+empty+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(RoleController, toml, nil); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file: %v", err)
	}
	if _, err := Load(RoleController, "", []string{"DAMSON_KEK_PREVIOUS=x"}); err == nil || !strings.Contains(err.Error(), "without") {
		t.Fatalf("previous without active: %v", err)
	}
}
