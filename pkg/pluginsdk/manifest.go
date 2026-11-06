package pluginsdk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/invopop/jsonschema"
)

// TargetMode says how a plugin uses inventory nodes.
type TargetMode string

// Target modes.
const (
	TargetNone       TargetMode = "none"        // runs on the agent itself
	TargetSingleNode TargetMode = "single_node" // one node
	TargetInventory  TargetMode = "inventory"   // a set of targets
	TargetNodes      TargetMode = "nodes"       // up to MaxNodePaths nodes named at Manifest.NodePaths
)

// MaxNodePaths bounds Manifest.NodePaths.
const MaxNodePaths = 8

// Valid reports whether m is a known mode.
func (m TargetMode) Valid() bool {
	return m == TargetNone || m == TargetSingleNode || m == TargetInventory || m == TargetNodes
}

// Manifest describes a plugin. Agents send manifests in agent.hello.
type Manifest struct {
	Name         string             `json:"name"`
	Version      string             `json:"version"` // semver
	Title        string             `json:"title"`
	Description  string             `json:"description"`
	InputSchema  *jsonschema.Schema `json:"input_schema"`
	OutputSchema *jsonschema.Schema `json:"output_schema"`
	Idempotent   bool               `json:"idempotent"`         // safe to retry after a "lost" execution
	Requires     []string           `json:"requires,omitempty"` // agent capabilities, e.g. "bin:git"
	TargetMode   TargetMode         `json:"target_mode"`
	// NodePaths are RFC 6901 JSON Pointers into the step input, each naming a
	// node (a string). Required with TargetNodes, forbidden otherwise. A pointer
	// absent from the input is skipped; one that is present must be a non-empty
	// string. omitempty keeps the checksum of existing manifests unchanged.
	NodePaths []string `json:"node_paths,omitempty"`
}

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	capRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*:[^\s:]+$`)
)

// Validate checks the manifest is well formed.
func (m Manifest) Validate() error {
	switch {
	case !nameRe.MatchString(m.Name):
		return fmt.Errorf("manifest name %q must match %s", m.Name, nameRe)
	case !semverRe.MatchString(m.Version):
		return fmt.Errorf("manifest version %q is not semver", m.Version)
	case m.Title == "":
		return errors.New("manifest title is empty")
	case m.InputSchema == nil:
		return errors.New("manifest input schema is nil")
	case m.OutputSchema == nil:
		return errors.New("manifest output schema is nil")
	case !m.TargetMode.Valid():
		return fmt.Errorf("manifest target mode %q is invalid", m.TargetMode)
	}
	if err := m.validateNodePaths(); err != nil {
		return err
	}
	for _, c := range m.Requires {
		if !capRe.MatchString(c) {
			return fmt.Errorf("manifest requirement %q must look like \"bin:git\"", c)
		}
	}
	return nil
}

// Checksum is a stable digest of the manifest. The controller compares it to
// detect schema conflicts between agents reporting the same name and version.
//
// The manifest is marshaled, decoded into generic values and marshaled again.
// encoding/json writes map keys sorted, so the digest depends on the schema
// content only, not on the key order or struct layout of the schema library.
func (m Manifest) Checksum() (string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshaling manifest: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("canonicalizing manifest: %w", err)
	}
	canon, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshaling canonical manifest: %w", err)
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

func (m Manifest) validateNodePaths() error {
	if m.TargetMode != TargetNodes {
		if len(m.NodePaths) > 0 {
			return fmt.Errorf("manifest node_paths needs target mode %q", TargetNodes)
		}
		return nil
	}
	if len(m.NodePaths) == 0 || len(m.NodePaths) > MaxNodePaths {
		return fmt.Errorf("manifest node_paths must have 1 to %d pointers", MaxNodePaths)
	}
	seen := map[string]bool{}
	for _, p := range m.NodePaths {
		if !pointerRe.MatchString(p) || seen[p] {
			return fmt.Errorf("manifest node path %q is not a unique JSON Pointer", p)
		}
		seen[p] = true
	}
	return nil
}

// pointerRe accepts a non-root RFC 6901 pointer.
var pointerRe = regexp.MustCompile(`^(/([^/~]|~[01])+)+$`)

// NodeNames returns the node names the input names at m's node paths, in path
// order and without duplicates. TargetSingleNode behaves as the single path
// "/node", which is required. A missing path is skipped for TargetNodes; a
// present one that is not a non-empty string is an error naming the pointer.
func (m Manifest) NodeNames(input map[string]any) ([]string, error) {
	paths := m.NodePaths
	required := false
	switch m.TargetMode {
	case TargetSingleNode:
		paths, required = []string{"/node"}, true
	case TargetNodes:
	default:
		return nil, nil
	}
	var names []string
	seen := map[string]bool{}
	for _, p := range paths {
		v, ok := lookupPointer(input, p)
		if !ok {
			if required {
				return nil, fmt.Errorf("step input %q must be a node name", p[1:])
			}
			continue
		}
		name, isStr := v.(string)
		if !isStr || name == "" {
			return nil, fmt.Errorf("step input at %q must be a node name", p)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

func lookupPointer(v any, ptr string) (any, bool) {
	for _, seg := range strings.Split(ptr[1:], "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[seg]; !ok {
			return nil, false
		}
	}
	return v, true
}
