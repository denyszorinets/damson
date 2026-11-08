package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/denyszorinets/damson/internal/apply"
)

// --- export --------------------------------------------------------------

// runExport implements `damson export`.
func runExport(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	fset := flag.NewFlagSet("export", flag.ContinueOnError)
	fset.SetOutput(stderr)
	var h httpFlags
	h.bind(fset, environ)
	project := fset.String("project", "", "the project to export, with the projects below it (required)")
	out := fset.String("o", "", "the folder to write (required)")
	force := fset.Bool("force", false, "write into a folder that is not empty")
	if err := fset.Parse(args); err != nil {
		return exitUsage
	}
	if *project == "" || *out == "" || fset.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: damson export --project PATH -o DIR [--force] [--json] [--server URL] [--token-file FILE]")
		return exitUsage
	}
	cl, err := newAPIClient(h, environ)
	if err != nil {
		fmt.Fprintf(stderr, "damson: %v\n", err)
		return exitUsage
	}
	if !*force {
		if entries, err := os.ReadDir(*out); err == nil && len(entries) > 0 {
			fmt.Fprintf(stderr, "damson: %s is not empty; use --force to write into it\n", *out)
			return exitError
		}
	}
	data, err := cl.do(ctx, http.MethodGet, "/api/v1/export?project="+url.QueryEscape(*project), nil)
	if err != nil {
		return reportAPIError(stderr, err)
	}
	var bundle struct {
		Files []applyFile `json:"files"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		fmt.Fprintf(stderr, "damson: the answer is not valid: %v\n", err)
		return exitError
	}
	if err := writeBundle(*out, bundle.Files); err != nil {
		fmt.Fprintf(stderr, "damson: %v\n", err)
		return exitError
	}
	if h.json {
		paths := make([]string, len(bundle.Files))
		for i, f := range bundle.Files {
			paths[i] = f.Path
		}
		b, _ := json.Marshal(map[string]any{"written": paths})
		fmt.Fprintln(stdout, string(b))
		return exitOK
	}
	fmt.Fprintf(stdout, "wrote %d files to %s\n", len(bundle.Files), *out)
	return exitOK
}

// writeBundle writes files under dir, each through a temporary file and a
// rename, inside an os.Root so a symbolic link in dir cannot lead a write
// elsewhere. A path the server sends is held to the rules apply uses: a project
// folder plus project.json or *.job.json, so an export can never write .git/hooks
// or a script into a checkout.
func writeBundle(dir string, files []applyFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, f := range files {
		if err := checkExportPath(f.Path); err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := json.Indent(&buf, f.Content, "", "  "); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		buf.WriteByte('\n')
		folder := path.Dir(f.Path)
		if err := root.MkdirAll(filepath.FromSlash(folder), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", folder, err)
		}
		tmp := path.Join(folder, ".damson-export-tmp")
		if err := root.WriteFile(filepath.FromSlash(tmp), buf.Bytes(), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", f.Path, err)
		}
		if err := root.Rename(filepath.FromSlash(tmp), filepath.FromSlash(f.Path)); err != nil {
			_ = root.Remove(filepath.FromSlash(tmp))
			return fmt.Errorf("writing %s: %w", f.Path, err)
		}
	}
	return nil
}

func checkExportPath(p string) error {
	if msg := apply.CheckPath(p); msg != "" {
		return fmt.Errorf("the server sent an unsafe path %q: %s", p, msg)
	}
	dir, base := path.Split(p)
	if dir == "" || (base != apply.ProjectFile && !strings.HasSuffix(base, apply.JobSuffix)) {
		return fmt.Errorf("the server sent a path that is not a project or job file: %q", p)
	}
	return nil
}
