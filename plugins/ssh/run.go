package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// killGrace is how long a cancelled command gets to exit after SIGTERM before
// the session and connection are closed.
const killGrace = 2 * time.Second

// runResult is what a finished remote command produced.
type runResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Values   map[string]any
}

// run executes call on a new session of client. Output is streamed line by
// line to emit. When ctx ends it sends SIGTERM through the session, then
// closes the session and the connection, and returns the context error
// together with what was collected so far.
func run(ctx context.Context, client *ssh.Client, in Input, call remoteCall, emit pluginsdk.Emitter) (runResult, error) {
	sess, err := client.NewSession()
	if err != nil {
		return runResult{}, ctxOr(ctx, pluginsdk.NewError(pluginsdk.KindConnection, "opening ssh session failed: "+err.Error(), err))
	}
	defer func() { _ = sess.Close() }() // may already be closed by wait
	if in.Pty {
		modes := ssh.TerminalModes{ssh.ECHO: 0}
		if err := sess.RequestPty("xterm", 24, 200, modes); err != nil {
			return runResult{}, pluginsdk.NewError(pluginsdk.KindConnection, "pty request refused: "+err.Error(), err)
		}
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return runResult{}, fmt.Errorf("opening stdout pipe: %w", err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return runResult{}, fmt.Errorf("opening stderr pipe: %w", err)
	}
	var stdin io.WriteCloser
	if call.Stdin != "" {
		if stdin, err = sess.StdinPipe(); err != nil {
			return runResult{}, fmt.Errorf("opening stdin pipe: %w", err)
		}
	}
	if err := sess.Start(call.Command); err != nil {
		return runResult{}, ctxOr(ctx, pluginsdk.NewError(pluginsdk.KindConnection, "starting remote command failed: "+err.Error(), err))
	}

	outC := &collector{emit: emit, stream: pluginsdk.Stdout, proto: true}
	errC := &collector{emit: emit, stream: pluginsdk.Stderr}
	var wg sync.WaitGroup
	wg.Go(func() { outC.consume(stdout) })
	wg.Go(func() { errC.consume(stderr) })
	if stdin != nil {
		wg.Go(func() {
			_, _ = io.WriteString(stdin, call.Stdin)
			_ = stdin.Close()
		})
	}

	waitErr := wait(ctx, client, sess)
	wg.Wait()

	res := runResult{ExitCode: -1, Stdout: outC.tail.String(), Stderr: errC.tail.String(), Values: outC.values}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	code, err := exitCode(waitErr)
	res.ExitCode = code
	return res, err
}

// wait returns the session result. On cancellation it signals the remote
// process, gives it killGrace to exit, then closes the session and client.
func wait(ctx context.Context, client *ssh.Client, sess *ssh.Session) error {
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	_ = sess.Signal(ssh.SIGTERM)
	t := time.NewTimer(killGrace)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
	}
	_ = sess.Close()
	_ = client.Close()
	return <-done
}

// exitCode maps the result of Session.Wait to an exit code. A nil error is
// exit code 0. A remote process killed by a signal has no exit code and is
// reported as -1 together with the signal.
func exitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		if sig := ee.Signal(); sig != "" {
			return -1, pluginsdk.NewError(pluginsdk.KindExitCode, "remote command was terminated by signal "+sig, err)
		}
		return ee.ExitStatus(), nil
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return -1, pluginsdk.NewError(pluginsdk.KindConnection, "connection ended before the remote command reported an exit status", err)
	}
	return -1, pluginsdk.NewError(pluginsdk.KindConnection, "waiting for the remote command failed: "+err.Error(), err)
}
