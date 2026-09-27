package store

import (
	"context"
	"encoding/base64"
	"fmt"
	"jungle-wallet-service/internal/domain"
	"strconv"
	"strings"
	"time"
)

type LedgerEntry struct {
	ID            string       `json:"id"`
	WalletID      string       `json:"walletId"`
	TransactionID string       `json:"transactionId"`
	Direction     string       `json:"direction"`
	Money         domain.Money `json:"money"`
	Before        domain.Money `json:"balanceBefore"`
	After         domain.Money `json:"balanceAfter"`
	Created       time.Time    `json:"createdAt"`
}
type LedgerPage struct {
	Entries    []LedgerEntry `json:"entries"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

func (s *Store) Ledger(ctx context.Context, wallet, cursor string, limit int) (LedgerPage, error) {
	if limit < 1 || limit > 100 {
		return LedgerPage{}, domain.ErrInvalidOperation
	}
	var after int64
	if cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil {
			return LedgerPage{}, domain.ErrInvalidOperation
		}
		parts := strings.Split(string(b), ":")
		if len(parts) != 2 || parts[0] != wallet {
			return LedgerPage{}, domain.ErrInvalidOperation
		}
		after, e = strconv.ParseInt(parts[1], 10, 64)
		if e != nil || after < 1 {
			return LedgerPage{}, domain.ErrInvalidOperation
		}
	}
	if _, err := s.Wallet(ctx, wallet); err != nil {
		return LedgerPage{}, err
	}
	rows, err := s.db.Query(ctx, `SELECT id,transaction_id,direction,amount,currency,balance_before,balance_after,created_at,wallet_version FROM wallet_ledger WHERE wallet_id=$1 AND wallet_version>$2 ORDER BY wallet_version LIMIT $3`, wallet, after, limit+1)
	if err != nil {
		return LedgerPage{}, err
	}
	defer rows.Close()
	page := LedgerPage{Entries: []LedgerEntry{}}
	last := after
	for rows.Next() {
		var e LedgerEntry
		e.WalletID = wallet
		var amount, before, after, version int64
		var currency string
		if err = rows.Scan(&e.ID, &e.TransactionID, &e.Direction, &amount, &currency, &before, &after, &e.Created, &version); err != nil {
			return page, err
		}
		if len(page.Entries) == limit {
			page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d", wallet, last)))
			break
		}
		e.Money, _ = domain.FromMinor(amount, currency)
		e.Before, _ = domain.FromMinor(before, currency)
		e.After, _ = domain.FromMinor(after, currency)
		if _, err = domain.RestoreWalletLedgerEntry(domain.LedgerSnapshot{ID: e.ID, WalletID: e.WalletID, TransactionID: e.TransactionID, Direction: domain.Direction(e.Direction), Money: e.Money, Before: e.Before, After: e.After, WalletVersion: version, CreatedAt: e.Created}); err != nil {
			return page, err
		}
		page.Entries = append(page.Entries, e)
		last = version
	}
	return page, rows.Err()
}
