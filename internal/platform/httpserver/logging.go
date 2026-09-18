package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// withRequestLogging emits one structured JSON log line per request. It
// assigns a correlationId when the caller doesn't send X-Correlation-Id,
// and never logs the request body - the spec explicitly forbids logging
// full financial payloads or credentials.
func withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		correlationID := r.Header.Get("X-Correlation-Id")
		if correlationID == "" {
			correlationID = uuid.NewString()
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		slog.Info("http_request",
			"correlationId", correlationID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"durationMs", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
