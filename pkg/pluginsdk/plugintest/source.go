package plugintest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/denyszorinets/damson"

// CheckSource statically checks the plugin package in dir against the
// conventions that make plugins loadable later (ARCHITECTURE.md section 6):
//
//   - it exports Plugins() []pluginsdk.Plugin
//   - it has no init() function
//   - it has no package-level mutable state (only blank vars, error sentinels
//     made with errors.New, and compiled regexps are allowed)
//   - it imports no Damson internals except pkg/pluginsdk and plugins/internal/*
//
// Test files are ignored.
func CheckSource(t *testing.T, dir string) {
	t.Helper()
	for _, p := range SourceProblems(t, dir) {
		t.Error(p)
	}
}

// SourceProblems returns the convention violations CheckSource would report.
func SourceProblems(t *testing.T, dir string) []string {
	t.Helper()
	files := parseDir(t, dir)
	var probs []string
	hasPlugins := false
	for fname, f := range files {
		probs = append(probs, checkImports(f, fname)...)
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.Name == "init" {
					probs = append(probs, fname+": init() is forbidden")
				}
				if d.Recv == nil && d.Name.Name == "Plugins" && isPluginsSig(d) {
					hasPlugins = true
				}
			case *ast.GenDecl:
				if d.Tok == token.VAR {
					probs = append(probs, checkVars(fname, d)...)
				}
			}
		}
	}
	if !hasPlugins {
		probs = append(probs, dir+": missing func Plugins() []pluginsdk.Plugin")
	}
	return probs
}

func checkImports(f *ast.File, fname string) (probs []string) {
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if !strings.HasPrefix(p, modulePath) {
			continue
		}
		rest := strings.TrimPrefix(p, modulePath+"/")
		ok := rest == "pkg/pluginsdk" || strings.HasPrefix(rest, "pkg/pluginsdk/") ||
			strings.HasPrefix(rest, "plugins/internal/")
		if !ok {
			probs = append(probs, fmt.Sprintf("%s: forbidden import %q (plugins may import only pkg/pluginsdk and plugins/internal/*)", fname, p))
		}
	}
	return probs
}

func isPluginsSig(d *ast.FuncDecl) bool {
	if d.Type.Params.NumFields() != 0 || d.Type.Results.NumFields() != 1 {
		return false
	}
	arr, ok := d.Type.Results.List[0].Type.(*ast.ArrayType)
	if !ok || arr.Len != nil {
		return false
	}
	sel, ok := arr.Elt.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Plugin"
}

func checkVars(fname string, d *ast.GenDecl) (probs []string) {
	for _, spec := range d.Specs {
		vs := spec.(*ast.ValueSpec)
		for i, n := range vs.Names {
			if n.Name == "_" {
				continue
			}
			if i < len(vs.Values) && len(vs.Names) == len(vs.Values) && isImmutableInit(vs.Values[i]) {
				continue
			}
			probs = append(probs, fmt.Sprintf("%s: package-level var %q is forbidden (no mutable package state)", fname, n.Name))
		}
	}
	return probs
}

func isImmutableInit(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch x.Name + "." + sel.Sel.Name {
	case "errors.New", "regexp.MustCompile":
		return true
	}
	return false
}

func parseDir(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		path := filepath.Join(dir, n)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		files[path] = f
	}
	if len(files) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	return files
}
