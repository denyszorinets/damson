package sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalPath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "a b.db")
	if err := os.WriteFile(real, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	tests := []struct{ name, dsn, want string }{
		{"plain", real, real},
		{"relative", "a b.db", real},
		{"symlink", link, real},
		{"file abs", "file:" + dir + "/a%20b.db", real},
		{"file triple slash", "file://" + dir + "/a%20b.db?cache=shared", real},
		{"file localhost", "file://localhost" + dir + "/a%20b.db", real},
		{"file opaque relative", "file:a%20b.db?mode=rwc", real},
		{"file symlink query", "file:" + link + "?_pragma=x", real},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalPath(tt.dsn); got != tt.want {
				t.Errorf("canonicalPath(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
		})
	}
}
