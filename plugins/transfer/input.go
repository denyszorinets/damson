package transfer

import (
	"bytes"
	"encoding/json"
	"path"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// Modes and verify levels.
const (
	modeCopy = "copy"
	modeMove = "move"

	verifyNone   = "none"
	verifySize   = "size"
	verifySHA256 = "sha256"

	protoSFTP = "sftp"
	protoSCP  = "scp"
)

// Input is the transfer plugin input (ARCHITECTURE.md section 6.3). A step
// moves exactly one file.
type Input struct {
	Mode        string   `json:"mode" jsonschema:"description=copy leaves the source in place; move deletes it after the copy was verified,enum=copy,enum=move"`
	Source      Endpoint `json:"source" jsonschema:"description=Where the file is read from"`
	Destination Endpoint `json:"destination" jsonschema:"description=Where the file is written to. A path ending in / gets the source file name appended."`
	Verify      string   `json:"verify,omitempty" jsonschema:"description=How the written copy is checked before it becomes visible: none; size (default) compares the byte counts; sha256 re-reads the written file and compares digests,enum=none,enum=size,enum=sha256,default=size"`
	Overwrite   bool     `json:"overwrite,omitempty" jsonschema:"description=Replace an existing destination. Without it an existing destination fails the step."`
	Mkdirs      bool     `json:"mkdirs,omitempty" jsonschema:"description=Create missing parent directories of the destination"`
}

// Endpoint is one side of a transfer. Exactly one field is set.
type Endpoint struct {
	Node *NodeEndpoint `json:"node,omitempty" jsonschema:"description=A file on an inventory node"`
}

// NodeEndpoint is a file on an inventory node, reached with a file transfer
// protocol. The node's credential and host key policy apply.
type NodeEndpoint struct {
	Name     string `json:"name" jsonschema:"description=Name of the inventory node,minLength=1"`
	Protocol string `json:"protocol" jsonschema:"description=File transfer protocol,enum=sftp,enum=scp"`
	Path     string `json:"path" jsonschema:"description=Absolute path of the file (or of the directory, ending in /, for a destination),minLength=1"`
}

// Output is the transfer plugin output.
type Output struct {
	DestinationPath string `json:"destination_path" jsonschema:"description=Path written on the destination node"`
	Bytes           int64  `json:"bytes" jsonschema:"description=Bytes transferred"`
	SHA256          string `json:"sha256" jsonschema:"description=Hex SHA-256 of the transferred bytes"`
}

// decode parses and validates the input. Unknown fields are rejected.
func decode(raw json.RawMessage) (Input, error) {
	var in Input
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, pluginsdk.ValidationError("invalid input: %v", err)
	}
	if in.Verify == "" {
		in.Verify = verifySize
	}
	return in, in.validate()
}

func (in Input) validate() error {
	switch in.Mode {
	case modeCopy, modeMove:
	default:
		return pluginsdk.ValidationError("mode must be %q or %q", modeCopy, modeMove)
	}
	switch in.Verify {
	case verifyNone, verifySize, verifySHA256:
	default:
		return pluginsdk.ValidationError("verify must be %q, %q or %q", verifyNone, verifySize, verifySHA256)
	}
	if err := in.Source.validate("source"); err != nil {
		return err
	}
	if err := in.Destination.validate("destination"); err != nil {
		return err
	}
	if strings.HasSuffix(in.Source.Node.Path, "/") {
		return pluginsdk.ValidationError("source path must name a file: a step moves a single file")
	}
	return nil
}

func (e Endpoint) validate(which string) error {
	if e.Node == nil {
		return pluginsdk.ValidationError("%s needs a node", which)
	}
	n := e.Node
	switch {
	case n.Name == "":
		return pluginsdk.ValidationError("%s node name is required", which)
	case n.Protocol != protoSFTP && n.Protocol != protoSCP:
		return pluginsdk.ValidationError("%s protocol %q is not supported (sftp, scp)", which, n.Protocol)
	case !path.IsAbs(n.Path):
		return pluginsdk.ValidationError("%s path must be absolute", which)
	case strings.ContainsAny(n.Path, "\x00\r\n"):
		return pluginsdk.ValidationError("%s path must be single-line text", which)
	}
	return nil
}

// destinationPath applies the trailing-slash rule: a directory path gets the
// source file name appended.
func destinationPath(dest, source string) string {
	if strings.HasSuffix(dest, "/") {
		return dest + path.Base(source)
	}
	return path.Clean(dest)
}
