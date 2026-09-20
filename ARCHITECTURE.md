# Architecture

This document records the decisions behind the implementation, why they
were made, and what is explicitly out of scope or incomplete.

## 1. Money

`Money` (`internal/domain/money`) is an immutable value object backed by
`int64` **minor units** (cents for BRL) — never `float32`/`float64`, at any
stage: parsing, arithmetic, serialization or persistence.

- `ParseExternal` is the strict constructor used for every external input
  (HTTP body, SQS `data.money`): the amount must match
  `^[0-9]+\.[0-9]{2}$` exactly — no sign, no exponent, no missing/excess
  scale, no `NaN`/`Infinity`. This regex alone rejects every case the spec
  lists (empty, `NaN`, `Infinity`, scientific notation, wrong scale,
  negative).
- `New` is the internal constructor and *does* allow negative amounts —
  legitimate for reconciliation differences (`storedBalance - calculatedBalance`)
  even though no wallet balance or external input may ever be negative.
  That non-negativity rule lives on `Wallet`, not on `Money`.
- All arithmetic (`Add`, `Sub`, `Negate`, and decimal parsing) is
  overflow-checked by hand, because Go does not trap `int64` overflow — it
  silently wraps, which would be an invisible financial bug.
- `MarshalJSON`/`UnmarshalJSON` are hand-written to guarantee
  `{"amount":"25.00","currency":"BRL"}` — `amount` as a JSON **string**.
  If it were a bare JSON number, `encoding/json` would decode it through
  `float64` internally, silently reintroducing the exact problem `Money`
  exists to prevent.
- Limits/representation: `int64` minor units at scale 2 supports amounts up
  to roughly 92 quadrillion currency units before overflow — far beyond any
  realistic wager. Only BRL is exercised in the main scenarios, but the
  type always carries a currency and every operation checks it
  (`ErrCurrencyMismatch`), satisfying the spec's currency-incompatibility
  requirement without hardcoding BRL anywhere in the domain layer.

## 2. Wallet, ledger, and transaction boundaries

`Wallet.Debit`/`Credit` (`internal/domain/wallet`) are pure functions: they
take the current wallet + amount and return a *new* wallet value plus a
`LedgerEntry`, doing no I/O. The SQL transaction boundary is owned entirely
by `internal/app` (the use-case layer), which is the only place aware that
a debit and its ledger entry and the transaction row and the outbox events
must commit atomically or not at all.

Within one `internal/app` transaction, insert order matters and is
deliberate: the `wager_transactions` row is inserted **before** its
`wallet_ledger_entries` row, because `wallet_ledger_entries.transaction_id`
has a foreign key that Postgres checks at `INSERT` time, not deferred to
`COMMIT`. Getting this backwards was an actual bug caught while building
this - see §12 for this and the other bugs found along the way.

## 3. Idempotency

Every external operation carries a client-supplied `Idempotency-Key` and a
server-computed `payloadHash`:

- **Hash scope**: `internal/app/idempotency.go` hashes exactly the business
  fields (`providerId`, `externalTransactionId`, `playerId`, `walletId`,
  `roundId`, `gameId`, `kind`, `money`, `referenceExternalTransactionId`) —
  never the `Idempotency-Key` itself, never transport metadata (SQS's
  `messageId`/`occurredAt`, HTTP headers). This is what lets HTTP and SQS
  entry points agree on the same hash for the same operation.
- **Canonicalization**: the fields are marshaled from a `map[string]any`,
  not a struct — `encoding/json` sorts map keys alphabetically, which is
  the entire "canonical JSON with key ordering" requirement; no bespoke
  canonicalization library needed.
- **Contract**: same key + same hash → replay (`idempotentReplay: true`,
  returns the *original* persisted result, including the balance observed
  at the time, even if the wallet has since moved further — this is why
  `WagerTransaction.ResultBalance()` is persisted on the row rather than
  recomputed). Same key + different hash → `409` (`ErrIdempotencyKeyConflict`).
  Same `(providerId, externalTransactionId)` under a *different* key →
  `409` (`ErrExternalTransactionKeyMismatch`) — the database enforces this
  with a unique partial index (`ux_wager_transactions_provider_external`),
  the application enforces it as a courtesy check before ever touching the
  wallet.
- **Concurrent duplicates**: when two identical submissions race (the
  mandatory "same bet 50 times in parallel" scenario), the loser's `INSERT`
  hits the unique constraint (`postgres.ErrIdempotencyConflict`) *after*
  the winner has already committed (Postgres blocks a conflicting insert
  until the earlier transaction resolves, so by the time the error
  surfaces the winner is guaranteed committed). The loser then re-runs the
  idempotency lookup and returns the winner's result as a replay, rather
  than failing the request.
