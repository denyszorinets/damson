package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/denyszorinets/damson/internal/apply"
)

// --- apply ---------------------------------------------------------------

type applyFile struct {
	Path    string          `json:"path"`
	Content json.RawMessage `json:"content"`
}

// readBundle walks dir and returns the files apply understands. Dot entries are
// skipped, a symlink is an error, other files are reported through warn.
func readBundle(dir string, warn func(string)) ([]applyFile, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	// WalkDir does not descend into a symbolic link given as the root, which
	// would send an empty bundle (and with --prune archive everything).
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	var files []applyFile
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; links are not followed", rel)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", rel)
		}
		base := path.Base(rel)
		if base != apply.ProjectFile && !strings.HasSuffix(base, apply.JobSuffix) {
			warn(rel + ": ignored (not " + apply.ProjectFile + " or *" + apply.JobSuffix + ")")
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > apply.MaxFileBytes {
			return fmt.Errorf("%s is larger than %d bytes", rel, apply.MaxFileBytes)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !json.Valid(b) {
			return fmt.Errorf("%s is not valid JSON", rel)
		}
		files = append(files, applyFile{Path: rel, Content: json.RawMessage(bytes.TrimSpace(b))})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no %s or *%s files", dir, apply.ProjectFile, apply.JobSuffix)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

type applyResponse struct {
	Source  string `json:"source"`
	DryRun  bool   `json:"dry_run"`
	Changes []struct {
		Kind        string   `json:"kind"`
		Project     string   `json:"project"`
		Name        string   `json:"name"`
		Action      string   `json:"action"`
		FromVersion int      `json:"from_version"`
		ToVersion   int      `json:"to_version"`
		Fields      []string `json:"fields"`
	} `json:"changes"`
	Summary map[string]int `json:"summary"`
}

var actionMark = map[string]string{
	"create": "+", "restore": "+", "update": "~", "adopt": "~", "archive": "-", "release": "-", "unchanged": "=",
}

func printApply(w io.Writer, r applyResponse) {
	for _, c := range r.Changes {
		what := "project " + c.Project
		if c.Kind == "job" {
			what = "job " + c.Project + "/" + c.Name
		}
		line := fmt.Sprintf("%s %-9s %s", actionMark[c.Action], c.Action, what)
		if c.Kind == "job" && c.ToVersion > 0 && c.Action != "unchanged" {
			if c.FromVersion > 0 && c.FromVersion != c.ToVersion {
				line += fmt.Sprintf(" (v%d -> v%d)", c.FromVersion, c.ToVersion)
			} else if c.FromVersion == 0 {
				line += fmt.Sprintf(" (v%d)", c.ToVersion)
			}
		}
		if len(c.Fields) > 0 {
			line += " [" + strings.Join(c.Fields, ", ") + "]"
		}
		fmt.Fprintln(w, line)
	}
	var parts []string
	for _, a := range []string{"create", "update", "adopt", "restore", "archive", "release", "unchanged"} {
		if n := r.Summary[a]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, a))
		}
	}
	if len(parts) == 0 {
		parts = []string{"nothing to do"}
	}
	suffix := ""
	if r.DryRun {
		suffix = " (dry run: nothing was written)"
	}
	fmt.Fprintf(w, "%s: %s%s\n", r.Source, strings.Join(parts, ", "), suffix)
}

func reportAPIError(stderr io.Writer, err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		var p problem
		if json.Unmarshal(ae.body, &p) == nil && (p.Detail != "" || p.Title != "") {
			fmt.Fprintf(stderr, "damson: %d %s\n", ae.status, strings.Join(p.lines(), "\n"))
			return exitError
		}
	}
	fmt.Fprintf(stderr, "damson: %v\n", err)
	return exitError
}

// runApply implements `damson apply`.
func runApply(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	fset := flag.NewFlagSet("apply", flag.ContinueOnError)
	fset.SetOutput(stderr)
	var h httpFlags
	h.bind(fset, environ)
	dir := fset.String("f", "", "the folder to apply (required)")
	source := fset.String("source", "", "the managing source, files:<name> (required)")
	prune := fset.Bool("prune", false, "archive the source's jobs that are no longer in the folder and release its projects")
	dry := fset.Bool("dry-run", false, "validate and show the changes without writing")
	adopt := fset.Bool("adopt", false, "take over projects and jobs nobody manages")
	if err := fset.Parse(args); err != nil {
		return exitUsage
	}
	if *dir == "" || *source == "" || fset.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: damson apply -f DIR --source files:NAME [--prune] [--dry-run] [--adopt] [--json] [--server URL] [--token-file FILE]")
		return exitUsage
	}
	cl, err := newAPIClient(h, environ)
	if err != nil {
		fmt.Fprintf(stderr, "damson: %v\n", err)
		return exitUsage
	}
	files, err := readBundle(*dir, func(m string) { fmt.Fprintf(stderr, "damson: warning: %s\n", m) })
	if err != nil {
		fmt.Fprintf(stderr, "damson: %v\n", err)
		return exitError
	}
	body := map[string]any{"source": *source, "prune": *prune, "dry_run": *dry, "adopt": *adopt, "files": files}
	data, err := cl.do(ctx, http.MethodPost, "/api/v1/apply", body)
	if err != nil {
		return reportAPIError(stderr, err)
	}
	if h.json {
		_, _ = stdout.Write(append(bytes.TrimSpace(data), '\n'))
		return exitOK
	}
	var res applyResponse
	if err := json.Unmarshal(data, &res); err != nil {
		fmt.Fprintf(stderr, "damson: the answer is not valid: %v\n", err)
		return exitError
	}
	printApply(stdout, res)
	return exitOK
}
