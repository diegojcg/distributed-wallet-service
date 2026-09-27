package domain

import (
	"errors"
	"github.com/google/uuid"
	"strings"
	"time"
)

type Direction string

const (
	Credit Direction = "CREDIT"
	Debit  Direction = "DEBIT"
)

// TransactionSnapshot is a detached value for persistence. Restore validates it;
// modifying a snapshot never mutates its originating aggregate.
type TransactionSnapshot struct {
	ID                               string
	Operation                        Operation
	Key, Hash                        string
	Status                           Status
	ResolvedReferenceID, FailureCode string
	Result                           Money
	HasResult                        bool
	Attempts                         int
	NextAttemptAt                    time.Time
	CreatedAt, UpdatedAt             time.Time
	CorrelationID, CausationID       string
}
type WagerTransaction struct{ state TransactionSnapshot }

func validID(id string) bool { parsed, err := uuid.Parse(id); return err == nil && parsed != uuid.Nil }
func validMetadata(correlation, causation string) bool {
	return correlation != "" && len(correlation) <= 128 && len(causation) <= 200 && strings.TrimSpace(correlation) == correlation && strings.TrimSpace(causation) == causation
}
func NewWagerTransaction(id string, o Operation, key, correlation, causation string, now time.Time) (WagerTransaction, error) {
	if !validID(id) || key == "" || len(key) > 200 || strings.TrimSpace(key) != key || now.IsZero() || !validMetadata(correlation, causation) {
		return WagerTransaction{}, ErrInvalidOperation
	}
	hash, err := o.Hash()
	if err != nil {
		return WagerTransaction{}, err
	}
	player, _ := uuid.Parse(o.PlayerID)
	wallet, _ := uuid.Parse(o.WalletID)
	o.PlayerID = player.String()
	o.WalletID = wallet.String()
	return WagerTransaction{TransactionSnapshot{ID: id, Operation: o, Key: key, Hash: hash, Status: Pending, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), CorrelationID: correlation, CausationID: causation}}, nil
}
func NewOpeningTransaction(id string, w Wallet, correlation, causation string, now time.Time) (WagerTransaction, error) {
	if !validID(id) || !validID(w.ID()) || !w.Balance().Valid() || w.Balance().Minor() <= 0 || w.Version() != 1 || now.IsZero() || !validMetadata(correlation, causation) {
		return WagerTransaction{}, ErrInvalidState
	}
	return WagerTransaction{TransactionSnapshot{ID: id, Operation: Operation{PlayerID: w.PlayerID(), WalletID: w.ID(), Kind: Opening, Money: w.Balance()}, Status: Processed, Result: w.Balance(), HasResult: true, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), CorrelationID: correlation, CausationID: causation}}, nil
}
func RestoreWagerTransaction(s TransactionSnapshot) (WagerTransaction, error) {
	s.CreatedAt = s.CreatedAt.UTC()
	s.UpdatedAt = s.UpdatedAt.UTC()
	if !s.NextAttemptAt.IsZero() {
		s.NextAttemptAt = s.NextAttemptAt.UTC()
	}
	if s.Operation.Kind == Opening {
		w, err := RestoreWallet(s.Operation.WalletID, s.Operation.PlayerID, s.Operation.Money, 1, s.CreatedAt, s.CreatedAt)
		if err != nil {
			return WagerTransaction{}, err
		}
		expected, err := NewOpeningTransaction(s.ID, w, s.CorrelationID, s.CausationID, s.CreatedAt)
		if err != nil {
			return WagerTransaction{}, err
		}
		if s != expected.state {
			return WagerTransaction{}, ErrInvalidState
		}
		return expected, nil
	}
	expected, err := NewWagerTransaction(s.ID, s.Operation, s.Key, s.CorrelationID, s.CausationID, s.CreatedAt)
	if err != nil {
		return WagerTransaction{}, err
	}
	if s.Hash != expected.state.Hash || s.UpdatedAt.Before(s.CreatedAt) || s.Attempts < 0 {
		return WagerTransaction{}, ErrInvalidState
	}
	if s.ResolvedReferenceID != "" && !validID(s.ResolvedReferenceID) {
		return WagerTransaction{}, ErrInvalidState
	}
	if s.HasResult && (!s.Result.Valid() || s.Result.Minor() < 0 || s.Result.Currency() != s.Operation.Money.Currency()) {
		return WagerTransaction{}, ErrInvalidState
	}
	switch s.Status {
	case Pending:
		if s.FailureCode != "" || s.HasResult || s.Attempts != 0 || !s.NextAttemptAt.IsZero() || s.ResolvedReferenceID != "" {
			return WagerTransaction{}, ErrInvalidState
		}
	case PendingReference:
		if s.Operation.Reference == "" || s.NextAttemptAt.IsZero() || s.FailureCode != "" || !s.HasResult {
			return WagerTransaction{}, ErrInvalidState
		}
	case Processed:
		if !s.HasResult || s.FailureCode != "" || !s.NextAttemptAt.IsZero() {
			return WagerTransaction{}, ErrInvalidState
		}
		if s.Operation.Reference != "" && s.ResolvedReferenceID == "" {
			return WagerTransaction{}, ErrInvalidState
		}
	case Rejected, Failed:
		if s.FailureCode == "" || !s.HasResult || !s.NextAttemptAt.IsZero() {
			return WagerTransaction{}, ErrInvalidState
		}
	default:
		return WagerTransaction{}, ErrInvalidState
	}
	return WagerTransaction{s}, nil
}
func (t WagerTransaction) Snapshot() TransactionSnapshot { return t.state }
func (t WagerTransaction) Status() Status                { return t.state.Status }
func (t WagerTransaction) Operation() Operation          { return t.state.Operation }
func (t WagerTransaction) ID() string                    { return t.state.ID }
func (t WagerTransaction) mutable(now time.Time) bool {
	return validID(t.state.ID) && (t.state.Status == Pending || t.state.Status == PendingReference) && !now.Before(t.state.UpdatedAt)
}
func (t *WagerTransaction) finish(status Status, code string, w Wallet, now time.Time) {
	t.state.Status = status
	t.state.FailureCode = code
	t.state.Result = w.Balance()
	t.state.HasResult = true
	t.state.UpdatedAt = now.UTC()
	t.state.NextAttemptAt = time.Time{}
}
func (t *WagerTransaction) MarkFailed(w Wallet, code string, now time.Time) error {
	if !t.mutable(now) || code != "INFRASTRUCTURE_PERMANENT" || w.ID() != t.state.Operation.WalletID || w.PlayerID() != t.state.Operation.PlayerID || w.Balance().Currency() != t.state.Operation.Money.Currency() || !w.Balance().Valid() {
		return ErrInvalidState
	}
	t.finish(Failed, code, w, now)
	return nil
}

