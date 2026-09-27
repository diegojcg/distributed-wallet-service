package store

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"jungle-wallet-service/internal/domain"
	"time"
)

// Only explicit server responses known to abort the SQL transaction are terminal.
// Network errors, cancellation, connection loss, deadlocks and serialization errors
// are never classified as permanent, especially when COMMIT is ambiguous.
func permanent(err error) bool {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return false
	}
	switch pg.Code {
	case "0A000", "22003", "23514", "42883":
		return true
	}
	return false
}
func (s *Store) auditFailure(ctx context.Context, o domain.Operation, key string, inbox *Message) (Result, error) {
	now := time.Now().UTC()
	id := uuid.NewString()
	m := metadata(ctx, id)
	t, err := domain.NewWagerTransaction(id, o, key, m.CorrelationID, m.CausationID, now)
	if err != nil {
		return Result{}, err
	}
	o = t.Operation()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer rollback(tx)
	if inbox != nil {
		if err = registerInbox(ctx, tx, *inbox); err != nil {
			return Result{}, err
		}
	}
	w, err := scanWallet(tx.QueryRow(ctx, "SELECT "+walletCols+" FROM wallets WHERE id=$1 FOR UPDATE", o.WalletID))
	if err != nil {
		return Result{}, err
	}
	if w.PlayerID() != o.PlayerID {
		return Result{}, ErrOwnership
	}
	existing, err := scanTransaction(tx.QueryRow(ctx, "SELECT "+transactionCols+" FROM wager_transactions WHERE provider_id=$1 AND (idempotency_key=$2 OR external_id=$3) LIMIT 1", o.ProviderID, key, o.ExternalID))
	if err == nil {
		// Full conflict/replay check covers both uniqueness constraints.
		r, _, e := replay(ctx, tx, o, key, t.Snapshot().Hash)
		if e != nil {
			return Result{}, e
		}
		if existing.Status() != domain.PendingReference && existing.Status() != domain.Pending {
			return r, tx.Commit(ctx)
		}
		t = existing
	} else if errors.Is(err, pgx.ErrNoRows) {
		inserted, e := insertTransaction(ctx, tx, t)
		if e != nil {
			return Result{}, e
		}
		if !inserted {
			return Result{}, ErrConflict
		}
	} else {
		return Result{}, err
	}
	if err = t.MarkFailed(w, "INFRASTRUCTURE_PERMANENT", time.Now().UTC()); err != nil {
		return Result{}, err
	}
	if err = updateTransaction(ctx, tx, t); err != nil {
		return Result{}, err
	}
	// FAILED is an infrastructure audit result, not a business rejection event.
	if err = tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return result(t, false), nil
}
