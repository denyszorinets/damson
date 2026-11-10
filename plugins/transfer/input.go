package transfer

import (
	"bytes"
	"encoding/json"
	"path"
	"slices"
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
	S3   *S3Endpoint   `json:"s3,omitempty" jsonschema:"description=An object in an S3-compatible bucket"`
}

// S3Endpoint is an object in a bucket. Connection is a secret ref whose
// fields are endpoint, region, access_key, secret_key and optionally
// session_token and path_style.
type S3Endpoint struct {
	Connection string `json:"connection" jsonschema:"description=Secret ref of the connection (fields: endpoint, region, access_key, secret_key, optional session_token and path_style),minLength=1,pattern=^secret://"`
	Bucket     string `json:"bucket" jsonschema:"description=Bucket name,minLength=1"`
	Key        string `json:"key" jsonschema:"description=Object key (for a destination, a key ending in / gets the source file name appended),minLength=1"`
}

// path is the file path or object key of the endpoint.
func (e Endpoint) path() string {
	if e.S3 != nil {
		return e.S3.Key
	}
	return e.Node.Path
}

// NodeEndpoint is a file on an inventory node, reached with a file transfer
// protocol. The node's credential and host key policy apply.
type NodeEndpoint struct {
	Name     string `json:"name" jsonschema:"description=Name of the inventory node,minLength=1"`
	Protocol string `json:"protocol" jsonschema:"description=File transfer protocol,enum=sftp,enum=scp,enum=ftp,enum=ftps"`
	Path     string `json:"path" jsonschema:"description=Absolute path of the file (or of the directory, ending in /, for a destination),minLength=1"`
}

// Output is the transfer plugin output.
type Output struct {
	DestinationPath string `json:"destination_path,omitempty" jsonschema:"description=Path written on the destination node (node destinations)"`
	DestinationKey  string `json:"destination_key,omitempty" jsonschema:"description=Object key written (S3 destinations)"`
	Bytes           int64  `json:"bytes" jsonschema:"description=Bytes transferred"`
	SHA256          string `json:"sha256" jsonschema:"description=Hex SHA-256 of the transferred bytes"`
	ETag            string `json:"etag,omitempty" jsonschema:"description=ETag of the stored object (S3 destinations)"`
	VersionID       string `json:"version_id,omitempty" jsonschema:"description=Version id of the stored object when the bucket is versioned (S3 destinations)"`
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
	if strings.HasSuffix(in.Source.path(), "/") {
		return pluginsdk.ValidationError("source path must name a file: a step moves a single file")
	}
	return nil
}

func (e Endpoint) validate(which string) error {
	switch {
	case e.Node != nil && e.S3 != nil:
		return pluginsdk.ValidationError("%s takes a node or s3, not both", which)
	case e.S3 != nil:
		return e.S3.validate(which)
	case e.Node != nil:
		return e.Node.validate(which)
	}
	return pluginsdk.ValidationError("%s needs a node or s3", which)
}

func (n *NodeEndpoint) validate(which string) error {
	switch {
	case n.Name == "":
		return pluginsdk.ValidationError("%s node name is required", which)
	case !slices.Contains([]string{protoSFTP, protoSCP, protoFTP, protoFTPS}, n.Protocol):
		return pluginsdk.ValidationError("%s protocol %q is not supported (sftp, scp, ftp, ftps)", which, n.Protocol)
	case !path.IsAbs(n.Path):
		return pluginsdk.ValidationError("%s path must be absolute", which)
	case strings.ContainsAny(n.Path, "\x00\r\n"):
		return pluginsdk.ValidationError("%s path must be single-line text", which)
	}
	return nil
}

func (s *S3Endpoint) validate(which string) error {
	switch {
	case !strings.HasPrefix(s.Connection, "secret://") || strings.ContainsAny(s.Connection, "\x00\r\n"):
		return pluginsdk.ValidationError("%s s3 connection must be a secret:// ref", which)
	case s.Bucket == "" || strings.ContainsAny(s.Bucket, "/\x00\r\n"):
		return pluginsdk.ValidationError("%s s3 bucket is required and must be a bucket name", which)
	case s.Key == "" || strings.HasPrefix(s.Key, "/") || strings.ContainsAny(s.Key, "\x00\r\n"):
		return pluginsdk.ValidationError("%s s3 key must be a non-empty single-line key without a leading /", which)
	}
	return nil
}

// destinationPath applies the trailing-slash rule: a directory path (or key
// prefix) gets the source file name appended. A node path is cleaned; an
// object key is used as written.
func destinationPath(dest Endpoint, source string) string {
	p := dest.path()
	if strings.HasSuffix(p, "/") {
		return p + path.Base(source)
	}
	if dest.S3 != nil {
		return p
	}
	return path.Clean(p)
}
