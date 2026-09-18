package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"backend-challenge-go/internal/domain/money"
)

// canonicalPayloadHash hashes exactly the business fields of an operation -
// never the Idempotency-Key itself, and never transport metadata (HTTP
// headers, the SQS envelope's messageId/occurredAt). Two submissions with
// the same key are only the same operation if this hash also matches;
// otherwise the key was reused for different content, which the spec
// requires rejecting rather than silently accepting.
//
// Determinism comes from marshaling a map[string]any: encoding/json sorts
// map keys alphabetically, which is what "canonical JSON with key
// ordering" means here - no separate canonicalization library needed.
func canonicalPayloadHash(fields SubmitWagerTransactionCommand) (string, error) {
	m := map[string]any{
		"providerId":            fields.ProviderID,
		"externalTransactionId": fields.ExternalTransactionID,
		"playerId":              fields.PlayerID.String(),
		"walletId":              fields.WalletID.String(),
		"roundId":               fields.RoundID,
		"gameId":                fields.GameID,
		"kind":                  string(fields.Kind),
		"money":                 moneyFields(fields.Money),
	}
	if fields.ReferenceExternalTransactionID != "" {
		m["referenceExternalTransactionId"] = fields.ReferenceExternalTransactionID
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("app: marshal canonical payload: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func moneyFields(m money.Money) map[string]any {
	return map[string]any{"amount": m.String(), "currency": m.Currency()}
}
