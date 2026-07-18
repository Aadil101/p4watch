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
package daemon

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Aadil101/p4watch/internal/p4"
	"github.com/Aadil101/p4watch/internal/reconcile"
)

// Server is the daemon's cached view of the workspace plus the connection it
// reconciles against. The cached fields are guarded for concurrent reads (the
// endpoints) against the single writer (the re-anchor loop). client and
// interval are set once at construction and never mutated, so they need no lock.
type Server struct {
	client   *p4.Client
	interval time.Duration

	mu       sync.RWMutex
	result   reconcile.Result
	ok       bool      // has any reconcile pass ever succeeded?
	lastErr  error     // error from the most recent pass, if it failed
	anchored time.Time // when the cached result was produced
}

// New builds a Server that reconciles c on the given poll interval.
func New(c *p4.Client, interval time.Duration) *Server {
	return &Server{client: c, interval: interval}
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

// Run drives the re-anchor loop until ctx is cancelled: one pass now (blocking
// is fine, we want a real answer before serving), then on the timer forever. The
// periodic pass is the correctness backstop; once filesystem events are added
// they only shrink the window between passes, they never replace the pass.
func (s *Server) Run(ctx context.Context) {
	s.anchor(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.anchor(ctx)
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
