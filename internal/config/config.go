// Package config loads process configuration from environment variables.
// It is intentionally the only package (besides cmd/api) allowed to read
// os.Getenv - every other package receives already-parsed values.
package config

import (
	"fmt"
	"os"
)

type Config struct {
	HTTPPort          string
	DatabaseURL       string
	KeycloakIssuerURL string

	AWSRegion                 string
	AWSEndpointURL            string
	AWSAccessKeyID            string
	AWSSecretAccessKey        string
	WagerTransactionsQueueURL string
	DomainEventsQueueURL      string
}

func Load() (Config, error) {
	databaseURL, err := requireEnv("DATABASE_URL")
	if err != nil {
		return Config{}, err
	}
	keycloakIssuerURL, err := requireEnv("KEYCLOAK_ISSUER_URL")
	if err != nil {
		return Config{}, err
	}
	wagerQueueURL, err := requireEnv("WAGER_TRANSACTIONS_QUEUE_URL")
	if err != nil {
		return Config{}, err
	}
	domainEventsQueueURL, err := requireEnv("DOMAIN_EVENTS_QUEUE_URL")
	if err != nil {
		return Config{}, err
	}
	return Config{
		HTTPPort:          getEnv("HTTP_PORT", "8080"),
		DatabaseURL:       databaseURL,
		KeycloakIssuerURL: keycloakIssuerURL,

		AWSRegion:                 getEnv("AWS_REGION", "us-east-1"),
		AWSEndpointURL:            getEnv("AWS_ENDPOINT_URL", ""),
		AWSAccessKeyID:            getEnv("AWS_ACCESS_KEY_ID", "test"),
		AWSSecretAccessKey:        getEnv("AWS_SECRET_ACCESS_KEY", "test"),
		WagerTransactionsQueueURL: wagerQueueURL,
		DomainEventsQueueURL:      domainEventsQueueURL,
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("config: required environment variable %s is not set", key)
	}
	return v, nil
}
