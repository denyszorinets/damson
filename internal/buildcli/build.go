package buildcli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/denyszorinets/damson/internal/version"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

const usage = `Usage: damson build [flags]

Builds a custom damson binary with extra plugin modules linked in.

  --with module[@version][=dir]   add a plugin module (repeatable); the module's
                                  root package must export Plugins()
  --damson-version v              damson version to build against
                                  (default: the version of this binary)
  -o file                         output file (default ./damson)
  --keep-workdir                  keep the temporary build module

Use --with ` + DamsonModule + `=<dir> to build against a checkout.
Extra plugins are fully trusted code; see docs/reference/cli.
`

// Env is what Run needs from its caller.
type Env struct {
	Stdout, Stderr io.Writer
	Environ        []string
	BuildInfo      *debug.BuildInfo // nil: read from this binary
	Run            Runner           // nil: run the go tool
}

// Runner runs the go tool in dir and returns its standard output.
type Runner func(ctx context.Context, dir string, environ []string, args ...string) (string, error)

type withFlags []string

func (w *withFlags) String() string     { return strings.Join(*w, ",") }
func (w *withFlags) Set(v string) error { *w = append(*w, v); return nil }

// Run is "damson build".
func Run(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("damson build", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprint(env.Stderr, usage) }
	var with withFlags
	fs.Var(&with, "with", "plugin module")
	damsonVer := fs.String("damson-version", "", "damson version")
	out := fs.String("o", "damson", "output file")
	keep := fs.Bool("keep-workdir", false, "keep the build module")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		return ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "damson build: unexpected argument %q\n", fs.Arg(0))
		return ExitUsage
	}
	withs, err := parseAll(with, *damsonVer)
	if err != nil {
		fmt.Fprintf(env.Stderr, "damson build: %v\n", err)
		return ExitUsage
	}
	if len(withs) == 0 {
		fmt.Fprint(env.Stderr, "damson build: nothing to build: give at least one --with module\n\n"+usage)
		return ExitUsage
	}
	if err := build(ctx, withs, *damsonVer, *out, *keep, env); err != nil {
		fmt.Fprintf(env.Stderr, "damson build: %v\n", err)
		return ExitError
	}
	return ExitOK
}

func parseAll(specs []string, damsonVer string) ([]With, error) {
	var withs []With
	seen := map[string]bool{}
	for _, s := range specs {
		w, err := parseWith(s)
		if err != nil {
			return nil, err
		}
		if seen[w.Path] {
			return nil, fmt.Errorf("module %s is given twice", w.Path)
		}
		seen[w.Path] = true
		if w.Path == DamsonModule && damsonVer != "" {
			return nil, errors.New("--damson-version cannot be combined with a replacement of the damson module")
		}
		withs = append(withs, w)
	}
	if damsonVer != "" && !damsonVersionRe.MatchString(damsonVer) {
		return nil, fmt.Errorf("invalid --damson-version %q", damsonVer)
	}
	return withs, nil
}

func build(ctx context.Context, withs []With, damsonVer, out string, keep bool, env Env) error {
	var damsonDir string
	var extras []With
	for _, w := range withs {
		if w.Path == DamsonModule {
			damsonDir = w.Dir
			continue
		}
		extras = append(extras, w)
	}
	linkVersion := "dev"
	_ = version.Version
	if damsonDir == "" {
		if damsonVer == "" {
			bi := env.BuildInfo
			if bi == nil {
				bi, _ = debug.ReadBuildInfo()
			}
			v, err := damsonVersion(bi)
			if err != nil {
				return err
			}
			damsonVer = v
		}
		linkVersion = damsonVer
	}
	if len(extras) == 0 {
		return errors.New("nothing to build: give a plugin module besides damson itself")
	}
	mainSrc, err := generateMain(extras)
	if err != nil {
		return fmt.Errorf("generating main: %w", err)
	}
	runner := env.Run
	if runner == nil {
		runner = func(ctx context.Context, dir string, environ []string, args ...string) (string, error) {
			return runGo(ctx, dir, environ, env.Stderr, args...)
		}
	}
	goEnv := buildEnv(env.Environ)
	warnInsecure(env.Environ, env.Stderr)

	work, err := os.MkdirTemp("", "damson-build-*")
	if err != nil {
		return err
	}
	if keep {
		fmt.Fprintf(env.Stderr, "damson build: keeping %s\n", work)
	} else {
		defer func() { _ = os.RemoveAll(work) }()
	}
	if err := writeModule(work, damsonDir, extras, mainSrc); err != nil {
		return err
	}
	modfile := "-modfile=" + filepath.Join(work, "go.mod")
	step := func(args ...string) (string, error) {
		fmt.Fprintf(env.Stderr, "+ go %s\n", strings.Join(args, " "))
		o, err := runner(ctx, work, goEnv, args...)
		if err != nil {
			return o, fmt.Errorf("go %s: %w", args[0], err)
		}
		return o, nil
	}
	checkDamson := func() error {
		got, err := step("list", modfile, "-mod=mod", "-m", "-f", "{{.Version}}", DamsonModule)
		if err != nil {
			return err
		}
		if got = strings.TrimSpace(got); got != damsonVer {
			return fmt.Errorf("a plugin module requires damson %s, not %s; pass --damson-version %s", got, damsonVer, got)
		}
		return nil
	}
	var get []string
	if damsonDir == "" {
		get = append(get, DamsonModule+"@"+damsonVer)
	}
	for _, w := range extras {
		if w.Dir == "" {
			get = append(get, w.Path+"@"+w.Version)
		}
	}
	if len(get) > 0 {
		if _, err := step(append([]string{"get", modfile, "--"}, get...)...); err != nil {
			return err
		}
	}
	if damsonDir == "" {
		if err := checkDamson(); err != nil {
			return err
		}
	}
	bin := filepath.Join(work, "damson")
	if runtime.GOOS == "windows" || envValue(goEnv, "GOOS") == "windows" {
		bin += ".exe"
	}
	ldflags := "-X=" + DamsonModule + "/internal/version.Version=" + linkVersion
	if _, err := step("build", modfile, "-mod=mod", "-trimpath", "-ldflags="+ldflags, "-o", bin, "."); err != nil {
		if strings.Contains(err.Error(), "Plugins") || strings.Contains(err.Error(), "no Go files") {
			err = fmt.Errorf("%w (each module's root package must export func Plugins() []pluginsdk.Plugin)", err)
		}
		return err
	}
	if damsonDir == "" {
		// -mod=mod may have added a module and raised damson.
		if err := checkDamson(); err != nil {
			return err
		}
	}
	if err := install(bin, out); err != nil {
		return err
	}
	fmt.Fprintf(env.Stderr, "damson build: wrote %s (damson %s", out, linkVersion)
	for _, w := range extras {
		fmt.Fprintf(env.Stderr, ", %s", w.Path)
	}
	fmt.Fprintln(env.Stderr, ")")
	return nil
}

