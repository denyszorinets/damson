package engine_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/denyszorinets/damson/internal/engine"
)

func TestStatusMapping(t *testing.T) {
	runs := []struct {
		store  string
		engine engine.RunStatus
	}{
		{"pending", engine.RunQueued}, {"running", engine.RunRunning}, {"succeeded", engine.RunSucceeded},
		{"failed", engine.RunFailed}, {"canceled", engine.RunCancelled},
	}
	for _, c := range runs {
		assert.Equal(t, c.engine, engine.RunStatusFromStore(c.store), c.store)
		assert.Equal(t, c.store, c.engine.Store(), string(c.engine))
	}
	steps := []struct {
		store  string
		engine engine.StepStatus
	}{
		{"pending", engine.StepPending}, {"succeeded", engine.StepSucceeded}, {"failed", engine.StepFailed},
		{"skipped", engine.StepSkipped}, {"canceled", engine.StepCancelled},
	}
	for _, c := range steps {
		assert.Equal(t, c.engine, engine.StepStatusFromStore(c.store), c.store)
		assert.Equal(t, c.store, c.engine.Store(), string(c.engine))
	}
	assert.Equal(t, engine.StepPending, engine.StepStatusFromStore("running"))
	execs := []struct {
		store  string
		engine engine.ExecState
	}{
		{"queued", engine.ExecQueued}, {"dispatched", engine.ExecDispatched}, {"running", engine.ExecRunning},
		{"succeeded", engine.ExecSucceeded}, {"failed", engine.ExecFailed}, {"canceled", engine.ExecCancelled}, {"lost", engine.ExecLost},
	}
	for _, c := range execs {
		assert.Equal(t, c.engine, engine.ExecStateFromStore(c.store), c.store)
		assert.Equal(t, c.store, c.engine.Store(), string(c.engine))
	}
	assert.Equal(t, engine.ExecFailed, engine.ExecStateFromStore("timed_out"))
}
