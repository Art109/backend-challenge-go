// Package sqs wraps the AWS SQS client used both to consume incoming
// wager-transaction requests and to publish outbox events, pointed at
// LocalStack locally (and at real AWS SQS in principle, by changing only
// configuration - nothing here is LocalStack-specific).
package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// ClientConfig is intentionally small: the endpoint override is what makes
// this point at LocalStack instead of real AWS, and the credentials are
// dummy values LocalStack accepts (it doesn't validate them) but the SDK
// still requires something to be present.
type ClientConfig struct {
	Region      string
	EndpointURL string
	AccessKeyID string
	SecretKey   string
}

func NewClient(ctx context.Context, cfg ClientConfig) (*sqs.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("sqs: load AWS config: %w", err)
	}
	return sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		if cfg.EndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.EndpointURL)
		}
	}), nil
}
