// Command p4watchd is the p4watch daemon.
//
// It keeps a cached answer to one question ("what in this workspace differs
// from the server?") and serves it over a local HTTP endpoint in O(1). The
// answer is produced by a full reconcile pass that runs at startup and then on
// a timer. Filesystem-event-driven invalidation is a later step; this version
// establishes the correct, always-available baseline: a poll-and-cache daemon.
//
// The design rule that makes this honest: the daemon never reports a confident
// "clean" unless a reconcile pass actually succeeded. Until the first pass
// completes, or after one fails, its state is "unknown", a distinct answer a
// caller (like a shell prompt) must render differently from clean. A cheap
// wrong "all clear" is the one failure this tool exists to avoid.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Aadil101/p4watch/internal/p4"
	"github.com/Aadil101/p4watch/internal/reconcile"
)

// state is the daemon's cached view of the workspace, guarded for concurrent
// reads (the endpoints) against the single writer (the re-anchor loop).
type state struct {
	mu       sync.RWMutex
	result   reconcile.Result
	ok       bool      // has any reconcile pass ever succeeded?
	lastErr  error     // error from the most recent pass, if it failed
	anchored time.Time // when the cached result was produced
}

func (s *state) store(r reconcile.Result) {
	s.mu.Lock()
	s.result, s.ok, s.lastErr, s.anchored = r, true, nil, r.At
	s.mu.Unlock()
}

func (s *state) fail(err error) {
	s.mu.Lock()
	s.lastErr = err
	// Note: we keep the last good result and ok flag. A single failed poll
	// doesn't erase what we last knew; it just ages. Callers see age + error.
	s.mu.Unlock()
}

// snapshot returns a consistent copy of the fields the endpoints report.
func (s *state) snapshot() (r reconcile.Result, ok bool, lastErr error, age time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	age = time.Duration(0)
	if !s.anchored.IsZero() {
		age = time.Since(s.anchored)
	}
	return s.result, s.ok, s.lastErr, age
}

func main() {
	var (
		root     = flag.String("root", `C:\p4bench\ws`, "local workspace root")
		port     = flag.String("p4port", "localhost:1666", "P4PORT")
		user     = flag.String("p4user", "bench", "P4USER")
		client   = flag.String("p4client", "bench_ws", "P4CLIENT (workspace name)")
		p4bin    = flag.String("p4bin", "p4", "path to the p4 executable")
		addr     = flag.String("addr", "127.0.0.1:7778", "status endpoint address")
		interval = flag.Duration("interval", 15*time.Second, "re-anchor poll interval")
	)
	flag.Parse()

	c := &p4.Client{Port: *port, User: *user, Client: *client, Root: *root, Bin: *p4bin}
	st := &state{}

	// Re-anchor loop: one pass now (blocking is fine, we want a real answer
	// before we start serving), then on the timer forever. The periodic pass is
	// the correctness backstop; once events are added later they only shrink the
	// window between passes, they never replace the pass.
	go func() {
		anchor := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			r, err := reconcile.Run(ctx, c)
			if err != nil {
				st.fail(err)
				log.Printf("reconcile failed: %v", err)
				return
			}
			st.store(r)
			log.Printf("reconciled: %d dirty (%d walked, %d hashed, %d depot) in %s",
				r.Count(), r.Walked, r.Hashed, r.DepotFiles, r.Took.Round(time.Millisecond))
		}
		anchor()
		t := time.NewTicker(*interval)
		defer t.Stop()
		for range t.C {
			anchor()
		}
	}()

	// /summary: the hot path. O(1): a cached count and the metadata a caller
	// needs to decide clean vs dirty vs unknown. This is what a shell prompt
	// calls on every render; it never serializes the file list.
	http.HandleFunc("/summary", func(w http.ResponseWriter, r *http.Request) {
		res, ok, lastErr, age := st.snapshot()
		body := map[string]any{
			"known":   ok, // false => never reconciled; render as unknown, not clean
			"count":   res.Count(),
			"age_ms":  age.Milliseconds(),
			"depot":   res.DepotFiles,
			"walked":  res.Walked,
			"hashed":  res.Hashed,
			"took_ms": res.Took.Milliseconds(),
			"stale":   lastErr != nil, // last poll errored; cached answer is aging
		}
		if lastErr != nil {
			body["error"] = lastErr.Error()
		}
		writeJSON(w, body)
	})

	// /status: the full dirty list. O(changes) in bytes, so it is NOT the
	// prompt's endpoint; it is for a human or a review UI that wants the paths.
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		res, ok, lastErr, age := st.snapshot()
		changes := make([]map[string]string, 0, len(res.Changes))
		for _, ch := range res.Changes {
			changes = append(changes, map[string]string{"path": ch.Path, "kind": ch.Kind.String()})
		}
		body := map[string]any{
			"known":   ok,
			"count":   len(changes),
			"age_ms":  age.Milliseconds(),
			"changes": changes,
		}
		if lastErr != nil {
			body["error"] = lastErr.Error()
		}
		writeJSON(w, body)
	})

	// /healthz: liveness only. Says nothing about the workspace.
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, ok, _, _ := st.snapshot()
		writeJSON(w, map[string]any{"up": true, "reconciled": ok})
	})

	log.Printf("p4watchd watching %s via %s", *root, *port)
	log.Printf("endpoints: http://%s/summary  /status  /healthz", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
