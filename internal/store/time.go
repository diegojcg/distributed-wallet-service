package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Financial timestamps use one clock shared by every application instance.
func databaseTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now)
	return now.UTC(), err
}

// Preserve monotonicity even for older rows written by a fast host clock.
func notBefore(now time.Time, floors ...time.Time) time.Time {
	for _, floor := range floors {
		if floor.After(now) {
			now = floor
		}
	}
	return now.UTC()
}
