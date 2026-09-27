package store

import "context"

// These nonmonetary aggregates are exclusively for observability.
func (s *Store) Statistics(ctx context.Context) (pending int64, age float64, references int64, err error) {
	err = s.db.QueryRow(ctx, `SELECT count(*),COALESCE(EXTRACT(EPOCH FROM now()-min(occurred_at)),0)::double precision,(SELECT count(*) FROM wager_transactions WHERE status='PENDING_REFERENCE') FROM outbox WHERE published_at IS NULL`).Scan(&pending, &age, &references)
	return
}
