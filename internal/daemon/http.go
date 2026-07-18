package daemon

import (
	"encoding/json"
	"net/http"
)

// Register wires the daemon's endpoints onto mux. Keeping this here (rather than
// in main) means the routes and their JSON contracts are exercised by tests.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/summary", s.handleSummary)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/healthz", s.handleHealthz)
}

// handleSummary is the hot path. O(1): a cached count plus the metadata a caller
// needs to decide clean vs dirty vs unknown. This is what a shell prompt calls
// on every render; it never serializes the file list. The JSON shape here is a
// contract callers depend on, so it is tested directly.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	res, ok, lastErr, age := s.snapshot()
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
}

// handleStatus is the full dirty list. O(changes) in bytes, so it is NOT the
// prompt's endpoint; it is for a human or a review UI that wants the paths.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	res, ok, lastErr, age := s.snapshot()
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
}

// handleHealthz is liveness only. It says nothing about the workspace.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	_, ok, _, _ := s.snapshot()
	writeJSON(w, map[string]any{"up": true, "reconciled": ok})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
