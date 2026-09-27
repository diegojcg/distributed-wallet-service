package domain

import (
	"encoding/json"
	"time"
)

// Concrete payloads define each event contract. Event stores their encoded snapshot
// privately, so callers cannot change event type, version, metadata or payload.
type TransactionProcessedData struct {
	TransactionID string `json:"transactionId"`
	WalletID      string `json:"walletId"`
	PlayerID      string `json:"playerId"`
	ProviderID    string `json:"providerId,omitempty"`
	ExternalID    string `json:"externalTransactionId,omitempty"`
	Kind          Kind   `json:"kind"`
	Money         Money  `json:"money"`
	Balance       Money  `json:"balance"`
}
type TransactionRejectedData struct {
	TransactionID string `json:"transactionId"`
	WalletID      string `json:"walletId"`
	ProviderID    string `json:"providerId"`
	ExternalID    string `json:"externalTransactionId"`
	Kind          Kind   `json:"kind"`
	FailureCode   string `json:"failureCode"`
}
type TransactionPendingReferenceData struct {
	TransactionID       string    `json:"transactionId"`
	WalletID            string    `json:"walletId"`
	ProviderID          string    `json:"providerId"`
	ExternalID          string    `json:"externalTransactionId"`
	ReferenceExternalID string    `json:"referenceExternalTransactionId"`
	NextAttemptAt       time.Time `json:"nextAttemptAt"`
}
type BalanceChangedData struct {
	WalletID      string    `json:"walletId"`
	TransactionID string    `json:"transactionId"`
	Direction     Direction `json:"direction"`
	Money         Money     `json:"money"`
	Before        Money     `json:"balanceBefore"`
	After         Money     `json:"balanceAfter"`
	WalletVersion int64     `json:"walletVersion"`
}
type Event struct {
	id, typ, aggregateID string
	occurredAt           time.Time
	encoded              []byte
}

func (e Event) ID() string            { return e.id }
func (e Event) Type() string          { return e.typ }
func (e Event) AggregateID() string   { return e.aggregateID }
func (e Event) OccurredAt() time.Time { return e.occurredAt }
func (e Event) JSON() []byte          { return append([]byte(nil), e.encoded...) }
func newEvent[T any](id, typ string, t WagerTransaction, data T) (Event, error) {
	s := t.Snapshot()
	if !validID(id) || !validID(t.ID()) || !validMetadata(s.CorrelationID, s.CausationID) {
		return Event{}, ErrInvalidState
	}
	raw, err := json.Marshal(struct {
		EventID       string    `json:"eventId"`
		EventType     string    `json:"eventType"`
		AggregateID   string    `json:"aggregateId"`
		CorrelationID string    `json:"correlationId"`
		CausationID   string    `json:"causationId,omitempty"`
		OccurredAt    time.Time `json:"occurredAt"`
		Version       int       `json:"version"`
		Data          T         `json:"data"`
	}{id, typ, s.Operation.WalletID, s.CorrelationID, s.CausationID, s.UpdatedAt.UTC(), 1, data})
	if err != nil {
		return Event{}, err
	}
	return Event{id, typ, s.Operation.WalletID, s.UpdatedAt.UTC(), raw}, nil
}
func NewTransactionProcessedEvent(id string, t WagerTransaction) (Event, error) {
	s := t.Snapshot()
	o := s.Operation
	if s.Status != Processed || !s.HasResult {
		return Event{}, ErrInvalidState
	}
	return newEvent(id, "WagerTransactionProcessed", t, TransactionProcessedData{s.ID, o.WalletID, o.PlayerID, o.ProviderID, o.ExternalID, o.Kind, o.Money, s.Result})
}
func NewTransactionRejectedEvent(id string, t WagerTransaction) (Event, error) {
	s := t.Snapshot()
	o := s.Operation
	if s.Status != Rejected {
		return Event{}, ErrInvalidState
	}
	return newEvent(id, "WagerTransactionRejected", t, TransactionRejectedData{s.ID, o.WalletID, o.ProviderID, o.ExternalID, o.Kind, s.FailureCode})
}
func NewTransactionPendingReferenceEvent(id string, t WagerTransaction) (Event, error) {
	s := t.Snapshot()
	o := s.Operation
	if s.Status != PendingReference {
		return Event{}, ErrInvalidState
	}
	return newEvent(id, "WagerTransactionPendingReference", t, TransactionPendingReferenceData{s.ID, o.WalletID, o.ProviderID, o.ExternalID, o.Reference, s.NextAttemptAt})
}
func NewWalletBalanceChangedEvent(id string, t WagerTransaction, l WalletLedgerEntry) (Event, error) {
	s := l.Snapshot()
	if t.Status() != Processed || s.TransactionID != t.ID() || s.WalletID != t.Operation().WalletID || s.Money != t.Operation().Money || s.After != t.Snapshot().Result {
		return Event{}, ErrInvalidState
	}
	return newEvent(id, "WalletBalanceChanged", t, BalanceChangedData{s.WalletID, s.TransactionID, s.Direction, s.Money, s.Before, s.After, s.WalletVersion})
}
