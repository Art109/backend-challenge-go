package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"backend-challenge-go/internal/app"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagertransaction"
	"backend-challenge-go/internal/platform/metrics"
)

// ConsumerName is this consumer's durable identity for inbox dedup - it
// must never change, since a redelivery is only recognized as such by
// matching (ConsumerName, messageId) against what was recorded before.
const ConsumerName = "wager-transactions-consumer"

// incomingEnvelope mirrors the message body shape from section 10 of the
// spec: a transport envelope (messageId/type/occurredAt) wrapping the same
// business fields the HTTP endpoint accepts.
type incomingEnvelope struct {
	MessageID  string       `json:"messageId"`
	Type       string       `json:"type"`
	OccurredAt time.Time    `json:"occurredAt"`
	Data       incomingData `json:"data"`
}

type incomingData struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	IdempotencyKey                 string      `json:"idempotencyKey"`
	PlayerID                       string      `json:"playerId"`
	WalletID                       string      `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
}

// Consumer polls wager-transactions.fifo and feeds each message through the
// exact same use case the HTTP handler calls, so HTTP and SQS entry points
// share one set of idempotency and financial guarantees.
type Consumer struct {
	client       *sqs.Client
	queueURL     string
	uc           *app.UseCases
	maxInFlight  int
	pollInterval time.Duration
}

func NewConsumer(client *sqs.Client, queueURL string, uc *app.UseCases) *Consumer {
	return &Consumer{
		client:       client,
		queueURL:     queueURL,
		uc:           uc,
		maxInFlight:  10,
		pollInterval: 0,
	}
}

// Run long-polls for messages until ctx is cancelled. On cancellation, it
// stops requesting new work immediately and waits for whatever batch is
// already being processed to finish - it never abandons an in-flight
// message mid-handling, and never fetches a new one after being asked to
// stop. Messages this instance can't finish before ctx's own deadline
// simply keep their SQS visibility timeout running out naturally, making
// them available for redelivery to another instance.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		out, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:              aws.String(c.queueURL),
			MaxNumberOfMessages:   int32(c.maxInFlight),
			WaitTimeSeconds:       10,
			MessageAttributeNames: []string{"All"},
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil // cancelled during a long poll - not a real error
			}
			slog.Error("sqs_receive_failed", "error", err)
			continue
		}

		var wg sync.WaitGroup
		for _, msg := range out.Messages {
			wg.Add(1)
			go func(msg types.Message) {
				defer wg.Done()
				c.handle(ctx, msg)
			}(msg)
		}
		wg.Wait()
	}
}

func (c *Consumer) handle(ctx context.Context, msg types.Message) {
	var env incomingEnvelope
	if err := json.Unmarshal([]byte(aws.ToString(msg.Body)), &env); err != nil {
		// A malformed message can never succeed on retry - dead-lettering
		// it (by not deleting it here; the queue's redrive policy moves it
		// after maxReceiveCount) is correct, but we also don't want it
		// silently stuck: log it clearly.
		metrics.SQSMessagesTotal.WithLabelValues("business_rejected").Inc()
		slog.Error("sqs_message_invalid_json", "error", err, "messageId", aws.ToString(msg.MessageId))
		return
	}

	playerID, err1 := uuid.Parse(env.Data.PlayerID)
	walletID, err2 := uuid.Parse(env.Data.WalletID)
	if err1 != nil || err2 != nil {
		metrics.SQSMessagesTotal.WithLabelValues("business_rejected").Inc()
		slog.Error("sqs_message_invalid_ids", "messageId", env.MessageID)
		return
	}

	result, err := c.uc.SubmitWagerTransaction(ctx, app.SubmitWagerTransactionCommand{
		ProviderID:                     env.Data.ProviderID,
		ExternalTransactionID:          env.Data.ExternalTransactionID,
		IdempotencyKey:                 env.Data.IdempotencyKey,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        env.Data.RoundID,
		GameID:                         env.Data.GameID,
		Kind:                           wagertransaction.Kind(env.Data.Kind),
		Money:                          env.Data.Money,
		ReferenceExternalTransactionID: env.Data.ReferenceExternalTransactionID,
		InboxConsumerName:              ConsumerName,
		InboxMessageID:                 env.MessageID,
	})
	if err != nil {
		if isBusinessValidationError(err) {
			// A malformed request body: like the JSON-parse case above,
			// retrying changes nothing. Let it dead-letter via
			// maxReceiveCount rather than deleting it ourselves.
			metrics.SQSMessagesTotal.WithLabelValues("business_rejected").Inc()
			slog.Error("sqs_message_rejected", "error", err, "messageId", env.MessageID)
			return
		}
		// Transient failure (DB unreachable, etc.): don't delete - leave
		// it for redelivery once visibility expires.
		metrics.SQSMessagesTotal.WithLabelValues("transient_failure").Inc()
		slog.Error("sqs_message_transient_failure", "error", err, "messageId", env.MessageID)
		return
	}
	metrics.SQSMessagesTotal.WithLabelValues("processed").Inc()

	slog.Info("sqs_message_processed",
		"messageId", env.MessageID,
		"transactionId", result.TransactionID,
		"status", result.Status,
		"idempotentReplay", result.IdempotentReplay,
	)

	// Only remove the message from the queue after its durable handling -
	// PROCESSED, REJECTED and PENDING_REFERENCE are all legitimate,
	// durably-recorded outcomes at this point.
	if _, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		slog.Error("sqs_delete_failed", "error", err, "messageId", env.MessageID)
	}
}

// isBusinessValidationError reports whether err reflects something about
// the message itself that will never succeed no matter how many times it's
// redelivered, as opposed to a transient infrastructure problem.
func isBusinessValidationError(err error) bool {
	return errors.Is(err, app.ErrIdempotencyKeyConflict) ||
		errors.Is(err, app.ErrExternalTransactionKeyMismatch) ||
		errors.Is(err, app.ErrWalletNotFound) ||
		errors.Is(err, app.ErrWalletPlayerMismatch) ||
		errors.Is(err, wagertransaction.ErrInvalidKind) ||
		errors.Is(err, wagertransaction.ErrNonPositiveAmount) ||
		errors.Is(err, wagertransaction.ErrNonZeroLossAmount) ||
		errors.Is(err, wagertransaction.ErrMissingReference) ||
		errors.Is(err, wagertransaction.ErrUnexpectedReference)
}
