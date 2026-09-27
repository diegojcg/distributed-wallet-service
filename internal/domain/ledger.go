package domain

import "time"

type LedgerSnapshot struct {
	ID, WalletID, TransactionID string
	Direction                   Direction
	Money, Before, After        Money
	WalletVersion               int64
	CreatedAt                   time.Time
}
type WalletLedgerEntry struct{ state LedgerSnapshot }

func NewWalletLedgerEntry(id, transactionID string, w Wallet, direction Direction, money, before Money, now time.Time) (WalletLedgerEntry, error) {
	return RestoreWalletLedgerEntry(LedgerSnapshot{id, w.ID(), transactionID, direction, money, before, w.Balance(), w.Version(), now.UTC()})
}
func RestoreWalletLedgerEntry(s LedgerSnapshot) (WalletLedgerEntry, error) {
	if !validID(s.ID) || !validID(s.WalletID) || !validID(s.TransactionID) || !s.Money.Valid() || s.Money.Minor() <= 0 || !s.Before.Valid() || !s.After.Valid() || s.Before.Minor() < 0 || s.After.Minor() < 0 || s.WalletVersion < 1 || s.CreatedAt.IsZero() {
		return WalletLedgerEntry{}, ErrInvalidState
	}
	var expected Money
	var err error
	switch s.Direction {
	case Credit:
		expected, err = s.Before.Add(s.Money)
	case Debit:
		expected, err = s.Before.Sub(s.Money)
	default:
		return WalletLedgerEntry{}, ErrInvalidState
	}
	if err != nil {
		return WalletLedgerEntry{}, err
	}
	if expected != s.After {
		return WalletLedgerEntry{}, ErrInvalidState
	}
	return WalletLedgerEntry{s}, nil
}
func (e WalletLedgerEntry) Snapshot() LedgerSnapshot { return e.state }