- **A genuinely subtle bug found here**: the idempotency lookup does two
  separate round trips (by key, then by external id) - not one atomic
  read. Under heavy concurrency, the winner can commit *between* those two
  calls: the first lookup (by key) misses, the second (by external id)
  hits, and naively that looks like "reused under a different key". Fixed
  by checking the found row's *actual* idempotency key before deciding
  it's a real conflict rather than the same row observed late.

## 4. Concurrency control

**Wallet**: optimistic concurrency via a `version` column. Every
balance-changing write is `UPDATE wallets SET ... WHERE id = $1 AND
version = $2`; zero rows affected means another writer won the race, and
the caller (`internal/app`) re-reads and retries (bounded at 20 attempts).
No row is ever locked while idle — unrelated wallets never contend, and a
losing writer never blocks, it just redoes cheap, already-in-memory work.
This was chosen over pessimistic locking because the operations here are
short and the failure mode (redo a few microseconds of CPU work) is
cheaper than holding a row lock across a network round trip.

**WagerTransaction**: the same pattern using `status` as the compare-and-swap
field (`UpdateStatus(... WHERE id = $1 AND status = $2)`), since a
transaction's meaningful "version" *is* its status for CAS purposes — this
is what lets two publisher/worker instances safely race to finish the same
`PENDING_REFERENCE` row without double-processing it.

**Outbox**: multiple publisher workers claim rows with `SELECT ... FOR
UPDATE SKIP LOCKED`. This is the one place pessimistic locking is used,
deliberately — the lock is held only for the duration of one short
transaction (claim + publish + mark), and `SKIP LOCKED` means concurrent
workers partition the work instead of blocking on each other. If a worker
crashes mid-batch, Postgres releases its row locks automatically when the
connection drops, so an "abandoned" claim needs no separate recovery
sweep — it's just claimable again on the next poll.

## 5. Pending references (REFUND/ROLLBACK arriving early)

If a `REFUND`/`ROLLBACK`'s reference isn't found, or is found but still
`PENDING`/`PENDING_REFERENCE` itself, the transaction is persisted as
`PENDING_REFERENCE` and a `WagerTransactionPendingReference` event is
staged. A background worker (`internal/platform/referenceretry`) polls
`wager_transactions` every 5s for due rows (`next_retry_at <= now()`) and
re-evaluates each one from scratch — this is entirely driven from
persisted state, so it survives an application restart with no special
handling.

- If the reference has since resolved to `PROCESSED` and matches (same
  provider/player/wallet/round/currency/amount, and a valid kind
  combination — see below), the reversal proceeds.
- If the reference resolved to `REJECTED`/`FAILED`, the reversal is
  rejected immediately with `REFERENCE_NOT_PROCESSABLE` — there's nothing
  to wait for.
- If still unresolved, `attempts` increments and the next retry is
  scheduled with doubling backoff (30s, 1m, 2m, 4m, 8m, capped at 10m).
  After 5 attempts, it's rejected with `REFERENCE_NOT_FOUND`.

**Reversal direction**: `REFUND` only ever reverses a `BET` (credit,
undoing the debit). `ROLLBACK` reverses a `BET` (credit) or a `WIN`/`REFUND`
(debit, undoing their credit) — any other combination is
`REFERENCE_MISMATCH`. A debit-direction `ROLLBACK` that would overdraw the
wallet is rejected with `INSUFFICIENT_BALANCE_FOR_REVERSAL` — a distinct
code from `INSUFFICIENT_BALANCE` (used for an under-funded `BET`), per the
spec's explicit requirement.