// Evaluate owns the business decision and all state transitions. It performs no I/O.
func (t *WagerTransaction) Evaluate(w Wallet, reference *WagerTransaction, alreadyReversed bool, now time.Time) (Wallet, Direction, error) {
	if !t.mutable(now) || w.ID() != t.state.Operation.WalletID || w.PlayerID() != t.state.Operation.PlayerID || now.Before(w.UpdatedAt()) {
		return w, "", ErrInvalidState
	}
	o := t.state.Operation
	if w.Balance().Currency() != o.Money.Currency() {
		return w, "", ErrCurrency
	}
	if t.state.Status == PendingReference {
		if now.Before(t.state.NextAttemptAt) {
			return w, "", ErrInvalidState
		}
		t.state.Attempts++
	}
	reject := func(code string) (Wallet, Direction, error) { t.finish(Rejected, code, w, now); return w, "", nil }
	if t.state.Attempts >= 10 || now.Sub(t.state.CreatedAt) >= 5*time.Minute {
		return reject("REFERENCE_NOT_FOUND")
	}
	direction := Credit
	if o.Kind == Bet {
		direction = Debit
	}
	if o.Reference != "" {
		if reference != nil {
			r := reference.state
			if !validID(r.ID) {
				return w, "", ErrInvalidState
			}
			t.state.ResolvedReferenceID = r.ID
			if r.Operation.ProviderID != o.ProviderID || r.Operation.ExternalID != o.Reference || r.Operation.WalletID != o.WalletID || r.Operation.PlayerID != o.PlayerID || r.Operation.Money.Currency() != o.Money.Currency() || r.Operation.RoundID != o.RoundID {
				return reject("REFERENCE_MISMATCH")
			}
			if r.Status == Rejected || r.Status == Failed {
				return reject("REFERENCE_UNSUCCESSFUL")
			}
		}
		if reference == nil || reference.Status() == Pending || reference.Status() == PendingReference {
			t.finish(PendingReference, "", w, now)
			t.state.NextAttemptAt = now.Add(time.Duration(1<<min(t.state.Attempts, 5)) * time.Second)
			return w, "", nil
		}
		r := reference.Operation()
		if (o.Kind == Win || o.Kind == Refund) && r.Kind != Bet || o.Kind == Rollback && r.Kind != Bet && r.Kind != Win && r.Kind != Refund {
			return reject("REFERENCE_KIND_INVALID")
		}
		if o.Kind == Refund || o.Kind == Rollback {
			if r.Money != o.Money {
				return reject("REFERENCE_AMOUNT_MISMATCH")
			}
			if alreadyReversed {
				return reject("ALREADY_REVERSED")
			}
			if o.Kind == Rollback && r.Kind != Bet {
				direction = Debit
			}
		}
	}
	after := w
	var err error
	if o.Kind != Loss {
		if direction == Debit {
			err = after.Debit(o.Money, now)
		} else {
			err = after.Credit(o.Money, now)
		}
	}
	if errors.Is(err, ErrInsufficientFunds) {
		if o.Kind == Rollback {
			return reject("REVERSAL_INSUFFICIENT_FUNDS")
		}
		return reject("INSUFFICIENT_FUNDS")
	}
	if errors.Is(err, ErrOverflow) {
		return reject("BALANCE_OVERFLOW")
	}
	if err != nil {
		return w, "", err
	}
	t.finish(Processed, "", after, now)
	if o.Kind == Loss {
		direction = ""
	}
	return after, direction, nil
}