// writeModule writes go.mod, a go.sum seeded from the local directories and main.go.
func writeModule(work, damsonDir string, extras []With, mainSrc []byte) error {
	var mod strings.Builder
	mod.WriteString("module damson-custom-build\n\ngo " + goLanguageVersion() + "\n")
	dirs := map[string]string{}
	if damsonDir != "" {
		dirs[DamsonModule] = damsonDir
	}
	for _, w := range extras {
		if w.Dir != "" {
			dirs[w.Path] = w.Dir
		}
	}
	for p := range dirs {
		fmt.Fprintf(&mod, "\nrequire %s v0.0.0\nreplace %s => %s\n", p, p, strconv.Quote(dirs[p]))
	}
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte(mod.String()), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(work, "main.go"), mainSrc, 0o600); err != nil {
		return err
	}
	// go.sum lines are taken only from directories the user named: a line makes
	// Go skip the checksum database for that module.
	seen := map[string]bool{}
	var sum bytes.Buffer
	for _, dir := range dirs {
		f, err := os.Open(filepath.Join(dir, "go.sum"))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			if l := sc.Text(); l != "" && !seen[l] {
				seen[l] = true
				sum.WriteString(l + "\n")
			}
		}
		_ = f.Close()
	}
	return os.WriteFile(filepath.Join(work, "go.sum"), sum.Bytes(), 0o600)
}

// buildEnv adds CGO_ENABLED=0 unless set, and always GOWORK=off.
func buildEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+2)
	for _, e := range environ {
		if !strings.HasPrefix(e, "GOWORK=") {
			out = append(out, e)
		}
	}
	if envValue(environ, "CGO_ENABLED") == "" {
		out = append(out, "CGO_ENABLED=0")
	}
	return append(out, "GOWORK=off")
}

func envValue(environ []string, key string) string {
	v := ""
	for _, e := range environ {
		if strings.HasPrefix(e, key+"=") {
			v = strings.TrimPrefix(e, key+"=")
		}
	}
	return v
}

// warnInsecure warns about operator settings that weaken module verification.
func warnInsecure(environ []string, w io.Writer) {
	if envValue(environ, "GOSUMDB") == "off" || envValue(environ, "GONOSUMDB") != "" ||
		envValue(environ, "GOINSECURE") != "" || strings.Contains(envValue(environ, "GOFLAGS"), "-insecure") {
		fmt.Fprintln(w, "damson build: warning: module checksum verification is weakened by your Go environment (GOSUMDB, GONOSUMDB, GOINSECURE or GOFLAGS)")
	}
}

func runGo(ctx context.Context, dir string, environ []string, stderr io.Writer, args ...string) (string, error) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return "", errors.New("the go tool is not in PATH; install Go " + strings.TrimPrefix(runtime.Version(), "go") + " or newer")
	}
	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Dir, cmd.Env, cmd.Stderr = dir, environ, stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	err = cmd.Run()
	return out.String(), err
}

// install copies bin next to out and renames it into place. The temporary
// file is created exclusively, so a planted symlink is never followed.
func install(bin, out string) error {
	src, err := os.Open(bin)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	tmp := dst.Name()
	fail := func(err error) error {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		return fail(err)
	}
	if err := dst.Chmod(0o755); err != nil {
		return fail(err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// goLanguageVersion is the go directive of the build module: the toolchain's
// own major.minor, which also turns on module graph pruning.
func goLanguageVersion() string {
	v := strings.TrimPrefix(runtime.Version(), "go")
	if parts := strings.SplitN(v, ".", 3); len(parts) >= 2 {
		minor := strings.TrimRightFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' })
		if minor == parts[1] && parts[0] != "" {
			return parts[0] + "." + minor
		}
	}
	return "1.25"
}
