package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loopHarness runs Server.loop in the background with injectable signal channels
// and an anchor action that just counts calls, so the loop's timing behavior can
// be exercised with no Perforce server and no filesystem.
type loopHarness struct {
	pokes  chan struct{}
	force  chan struct{}
	count  atomic.Int64
	cancel context.CancelFunc
}

func startLoop(t *testing.T, cfg Config) *loopHarness {
	t.Helper()
	s := New(nil, cfg) // client is unused: the anchor action is injected below
	h := &loopHarness{
		pokes: make(chan struct{}),
		force: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	go s.loop(ctx, h.pokes, h.force, func() { h.count.Add(1) })
	return h
}

// waitForCount polls until the anchor count reaches at least want, or fails.
func (h *loopHarness) waitForCount(t *testing.T, want int64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if h.count.Load() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for anchor count >= %d, got %d", want, h.count.Load())
}

// The loop must run one pass immediately, before any event, so the daemon has a
// real answer to serve at startup rather than "unknown".
func TestLoopInitialAnchor(t *testing.T) {
	h := startLoop(t, Config{Debounce: time.Hour, MaxWait: time.Hour, Backstop: time.Hour})
	h.waitForCount(t, 1, time.Second)
}

// A burst of pokes must coalesce into a single re-anchor after the debounce, not
// one anchor per event.
func TestLoopCoalescesBurst(t *testing.T) {
	h := startLoop(t, Config{Debounce: 30 * time.Millisecond, MaxWait: time.Second, Backstop: time.Hour})
	h.waitForCount(t, 1, time.Second) // initial

	for i := 0; i < 8; i++ {
		h.pokes <- struct{}{}
	}
	h.waitForCount(t, 2, time.Second) // the coalesced pass

	// The burst produced exactly one extra anchor: give it room to misbehave.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int64(2), h.count.Load(), "burst should coalesce to one re-anchor")
}

// A forced signal (a dropped-events situation) must re-anchor immediately, not
// wait out the debounce.
func TestLoopForceIsImmediate(t *testing.T) {
	// Debounce is huge, so if force were debounced this test would time out.
	h := startLoop(t, Config{Debounce: time.Hour, MaxWait: time.Hour, Backstop: time.Hour})
	h.waitForCount(t, 1, time.Second) // initial

	start := time.Now()
	h.force <- struct{}{}
	h.waitForCount(t, 2, time.Second)
	assert.Less(t, time.Since(start), 500*time.Millisecond, "force must not wait for the debounce")
}

// With no events at all, the backstop ticker must keep re-anchoring: this is the
// safety net that bounds staleness when the event stream misses something.
func TestLoopBackstopFiresWithoutEvents(t *testing.T) {
	h := startLoop(t, Config{Debounce: time.Hour, MaxWait: time.Hour, Backstop: 25 * time.Millisecond})
	// Initial pass plus several backstop passes, with no pokes or forces sent.
	h.waitForCount(t, 4, time.Second)
}

// Under sustained churn the debounce never goes quiet, so the maxWait cap is
// what guarantees the answer still refreshes instead of waiting forever.
func TestLoopMaxWaitCapsSustainedChurn(t *testing.T) {
	h := startLoop(t, Config{Debounce: 50 * time.Millisecond, MaxWait: 80 * time.Millisecond, Backstop: time.Hour})
	h.waitForCount(t, 1, time.Second) // initial

	// Poke every 20ms for ~300ms: faster than the 50ms debounce ever settles, so
	// only the 80ms cap can trigger a re-anchor.
	done := time.After(300 * time.Millisecond)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			// At least two cap-driven anchors should have fired during the churn.
			require.GreaterOrEqual(t, h.count.Load(), int64(3),
				"maxWait cap should re-anchor during sustained churn")
			return
		case <-tick.C:
			h.pokes <- struct{}{}
		}
	}
}
