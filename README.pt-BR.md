# Backend Challenge — Processamento Distribuído de Apostas (Go)

🇺🇸 Also available in [English](README.md).

Solução para o desafio descrito em [CHALLENGE.md](CHALLENGE.md). As decisões
de design, tradeoffs e limitações conhecidas estão documentadas em
[ARCHITECTURE.md](ARCHITECTURE.md) (em inglês).

## Pré-requisitos

- Docker e Docker Compose (todo o resto roda em containers)
- Go 1.27+ (só necessário para rodar os testes localmente, fora do Docker)
- Um compilador C (ex: MinGW-w64 no Windows, ou `build-essential` no Linux)
  é necessário para `go test -race`, já que o detector de corrida depende de cgo.

## Início rápido

```sh
cp .env.example .env
docker compose up --build
```

Isso sobe, em ordem (via `depends_on: condition: service_healthy`):
PostgreSQL, Keycloak (com o realm abaixo importado automaticamente),
LocalStack (com as filas abaixo provisionadas automaticamente), e por fim a
própria API, que roda suas próprias migrations do banco na inicialização.

A API fica disponível em `http://localhost:8080` (pode ser trocado com `API_PORT`).

## Variáveis de ambiente

Veja [.env.example](.env.example) para os valores usados pelo Docker
Compose (credenciais do Postgres, portas expostas). O serviço `api` em si é
configurado inteiramente pelo `docker-compose.yml` quando rodado via
Compose; se você for rodar o binário diretamente (ex: `go run ./cmd/api`
contra os serviços já de pé via `docker compose up postgres keycloak localstack`),
defina:

| Variável | Exemplo (falando com a stack do compose a partir do host) |
| --- | --- |
| `DATABASE_URL` | `postgres://app:app@localhost:5432/backend_challenge?sslmode=disable` |
| `KEYCLOAK_ISSUER_URL` | `http://localhost:8081/realms/backend-challenge` (veja a pegadinha de autenticação abaixo) |
| `HTTP_PORT` | `8080` |
| `AWS_REGION` | `us-east-1` |
| `AWS_ENDPOINT_URL` | `http://localhost:4566` |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | `test` / `test` (o LocalStack ignora esses valores) |
| `WAGER_TRANSACTIONS_QUEUE_URL` | `http://localhost:4566/000000000000/wager-transactions.fifo` |
| `DOMAIN_EVENTS_QUEUE_URL` | `http://localhost:4566/000000000000/domain-events.fifo` |

> **Pegadinha de autenticação ao rodar o binário fora do Compose:** o
> `KC_HOSTNAME` do Keycloak está fixado em `http://keycloak:8080` (veja
> ARCHITECTURE.md §7) para que a validação de token da API containerizada
> funcione. Se você rodar o binário da API diretamente no host em vez de
> via Compose, aponte `KEYCLOAK_ISSUER_URL` para
> `http://keycloak:8080/realms/backend-challenge` também (não
> `localhost:8081`) — o que exige que `keycloak` resolva a partir do seu
> host, por exemplo adicionando `127.0.0.1 keycloak` no seu arquivo de
> hosts. É por isso que o caminho de reprodução pretendido é
> `docker compose up --build`, onde todo serviço alcança o Keycloak pelo
> mesmo nome dentro da rede.

## Identidades provisionadas no Keycloak

O realm `backend-challenge` é importado automaticamente a partir de
[deploy/keycloak/realm-export.json](deploy/keycloak/realm-export.json).
Os três clients usam o grant `client_credentials`:

| `client_id` | `client_secret` | role do realm | pode chamar |
| --- | --- | --- | --- |
| `provider-a` | `provider-a-secret` | `provider` | `/wagering/*` apenas como `provider-a` |
| `provider-b` | `provider-b-secret` | `provider` | `/wagering/*` apenas como `provider-b` |
| `internal-service` | `internal-service-secret` | `internal` | `/wallets*` (abrir, ler, ledger, reconciliação) |

Obtendo um token:

```sh
curl -s http://localhost:8081/realms/backend-challenge/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=provider-a \
  -d client_secret=provider-a-secret \
  | jq -r .access_token
```

