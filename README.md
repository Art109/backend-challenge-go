# Backend Challenge — Distributed Betting Operations (Go)

🇧🇷 Também disponível em [português](README.pt-BR.md).

Solution to the challenge described in [CHALLENGE.md](CHALLENGE.md). Design
decisions, tradeoffs and known limitations are documented in
[ARCHITECTURE.md](ARCHITECTURE.md).

## Prerequisites

- Docker & Docker Compose (everything else runs in containers)
- Go 1.27+ (only needed to run tests locally, outside Docker)
- A C compiler (e.g. MinGW-w64 on Windows, or `build-essential` on Linux)
  is required for `go test -race`, since the race detector depends on cgo.

## Quick start

```sh
cp .env.example .env
docker compose up --build
```

This starts, in order (via `depends_on: condition: service_healthy`):
PostgreSQL, Keycloak (with the realm below auto-imported), LocalStack
(with the queues below auto-provisioned), then the API itself, which runs
its own database migrations on startup.

The API listens on `http://localhost:8080` (override with `API_PORT`).
Prometheus is at `http://localhost:9090`, and a ready-made Grafana
dashboard is at `http://localhost:3000` (open access for viewing; sign in
with `admin`/`admin` to edit) — see "Dashboard" below.

## Environment variables

See [.env.example](.env.example) for the Docker Compose-level values
(Postgres credentials, exposed ports). The `api` service itself is
configured entirely through docker-compose.yml when run via Compose; if
running the binary directly (e.g. `go run ./cmd/api` against services
already up via `docker compose up postgres keycloak localstack`), set:

| Variable | Example (talking to the compose stack from the host) |
| --- | --- |
| `DATABASE_URL` | `postgres://app:app@localhost:5432/backend_challenge?sslmode=disable` |
| `KEYCLOAK_ISSUER_URL` | `http://localhost:8081/realms/backend-challenge` (see the auth gotcha below) |
| `HTTP_PORT` | `8080` |
| `AWS_REGION` | `us-east-1` |
| `AWS_ENDPOINT_URL` | `http://localhost:4566` |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | `test` / `test` (LocalStack ignores these) |
| `WAGER_TRANSACTIONS_QUEUE_URL` | `http://localhost:4566/000000000000/wager-transactions.fifo` |
| `DOMAIN_EVENTS_QUEUE_URL` | `http://localhost:4566/000000000000/domain-events.fifo` |

> **Auth gotcha when running the binary outside Compose:** Keycloak's
> `KC_HOSTNAME` is pinned to `http://keycloak:8080` (see
> ARCHITECTURE.md §7) so the containerized API's token validation works.
> If you run the API binary directly on the host instead of via Compose,
> point `KEYCLOAK_ISSUER_URL` at `http://keycloak:8080/realms/backend-challenge`
> too (not `localhost:8081`) — which requires `keycloak` to resolve from
> your host, e.g. by adding `127.0.0.1 keycloak` to your hosts file. This
> is why the intended reproduction path is `docker compose up --build`,
> where every service reaches Keycloak through the same in-network name.

## Identities provisioned in Keycloak

The realm `backend-challenge` is imported automatically from
[deploy/keycloak/realm-export.json](deploy/keycloak/realm-export.json).
All three clients use the `client_credentials` grant:

| `client_id` | `client_secret` | realm role | may call |
| --- | --- | --- | --- |
| `provider-a` | `provider-a-secret` | `provider` | `/wagering/*` as `provider-a` only |
| `provider-b` | `provider-b-secret` | `provider` | `/wagering/*` as `provider-b` only |
| `internal-service` | `internal-service-secret` | `internal` | `/wallets*` (open, read, ledger, reconciliation) |

Get a token:

```sh
curl -s http://localhost:8081/realms/backend-challenge/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=provider-a \
  -d client_secret=provider-a-secret \
  | jq -r .access_token
```

## Queues provisioned in LocalStack

[deploy/localstack/init-queues.sh](deploy/localstack/init-queues.sh) runs
automatically once LocalStack is ready:

- `wager-transactions.fifo` — incoming operations, consumed by the API
  (visibility timeout 30s, redrive to the DLQ after 5 failed receives)
- `wager-transactions-dlq.fifo` — dead-letter queue
- `domain-events.fifo` — the outbox worker's publish destination

## Migrations

Applied automatically on API startup. To run them manually (e.g. against a
freshly-started `postgres` service, or to roll back):

