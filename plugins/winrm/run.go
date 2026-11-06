package winrm

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	mwinrm "github.com/masterzen/winrm"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/internal/scriptout"
)

// cancelWait bounds how long a cancel waits for the node to acknowledge the
// terminate signal and the shell delete. The agent's default cancel grace is
// 5 seconds; this stays well inside it. Whatever is still in flight ends by
// itself within httpTimeout.
const cancelWait = 2 * time.Second

// runResult is what a finished command reports.
type runResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Values   map[string]any
}

// callCtx runs f, which does not take a context (the library's HTTP calls
// ignore it), and returns early when ctx ends. The call finishes by itself
// within httpTimeout. When it succeeds after the caller gave up, orphan gets
// the result, so a shell created for nobody is deleted again.
func callCtx[T any](ctx context.Context, f func() (T, error), orphan func(T)) (T, error) {
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := f()
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		go func() {
			if r := <-done; r.err == nil {
				orphan(r.v)
			}
		}()
		var zero T
		return zero, ctx.Err()
	}
}

// run opens a shell, starts the bootstrap, feeds it the payload on stdin and
// collects the output until the command ends or ctx is cancelled.
//
// The command is started with a background context on purpose: the library
// closes the command itself when the context it was given ends, and a second
// close from cancel below could panic (it closes a channel). Cancellation is
// handled here, once.
func run(ctx context.Context, client *mwinrm.Client, payload []byte, emit pluginsdk.Emitter) (runResult, error) {
	shell, err := callCtx(ctx, client.CreateShell, closeShell)
	if err != nil {
		return runResult{ExitCode: -1}, err
	}
	cmd, err := callCtx(ctx, func() (*mwinrm.Command, error) {
		return shell.ExecuteWithContext(context.Background(), bootstrapCommand())
	}, func(*mwinrm.Command) { closeShell(shell) })
	if err != nil {
		closeShell(shell)
		return runResult{ExitCode: -1}, err
	}

	outC, errC := scriptout.NewStdout(emit), scriptout.NewStderr(emit)
	var wg sync.WaitGroup
	wg.Go(func() { outC.Consume(cmd.Stdout) })
	wg.Go(func() { errC.Consume(cmd.Stderr) })
	stdinErr := make(chan error, 1)
	wg.Go(func() { stdinErr <- sendPayload(cmd, payload) })
	finished := make(chan struct{})
	go func() { cmd.Wait(); wg.Wait(); close(finished) }()

	select {
	case <-finished:
		closeShell(shell)
	case err := <-stdinErr:
		// Without its input the bootstrap would wait for it forever.
		if err != nil {
			cancel(cmd, shell, finished)
			stop(outC, errC)
			return collect(outC, errC, -1), pluginsdk.NewError(pluginsdk.KindConnection, "sending the script to the node failed: "+err.Error(), err)
		}
		return awaitFinished(ctx, cmd, shell, finished, outC, errC)
	case <-ctx.Done():
		cancel(cmd, shell, finished)
		stop(outC, errC)
		return collect(outC, errC, -1), ctx.Err()
	}
	return result(cmd, outC, errC)
}

// awaitFinished waits for the end of the command after the input was sent.
func awaitFinished(ctx context.Context, cmd *mwinrm.Command, shell *mwinrm.Shell, finished <-chan struct{}, outC, errC *scriptout.Collector) (runResult, error) {
	select {
	case <-finished:
		closeShell(shell)
		return result(cmd, outC, errC)
	case <-ctx.Done():
		cancel(cmd, shell, finished)
		stop(outC, errC)
		return collect(outC, errC, -1), ctx.Err()
	}
}

func result(cmd *mwinrm.Command, outC, errC *scriptout.Collector) (runResult, error) {
	res := collect(outC, errC, cmd.ExitCode())
	if err := cmd.Error(); err != nil {
		res.ExitCode = -1
		return res, err
	}
	return res, nil
}

// sendPayload writes the request and closes stdin. The library's WriteClose
// refuses its own write, so this writes and closes.
func sendPayload(cmd *mwinrm.Command, payload []byte) error {
	if _, err := cmd.Stdin.Write(payload); err != nil {
		return err
	}
	if err := cmd.Stdin.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	return nil
}

// stop makes the collectors drop whatever still arrives: the step has ended and
// the plugin must not emit after Execute returned.
func stop(outC, errC *scriptout.Collector) {
	outC.Stop()
	errC.Stop()
}

func collect(outC, errC *scriptout.Collector, code int) runResult {
	return runResult{ExitCode: code, Stdout: outC.Tail(), Stderr: errC.Tail(), Values: outC.Values()}
}

// cancel terminates the command and deletes the shell (which ends the whole
// process tree on the node), waiting at most cancelWait.
func cancel(cmd *mwinrm.Command, shell *mwinrm.Shell, finished <-chan struct{}) {
	ended := make(chan struct{})
	go func() {
		_ = cmd.Close()
		_ = shell.Close()
		close(ended)
	}()
	t := time.NewTimer(cancelWait)
	defer t.Stop()
	select {
	case <-ended:
	case <-t.C:
		return
	}
	select {
	case <-finished:
	case <-t.C:
	}
}

// closeShell deletes the shell best effort, without blocking the step.
func closeShell(shell *mwinrm.Shell) {
	done := make(chan struct{})
	go func() { _ = shell.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(cancelWait):
	}
}
