package httpserver

import (
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/platform/metrics"
)

// idSegment matches a UUID path segment, so /wallets/<uuid>/ledger is
// recorded as one time series in Prometheus ("/wallets/{id}/ledger")
// instead of one new series per wallet ever requested.
var idSegment = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func normalizePath(path string) string {
	return idSegment.ReplaceAllString(path, "{id}")
}

// withRequestLogging emits one structured JSON log line per request, and
// records the "latência de processamento" metric the spec's observability
// section requires. It assigns a correlationId when the caller doesn't send
// X-Correlation-Id, and never logs the request body - the spec explicitly
// forbids logging full financial payloads or credentials.
func withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		correlationID := r.Header.Get("X-Correlation-Id")
		if correlationID == "" {
			correlationID = uuid.NewString()
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		duration := time.Since(start)

		path := normalizePath(r.URL.Path)
		metrics.HTTPRequestDuration.
			WithLabelValues(r.Method, path, strconv.Itoa(rec.status)).
			Observe(duration.Seconds())

		slog.Info("http_request",
			"correlationId", correlationID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"durationMs", duration.Milliseconds(),
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
