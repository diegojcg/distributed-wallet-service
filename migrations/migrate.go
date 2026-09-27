package migrations

import (
	"context"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5"
)

//go:embed *.sql
var files embed.FS

// Apply serializes schema changes across processes and commits the migration batch atomically.
func Apply(ctx context.Context, conn *pgx.Conn, direction string) error {
	if direction != "up" && direction != "down" {
		return fmt.Errorf("expected up or down")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(720261001)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY)"); err != nil {
		return err
	}

	versions := []struct {
		number int
		file   string
	}{{1, "001_initial"}, {2, "002_trace_metadata"}, {3, "003_ledger_identity"}}
	if direction == "down" {
		for i, j := 0, len(versions)-1; i < j; i, j = i+1, j-1 {
			versions[i], versions[j] = versions[j], versions[i]
		}
	}
	for _, version := range versions {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", version.number).Scan(&exists); err != nil {
			return err
		}
		if (direction == "up" && !exists) || (direction == "down" && exists) {
			data, e := files.ReadFile(version.file + "." + direction + ".sql")
			if e != nil {
				return e
			}
			if _, err = tx.Exec(ctx, string(data)); err != nil {
				return err
			}
			if direction == "up" {
				_, err = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", version.number)
			} else {
				_, err = tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=$1", version.number)
			}
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
