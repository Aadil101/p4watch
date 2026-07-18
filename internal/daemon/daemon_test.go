package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Aadil101/p4watch/internal/reconcile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A fresh daemon has never reconciled. It must report unknown, not clean: a
// caller (shell prompt) has to render this differently from "0 dirty".
func TestStateFreshIsUnknown(t *testing.T) {
	s := &Server{}
	res, ok, lastErr, age := s.snapshot()
	assert.False(t, ok, "fresh state must be unknown, not clean")
	assert.Zero(t, res.Count())
	assert.NoError(t, lastErr)
	assert.Zero(t, age, "no pass yet, so no age")
}

// A successful pass makes the answer known and stamps its age from the pass time.
func TestStateStoreMakesKnown(t *testing.T) {
	s := &Server{}
	s.store(reconcile.Result{
		Changes: []reconcile.Change{
			{Path: `C:\ws\a.txt`, Kind: reconcile.Modified},
			{Path: `C:\ws\b.txt`, Kind: reconcile.Added},
		},
		At: time.Now(),
	})

	res, ok, lastErr, age := s.snapshot()
	assert.True(t, ok, "after a successful pass the answer is known")
	assert.Equal(t, 2, res.Count())
	assert.NoError(t, lastErr)
	assert.GreaterOrEqual(t, age, time.Duration(0))
	assert.Less(t, age, time.Minute, "age stamped from pass time, so ~0")
}

// A failed pass must NOT erase the last good answer. It keeps the result and the
// known flag, but surfaces the error so /summary can flag the answer as stale.
func TestStateFailKeepsLastGood(t *testing.T) {
	s := &Server{}
	s.store(reconcile.Result{
		Changes: []reconcile.Change{{Path: `C:\ws\a.txt`, Kind: reconcile.Modified}},
		At:      time.Now(),
	})

	wantErr := errors.New("p4 fstat: connection refused")
	s.fail(wantErr)

	res, ok, lastErr, _ := s.snapshot()
	assert.True(t, ok, "a failed poll keeps the last good answer known")
	assert.Equal(t, 1, res.Count(), "result not erased on failure")
	assert.EqualError(t, lastErr, wantErr.Error(), "error surfaced for the stale flag")
}

// getSummary drives the /summary handler through httptest and decodes the body.
func getSummary(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleSummary(rr, httptest.NewRequest(http.MethodGet, "/summary", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	return body
}

// The /summary JSON body is the contract a shell prompt renders against, so its
// shape is pinned directly: an un-reconciled daemon must be known:false (render
// as unknown, never as clean) and must not carry an error key.
func TestSummaryContractUnknown(t *testing.T) {
	body := getSummary(t, &Server{})
	assert.Equal(t, false, body["known"], "un-reconciled must be known:false")
	assert.Equal(t, float64(0), body["count"])
	assert.Equal(t, false, body["stale"])
	assert.NotContains(t, body, "error", "no error key when nothing has failed")
}

// After a good pass the body reports the count and clears stale; after a later
// failure it stays known but flips stale and exposes the error string.
func TestSummaryContractKnownThenStale(t *testing.T) {
	s := &Server{}
	s.store(reconcile.Result{
		Changes: []reconcile.Change{
			{Path: `C:\ws\a.txt`, Kind: reconcile.Modified},
			{Path: `C:\ws\b.txt`, Kind: reconcile.Added},
		},
		DepotFiles: 100,
		At:         time.Now(),
	})

	body := getSummary(t, s)
	assert.Equal(t, true, body["known"])
	assert.Equal(t, float64(2), body["count"])
	assert.Equal(t, float64(100), body["depot"])
	assert.Equal(t, false, body["stale"])
	assert.NotContains(t, body, "error")

	s.fail(errors.New("p4 fstat: connection refused"))

	body = getSummary(t, s)
	assert.Equal(t, true, body["known"], "last good answer stays known")
	assert.Equal(t, float64(2), body["count"], "count not erased")
	assert.Equal(t, true, body["stale"], "stale flips after a failed poll")
	assert.Equal(t, "p4 fstat: connection refused", body["error"])
}
