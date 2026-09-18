package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Publisher sends messages to one FIFO queue - the outbox worker's
// destination for domain events.
type Publisher struct {
	client   *sqs.Client
	queueURL string
}

func NewPublisher(client *sqs.Client, queueURL string) *Publisher {
	return &Publisher{client: client, queueURL: queueURL}
}

// Publish sends body to the queue. groupID controls FIFO ordering (events
// for the same aggregate are ordered relative to each other, different
// aggregates are independent); dedupID is SQS's own content-based dedup
// key for this send - using the domain eventId here means a retried
// publish of the same event never enqueues a second copy even if the first
// send actually reached SQS but the caller didn't get to record that fact
// (e.g. crashed between send and commit).
func (p *Publisher) Publish(ctx context.Context, groupID, dedupID, body string) error {
	_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.queueURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(dedupID),
	})
	if err != nil {
		return fmt.Errorf("sqs: publish: %w", err)
	}
	return nil
}
