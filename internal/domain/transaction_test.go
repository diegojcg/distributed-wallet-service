package domain

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"math"
	"testing"
	"time"
)

func fixture(t *testing.T, amount string) (Wallet, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	m, e := ParseMoney(amount, "BRL")
	if e != nil {
		t.Fatal(e)
	}
	w, e := NewWallet(uuid.NewString(), uuid.NewString(), m, now)
	if e != nil {
		t.Fatal(e)
	}
	return w, now
}
func transaction(t *testing.T, w Wallet, k Kind, amount, ref string, now time.Time) WagerTransaction {
	t.Helper()
	m, e := ParseMoney(amount, "BRL")
	if e != nil {
		t.Fatal(e)
	}
	tx, e := NewWagerTransaction(uuid.NewString(), Operation{ProviderID: "p", ExternalID: uuid.NewString(), PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "r", GameID: "g", Kind: k, Money: m, Reference: ref}, "key", "correlation-1", "cause-1", now)
	if e != nil {
		t.Fatal(e)
	}
	return tx
}
func TestTransactionDecisions(t *testing.T) {
	for _, tc := range []struct {
		kind          Kind
		amount        string
		status        Status
		balance, code string
		direction     Direction
	}{{Bet, "25", Processed, "75.00", "", Debit}, {Bet, "101", Rejected, "100.00", "INSUFFICIENT_FUNDS", ""}, {Win, "25", Processed, "125.00", "", Credit}, {Loss, "0", Processed, "100.00", "", ""}} {
		t.Run(string(tc.kind)+tc.amount, func(t *testing.T) {
			w, now := fixture(t, "100")
			tx := transaction(t, w, tc.kind, tc.amount, "", now)
			after, d, e := tx.Evaluate(w, nil, false, now)
			if e != nil || tx.Status() != tc.status || after.Balance().Amount() != tc.balance || d != tc.direction || tx.Snapshot().FailureCode != tc.code {
				t.Fatal(tx, after, d, e)
			}
			snapshot := tx.Snapshot()
			restored, e := RestoreWagerTransaction(snapshot)
			if e != nil || restored.Snapshot() != snapshot {
				t.Fatal(e)
			}
			snapshot.Operation.ProviderID = "evil"
			if tx.Operation().ProviderID != "p" {
				t.Fatal("snapshot aliases state")
			}
			if _, _, e = tx.Evaluate(after, nil, false, now); !errors.Is(e, ErrInvalidState) {
				t.Fatal("terminal transition", e)
			}
		})
	}
}
func TestReversalDecisions(t *testing.T) {
	w, now := fixture(t, "100")
	bet := transaction(t, w, Bet, "25", "", now)
	w, _, _ = bet.Evaluate(w, nil, false, now)
	refund := transaction(t, w, Refund, "25", bet.Operation().ExternalID, now)
	refunded, d, e := refund.Evaluate(w, &bet, false, now)
	if e != nil || d != Credit || refunded.Balance().Amount() != "100.00" {
		t.Fatal(e)
	}
	rollback := transaction(t, refunded, Rollback, "25", refund.Operation().ExternalID, now)
	after, d, e := rollback.Evaluate(refunded, &refund, false, now)
	if e != nil || d != Debit || after.Balance().Amount() != "75.00" {
		t.Fatal(e)
	}
	repeated := transaction(t, w, Rollback, "25", bet.Operation().ExternalID, now)
	_, _, e = repeated.Evaluate(w, &bet, true, now)
	if e != nil || repeated.Snapshot().FailureCode != "ALREADY_REVERSED" {
		t.Fatal(repeated, e)
	}
	mismatch := transaction(t, w, Refund, "24", bet.Operation().ExternalID, now)
	_, _, _ = mismatch.Evaluate(w, &bet, false, now)
	if mismatch.Snapshot().FailureCode != "REFERENCE_AMOUNT_MISMATCH" {
		t.Fatal(mismatch)
	}
	winning := transaction(t, w, Win, "100", "", now)
	richer, _, _ := winning.Evaluate(w, nil, false, now)
	spend := transaction(t, richer, Bet, "175", "", now)
	empty, _, _ := spend.Evaluate(richer, nil, false, now)
	reversal := transaction(t, empty, Rollback, "100", winning.Operation().ExternalID, now)
	_, _, _ = reversal.Evaluate(empty, &winning, false, now)
	if reversal.Snapshot().FailureCode != "REVERSAL_INSUFFICIENT_FUNDS" {
		t.Fatal(reversal)
	}
}
func TestPendingAndFailureStates(t *testing.T) {
	w, now := fixture(t, "100")
	tx := transaction(t, w, Refund, "25", "missing", now)
	_, _, e := tx.Evaluate(w, nil, false, now)
	if e != nil || tx.Status() != PendingReference {
		t.Fatal(tx, e)
	}
	snapshot := tx.Snapshot()
	restored, e := RestoreWagerTransaction(snapshot)
	if e != nil || restored.Snapshot() != snapshot {
		t.Fatal(e)
	}
	if _, _, e = tx.Evaluate(w, nil, false, now); e == nil {
		t.Fatal("retry before due time accepted")
	}
	_, _, e = tx.Evaluate(w, nil, false, now.Add(5*time.Minute))
	if e != nil || tx.Status() != Rejected || tx.Snapshot().FailureCode != "REFERENCE_NOT_FOUND" {
		t.Fatal(tx, e)
	}
	failed := transaction(t, w, Bet, "25", "", now)
	if e = failed.MarkFailed(w, "INFRASTRUCTURE_PERMANENT", now); e != nil {
		t.Fatal(e)
	}
	ref := transaction(t, w, Refund, "25", failed.Operation().ExternalID, now)
	_, _, e = ref.Evaluate(w, &failed, false, now)
	if e != nil || ref.Snapshot().FailureCode != "REFERENCE_UNSUCCESSFUL" {
		t.Fatal(ref, e)
	}
	if e = failed.MarkFailed(w, "INFRASTRUCTURE_PERMANENT", now); e == nil {
		t.Fatal("failed was mutable")
	}
	if _, e = RestoreWagerTransaction(TransactionSnapshot{}); e == nil {
		t.Fatal("zero transaction rehydrated")
	}
	snapshot.Hash = "wrong"
	if _, e = RestoreWagerTransaction(snapshot); e == nil {
		t.Fatal("invalid hash rehydrated")
	}
}
func TestOverflowOpeningLedgerAndEvents(t *testing.T) {
	w, now := fixture(t, "92233720368547758.07")
	tx := transaction(t, w, Win, "0.01", "", now)
	after, _, e := tx.Evaluate(w, nil, false, now)
	if e != nil || after.Balance().Minor() != math.MaxInt64 || tx.Snapshot().FailureCode != "BALANCE_OVERFLOW" {
		t.Fatal(tx, e)
	}
	w, now = fixture(t, "100")
	opening, e := NewOpeningTransaction(uuid.NewString(), w, "trace", "cause", now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = RestoreWagerTransaction(opening.Snapshot()); e != nil {
		t.Fatal(e)
	}
	zero, _ := Zero("BRL")
	l, e := NewWalletLedgerEntry(uuid.NewString(), opening.ID(), w, Credit, w.Balance(), zero, now)
	if e != nil {
		t.Fatal(e)
	}
	s := l.Snapshot()
	s.After, _ = ParseMoney("99", "BRL")
	if _, e = RestoreWalletLedgerEntry(s); e == nil {
		t.Fatal("inconsistent ledger accepted")
	}
	if l.Snapshot().After.Amount() != "100.00" {
		t.Fatal("mutable ledger")
	}
	event, e := NewWalletBalanceChangedEvent(uuid.NewString(), opening, l)
	if e != nil {
		t.Fatal(e)
	}
	raw := event.JSON()
	raw[0] = 'x'
	var decoded map[string]json.RawMessage
	if e = json.Unmarshal(event.JSON(), &decoded); e != nil {
		t.Fatal("event bytes were mutable")
	}
	if string(decoded["version"]) != "1" || string(decoded["correlationId"]) != `"trace"` || string(decoded["causationId"]) != `"cause"` {
		t.Fatal(decoded)
	}
	if _, e = NewTransactionRejectedEvent(uuid.NewString(), opening); e == nil {
		t.Fatal("processed as rejection")
	}
}
func TestReferenceContextAndKind(t *testing.T) {
	w, now := fixture(t, "100")
	ref := transaction(t, w, Win, "25", "", now)
	w, _, _ = ref.Evaluate(w, nil, false, now)
	tx := transaction(t, w, Refund, "25", ref.Operation().ExternalID, now)
	_, _, _ = tx.Evaluate(w, &ref, false, now)
	if tx.Snapshot().FailureCode != "REFERENCE_KIND_INVALID" {
		t.Fatal(tx)
	}
	s := ref.Snapshot()
	s.Operation.RoundID = "other"
	s.Hash, _ = s.Operation.Hash()
	other, e := RestoreWagerTransaction(s)
	if e != nil {
		t.Fatal(e)
	}
	tx = transaction(t, w, Rollback, "25", ref.Operation().ExternalID, now)
	_, _, _ = tx.Evaluate(w, &other, false, now)
	if tx.Snapshot().FailureCode != "REFERENCE_MISMATCH" {
		t.Fatal(tx)
	}
	pending := transaction(t, w, Bet, "25", "", now)
	tx = transaction(t, w, Refund, "25", pending.Operation().ExternalID, now)
	_, _, _ = tx.Evaluate(w, &pending, false, now)
	if tx.Status() != PendingReference {
		t.Fatal(tx)
	}
}
