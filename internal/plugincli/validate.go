package plugincli

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// runValidate checks the manifest of every selected plugin: the manifest rules
// (name, semver, requirements, target mode), that the schemas are JSON
// objects, that the input schema compiles within the registry's bounds, and
// that no name@version is listed twice. It reports every problem, not the first.
func runValidate(args []string, env Env) int {
	fs := newFlags("validate", env)
	pos, err := parse(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(pos) > 1 {
		fmt.Fprintln(env.Stderr, "damson plugin validate: at most one plugin reference")
		return ExitUsage
	}
	plugins := env.Plugins
	if len(pos) == 1 {
		sel, serr := selectPlugins(plugins, pos[0])
		if serr != nil {
			fmt.Fprintf(env.Stderr, "damson plugin validate: %v\n", serr)
			return ExitUsage
		}
		plugins = sel
	}
	failed := 0
	seen := map[string]bool{}
	for _, p := range plugins {
		m := p.Manifest()
		id := m.Name + "@" + m.Version
		var problems []string
		if seen[id] {
			problems = append(problems, "listed twice")
		}
		seen[id] = true
		problems = append(problems, checkManifest(m)...)
		if len(problems) == 0 {
			fmt.Fprintf(env.Stdout, "ok    %s\n", id)
			continue
		}
		failed++
		for _, pr := range problems {
			fmt.Fprintf(env.Stdout, "FAIL  %s: %s\n", id, pr)
		}
	}
	if len(plugins) == 0 {
		fmt.Fprintln(env.Stdout, "no plugins linked")
	}
	if failed > 0 {
		fmt.Fprintf(env.Stderr, "damson plugin validate: %d of %d plugins failed\n", failed, len(plugins))
		return ExitError
	}
	return ExitOK
}

// checkManifest returns every problem found with m.
func checkManifest(m pluginsdk.Manifest) []string {
	var problems []string
	if err := m.Validate(); err != nil {
		problems = append(problems, err.Error())
		return problems // the rest assumes a valid manifest
	}
	if _, err := m.Checksum(); err != nil {
		problems = append(problems, err.Error())
	}
	for name, s := range map[string]any{"input schema": m.InputSchema, "output schema": m.OutputSchema} {
		raw, err := json.Marshal(s)
		if err != nil {
			problems = append(problems, name+" does not marshal: "+err.Error())
			continue
		}
		if err := objectSchema(raw); err != nil {
			problems = append(problems, name+": "+err.Error())
		}
	}
	// The registry compiles the input schema within its size and depth bounds.
	reg, err := pluginreg.New(nil, slog.New(slog.DiscardHandler))
	if err == nil {
		sum, _ := m.Checksum()
		err = reg.ValidateEntry(pluginreg.Entry{Manifest: m, Checksum: sum})
	}
	if err != nil {
		problems = append(problems, "input schema is not usable: "+err.Error())
	}
	slices.Sort(problems)
	return problems
}

// objectSchema requires a schema that describes a JSON object, which is what a
// plugin takes and returns. A $ref to a $defs entry (how the generator writes a
// struct) is followed one level.
func objectSchema(raw []byte) error {
	var s struct {
		Type string                     `json:"type"`
		Ref  string                     `json:"$ref"`
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return errors.New("is not a JSON object")
	}
	if s.Ref != "" {
		def, ok := s.Defs[strings.TrimPrefix(s.Ref, "#/$defs/")]
		if !ok {
			return fmt.Errorf("refers to %s, which is not defined", s.Ref)
		}
		var d struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(def, &d); err != nil {
			return errors.New("has an unreadable definition")
		}
		s.Type = d.Type
	}
	if s.Type != "object" {
		return fmt.Errorf("must describe an object, not %q", s.Type)
	}
	return nil
}
