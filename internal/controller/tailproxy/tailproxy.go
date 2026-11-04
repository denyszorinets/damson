// Package tailproxy gives the API the live log tail of an execution owned by
// another controller: it finds the owner, calls its internal tail endpoint
// with a signed request and relays the lines. Any failure means "no live
// tail", and the log stream falls back to polling the stored chunks, which
// drops what it already sent by sequence number (ARCHITECTURE 8.5, ADR-0040).
package tailproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/controller/internalapi"
	"github.com/denyszorinets/damson/internal/controller/internalauth"
	"github.com/denyszorinets/damson/internal/store"
)

// Defaults.
const (
	// StaleAfter is how long an owner may be silent before it counts as gone.
	DefaultStaleAfter = 15 * time.Second
	maxLineBytes      = 1 << 20
	queue             = 256
)

// Local is this controller's own live tail.
type Local interface {
	Subscribe(ctx context.Context, executionID string, afterSeq int64) (<-chan api.LogLine, error)
}

// Registry is the part of the store the proxy reads.
type Registry interface {
	GetExecution(ctx context.Context, id string) (store.Execution, error)
	ListControllerInstances(ctx context.Context) ([]store.ControllerInstance, error)
}

// Config configures a Tail.
type Config struct {
	Local Local
	Store Registry
	Self  string
	Keys  *internalauth.Keyring // nil: never proxy
	// StaleAfter is the silence after which an owner is not tried; Idle is how
	// long a tail may stay silent (it gets heartbeats) before it is dropped.
	StaleAfter, Idle time.Duration
	Now              func() time.Time
	Client           *http.Client
	Logger           *slog.Logger
}

// Tail implements api.LogTail.
type Tail struct {
	cfg Config
	log *slog.Logger

	lastWarn atomic.Int64 // unix seconds of the last unauthorized warning
}

// New validates cfg and fills defaults.
func New(cfg Config) (*Tail, error) {
	if cfg.Local == nil || cfg.Self == "" {
		return nil, errors.New("tailproxy: local tail and self id are required")
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	if cfg.Idle <= 0 {
		cfg.Idle = 3 * internalapi.DefaultHeartbeat
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Client == nil {
		cfg.Client = newClient()
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Tail{cfg: cfg, log: log}, nil
}

// newClient never follows redirects and bounds connecting and the response
// header; the body is a long-lived stream bounded by the idle deadline.
func newClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			Proxy:                 nil,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Subscribe tries the local buffer, then the owner's.
func (t *Tail) Subscribe(ctx context.Context, executionID string, afterSeq int64) (<-chan api.LogLine, error) {
	ch, err := t.cfg.Local.Subscribe(ctx, executionID, afterSeq)
	if !errors.Is(err, api.ErrNoLiveTail) || t.cfg.Keys == nil {
		return ch, err
	}
	o, ok := t.owner(ctx, executionID)
	if !ok {
		return nil, api.ErrNoLiveTail
	}
	ch, err = t.open(ctx, o, executionID, afterSeq)
	if err != nil {
		t.log.Debug("remote log tail unavailable", "execution_id", executionID, "error", err)
		return nil, api.ErrNoLiveTail
	}
	return ch, nil
}

// ownerRef is the controller running an execution: its id, internal URL and the
// attempt to ask for.
type ownerRef struct{ id, url, attempt string }

// owner resolves the controller running the execution. It is not found when the
// execution is finished, ours, unowned, or the owner's registry row is missing
// or silent.
func (t *Tail) owner(ctx context.Context, executionID string) (ownerRef, bool) {
	ex, err := t.cfg.Store.GetExecution(ctx, executionID)
	if err != nil || ex.State.Terminal() || ex.ControllerID == "" || ex.ControllerID == t.cfg.Self || ex.AttemptID == "" {
		return ownerRef{}, false
	}
	peers, err := t.cfg.Store.ListControllerInstances(ctx)
	if err != nil {
		return ownerRef{}, false
	}
	for _, p := range peers {
		if p.ID != ex.ControllerID {
			continue
		}
		if t.cfg.Now().Sub(p.LastSeen) > t.cfg.StaleAfter || config.ValidateInternalURL(p.InternalURL) != nil {
			return ownerRef{}, false
		}
		return ownerRef{id: p.ID, url: p.InternalURL, attempt: ex.AttemptID}, true
	}
	return ownerRef{}, false
}

// open calls the owner and starts relaying. The owner's id is the one the
// signature is bound to.
func (t *Tail) open(ctx context.Context, o ownerRef, executionID string, afterSeq int64) (<-chan api.LogLine, error) {
	u, err := url.Parse(o.url)
	if err != nil {
		return nil, fmt.Errorf("parsing internal url: %w", err)
	}
	u.Path = fmt.Sprintf(internalapi.TailPath, url.PathEscape(executionID))
	u.RawPath = ""
	q := url.Values{"attempt_id": {o.attempt}, "after_seq": {fmt.Sprint(afterSeq)}}
	u.RawQuery = q.Encode()
	rctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("building request: %w", err)
	}
	t.cfg.Keys.Sign(req, t.cfg.Self, o.id, t.cfg.Now())
	resp, err := t.cfg.Client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("calling the owner: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			t.warnUnauthorized(o.id)
		}
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("owner answered %d", resp.StatusCode)
	}
	out := make(chan api.LogLine, queue)
	go t.relay(rctx, cancel, resp, out)
	return out, nil
}

// warnUnauthorized says, at most once a minute, that a peer refused our
// signature: different cluster secrets or clocks more than the window apart.
func (t *Tail) warnUnauthorized(peer string) {
	now := t.cfg.Now().Unix()
	if last := t.lastWarn.Load(); now-last < 60 || !t.lastWarn.CompareAndSwap(last, now) {
		return
	}
	t.log.Warn("controller refused the log tail request: check that the cluster secrets match and the clocks agree", "peer_id", peer)
}

// relay copies lines until the stream ends, goes silent or ctx ends, then
// closes out: the log stream continues from the store.
func (t *Tail) relay(ctx context.Context, cancel context.CancelFunc, resp *http.Response, out chan<- api.LogLine) {
	defer close(out)
	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	// An idle owner is dropped: canceling the request unblocks the scan.
	idle := time.AfterFunc(t.cfg.Idle, cancel)
	defer idle.Stop()
	for sc.Scan() {
		idle.Reset(t.cfg.Idle)
		var it internalapi.Item
		if json.Unmarshal(sc.Bytes(), &it) != nil {
			return
		}
		if it.Type != internalapi.TypeLine {
			continue
		}
		select {
		case out <- api.LogLine{Seq: it.Seq, TS: it.TS, Stream: it.Stream, Line: it.Line}:
		case <-ctx.Done():
			return
		}
	}
}
