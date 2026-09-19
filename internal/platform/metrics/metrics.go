// Package metrics defines every metric the spec's observability section
// requires: results by status, duplicates, retries, DLQ-bound failures,
// concurrency conflicts, outbox delay, processing latency, and
// reconciliation divergences. Each is a package-level collector registered
// once at import time (the idiomatic Prometheus-Go pattern via promauto),
// so callers anywhere in the codebase just call .Inc()/.Observe() without
// needing the metric injected through Fx - the same way internal/platform
// packages call log/slog directly rather than threading a logger through
// every constructor.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// TransactionsTotal covers "resultados por status": every terminal outcome
// of SubmitWagerTransaction, labeled by kind and the status it landed in.
var TransactionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "wager_transactions_total",
	Help: "Wager transactions processed, by kind and final status.",
}, []string{"kind", "status"})

// IdempotentReplaysTotal covers "duplicatas": a submission that matched an
// already-persisted operation instead of creating a new one - whether the
// duplicate arrived via HTTP or SQS.
var IdempotentReplaysTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "wager_idempotent_replays_total",
	Help: "Submissions resolved as an idempotent replay of an existing operation.",
})

// VersionConflictsTotal covers "conflitos de concorrência": every time the
// optimistic-concurrency retry loop lost a race on a wallet's version and
// had to re-read and retry.
var VersionConflictsTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "wallet_version_conflicts_total",
	Help: "Optimistic-concurrency conflicts on wallet updates that triggered a retry.",
})

// ReferenceRetriesTotal covers the "retries" half of "retries, DLQ": each
// time the reference-retry worker re-evaluated a PENDING_REFERENCE
// transaction and it was still unresolved, so it rescheduled with backoff.
var ReferenceRetriesTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "reference_retries_total",
	Help: "REFUND/ROLLBACK re-evaluations that were still unresolved and were rescheduled.",
})

// SQSMessagesTotal covers the "DLQ" half: outcome is one of "processed",
// "business_rejected" (malformed/invalid - left for the queue's redrive
// policy to eventually dead-letter) or "transient_failure" (left for
// natural redelivery). This process never publishes to the DLQ directly
// (SQS's redrive policy does that after maxReceiveCount), so this counter
// is the closest first-party signal for "messages trending toward the DLQ".
var SQSMessagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "sqs_messages_total",
	Help: "Incoming SQS messages by processing outcome.",
}, []string{"outcome"})

// OutboxPublishTotal covers publish attempts by the outbox worker.
var OutboxPublishTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "outbox_publish_attempts_total",
	Help: "Outbox publish attempts, by outcome (success/failure).",
}, []string{"outcome"})

// OutboxDelaySeconds covers "atraso da outbox": the time between an event
// occurring (its envelope's occurredAt) and the moment a worker actually
// publishes it - the end-to-end publish lag under normal operation and
// under backlog/backoff.
var OutboxDelaySeconds = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "outbox_publish_delay_seconds",
	Help:    "Seconds between an event occurring and being published.",
	Buckets: prometheus.ExponentialBuckets(0.05, 2, 14), // 50ms .. ~410s
})

// HTTPRequestDuration covers "latência de processamento" for the HTTP entry point.
var HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "http_request_duration_seconds",
	Help:    "HTTP request processing latency.",
	Buckets: prometheus.DefBuckets,
}, []string{"method", "path", "status"})

// ReconciliationDivergencesTotal covers "divergências de reconciliação":
// every time Reconcile found the stored balance didn't match the ledger.
var ReconciliationDivergencesTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "reconciliation_divergences_total",
	Help: "Reconciliation checks that found stored and calculated balances disagree.",
})
