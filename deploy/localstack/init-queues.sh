#!/bin/bash
# Runs automatically once LocalStack is ready (mounted at
# /etc/localstack/init/ready.d/). Provisions:
#   - wager-transactions.fifo       incoming operations, consumed by the app
#   - wager-transactions-dlq.fifo   redrive target after 5 failed receives
#   - domain-events.fifo            outbox worker's publish destination
set -euo pipefail

REGION="${AWS_DEFAULT_REGION:-us-east-1}"

echo "[init-queues] provisioning SQS queues in region $REGION..."

DLQ_URL=$(awslocal sqs create-queue \
  --queue-name wager-transactions-dlq.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false \
  --region "$REGION" --query 'QueueUrl' --output text)

DLQ_ARN=$(awslocal sqs get-queue-attributes \
  --queue-url "$DLQ_URL" --attribute-names QueueArn \
  --region "$REGION" --query 'Attributes.QueueArn' --output text)

echo "[init-queues] DLQ ARN: $DLQ_ARN"

cat > /tmp/wager-transactions-attrs.json <<EOF
{
  "FifoQueue": "true",
  "ContentBasedDeduplication": "false",
  "VisibilityTimeout": "30",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"$DLQ_ARN\",\"maxReceiveCount\":\"5\"}"
}
EOF

awslocal sqs create-queue \
  --queue-name wager-transactions.fifo \
  --attributes file:///tmp/wager-transactions-attrs.json \
  --region "$REGION"

awslocal sqs create-queue \
  --queue-name domain-events.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false \
  --region "$REGION"

echo "[init-queues] done. Queues:"
awslocal sqs list-queues --region "$REGION"
