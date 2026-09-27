package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"strings"
)

type Kind string

const (
	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
	Opening  Kind = "OPENING"
)

type Status string

const (
	Pending          Status = "PENDING"
	PendingReference Status = "PENDING_REFERENCE"
	Processed        Status = "PROCESSED"
	Rejected         Status = "REJECTED"
	Failed           Status = "FAILED"
)

type Operation struct {
	ProviderID string `json:"providerId"`
	ExternalID string `json:"externalTransactionId"`
	PlayerID   string `json:"playerId"`
	WalletID   string `json:"walletId"`
	RoundID    string `json:"roundId"`
	GameID     string `json:"gameId"`
	Kind       Kind   `json:"kind"`
	Money      Money  `json:"money"`
	Reference  string `json:"referenceExternalTransactionId,omitempty"`
}

func (o Operation) Validate() error {
	for _, s := range []string{o.ProviderID, o.ExternalID, o.RoundID, o.GameID} {
		if s == "" || len(s) > 200 || strings.TrimSpace(s) != s {
			return ErrInvalidOperation
		}
	}
	if (o.Reference != "" && o.Reference == o.ExternalID) || len(o.Reference) > 200 || strings.TrimSpace(o.Reference) != o.Reference {
		return ErrInvalidOperation
	}
	if !validID(o.PlayerID) {
		return ErrInvalidOperation
	}
	if !validID(o.WalletID) {
		return ErrInvalidOperation
	}
	if !o.Money.Valid() || o.Money.Currency() != "BRL" || o.Money.Minor() < 0 {
		return ErrInvalidMoney
	}
	switch o.Kind {
	case Loss:
		if o.Money.Minor() != 0 || o.Reference != "" {
			return ErrInvalidOperation
		}
	case Bet:
		if o.Money.Minor() == 0 || o.Reference != "" {
			return ErrInvalidOperation
		}
	case Win:
		if o.Money.Minor() == 0 {
			return ErrInvalidOperation
		}
	case Refund, Rollback:
		if o.Money.Minor() == 0 || o.Reference == "" || o.Reference == o.ExternalID {
			return ErrInvalidOperation
		}
	default:
		return ErrInvalidOperation
	}
	return nil
}

// Hash uses sorted JSON map keys and string values only, including canonical Money.
// Transport timestamps/message IDs and the idempotency key never participate.
func (o Operation) Hash() (string, error) {
	if err := o.Validate(); err != nil {
		return "", err
	}
	player, _ := uuid.Parse(o.PlayerID)
	wallet, _ := uuid.Parse(o.WalletID)
	fields := map[string]any{"providerId": o.ProviderID, "externalTransactionId": o.ExternalID, "playerId": player.String(), "walletId": wallet.String(), "roundId": o.RoundID, "gameId": o.GameID, "kind": string(o.Kind), "money": map[string]string{"amount": o.Money.Amount(), "currency": o.Money.Currency()}, "referenceExternalTransactionId": o.Reference}
	raw, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
