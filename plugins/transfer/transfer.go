// Package transfer is the built-in transfer plugin: it copies or moves one
// file between inventory nodes (ARCHITECTURE.md section 6.3). It uses only
// pkg/pluginsdk and plugins/internal/*.
package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/sshconn"
)

// ConfigAllowInsecureHostKeys is the ExecRequest.Config key that permits the
// "insecure" host key policy, as for the ssh plugin. Its value must be "true".
const ConfigAllowInsecureHostKeys = sshconn.ConfigAllowInsecureHostKeys

// Plugins returns the plugins in this package.
func Plugins() []pluginsdk.Plugin {
	return []pluginsdk.Plugin{plugin{manifest: pluginsdk.Manifest{
		Name:         "transfer",
		Version:      "0.1.0",
		Title:        "Transfer",
		Description:  "Copies or moves one file between nodes, with verification.",
		InputSchema:  pluginsdk.MustSchemaFor[Input](),
		OutputSchema: pluginsdk.MustSchemaFor[Output](),
		// A lost attempt may still be running on a disconnected agent; a move
		// whose source is already gone fails on retry.
		Idempotent: false,
		TargetMode: pluginsdk.TargetNodes,
		NodePaths:  []string{"/source/node/name", "/destination/node/name"},
	}}}
}

type plugin struct{ manifest pluginsdk.Manifest }

func (p plugin) Manifest() pluginsdk.Manifest { return p.manifest }

func (plugin) Validate(_ context.Context, input json.RawMessage) error {
	_, err := decode(input)
	return err
}

func (plugin) Execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	res, err := execute(ctx, req, emit)
	out := pluginsdk.Result{}
	if res.Path != "" {
		raw, merr := json.Marshal(Output{DestinationPath: res.Path, Bytes: res.Bytes, SHA256: res.SHA256})
		if merr != nil {
			return out, nil
		}
		out.Outputs = raw
	}
	if err != nil {
		out.Error = pluginsdk.AsStepError(err)
	}
	return out, nil
}

func execute(ctx context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (result, error) {
	if err := ctx.Err(); err != nil {
		return result{}, err
	}
	in, err := decode(req.Input)
	if err != nil {
		return result{}, err
	}
	src, err := open(ctx, req, in.Source, emit)
	if err != nil {
		return result{}, err
	}
	defer func() { _ = src.Close() }()
	dst, err := open(ctx, req, in.Destination, emit)
	if err != nil {
		return result{}, err
	}
	defer func() { _ = dst.Close() }()
	// Closing the connections aborts a read or write that is blocked on the
	// network when the step is cancelled.
	stop := context.AfterFunc(ctx, func() { _ = src.Close(); _ = dst.Close() })
	defer stop()

	srcPath := in.Source.Node.Path
	dstPath := destinationPath(in.Destination.Node.Path, srcPath)
	emit.Log(pluginsdk.System, in.Mode+" "+in.Source.Node.Name+":"+srcPath+" -> "+in.Destination.Node.Name+":"+dstPath)
	cleanup := func(partial string) { removePartial(ctx, req, in.Destination, dst, partial, emit) }
	res, err := run(ctx, in, srcPath, dstPath, src, dst, emit, cleanup)
	if err == nil {
		emit.Log(pluginsdk.System, "transferred "+itoa(res.Bytes)+" bytes, sha256 "+res.SHA256)
	}
	return res, err
}

// open connects to the node of one endpoint with its protocol.
func open(ctx context.Context, req pluginsdk.ExecRequest, e Endpoint, emit pluginsdk.Emitter) (fileSystem, error) {
	node, ok := req.Nodes[e.Node.Name]
	if !ok {
		return nil, pluginsdk.ValidationError("node %q is not part of this step", e.Node.Name)
	}
	switch e.Node.Protocol {
	case protoSFTP:
		return dialSFTP(ctx, req, node, emit)
	case protoSCP:
		return dialSCP(ctx, req, node, emit)
	case protoFTP, protoFTPS:
		return dialFTP(ctx, req, node, e.Node.Protocol, emit)
	}
	return nil, errors.New("unsupported protocol " + e.Node.Protocol)
}

const cleanupTimeout = 10 * time.Second

// removePartial deletes a partial file best effort. After a cancel the
// connection is closed (that is how a blocked write is aborted), so it
// connects again, with a context that outlives the cancelled one.
func removePartial(ctx context.Context, req pluginsdk.ExecRequest, e Endpoint, dst fileSystem, partial string, emit pluginsdk.Emitter) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if ctx.Err() == nil {
		if dst.Remove(cctx, partial) == nil {
			return
		}
	}
	fresh, err := open(cctx, req, e, emit)
	if err != nil {
		return
	}
	defer func() { _ = fresh.Close() }()
	_ = fresh.Remove(cctx, partial)
}
