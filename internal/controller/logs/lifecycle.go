package logs

import (
	"context"
	"errors"
	"fmt"
)

// Start launches the flush loop. The context supplies values only: the loop
// ends through Stop, so a signal-cancelled ctx cannot cut the final flush
// short.
func (i *Ingest) Start(ctx context.Context) error {
	i.lifeMu.Lock()
	defer i.lifeMu.Unlock()
	if i.started {
		return errors.New("logs: ingest already started")
	}
	i.started = true
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	i.cancel = cancel
	tick := i.cfg.Clock.ticker(i.cfg.CheckInterval) // created before the goroutine: no tick is lost
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		defer tick.Stop()
		i.loop(loopCtx, tick)
	}()
	return nil
}

func (i *Ingest) loop(ctx context.Context, tick Ticker) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C():
		case <-i.wake:
		}
		i.duePass(ctx)
	}
}

// Stop ends the loop, flushes every pending chunk and sends the last acks,
// all bounded by ctx, then closes every live-tail subscription. It is safe
// to call without Start and more than once.
func (i *Ingest) Stop(ctx context.Context) error {
	i.lifeMu.Lock()
	if i.stopped {
		i.lifeMu.Unlock()
		return nil
	}
	i.stopped = true
	cancel := i.cancel
	i.lifeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	i.wg.Wait()

	i.mu.Lock()
	states := make([]*execState, 0, len(i.states))
	for _, st := range i.states {
		states = append(states, st)
	}
	i.mu.Unlock()

	var errs []error
	for _, st := range states {
		if err := i.flushState(ctx, st); err != nil {
			errs = append(errs, fmt.Errorf("execution %s: %w", st.execID, err))
		}
	}
	i.sendAcks(ctx)
	for _, st := range states {
		st.mu.Lock()
		st.closeLocked()
		st.mu.Unlock()
	}
	i.mu.Lock()
	i.states = make(map[string]*execState)
	i.mu.Unlock()
	return errors.Join(errs...)
}

// Subscribe replays the buffered lines with seq > afterSeq, then delivers new
// lines until ctx ends, the execution is finished (the channel closes) or the
// subscriber falls so far behind that its queue fills up (the channel closes
// too; ingest never blocks on a subscriber). It returns ErrNoLiveTail when
// this controller holds no buffer for the execution.
func (i *Ingest) Subscribe(ctx context.Context, executionID string, afterSeq int64) (<-chan Line, error) {
	st := i.state(executionID)
	if st == nil {
		return nil, ErrNoLiveTail
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return nil, ErrNoLiveTail
	}
	replay := st.ring.after(afterSeq)
	sub := &subscriber{ch: make(chan Line, len(replay)+i.cfg.SubscriberQueue)}
	for _, l := range replay {
		sub.ch <- l
	}
	st.subs[sub] = struct{}{}
	context.AfterFunc(ctx, func() { st.removeSub(sub) })
	return sub.ch, nil
}
