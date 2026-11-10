package cli

import (
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// linkPlugins returns the built-in plugins followed by the plugins of the
// extra modules, in module order. An extra plugin may not take the name of a
// built-in at any version, a name belongs to one module, and a name@version
// is unique (ADR-0050). A module that breaks a rule stops the binary at start.
func linkPlugins(builtin []pluginsdk.Plugin, modules []Module) ([]pluginsdk.Plugin, error) {
	out := append([]pluginsdk.Plugin(nil), builtin...)
	owner := map[string]string{} // plugin name -> module path ("" for built-ins)
	for _, p := range builtin {
		owner[p.Manifest().Name] = ""
	}
	seen := map[string]bool{}
	for _, mod := range modules {
		ps, err := modulePlugins(mod)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			m, err := safeManifest(p)
			if err != nil {
				return nil, fmt.Errorf("module %s: %w", mod.Path, err)
			}
			if err := m.Validate(); err != nil {
				return nil, fmt.Errorf("module %s: plugin %q: invalid manifest: %w", mod.Path, m.Name, err)
			}
			if _, err := m.Checksum(); err != nil {
				return nil, fmt.Errorf("module %s: plugin %q: manifest checksum: %w", mod.Path, m.Name, err)
			}
			switch who, taken := owner[m.Name]; {
			case taken && who == "":
				return nil, fmt.Errorf("module %s: plugin %q would shadow a built-in plugin", mod.Path, m.Name)
			case taken && who != mod.Path:
				return nil, fmt.Errorf("plugin %q is provided by both %s and %s", m.Name, who, mod.Path)
			}
			key := m.Name + "@" + m.Version
			if seen[key] {
				return nil, fmt.Errorf("module %s: plugin %s is provided twice", mod.Path, key)
			}
			seen[key] = true
			owner[m.Name] = mod.Path
			out = append(out, p)
		}
	}
	return out, nil
}

// modulePlugins calls a module's Plugins function, turning a panic into an error.
func modulePlugins(mod Module) (ps []pluginsdk.Plugin, err error) {
	if mod.Plugins == nil {
		return nil, fmt.Errorf("module %s has no Plugins function", mod.Path)
	}
	defer func() {
		if r := recover(); r != nil {
			ps, err = nil, fmt.Errorf("module %s: Plugins panicked (%T)", mod.Path, r)
		}
	}()
	return mod.Plugins(), nil
}

func modulePaths(modules []Module) []string {
	out := make([]string, len(modules))
	for i, m := range modules {
		out[i] = m.Path
	}
	return out
}

// moduleVersion is the version of a linked module from the build info.
func moduleVersion(path string) string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown)"
	}
	for _, d := range bi.Deps {
		if d.Path == path {
			if d.Replace != nil {
				return "(replaced)"
			}
			return d.Version
		}
	}
	if bi.Main.Path == path {
		return bi.Main.Version
	}
	return "(unknown)"
}

// safeManifest reads a manifest, turning a nil plugin or a panic into an error.
func safeManifest(p pluginsdk.Plugin) (m pluginsdk.Manifest, err error) {
	if p == nil {
		return m, errors.New("the Plugins function returned a nil plugin")
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("a plugin's Manifest panicked (%T)", r)
		}
	}()
	return p.Manifest(), nil
}
