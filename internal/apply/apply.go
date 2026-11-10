// Package apply turns a bundle of job and project files into the writes that
// make the instance match it (ADR-0069). It is pure: Parse reads the files,
// Plan compares them with the stored state; the store runs the resulting ops.
// A future Git sync feeds the same functions with another source.
package apply

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/denyszorinets/damson/internal/store"
)

// Limits of a bundle.
const (
	MaxFiles     = 2000
	MaxFileBytes = 1 << 20
	MaxNameBytes = 128
)

// File formats.
const (
	APIVersion  = "damson.dev/v1"
	KindProject = "Project"
	ProjectFile = "project.json"
	JobSuffix   = ".job.json"
)

// File is one file of a bundle: a slash-separated path relative to the root and
// its JSON content.
type File struct {
	Path    string
	Content json.RawMessage
}

// Issue is one problem of a bundle. File is the index of the offending file, or
// -1 when it concerns the bundle as a whole; Field locates it inside the file.
type Issue struct {
	File int
	// Project is the project a plan problem is about, so the caller can check
	// its role before it reveals the problem. Empty for parse problems.
	Project string
	Path    string
	Field   string
	Message string
}

func (i Issue) String() string {
	loc := i.Path
	if i.Field != "" {
		loc += ": " + i.Field
	}
	if loc == "" {
		return i.Message
	}
	return loc + ": " + i.Message
}

// FieldIssue is a problem a Validator found inside a document.
type FieldIssue struct{ Field, Message string }

// Validator is what the caller supplies for the checks that need the running
// instance: the job spec rules (engine, plugins, secret refs) and the CEL
// access rules and node scope.
type Validator interface {
	// CheckJob validates a job document and returns the project and name it
	// declares and its checksum.
	CheckJob(raw json.RawMessage) (project, name, checksum string, issues []FieldIssue)
	// CheckProject validates a project's description, rules and node scope.
	CheckProject(description string, rules []store.AccessRule, nodeScope string) []FieldIssue
}

// Project is the declared state of a project.
type Project struct {
	Path        string
	Description string
	Rules       []store.AccessRule
	NodeScope   string
	File        int
}

// Job is the declared state of a job.
type Job struct {
	Project  string
	Name     string
	Spec     json.RawMessage
	Checksum string
	File     int
}

// Bundle is a parsed set of files. Projects are ordered parents first.
type Bundle struct {
	Projects []Project
	Jobs     []Job
}

