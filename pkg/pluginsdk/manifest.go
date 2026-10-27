package pluginsdk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/invopop/jsonschema"
)

// TargetMode says how a plugin uses inventory nodes.
type TargetMode string

// Target modes.
const (
	TargetNone       TargetMode = "none"        // runs on the agent itself
	TargetSingleNode TargetMode = "single_node" // one node
	TargetInventory  TargetMode = "inventory"   // a set of targets
)

// Valid reports whether m is a known mode.
func (m TargetMode) Valid() bool {
	return m == TargetNone || m == TargetSingleNode || m == TargetInventory
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
