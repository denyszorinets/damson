// Package devnodes serves nodes and secrets from a JSON file, for
// `damson dev` only (ADR-0034). It stands in for the inventory and the secret
// store until M4 and implements dispatch.NodeSource and dispatch.SecretSource.
//
// The file holds no secret value except usernames: every other field is the
// path of a file (mode 0600 or stricter) that is read at each dispatch, so the
// controller never keeps a value in memory between sends.
package devnodes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

const (
	fileVersion  = 1
	maxFileBytes = 1 << 20
	maxKeyBytes  = 64 << 10
	filesSuffix  = "_file"
	defaultPort  = 22
)

// File is a loaded dev nodes file. Values are not part of it.
type File struct {
	nodes   map[string]pluginsdk.NodeInfo
	secrets map[string]map[string]string // ref -> field -> literal (username) or "<field>_file" -> path
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
	Attributes    map[string]any `json:"attributes"`
	Credential    string         `json:"credential"`
	HostKey       string         `json:"host_key"`
	HostKeyPolicy string         `json:"host_key_policy"`
}

// Load reads and validates the file at path.
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
	out := &File{nodes: map[string]pluginsdk.NodeInfo{}, secrets: raw.Secrets}
	if out.secrets == nil {
		out.secrets = map[string]map[string]string{}
	}
	if err := out.validateSecrets(); err != nil {
		return nil, err
	}
	for name, n := range raw.Nodes {
		node, err := buildNode(name, n)
		if err != nil {
			return nil, err
		}
		if node.Credential != "" {
			if _, ok := out.secrets[node.Credential]; !ok {
				return nil, fmt.Errorf("node %q: credential %q is not defined in secrets", name, node.Credential)
			}
		}
		out.nodes[name] = node
	}
	return out, nil
}

func buildNode(name string, n rawNode) (pluginsdk.NodeInfo, error) {
	switch {
	case name == "":
		return pluginsdk.NodeInfo{}, errors.New("a node has an empty name")
	case n.Address == "":
		return pluginsdk.NodeInfo{}, fmt.Errorf("node %q: address is empty", name)
	case n.Credential != "" && !strings.HasPrefix(n.Credential, "secret://"):
		return pluginsdk.NodeInfo{}, fmt.Errorf("node %q: credential must be a secret:// ref", name)
	}
	port := n.Port
	if port == 0 {
		port = defaultPort
	}
	if port < 1 || port > 65535 {
		return pluginsdk.NodeInfo{}, fmt.Errorf("node %q: port %d is out of range", name, port)
	}
	if !slices.Contains([]string{"strict", "tofu", "insecure"}, n.HostKeyPolicy) {
		return pluginsdk.NodeInfo{}, fmt.Errorf("node %q: host_key_policy must be strict, tofu or insecure", name)
	}
	transport := n.Transport
	if transport == "" {
		transport = "ssh"
	}
	return pluginsdk.NodeInfo{
		Name: name, Address: n.Address, Port: port, Transport: transport, OS: n.OS, Attributes: n.Attributes,
		Credential: n.Credential, HostKey: n.HostKey, HostKeyPolicy: n.HostKeyPolicy,
	}, nil
}

func (f *File) validateSecrets() error {
	for ref, fields := range f.secrets {
		if !strings.HasPrefix(ref, "secret://") || strings.Contains(ref, "#") {
			return fmt.Errorf("secret %q: the key must be a secret:// ref without #field", ref)
		}
		for field, v := range fields {
			switch {
			case strings.HasSuffix(field, filesSuffix):
				if !filepath.IsAbs(v) {
					return fmt.Errorf("secret %q: %s must be an absolute path", ref, field)
				}
			case field == "username":
			default:
				return fmt.Errorf("secret %q: put the value of %q in a file: use %q", ref, field, field+filesSuffix)
			}
		}
	}
	return nil
}

// Names returns the node names, sorted, for the startup warning.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.nodes))
	for n := range f.nodes {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Node implements dispatch.NodeSource.
func (f *File) Node(_ context.Context, _, name string) (pluginsdk.NodeInfo, error) {
	n, ok := f.nodes[name]
	if !ok {
		return pluginsdk.NodeInfo{}, dispatch.ErrUnknownNode
	}
	return n, nil
}

// Secret implements dispatch.SecretSource. It reads the files on every call.
func (f *File) Secret(_ context.Context, _, ref string) (map[string]string, error) {
	fields, ok := f.secrets[ref]
	if !ok {
		return nil, dispatch.ErrUnknownSecret
	}
	out := make(map[string]string, len(fields))
	for field, v := range fields {
		name, isFile := strings.CutSuffix(field, filesSuffix)
		if !isFile {
			out[field] = v
			continue
		}
		b, err := readSecretFile(v)
		if err != nil {
			clear(out)
			return nil, fmt.Errorf("secret %q field %q: %w", ref, name, err)
		}
		out[name] = string(b)
	}
	return out, nil
}

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
