// Package httpserver exposes the application's use cases over HTTP. It
// knows about net/http, JSON, and status codes; it does not know about
// Postgres, Fx, or SQS - it only calls into internal/app.
package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"backend-challenge-go/internal/app"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/platform/keycloak"
)

type Server struct {
	uc   *app.UseCases
	pool *pgxpool.Pool // used only for the readiness health check
}

// NewRouter builds the full request mux. Kept separate from *http.Server so
// tests can exercise it directly (httptest.NewServer(router)) without
// spinning up a real listener.
//
// Every business endpoint requires a valid token; only the two health
// checks are public. Wallet management is gated on the "internal" realm
// role (never granted to a provider client - see
// deploy/keycloak/realm-export.json); the wagering endpoints are gated on
// "provider", with the handlers themselves further restricting a provider
// to its own data by comparing the token's client identity against the
// resource being accessed.
func NewRouter(uc *app.UseCases, pool *pgxpool.Pool, verifier *keycloak.Verifier) http.Handler {
	s := &Server{uc: uc, pool: pool}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /wallets", requireRole(verifier, RoleInternal, s.handleOpenWallet))
	mux.HandleFunc("GET /wallets/{walletId}", requireRole(verifier, RoleInternal, s.handleGetWallet))
	mux.HandleFunc("GET /wallets/{walletId}/ledger", requireRole(verifier, RoleInternal, s.handleGetWalletLedger))
	mux.HandleFunc("POST /wallets/{walletId}/reconciliation", requireRole(verifier, RoleInternal, s.handleReconcileWallet))
	mux.HandleFunc("POST /wagering/transactions", requireRole(verifier, RoleProvider, s.handleSubmitWagerTransaction))
	mux.HandleFunc("GET /wagering/transactions/{transactionId}", requireRole(verifier, RoleProvider, s.handleGetWagerTransaction))
	mux.HandleFunc("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", requireRole(verifier, RoleProvider, s.handleGetWagerTransactionByExternalID))
	mux.HandleFunc("GET /health/live", s.handleLive)
	mux.HandleFunc("GET /health/ready", s.handleReady)
	// Public like the health checks: a Prometheus scraper has no bearer
	// token, and the exposed data is aggregate counts/latencies only -
	// never a financial payload or credential.
	mux.Handle("GET /metrics", promhttp.Handler())

	return withRequestLogging(mux)
}

func NewServer(cfg config.Config, router http.Handler) *http.Server {
	return &http.Server{
		Addr:    ":" + cfg.HTTPPort,
		Handler: router,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Code: code, Message: message})
}
