package node

import (
	"context"
	"errors"
	"fmt"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/controller/leader"
	"github.com/denyszorinets/damson/internal/controller/scheduler"
)

// startScheduler starts the scheduler duty and its lease (ARCHITECTURE 12.3,
// ADR-0058). It starts nothing on an unmigrated database, where there are no
// schedules and no run.advance to wake.
func (n *Node) startScheduler(ctx context.Context, cfg Config, apiCfg api.Config) error {
	if n.adv == nil {
		return nil
	}
	t := cfg.Timing
	el, err := leader.New(leader.Config{
		Store: cfg.Store, Name: scheduler.LeaderName, Holder: cfg.ControllerID, TTL: t.LeaderTTL, Renew: t.LeaderRenew, Logger: cfg.Logger,
	})
	if err != nil {
		return fmt.Errorf("creating the scheduler lease: %w", err)
	}
	sc, err := scheduler.New(scheduler.Config{
		Store: cfg.Store, Starter: api.NewStarter(apiCfg), Leader: el, Holder: cfg.ControllerID, Interval: t.Schedule, Logger: cfg.Logger,
	})
	if err != nil {
		return fmt.Errorf("creating the scheduler: %w", err)
	}
	if err := el.Start(ctx); err != nil {
		return fmt.Errorf("starting the scheduler lease: %w", err)
	}
	if err := sc.Start(ctx); err != nil {
		return errors.Join(fmt.Errorf("starting the scheduler: %w", err), el.Stop(context.WithoutCancel(ctx)))
	}
	n.schedLead, n.sched = el, sc
	return nil
}

func (n *Node) stopScheduler(ctx context.Context) error {
	if n.sched == nil {
		return nil
	}
	return errors.Join(n.sched.Stop(ctx), n.schedLead.Stop(ctx))
}
