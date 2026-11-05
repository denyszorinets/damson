// Package devnodes loads the --dev-nodes file and seeds its nodes and secrets
// into the inventory and the secret store at startup (ADR-0044, which replaced
// the file-backed sources of ADR-0034). After seeding, dispatch, targets and
// authorization use the real stores exactly as without the flag.
//
// The file holds no secret value except usernames: every other field is the
// path of a file (mode 0600 or stricter) read when seeding, so the controller
// keeps no value in memory afterwards.
package devnodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
)

// Source is the inventory source name of the nodes this file creates.
const Source = inventory.SourceDevfile

const (
	fileVersion  = 1
	maxFileBytes = 1 << 20
	maxKeyBytes  = 64 << 10
	filesSuffix  = "_file"
)

// File is a loaded dev nodes file. Values are not part of it.
type File struct {
	nodes   []store.Node // sorted by name
	secrets []secretDef  // sorted by ref
}

// secretDef is one secret of the file: literal fields (username) and fields
// whose value is read from a file when seeding.
type secretDef struct {
	ref      secrets.Ref
	typ      string
	literals map[string]string // field -> value (username only)
	files    map[string]string // field -> absolute path
}

type rawFile struct {
	Version int                          `json:"version"`
	Nodes   map[string]rawNode           `json:"nodes"`
	Secrets map[string]map[string]string `json:"secrets"`
}

type rawNode struct {
	Address       string         `json:"address"`
	Port          int            `json:"port"`
	Transport     string         `json:"transport"`
	OS            string         `json:"os"`
	Pool          string         `json:"pool"`
	Tags          []string       `json:"tags"`
	Attributes    map[string]any `json:"attributes"`
	Credential    string         `json:"credential"`
	HostKey       string         `json:"host_key"`
	HostKeyPolicy string         `json:"host_key_policy"`
}

// Load reads and validates the file at path. Node and secret errors name the
// node or ref, never a value.
func Load(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening dev nodes file: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading dev nodes file: %w", err)
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("dev nodes file is larger than %d bytes", maxFileBytes)
	}
	var raw rawFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parsing dev nodes file: %w", err)
	}
	if raw.Version != fileVersion {
		return nil, fmt.Errorf("dev nodes file version %d is not supported (want %d)", raw.Version, fileVersion)
	}
	out := &File{}
	for ref, fields := range raw.Secrets {
		def, err := buildSecret(ref, fields)
		if err != nil {
			return nil, err
		}
		out.secrets = append(out.secrets, def)
	}
	sort.Slice(out.secrets, func(i, j int) bool { return out.secrets[i].ref.Base() < out.secrets[j].ref.Base() })
	lower := map[string]string{}
	for name, n := range raw.Nodes {
		node, err := buildNode(name, n)
		if err != nil {
			return nil, err
		}
		if other, dup := lower[strings.ToLower(name)]; dup {
			return nil, fmt.Errorf("nodes %q and %q differ only in case", other, name)
		}
		lower[strings.ToLower(name)] = name
		out.nodes = append(out.nodes, node)
	}
	sort.Slice(out.nodes, func(i, j int) bool { return out.nodes[i].Name < out.nodes[j].Name })
	return out, nil
}

func buildNode(name string, n rawNode) (store.Node, error) {
	transport := n.Transport
	if transport == "" {
		transport = inventory.TransportSSH
	}
	port := n.Port
	if port == 0 {
		port = inventory.DefaultPort(transport)
	}
	node := store.Node{
		Name: name, ExternalID: name, Address: n.Address, Port: port, Transport: transport, OS: n.OS, Pool: n.Pool,
		Tags: n.Tags, Attributes: n.Attributes, Credential: n.Credential, HostKey: n.HostKey,
		HostKeyPolicy: n.HostKeyPolicy, Source: Source, Enabled: true,
	}
	if is := inventory.Validate(&node); len(is) > 0 {
		return store.Node{}, fmt.Errorf("node %q: %s: %s", name, is[0].Field, is[0].Message)
	}
	return node, nil
}

func buildSecret(ref string, fields map[string]string) (secretDef, error) {
	r, err := secrets.ParseRef(ref)
	if err != nil || r.Field != "" {
		return secretDef{}, fmt.Errorf("secret %q: the key must be a secret://<scope>/<name> ref without #field (a scope is a project such as default, or _inventory for node credentials)", ref)
	}
	def := secretDef{ref: r, typ: "generic", literals: map[string]string{}, files: map[string]string{}}
	for field, v := range fields {
		switch {
		case field == "type":
			def.typ = v
		case strings.HasSuffix(field, filesSuffix):
			if !filepath.IsAbs(v) {
				return secretDef{}, fmt.Errorf("secret %q: %s must be an absolute path", ref, field)
			}
			def.files[strings.TrimSuffix(field, filesSuffix)] = v
		case field == "username":
			def.literals[field] = v
		default:
			return secretDef{}, fmt.Errorf("secret %q: put the value of %q in a file: use %q", ref, field, field+filesSuffix)
		}
	}
	t, err := secrets.LookupType(def.typ)
	if err != nil {
		return secretDef{}, fmt.Errorf("secret %q: %w", ref, err)
	}
	if len(def.literals)+len(def.files) == 0 {
		return secretDef{}, fmt.Errorf("secret %q: no fields", ref)
	}
	// Check field names and requirements without reading any file.
	probe := map[string]string{}
	for f := range def.literals {
		probe[f] = "x"
	}
	for f := range def.files {
		probe[f] = "x"
	}
	if err := t.ValidateFields(probe); err != nil {
		return secretDef{}, fmt.Errorf("secret %q: %w", ref, err)
	}
	return def, nil
}

// Names returns the node names, sorted, for the startup warning.
func (f *File) Names() []string {
	names := make([]string, len(f.nodes))
	for i, n := range f.nodes {
		names[i] = n.Name
	}
	return names
}

// Desired returns the nodes of the file as inventory nodes (source devfile,
// external id = name), validated and normalized.
func (f *File) Desired() []store.Node {
	return slices.Clone(f.nodes)
}

// HasSecrets reports whether the file defines any secret.
func (f *File) HasSecrets() bool { return len(f.secrets) > 0 }

// readSecretFile reads a regular, private file. Errors name the path, never
// the content.
func readSecretFile(path string) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, errors.Unwrap(err))
	}
	defer func() { _ = fh.Close() }()
	info, err := fh.Stat()
	switch {
	case err != nil:
		return nil, fmt.Errorf("stat %s: %w", path, err)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", path)
	case info.Mode().Perm()&0o077 != 0:
		return nil, fmt.Errorf("%s must not be accessible by group or others (mode %04o)", path, info.Mode().Perm())
	case info.Size() > maxKeyBytes:
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxKeyBytes)
	}
	b, err := io.ReadAll(io.LimitReader(fh, maxKeyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return b, nil
}