## Filas provisionadas no LocalStack

[deploy/localstack/init-queues.sh](deploy/localstack/init-queues.sh) roda
automaticamente assim que o LocalStack fica pronto:

- `wager-transactions.fifo` — operações de entrada, consumidas pela API
  (visibility timeout de 30s, redrive pra DLQ após 5 tentativas falhas)
- `wager-transactions-dlq.fifo` — fila de mensagens mortas (DLQ)
- `domain-events.fifo` — destino de publicação do worker do outbox

## Migrations

Aplicadas automaticamente na inicialização da API. Para rodá-las manualmente
(ex: contra um serviço `postgres` recém-iniciado, ou para reverter):

```sh
go run github.com/golang-migrate/migrate/v4/cmd/migrate@latest \
  -path migrations \
  -database "postgres://app:app@localhost:5432/backend_challenge?sslmode=disable" \
  up      # ou: down 1, para reverter a migration mais recente
```

## Exemplos de chamadas à API

Troque `$TOKEN` pelo token do client correspondente (veja acima).

```sh
# Abrir uma carteira (token do internal-service)
curl -s -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"1000.00","currency":"BRL"}}'

# Fazer uma aposta (token do provider-a; o providerId no corpo precisa ser igual ao client_id do token)
curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:transaction-123" \
  -d '{"providerId":"provider-a","externalTransactionId":"transaction-123","playerId":"...","walletId":"...","roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}'

# Ler uma carteira, seu ledger, e reconciliá-la (token do internal-service)
curl -s http://localhost:8080/wallets/$WALLET_ID -H "Authorization: Bearer $TOKEN"
curl -s "http://localhost:8080/wallets/$WALLET_ID/ledger?limit=50" -H "Authorization: Bearer $TOKEN"
curl -s -X POST http://localhost:8080/wallets/$WALLET_ID/reconciliation -H "Authorization: Bearer $TOKEN"

# Health checks (sem autenticação)
curl -s http://localhost:8080/health/live
curl -s http://localhost:8080/health/ready
```

Enviando uma mensagem diretamente pra fila de entrada (simulando uma
integração de provedor via SQS em vez de HTTP) — mesmo caso de uso, mesmas
garantias de idempotência:

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

(`awslocal` é o wrapper da AWS CLI pré-configurado pelo LocalStack; rode
dentro do container com `docker compose exec localstack awslocal ...`, ou
use a CLI `aws` normal com `--endpoint-url http://localhost:4566` e
credenciais fictícias.)

## Rodando os testes

```sh
go vet ./...
go test ./...                              # testes unitários: pacotes de domínio, sem infraestrutura
go test -race ./...                        # os mesmos, com o detector de corrida
```

Os testes de integração (`internal/platform/postgres`, `internal/app`)
rodam contra uma instância **real** de PostgreSQL — nunca mockada, conforme
exigido pela especificação — e ficam atrás da build tag `integration`, então
não rodam por padrão:

```sh
docker compose up -d postgres
go test -tags=integration -race ./...
```

Esses testes cobrem, entre outras coisas:

- o cenário obrigatório de concorrência (carteira de 100,00 BRL, duas
  apostas concorrentes de 80,00 BRL → exatamente uma processada, uma
  rejeitada, saldo final de 20,00, um único lançamento no ledger)
- o cenário obrigatório de duplicidade (a mesma aposta enviada 50 vezes em
  paralelo → exatamente um débito, 49 replays idempotentes)
- dois publishers do outbox disputando os mesmos eventos → nenhuma
  publicação duplicada
- deduplicação de reentrega via SQS/inbox
- resolução de referência de REFUND/ROLLBACK, rejeição de reversão
  duplicada, e o fluxo de chegada antecipada → PENDING_REFERENCE →
  resolução posterior

Autenticação, comportamento real de consumo/DLQ via SQS, e recuperação após
reinício com múltiplas instâncias foram verificados manualmente contra a
stack completa do `docker compose up` (veja ARCHITECTURE.md §9 para o que
ainda não está capturado como casos automatizados de `go test`).

## Formatação

```sh
gofmt -l .   # não deve imprimir nada
```