```sh
go run github.com/golang-migrate/migrate/v4/cmd/migrate@latest \
  -path migrations \
  -database "postgres://app:app@localhost:5432/backend_challenge?sslmode=disable" \
  up      # or: down 1, to roll back the most recent migration
```

## Example API calls

Replace `$TOKEN` with a token for the relevant client (see above).

```sh
# Open a wallet (internal-service token)
curl -s -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"1000.00","currency":"BRL"}}'

# Place a bet (provider-a token; providerId in the body must equal the token's client_id)
curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:transaction-123" \
  -d '{"providerId":"provider-a","externalTransactionId":"transaction-123","playerId":"...","walletId":"...","roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}'

# Read a wallet, its ledger, and reconcile it (internal-service token)
curl -s http://localhost:8080/wallets/$WALLET_ID -H "Authorization: Bearer $TOKEN"
curl -s "http://localhost:8080/wallets/$WALLET_ID/ledger?limit=50" -H "Authorization: Bearer $TOKEN"
curl -s -X POST http://localhost:8080/wallets/$WALLET_ID/reconciliation -H "Authorization: Bearer $TOKEN"

# Health checks (no auth)
curl -s http://localhost:8080/health/live
curl -s http://localhost:8080/health/ready

# Prometheus metrics (no auth)
curl -s http://localhost:8080/metrics
```

Sending a message directly to the incoming queue (simulating a provider
integration over SQS instead of HTTP) — same use case, same idempotency
guarantees:

```sh
awslocal sqs send-message \
  --endpoint-url http://localhost:4566 \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id "provider-a:transaction-456" \
  --message-deduplication-id "msg-456" \
  --message-body '{
    "messageId": "msg-456",
    "type": "WagerTransactionRequested",
    "occurredAt": "2026-09-18T12:00:00.000Z",
    "data": {
      "providerId": "provider-a",
      "externalTransactionId": "transaction-456",
      "idempotencyKey": "provider-a:transaction-456",
      "playerId": "...",
      "walletId": "...",
      "roundId": "round-987",
      "gameId": "fortune-chimp",
      "kind": "BET",
      "money": {"amount": "25.00", "currency": "BRL"}
    }
  }'
```

(`awslocal` is the LocalStack-preconfigured AWS CLI wrapper; run it inside
the container with `docker compose exec localstack awslocal ...`, or use
the plain `aws` CLI with `--endpoint-url http://localhost:4566` and dummy
credentials.)

## Dashboard (optional differentiator)

`docker compose up --build` also starts Prometheus (scraping `api:8080/metrics`
every 5s) and Grafana, with a dashboard already provisioned — no manual
setup needed. Open `http://localhost:3000` and go to
**Dashboards → Backend Challenge — Betting Operations**, or directly:
`http://localhost:3000/d/backend-challenge-overview`. It covers all 8
metrics from the observability section: transactions by status/kind,
idempotent replays, version conflicts, reference retries, reconciliation
divergences, SQS outcomes, outbox publish outcomes, and both outbox delay
and HTTP latency as p50/p95. Neither service is required for the rest of
the stack to work — removing them from docker-compose.yml doesn't affect
`api`/`postgres`/`keycloak`/`localstack` at all.

## Running the tests

```sh
go vet ./...
go test ./...                              # unit tests: domain packages, no infrastructure needed
go test -race ./...                        # same, with the race detector
```

Integration tests (`internal/platform/postgres`, `internal/app`) run
against a **real** PostgreSQL instance — never mocked, per the spec's
requirement — and are behind the `integration` build tag so they don't run
by default:

```sh
docker compose up -d postgres
go test -tags=integration -race ./...
```

These cover, among others:

- the mandatory concurrency scenario (100.00 BRL wallet, two concurrent
  80.00 BRL bets → exactly one processed, one rejected, final balance
  20.00, single ledger entry)
- the mandatory duplication scenario (the same bet submitted 50 times in
  parallel → exactly one debit, 49 idempotent replays)
- two outbox publishers racing for the same events → no double-claim
- SQS/inbox redelivery dedup
- REFUND/ROLLBACK reference resolution, duplicate-reversal rejection,
  and the early-arrival → PENDING_REFERENCE → later resolution flow

Auth, live SQS consumption/DLQ behavior, and multi-instance restart
recovery were verified manually against the full `docker compose up`
stack (see ARCHITECTURE.md §11 for what isn't yet captured as automated
`go test` cases).

## Formatting

```sh
gofmt -l .   # should print nothing
```
