package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
)

func TestPermanentClassification(t *testing.T) {
	for _, code := range []string{"0A000", "22003", "23514", "42883"} {
		if !permanent(&pgconn.PgError{Code: code}) {
			t.Fatal(code)
		}
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("EOF during COMMIT"), &pgconn.PgError{Code: "40001"}, &pgconn.PgError{Code: "40P01"}, &pgconn.PgError{Code: "08006"}, &pgconn.PgError{Code: "23505"}} {
		if permanent(err) {
			t.Fatal(err)
		}
	}
}
