package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store/sqlite"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

// controllerFor serves the real API over a fresh SQLite database and returns
// its URL and an admin token.
func controllerFor(t *testing.T) (url, token string) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "damson.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrate(ctx))
	admin, hash, err := tokens.GenerateWithPrefix(rand.Reader, tokens.AdminPrefix)
	require.NoError(t, err)
	_, err = st.CreateSetting(ctx, tokens.AdminSettingKey, hash)
	require.NoError(t, err)
	srv := api.NewServer(api.Config{Addr: "127.0.0.1:0", Store: st, Data: st, UI: http.NotFoundHandler(), Now: time.Now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL, admin
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func job(project, name, host string) string {
	return `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"` + project + `","name":"` + name + `"},` +
		`"spec":{"steps":[{"id":"a","plugin":"ssh","with":{"host":"` + host + `"}}]}}`
}

func cmd(t *testing.T, env []string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(context.Background(), args, &o, &e, env)
	return code, o.String(), e.String()
}

func TestApplyAndExportRoundTrip(t *testing.T) {
	url, token := controllerFor(t)
	env := []string{envServer + "=" + url, envToken + "=" + token}
	src := t.TempDir()
	write(t, src, "infra/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project","description":"Infra"}`)
	write(t, src, "infra/billing/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project"}`)
	write(t, src, "infra/billing/deploy.job.json", job("infra/billing", "deploy", "h1"))
	write(t, src, "infra/README.md", "not a bundle file")
	write(t, src, ".git/config", "ignored")

	code, out, errOut := cmd(t, env, "apply", "-f", src, "--source", "files:main", "--dry-run")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "+ create    project infra")
	assert.Contains(t, out, "+ create    job infra/billing/deploy (v1)")
	assert.Contains(t, out, "dry run: nothing was written")
	assert.Contains(t, errOut, "README.md: ignored")
	assert.NotContains(t, errOut, ".git")

	code, out, errOut = cmd(t, env, "apply", "-f", src, "--source", "files:main")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "3 create")
	code, out, _ = cmd(t, env, "apply", "-f", src, "--source", "files:main")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "3 unchanged")

	// Export, then apply what was exported: no change.
	dst := filepath.Join(t.TempDir(), "out")
	code, out, errOut = cmd(t, env, "export", "--project", "infra", "-o", dst)
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "wrote 3 files")
	exported, err := os.ReadFile(filepath.Join(dst, "infra", "billing", "deploy.job.json"))
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(string(exported), "}\n"))
	assert.Contains(t, string(exported), "\n  \"metadata\"")
	code, out, errOut = cmd(t, env, "apply", "-f", dst, "--source", "files:main")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "3 unchanged")

	// A non-empty target is refused without --force.
	code, _, errOut = cmd(t, env, "export", "--project", "infra", "-o", dst)
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "not empty")

	// --json prints the answer as it is.
	code, out, _ = cmd(t, env, "apply", "-f", src, "--source", "files:main", "--json")
	require.Equal(t, 0, code)
	var res map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.Equal(t, "files:main", res["source"])
}

func TestApplyReportsProblems(t *testing.T) {
	url, token := controllerFor(t)
	env := []string{envServer + "=" + url, envToken + "=" + token}
	src := t.TempDir()
	write(t, src, "infra/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project"}`)
	write(t, src, "infra/a.job.json", job("infra", "wrong", "h"))
	write(t, src, "infra/b.job.json", job("other", "b", "h"))
	code, _, errOut := cmd(t, env, "apply", "-f", src, "--source", "files:main")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "infra/a.job.json")
	assert.Contains(t, errOut, "infra/b.job.json", "every problem is printed, not the first")

	code, _, errOut = cmd(t, env, "apply", "-f", src, "--source", "ui")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "422")
}

func TestApplyUsageAndToken(t *testing.T) {
	src := t.TempDir()
	code, _, _ := cmd(t, nil, "apply")
	assert.Equal(t, exitUsage, code)
	code, _, errOut := cmd(t, nil, "apply", "-f", src, "--source", "files:x")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "no token")
	code, _, errOut = cmd(t, []string{envToken + "=t"}, "apply", "-f", src, "--source", "files:x", "--server", "ftp://x")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "--server")

	// An empty folder is refused: with --prune it would archive everything.
	code, _, errOut = cmd(t, []string{envToken + "=t"}, "apply", "-f", src, "--source", "files:x", "--prune")
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "holds no project.json")
	write(t, src, "infra/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project"}`)

	// A token in a file works, and the secret is never printed.
	url, token := controllerFor(t)
	tf := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tf, []byte(token+"\n"), 0o600))
	code, out, errOut := cmd(t, nil, "apply", "-f", src, "--source", "files:x", "--server", url, "--token-file", tf)
	assert.Equal(t, 0, code, errOut)
	assert.NotContains(t, out+errOut, token)
	code, _, errOut = cmd(t, []string{envToken + "=dma_wrong"}, "apply", "-f", src, "--source", "files:x", "--server", url)
	assert.Equal(t, 1, code)
	assert.NotContains(t, errOut, "dma_wrong")
}

func TestReadBundleRefusesSymlinksAndBadJSON(t *testing.T) {
	root := t.TempDir()
	write(t, root, "infra/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project"}`)
	outside := filepath.Join(t.TempDir(), "secret.job.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{}`), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "infra", "link.job.json")))
	_, err := readBundle(root, func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symbolic link")

	root2 := t.TempDir()
	write(t, root2, "infra/bad.job.json", `{not json`)
	_, err = readBundle(root2, func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

func TestWriteBundleRefusesUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"../x.json", "/abs.json", "a/../../x.json", "a\\b.json", "", "./a.json"} {
		err := writeBundle(dir, []applyFile{{Path: p, Content: json.RawMessage(`{}`)}})
		assert.Error(t, err, "path %q", p)
	}
	entries, _ := os.ReadDir(filepath.Dir(dir))
	for _, e := range entries {
		assert.NotEqual(t, "x.json", e.Name())
	}
}

func TestReadBundleFollowsASymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	write(t, real, "infra/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project"}`)
	link := filepath.Join(t.TempDir(), "jobs")
	require.NoError(t, os.Symlink(real, link))
	files, err := readBundle(link, func(string) {})
	require.NoError(t, err)
	assert.Len(t, files, 1, "a linked root is resolved, not read as empty")
}

func TestWriteBundleOnlyWritesBundleFilesInsideTheRoot(t *testing.T) {
	dir := t.TempDir()
	body := json.RawMessage(`{}`)
	for _, p := range []string{".git/hooks/post-checkout", "infra/evil.sh", "project.json", "infra/notes.txt", "infra/.hidden/project.json"} {
		assert.Error(t, writeBundle(dir, []applyFile{{Path: p, Content: body}}), "path %q", p)
	}
	// A directory component that is a symlink must not lead the write elsewhere.
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "infra")))
	assert.Error(t, writeBundle(dir, []applyFile{{Path: "infra/project.json", Content: body}}))
	entries, _ := os.ReadDir(outside)
	assert.Empty(t, entries, "nothing was written through the link")
	// A good bundle is written and indented.
	good := t.TempDir()
	require.NoError(t, writeBundle(good, []applyFile{{Path: "a/b/project.json", Content: json.RawMessage(`{"k":1}`)}}))
	b, err := os.ReadFile(filepath.Join(good, "a", "b", "project.json"))
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"k\": 1\n}\n", string(b))
}