**Duplicate reversals**: before applying a reversal, `ExistsProcessedReversal`
checks whether a `PROCESSED` transaction of the *same kind* already
resolved to this reference — a `BET` may have one successful `REFUND` and,
independently, one successful `ROLLBACK` (they're different kinds), but
never two of the same kind.

## 6. Inbox / outbox

Both tables share one invariant with the domain write they accompany: the
`inbox_messages` row (SQS path only) and the `outbox_events` row(s) are
inserted in the *same* database transaction as the wallet/ledger/transaction
change — see `internal/app/usecases.go`'s `stageEvent` and
`SubmitWagerTransactionCommand.InboxConsumerName/InboxMessageID`. This is
what makes "publish only after commit" true without a distributed
transaction: the outbox row simply doesn't exist for anyone to read until
the whole business transaction commits.

The outbox worker (`internal/platform/outbox`) is a separate process
concern entirely — it never runs inside the business transaction, only
reads already-committed rows afterward, on a 2-second poll. Each event
`payload` column stores the **entire envelope** (`eventId`, `eventType`,
`aggregateId`, `correlationId`, `causationId`, `occurredAt`, `version`,
`data`), not just the inner data, so a retried publish sends byte-identical
content and `eventId` is preserved across retries automatically (it's part
of what's stored).

**Found bug**: `events.Envelope` originally had no JSON tags and
`OutboxRepository.Insert` only marshaled `env.Data`, so the *actual*
messages published to SQS were missing the envelope wrapper entirely (just
bare, Go-capitalized inner fields). Fixed by tagging `Envelope` properly
and marshaling the whole struct.

## 7. Authentication & authorization

**IdP choice**: Keycloak, per the spec's recommendation, running via
`start-dev --import-realm`. The realm (`deploy/keycloak/realm-export.json`)
provisions three `client_credentials`-only clients:
`provider-a`/`provider-b` (realm role `provider`) and `internal-service`
(realm role `internal`) — mirroring the spec's split between provider
operations (wagering) and internal-only operations (wallet management,
reconciliation).

**Validation**: `internal/platform/keycloak` does OIDC discovery + JWKS-based
JWT verification (`go-oidc`), with `SkipClientIDCheck: true` — these are
`client_credentials` access tokens, not audience-scoped ID tokens, so
audience checking isn't the right authorization mechanism here. Instead:

- **Role** (`realm_access.roles`) gates which *class* of endpoint a token
  may call at all (`internal` vs `provider`), enforced by
  `httpserver.requireRole`.
- **Client identity** (`azp`/`client_id` claim) is what the spec means by
  "the authenticated identity determines the authorized providerId" — the
  `providerId` in a request body/path is **never trusted on its own**; it
  must equal the token's client identity, or the request is rejected
  (`403` for a body/path mismatch on write; `404`, not `403`, when reading
  another provider's already-existing transaction, so a 403 doesn't
  itself confirm the resource exists).

**Deployment gotcha worth remembering**: Keycloak derives each issued
token's `iss` claim from whichever host:port the *caller* used to reach
it, unless pinned. Since the containerized API discovers the issuer via
the `keycloak` compose service name (port 8080) but a human/test client
requests tokens through the published host port (8081), those two paths
would otherwise produce tokens with different issuers and validation would
fail for tokens obtained via the host port. Fixed by setting `KC_HOSTNAME`
to the **full URL** `http://keycloak:8080` (setting `KC_HOSTNAME_PORT`
alone was not sufficient — Keycloak's hostname-v2 provider kept echoing
back whichever port a request actually arrived on).

## 8. Uber Fx composition & shutdown

`cmd/api/main.go` is the only place that constructs concrete
infrastructure and wires it together; `internal/domain/*` never imports Fx,
HTTP, SQS or Postgres, and `internal/app` never imports Fx or HTTP.

Every component with a start/stop concern registers an `fx.Hook`:

- The Postgres pool's hook is registered **first** (`OnStart` runs
  migrations; `OnStop` closes the pool) — Fx runs `OnStop` hooks in
  *reverse* registration order, so the pool closes **last**, after the
  HTTP server and every background worker that depends on it has already
  stopped.
- The HTTP server's `OnStop` calls `server.Shutdown(ctx)`: stops accepting
  new connections, lets in-flight requests finish within the shutdown
  deadline.
- The three background workers (SQS consumer, outbox publisher,
  reference-retry) share a `backgroundWorker` helper: `OnStart` launches a
  goroutine with its own cancellable context; `OnStop` cancels that context
  **and blocks on a `sync.WaitGroup`** until the goroutine has actually
  returned, not just been asked to. This is what makes shutdown
  *observable* rather than fire-and-forget, per the spec's requirement.
- The SQS consumer specifically: on cancellation it stops issuing new
  `ReceiveMessage` calls immediately, but a message already being handled
  finishes normally (deleted from the queue only after its durable
  handling committed). Anything it can't finish in time simply keeps its
  SQS visibility timeout running out on its own — no special "release"
  call needed, redelivery to another instance is the natural outcome.

## 9. Observability

Structured JSON logs (`log/slog`) cover every identifier the spec names
("correlationId, messageId, transactionId, walletId e providerId"), split
across the layer where each is naturally available rather than forced into
one line: `correlationId` + method/path/status/duration on every HTTP
request; `messageId` + `transactionId` + status on SQS processing;
`transactionId` + `walletId` + `providerId` + `kind` + `status` +
`failureCode` on every `SubmitWagerTransaction` outcome (HTTP or SQS,
new or replayed - one log call in `internal/app`, not duplicated per
transport). Never a credential or full financial payload.

Metrics are exposed at `GET /metrics` (Prometheus text format, public like
the health checks — a scraper carries no bearer token, and the data is
aggregate counts/latencies, never a financial payload) via
`internal/platform/metrics`, one package-level collector per concern so any
layer can record without threading a metrics client through every
constructor:

| Metric | Spec requirement it covers |
| --- | --- |
| `wager_transactions_total{kind,status}` | "resultados por status" |
| `wager_idempotent_replays_total` | "duplicatas" |
| `wallet_version_conflicts_total` | "conflitos de concorrência" |
| `reference_retries_total` | "retries" (REFUND/ROLLBACK backoff) |
| `sqs_messages_total{outcome}` | the "DLQ" half — this process never publishes to the DLQ itself (SQS's redrive policy does that after `maxReceiveCount`), so `business_rejected`/`transient_failure` counts are the closest first-party signal for messages trending toward it |
| `outbox_publish_attempts_total{outcome}` / `outbox_publish_delay_seconds` | "atraso da outbox" |
| `http_request_duration_seconds{method,path,status}` | "latência de processamento" |
| `reconciliation_divergences_total` | "divergências de reconciliação" |

`path` labels normalize UUID segments to `{id}` (e.g.
`/wallets/{id}/ledger`) before labeling, so the cardinality stays bounded by
route count rather than growing with every wallet ever requested.

**Dashboard (optional differentiator):** the spec explicitly names
dashboards as an optional extra ("dashboards são diferenciais opcionais",
CHALLENGE.md §12), so `prometheus` and `grafana` services were added on top of the
metrics above - never required for `api`/`postgres`/`keycloak`/`localstack`
to work, purely additive. Prometheus scrapes `api:8080/metrics` every 5s
(`deploy/prometheus/prometheus.yml`); Grafana auto-provisions a
datasource pointing at it and a ready-made dashboard
(`deploy/grafana/dashboards/backend-challenge.json`) with one panel per
metric above, available at `http://localhost:3000` (anonymous viewer
access enabled locally - no login needed to look, `admin`/`admin` to edit).

## 10. Manually verified against the live stack

These scenarios were exercised against the real `docker compose` stack
(not mocked) but are not yet captured as automated `go test` cases - the
domain, persistence, and application-layer concurrency/idempotency
scenarios *are* covered by automated integration tests (see README's test
section); these are the infrastructure-adjacent ones verified by hand:

- **Graceful shutdown (SIGTERM)**: `docker compose stop api` was observed
  to stop, in order, the reference-retry worker, the outbox publisher, the
  SQS consumer, then the HTTP server, then the Postgres pool - exactly the
  reverse-registration order documented in §8, with zero errors, in under
  one second.
- **Abrupt termination + restart recovery**: a REFUND was submitted
  referencing a BET that didn't exist yet (persisted as
  `PENDING_REFERENCE`); the API process was then killed with `docker kill`
  (SIGKILL, no graceful shutdown at all) and restarted; the missing BET was
  submitted; the reference-retry worker **in the new process** picked up
  the pending row from the database within one tick (5s) and resolved the
  REFUND to `PROCESSED` with the correct balance - proving the retry state
  is durable, not in-memory, and survives a crash mid-flow.
- **SQS redelivery → DLQ**: a message with an unparsable `playerId` was
  sent to `wager-transactions.fifo`; it was redelivered and rejected
  exactly 5 times (`sqs_messages_total{outcome="business_rejected"}` read
  5 after the fact), then LocalStack's redrive policy moved it to
  `wager-transactions-dlq.fifo` automatically, ~150s later (5 × the 30s
  visibility timeout) - confirmed by polling the DLQ's message count.
- **Grafana dashboard**: confirmed rendering live data (not just
  provisioned-but-empty) after generating sample traffic - see §9.
- **Postgres unavailability, mid-operation and recovery**: `docker compose
  stop postgres` was run while the api container kept running. `GET
  /health/live` stayed `UP` (the process itself is fine) while `GET
  /health/ready` correctly flipped to `DOWN`/503. A bet submission during
  the outage returned `503 SERVICE_UNAVAILABLE` with a generic message
  (the real connection error - which names the DSN/hostname - was
  confirmed to land only in the server-side log, never the HTTP response).
  Postgres was then restarted with **no restart of the api process**:
  `/health/ready` recovered on its own (`pgxpool` reconnects
  transparently) and resubmitting the exact same request (same
  Idempotency-Key) processed cleanly as a brand-new operation, at the
  correct balance - proving the failed attempts left no partial or
  corrupt state behind. This fixed a real gap found while testing it: the
  error path previously returned a generic `500` with the raw internal
  error text (including the Postgres DSN/hostname) in the response body;
  `postgres.IsUnavailable` (checks for `*pgconn.ConnectError`, `net.Error`,
  and `context.DeadlineExceeded`) now maps this specific case to `503`
  with a safe message, satisfying the spec's requirement that transient
  unavailability be distinguishable from other failures in the HTTP
  contract. SQS unavailability doesn't have an equivalent HTTP-path test
  because nothing in the HTTP request path calls SQS synchronously
  (publishing happens only in the background outbox worker); the
  consumer's and outbox worker's own resilience to a down SQS - log and
  retry, never crash - is exercised implicitly every time LocalStack
  finishes starting after them in `docker compose up`, and was watched
  directly in the container logs during this same test window.
- **The same operation crossing HTTP and SQS**: a BET was submitted over
  HTTP with `Idempotency-Key: provider-a:cross-channel-1` and processed
  normally. The identical operation (same `providerId` +
  `externalTransactionId` + `idempotencyKey` + payload) was then sent a
  second time, this time as a raw SQS message to `wager-transactions.fifo`
  instead of an HTTP request. The consumer logged
  `idempotentReplay=true` against the **same** `transactionId` the HTTP
  call had produced, the wallet balance was unchanged, and the ledger
  still held exactly one debit for it - confirming the two entry points
  genuinely share one idempotency/financial guarantee (they call the same
  `app.UseCases.SubmitWagerTransaction`, per `internal/platform/sqs/consumer.go`)
  rather than each keeping its own bookkeeping that happens to agree only
  in the untested happy path.

## 11. Known limitations / not implemented

- **Tracing**: not implemented (explicitly an optional differentiator per
  the spec).
- **Double-entry ledger**: not implemented (explicitly optional).
- **Load testing**: not implemented (explicitly optional, no minimum RPS
  required).
- **"At least three independent processes" (§8)**: the automated
  concurrency tests use goroutines with independent `pgxpool.Pool`
  connections (their own connections and memory, racing for real against
  Postgres) rather than three literal separate OS processes. Postgres
  cannot tell the difference - the guarantee it enforces (the `version`
  column's compare-and-swap) is identical either way - but this is a
  literal reading of "processos independentes" the current test suite
  doesn't satisfy verbatim.
- **Currency scope**: only BRL is exercised end-to-end; the type system
  supports any ISO 4217 code and currency-mismatch is tested, but no
  multi-currency wallet scenario was built.

## 12. Bugs found while building this

Four application-level bugs were found and fixed during development -
each is explained in full where it's contextually relevant (linked
below); this section is just the consolidated list. (Environment/tooling
friction - WSL, MinGW, the Keycloak hostname gotcha - is a separate
concern, covered in each README's "Troubleshooting" section instead,
since it's about reproducing the dev machine, not about the application's
own correctness.)

1. **Ledger entry inserted before its transaction row (§2)**: violated a
   foreign key that Postgres checks at `INSERT` time, not at `COMMIT`.
   Fixed by inserting the `wager_transactions` row first.
2. **Idempotency lookup race under heavy concurrency (§3)**: two
   sequential, non-atomic reads (by key, then by external id) could
   observe the winner's row committing in between them, misclassifying a
   late-observed replay as a "reused under a different key" conflict.
   Fixed by checking the found row's actual idempotency key before
   deciding it's a real conflict.
3. **Outbox events missing their envelope (§6)**: `events.Envelope` had no
   JSON tags and only `env.Data` was persisted, so messages published to
   SQS were missing `eventId`/`eventType`/`aggregateId`/`correlationId`/
   `version` entirely. Fixed by tagging `Envelope` and marshaling the
   whole struct.
4. **Transient Postgres unavailability returned a generic 500 with leaked
   internal detail (§10)**: found by deliberately stopping the `postgres`
   container while the API was serving traffic. Fixed with
   `postgres.IsUnavailable` mapping connection-level failures to `503`
   with a safe message, logging the real error server-side only.
