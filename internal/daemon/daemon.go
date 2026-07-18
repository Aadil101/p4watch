// Package daemon is the p4watch daemon core: the cached view of the workspace,
// the loop that keeps it fresh, and the HTTP endpoints that serve it. The
// command in cmd/p4watchd is a thin wrapper that parses flags and starts a
// Server; everything with behavior lives here so it can be tested without a
// process or a socket.
//
// The design rule that makes this honest: the daemon never reports a confident
// "clean" unless a reconcile pass actually succeeded. Until the first pass
// completes, or after one fails, its state is "unknown", a distinct answer a
// caller (like a shell prompt) must render differently from clean. A cheap
// wrong "all clear" is the one failure this tool exists to avoid.
//
// Freshness comes from two sources. Filesystem events (see treeWatcher) TRIGGER
// re-anchors so the answer tracks edits within a debounce window. A periodic
// backstop pass runs regardless, so a change that produced no event, or an event
// the OS dropped, can never keep the answer wrong for longer than the backstop
// interval. Events shrink the staleness window; the backstop, not the events, is
// the correctness guarantee.
package daemon

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Aadil101/p4watch/internal/p4"
	"github.com/Aadil101/p4watch/internal/reconcile"
)

// Default loop timings. Debounce and MaxWait shape how event bursts coalesce;
// Backstop is the safety-net re-anchor interval that bounds staleness even if
// events fail entirely.
const (
	defaultBackstop = 2 * time.Minute
	defaultDebounce = 500 * time.Millisecond
	defaultMaxWait  = 2 * time.Second
)

// Config tunes the re-anchor loop. Zero fields take the package defaults.
type Config struct {
	Backstop time.Duration // periodic re-anchor; the missed-event safety net
	Debounce time.Duration // quiet window after an event before re-anchoring
	MaxWait  time.Duration // cap so sustained churn still re-anchors this often
}

func (c Config) withDefaults() Config {
	if c.Backstop <= 0 {
		c.Backstop = defaultBackstop
	}
	if c.Debounce <= 0 {
		c.Debounce = defaultDebounce
	}
	if c.MaxWait <= 0 {
		c.MaxWait = defaultMaxWait
	}
	return c
}

// Server is the daemon's cached view of the workspace plus the connection it
// reconciles against. The cached fields are guarded for concurrent reads (the
// endpoints) against the single writer (the re-anchor loop). client and cfg are
// set once at construction and never mutated, so they need no lock.
type Server struct {
	client   *p4.Client
	backstop time.Duration
	debounce time.Duration
	maxWait  time.Duration

	mu       sync.RWMutex
	result   reconcile.Result
	ok       bool      // has any reconcile pass ever succeeded?
	lastErr  error     // error from the most recent pass, if it failed
	anchored time.Time // when the cached result was produced
}

// New builds a Server that reconciles c, using cfg (with defaults applied).
func New(c *p4.Client, cfg Config) *Server {
	cfg = cfg.withDefaults()
	return &Server{
		client:   c,
		backstop: cfg.Backstop,
		debounce: cfg.Debounce,
		maxWait:  cfg.MaxWait,
	}
}

func (s *Server) store(r reconcile.Result) {
	s.mu.Lock()
	s.result, s.ok, s.lastErr, s.anchored = r, true, nil, r.At
	s.mu.Unlock()
}

func (s *Server) fail(err error) {
	s.mu.Lock()
	s.lastErr = err
	// Note: we keep the last good result and ok flag. A single failed poll
	// doesn't erase what we last knew; it just ages. Callers see age + error.
	s.mu.Unlock()
}

// snapshot returns a consistent copy of the fields the endpoints report.
func (s *Server) snapshot() (r reconcile.Result, ok bool, lastErr error, age time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	age = time.Duration(0)
	if !s.anchored.IsZero() {
		age = time.Since(s.anchored)
	}
	return s.result, s.ok, s.lastErr, age
}

// Run keeps the cached answer fresh until ctx is cancelled. It starts a file
// watcher and drives re-anchoring from its signals; if the watcher cannot start,
// it degrades to backstop-only polling rather than failing, since a slower-to-
// refresh answer is still correct.
func (s *Server) Run(ctx context.Context) {
	tw, err := newTreeWatcher(s.client.Root)
	if err != nil {
		log.Printf("file watching unavailable (%v); backstop polling only", err)
		s.loop(ctx, nil, nil, func() { s.anchor(ctx) })
		return
	}
	defer tw.Close()
	go tw.run(ctx)
	s.loop(ctx, tw.pokes, tw.force, func() { s.anchor(ctx) })
}

// loop drives re-anchoring from three sources until ctx is done:
//   - pokes: coalesced filesystem-change signals, re-anchored after a debounce
//     (capped by maxWait so sustained churn still refreshes)
//   - force: signals that demand an immediate re-anchor (e.g. dropped events)
//   - the backstop ticker: runs even with no events, bounding staleness
//
// anchor is the action to run; it is injected so the loop's timing behavior can
// be tested without a live Perforce server.
func (s *Server) loop(ctx context.Context, pokes, force <-chan struct{}, anchor func()) {
	anchor() // one pass now: we want a real answer before we start serving

	backstop := time.NewTicker(s.backstop)
	defer backstop.Stop()

	var (
		timer         *time.Timer
		debounceC     <-chan time.Time
		burstDeadline time.Time // latest we will let a burst delay a re-anchor
	)
	stopDebounce := func() {
		if timer != nil {
			timer.Stop()
		}
		debounceC = nil
		burstDeadline = time.Time{}
	}

	for {
		select {
		case <-ctx.Done():
			stopDebounce()
			return

		case <-pokes:
			// Debounce: wait for the edits to settle, but never past the burst
			// deadline, so a stream that never goes quiet still re-anchors.
			if debounceC == nil {
				burstDeadline = time.Now().Add(s.maxWait)
			}
			wait := s.debounce
			if d := time.Until(burstDeadline); d < wait {
				wait = d
			}
			if wait < 0 {
				wait = 0
			}
			if timer == nil {
				timer = time.NewTimer(wait)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(wait)
			}
			debounceC = timer.C

		case <-debounceC:
			stopDebounce()
			anchor()

		case <-force:
			stopDebounce()
			anchor()

		case <-backstop.C:
			anchor()
		}
	}
}

// anchor performs one full reconcile pass and records the result honestly: a
// failure keeps the last good answer and surfaces the error, it never fabricates
// a clean state. Each pass is bounded so a hung server can't wedge the loop.
func (s *Server) anchor(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()

	r, err := reconcile.Run(ctx, s.client)
	if err != nil {
		s.fail(err)
		log.Printf("reconcile failed: %v", err)
		return
	}
	s.store(r)
	log.Printf("reconciled: %d dirty (%d walked, %d hashed, %d depot) in %s",
		r.Count(), r.Walked, r.Hashed, r.DepotFiles, r.Took.Round(time.Millisecond))
}