type projectDoc struct {
	APIVersion  string `json:"apiVersion"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	AccessRules []struct {
		Role string `json:"role"`
		Rule string `json:"rule"`
	} `json:"access_rules"`
	NodeScope string `json:"node_scope"`
}

// ValidSource reports whether src may be used over the API: "files:<name>".
func ValidSource(src string) bool {
	return strings.HasPrefix(src, "files:") && store.ValidSource(src)
}

// Parse reads the files of a bundle. It reports every problem it finds, not
// the first.
func Parse(files []File, v Validator) (Bundle, []Issue) {
	var b Bundle
	var issues []Issue
	add := func(i int, path, field, format string, a ...any) {
		issues = append(issues, Issue{File: i, Path: path, Field: field, Message: fmt.Sprintf(format, a...)})
	}
	if len(files) > MaxFiles {
		add(-1, "", "", "a bundle holds at most %d files; got %d", MaxFiles, len(files))
		return b, issues
	}
	seen := map[string]bool{}
	projects := map[string]bool{}
	for i, f := range files {
		if msg := CheckPath(f.Path); msg != "" {
			add(i, f.Path, "", "%s", msg)
			continue
		}
		if seen[f.Path] {
			add(i, f.Path, "", "appears twice")
			continue
		}
		seen[f.Path] = true
		if len(f.Content) > MaxFileBytes {
			add(i, f.Path, "", "is larger than %d bytes", MaxFileBytes)
			continue
		}
		dir, base := splitPath(f.Path)
		switch {
		case dir == "":
			add(i, f.Path, "", "files must be inside a project folder")
		case base == ProjectFile:
			p, ps := parseProject(i, dir, f, v)
			issues = append(issues, ps...)
			if len(ps) == 0 {
				projects[dir] = true
				b.Projects = append(b.Projects, p)
			}
		case strings.HasSuffix(base, JobSuffix):
			j, js := parseJob(i, dir, base, f, v)
			issues = append(issues, js...)
			if len(js) == 0 {
				b.Jobs = append(b.Jobs, j)
			}
		default:
			add(i, f.Path, "", "is not %s or *%s", ProjectFile, JobSuffix)
		}
	}
	sort.SliceStable(b.Projects, func(i, j int) bool {
		di, dj := strings.Count(b.Projects[i].Path, "/"), strings.Count(b.Projects[j].Path, "/")
		if di != dj {
			return di < dj
		}
		return b.Projects[i].Path < b.Projects[j].Path
	})
	sort.SliceStable(b.Jobs, func(i, j int) bool {
		if b.Jobs[i].Project != b.Jobs[j].Project {
			return b.Jobs[i].Project < b.Jobs[j].Project
		}
		return b.Jobs[i].Name < b.Jobs[j].Name
	})
	return b, issues
}

func splitPath(p string) (dir, base string) {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

// CheckPath rejects paths that could escape the bundle or name an invalid
// project. It returns "" for a good path.
func CheckPath(p string) string {
	switch {
	case p == "":
		return "has an empty path"
	case strings.ContainsRune(p, 0) || strings.Contains(p, "\\"):
		return "path holds a NUL byte or a backslash"
	case strings.HasPrefix(p, "/"):
		return "path must be relative"
	}
	segs := strings.Split(p, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "path has an empty, . or .. segment"
		}
	}
	dir, base := splitPath(p)
	if len(base) > MaxNameBytes {
		return fmt.Sprintf("file name is longer than %d bytes", MaxNameBytes)
	}
	if dir != "" {
		if err := store.ValidateProjectPath(dir); err != nil {
			return "the folder is not a valid project path (1 to 8 segments of a-z, 0-9, -)"
		}
	}
	return ""
}

func decodeStrict(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("holds more than one JSON value")
	}
	return nil
}

func parseProject(i int, dir string, f File, v Validator) (Project, []Issue) {
	var doc projectDoc
	if err := decodeStrict(f.Content, &doc); err != nil {
		return Project{}, []Issue{{File: i, Path: f.Path, Message: "is not a valid project file: " + shortJSONError(err)}}
	}
	var issues []Issue
	add := func(field, msg string) {
		issues = append(issues, Issue{File: i, Path: f.Path, Field: field, Message: msg})
	}
	if doc.APIVersion != APIVersion {
		add("apiVersion", "must be "+APIVersion)
	}
	if doc.Kind != KindProject {
		add("kind", "must be "+KindProject)
	}
	rules := make([]store.AccessRule, len(doc.AccessRules))
	for k, r := range doc.AccessRules {
		rules[k] = store.AccessRule{Role: r.Role, Rule: r.Rule}
	}
	for _, fi := range v.CheckProject(doc.Description, rules, doc.NodeScope) {
		add(fi.Field, fi.Message)
	}
	if len(issues) > 0 {
		return Project{}, issues
	}
	return Project{Path: dir, Description: doc.Description, Rules: rules, NodeScope: doc.NodeScope, File: i}, nil
}

func parseJob(i int, dir, base string, f File, v Validator) (Job, []Issue) {
	name := strings.TrimSuffix(base, JobSuffix)
	var issues []Issue
	add := func(field, msg string) {
		issues = append(issues, Issue{File: i, Path: f.Path, Field: field, Message: msg})
	}
	project, declared, sum, fis := v.CheckJob(f.Content)
	for _, fi := range fis {
		add(fi.Field, fi.Message)
	}
	if len(fis) == 0 {
		if project != dir {
			add("metadata.project", fmt.Sprintf("is %q but the file is in the folder %q", project, dir))
		}
		if declared != name {
			add("metadata.name", fmt.Sprintf("is %q but the file name says %q", declared, name))
		}
	}
	if len(issues) > 0 {
		return Job{}, issues
	}
	return Job{Project: dir, Name: name, Spec: f.Content, Checksum: sum, File: i}, nil
}

// shortJSONError keeps a decoding error to one line without echoing content.
func shortJSONError(err error) string {
	msg := err.Error()
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return msg
}
