package httpserver

import "net/http"

type healthResponse struct {
	Status string `json:"status"`
}

// handleLive answers "is the process alive at all" - it never touches
// Postgres or any other dependency, so it stays fast and reliable even if
// those are struggling.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "UP"})
}

// handleReady additionally checks PostgreSQL connectivity - a load
// balancer should stop routing traffic here if this fails, even though the
// process itself is still running.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.pool.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "DOWN"})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "UP"})
}
