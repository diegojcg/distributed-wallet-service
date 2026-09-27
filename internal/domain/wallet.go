package domain

import (
	"github.com/google/uuid"
	"math"
	"time"
)

type Wallet struct {
	id, playerID         string
	balance              Money
	version              int64
	createdAt, updatedAt time.Time
}

func NewWallet(id, playerID string, balance Money, now time.Time) (Wallet, error) {
	return RestoreWallet(id, playerID, balance, 1, now, now)
}
func RestoreWallet(id, playerID string, balance Money, version int64, createdAt, updatedAt time.Time) (Wallet, error) {
	if !validID(id) {
		return Wallet{}, ErrInvalidState
	}
	if !validID(playerID) {
		return Wallet{}, ErrInvalidState
	}
	if !balance.Valid() || balance.Minor() < 0 || version < 1 || createdAt.IsZero() || updatedAt.Before(createdAt) {
		return Wallet{}, ErrInvalidState
	}
	wid, _ := uuid.Parse(id)
	pid, _ := uuid.Parse(playerID)
	return Wallet{wid.String(), pid.String(), balance, version, createdAt.UTC(), updatedAt.UTC()}, nil
}
func (w Wallet) ID() string           { return w.id }
func (w Wallet) PlayerID() string     { return w.playerID }
func (w Wallet) Balance() Money       { return w.balance }
func (w Wallet) Version() int64       { return w.version }
func (w Wallet) CreatedAt() time.Time { return w.createdAt }
func (w Wallet) UpdatedAt() time.Time { return w.updatedAt }
func (w *Wallet) apply(value Money, debit bool, now time.Time) error {
	if w.id == "" || !value.Valid() || value.Minor() < 0 || now.Before(w.updatedAt) {
		return ErrInvalidState
	}
	var next Money
	var err error
	if debit {
		next, err = w.balance.Sub(value)
	} else {
		next, err = w.balance.Add(value)
	}
	if err != nil {
		return err
	}
	if next.Minor() < 0 {
		return ErrInsufficientFunds
	}
	if value.Minor() == 0 {
		return nil
	}
	if w.version == math.MaxInt64 {
		return ErrOverflow
	}
	w.balance = next
	w.version++
	w.updatedAt = now.UTC()
	return nil
}
func (w *Wallet) Debit(value Money, now time.Time) error  { return w.apply(value, true, now) }
func (w *Wallet) Credit(value Money, now time.Time) error { return w.apply(value, false, now) }
