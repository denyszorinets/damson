package plugincli

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"text/template"
)

// pluginNameRe is the manifest rule for plugin names (pkg/pluginsdk).
var pluginNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// moduleRe is a conservative Go module path.
var moduleRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._~/-]*$`)

// scaffold is what the templates are filled with.
type scaffold struct {
	Name      string // plugin name, e.g. "my-plugin"
	Pkg       string // Go package name, e.g. "myplugin"
	Title     string // e.g. "My Plugin"
	Module    string
	GoVersion string
}

// runNew writes a plugin module: go.mod, the plugin, its test and a README.
func runNew(args []string, env Env) int {
	fs := newFlags("new", env)
	dir := fs.String("dir", "", "target directory (default ./damson-plugin-<name>)")
	module := fs.String("module", "", "Go module path (default example.com/damson-plugin-<name>)")
	pos, err := parse(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintln(env.Stderr, "damson plugin new: exactly one plugin name is required")
		return ExitUsage
	}
	sc, err := newScaffold(pos[0], *module)
	if err != nil {
		fmt.Fprintf(env.Stderr, "damson plugin new: %v\n", err)
		return ExitUsage
	}
	target := *dir
	if target == "" {
		target = "damson-plugin-" + sc.Name
	}
	if err := writeScaffold(target, sc); err != nil {
		fmt.Fprintf(env.Stderr, "damson plugin new: %v\n", err)
		return ExitError
	}
	fmt.Fprintf(env.Stdout, "created %s\n\nNext steps:\n  cd %s\n  go get github.com/denyszorinets/damson@<version>\n  go mod tidy\n  go test ./...\n", target, target)
	return ExitOK
}

func newScaffold(name, module string) (scaffold, error) {
	if !pluginNameRe.MatchString(name) {
		return scaffold{}, fmt.Errorf("plugin name %q must match %s", name, pluginNameRe)
	}
	if module == "" {
		module = "example.com/damson-plugin-" + name
	}
	if !moduleRe.MatchString(module) || strings.Contains(module, "..") {
		return scaffold{}, fmt.Errorf("module path %q is not a valid Go module path", module)
	}
	pkg := strings.NewReplacer("-", "", "_", "").Replace(name)
	if token.IsKeyword(pkg) || pkg == "main" || pkg == "plugin" || pkg == "pluginsdk" || pkg == "plugintest" ||
		pkg == "json" || pkg == "context" || pkg == "testing" {
		pkg += "plugin"
	}
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return scaffold{Name: name, Pkg: pkg, Title: strings.Join(words, " "), Module: module, GoVersion: goVersion()}, nil
}

// goVersion is the language version of the toolchain that built this binary,
// as the go directive wants it (1.24), with a fallback for development builds.
func goVersion() string {
	v := strings.TrimPrefix(runtime.Version(), "go")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 2 && regexp.MustCompile(`^\d+$`).MatchString(parts[0]) && regexp.MustCompile(`^\d+`).MatchString(parts[1]) {
		minor := regexp.MustCompile(`^\d+`).FindString(parts[1])
		return parts[0] + "." + minor
	}
	return "1.24"
}

func writeScaffold(dir string, sc scaffold) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s exists and is not empty", dir)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	files := map[string]string{
		"go.mod":            goModTmpl,
		sc.Pkg + ".go":      pluginTmpl,
		sc.Pkg + "_test.go": testTmpl,
		"README.md":         readmeTmpl,
	}
	rendered := map[string][]byte{}
	for name, text := range files {
		var buf bytes.Buffer
		if err := template.Must(template.New(name).Parse(text)).Execute(&buf, sc); err != nil {
			return fmt.Errorf("rendering %s: %w", name, err)
		}
		b := buf.Bytes()
		if strings.HasSuffix(name, ".go") {
			var err error
			if b, err = format.Source(b); err != nil {
				return fmt.Errorf("the generated %s is not valid Go (this is a bug): %w", name, err)
			}
		}
		rendered[name] = b
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // a source tree
		return err
	}
	for name, b := range rendered {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil { //nolint:gosec // source files
			return err
		}
	}
	return nil
}
