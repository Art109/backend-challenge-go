// Command api is the HTTP entry point. Its only job is wiring: every
// concrete type is constructed here (or in the fx.Provide list below) and
// handed to Uber Fx, which resolves the dependency graph and drives
// startup/shutdown through fx.Lifecycle. No business logic lives here.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"backend-challenge-go/internal/app"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/platform/httpserver"
	"backend-challenge-go/internal/platform/keycloak"
	"backend-challenge-go/internal/platform/outbox"
	"backend-challenge-go/internal/platform/postgres"
	"backend-challenge-go/internal/platform/referenceretry"
	"backend-challenge-go/internal/platform/sqs"
)

func main() {
	fx.New(
		fx.Provide(
			config.Load,
			newPostgresPool,
			newKeycloakVerifier,
			newSQSClient,
			newOutboxPublisher,
			postgres.NewWalletRepository,
			postgres.NewLedgerRepository,
			postgres.NewWagerTransactionRepository,
			postgres.NewInboxRepository,
			postgres.NewOutboxRepository,
			newClock,
			app.NewUseCases,
			httpserver.NewRouter,
			httpserver.NewServer,
			newSQSConsumer,
			outbox.NewWorker,
			referenceretry.NewWorker,
		),
		fx.Invoke(
			runHTTPServer,
			runSQSConsumer,
			runOutboxWorker,
			runReferenceRetryWorker,
		),
	).Run()
}

func newKeycloakVerifier(cfg config.Config) (*keycloak.Verifier, error) {
	return keycloak.NewVerifier(context.Background(), cfg.KeycloakIssuerURL)
}

func newClock() app.Clock {
	return func() time.Time { return time.Now().UTC() }
}

func newPostgresPool(lc fx.Lifecycle, cfg config.Config) (*pgxpool.Pool, error) {
	pool, err := postgres.NewPool(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("main: open postgres pool: %w", err)
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return postgres.RunMigrations(cfg.DatabaseURL)
		},
		OnStop: func(ctx context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}

func newSQSClient(cfg config.Config) (*awssqs.Client, error) {
	return sqs.NewClient(context.Background(), sqs.ClientConfig{
		Region:      cfg.AWSRegion,
		EndpointURL: cfg.AWSEndpointURL,
		AccessKeyID: cfg.AWSAccessKeyID,
		SecretKey:   cfg.AWSSecretAccessKey,
	})
}

func newOutboxPublisher(client *awssqs.Client, cfg config.Config) *sqs.Publisher {
	return sqs.NewPublisher(client, cfg.DomainEventsQueueURL)
}

func newSQSConsumer(client *awssqs.Client, cfg config.Config, uc *app.UseCases) *sqs.Consumer {
	return sqs.NewConsumer(client, cfg.WagerTransactionsQueueURL, uc)
}

func runHTTPServer(lc fx.Lifecycle, server *http.Server) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := net.Listen("tcp", server.Addr)
			if err != nil {
				return fmt.Errorf("main: listen on %s: %w", server.Addr, err)
			}
			go func() {
				if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
					slog.Error("http_server_error", "error", err)
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return server.Shutdown(ctx)
		},
	})
}

// backgroundWorker registers a run(ctx) function as a lifecycle-managed
// goroutine: OnStart launches it, OnStop cancels its context and blocks
// until the goroutine has actually returned (via WaitGroup) - not just
// signaled to stop. This is what makes a worker's shutdown *observable*
// rather than fire-and-forget, as the spec requires.
func backgroundWorker(lc fx.Lifecycle, name string, run func(ctx context.Context)) {
	var (
		cancel context.CancelFunc
		wg     sync.WaitGroup
	)
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var workerCtx context.Context
			workerCtx, cancel = context.WithCancel(context.Background())
			wg.Add(1)
			go func() {
				defer wg.Done()
				slog.Info("worker_started", "worker", name)
				run(workerCtx)
				slog.Info("worker_stopped", "worker", name)
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("main: worker %s did not stop within the shutdown deadline", name)
			}
		},
	})
}

func runSQSConsumer(lc fx.Lifecycle, consumer *sqs.Consumer) {
	backgroundWorker(lc, "sqs-consumer", func(ctx context.Context) { _ = consumer.Run(ctx) })
}

func runOutboxWorker(lc fx.Lifecycle, worker *outbox.Worker) {
	backgroundWorker(lc, "outbox-publisher", worker.Run)
}

func runReferenceRetryWorker(lc fx.Lifecycle, worker *referenceretry.Worker) {
	backgroundWorker(lc, "reference-retry", worker.Run)
}
