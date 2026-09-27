//go:build integration

package integration

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-wallet-service/internal/domain"
	"jungle-wallet-service/internal/store"
	"jungle-wallet-service/migrations"
)

func TestHTTPBoundaryAudit(t *testing.T) {
	h := setup(t)
	w, p := h.wallet(t, "100")
	t.Run("MissingBusinessFieldsAreInvalidInput", func(t *testing.T) {
		call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), map[string]any{}, 400)
		call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, uuid.NewString(), map[string]any{"providerId": "provider-a"}, 400)
		assertSQLCount(t, h, "SELECT count(*) FROM wager_transactions WHERE wallet_id=$1", 1, w)
	})
	t.Run("CanonicalRouteIDs", func(t *testing.T) {
		for _, id := range []string{strings.ToUpper(w), "urn:uuid:" + w, strings.ReplaceAll(w, "-", "")} {
			out := call(t, h.urls[0], "GET", "/wallets/"+id, h.internal, "", nil, 200)
			if out["id"] != w {
				t.Fatal(out)
			}
			call(t, h.urls[1], "GET", "/wallets/"+id+"/ledger", h.internal, "", nil, 200)
			call(t, h.urls[2], "POST", "/wallets/"+id+"/reconciliation", h.internal, "", nil, 200)
		}
		for _, id := range []string{"invalid", uuid.Nil.String()} {
			call(t, h.urls[0], "GET", "/wallets/"+id, h.internal, "", nil, 400)
			call(t, h.urls[0], "GET", "/wagering/transactions/"+id, h.provider, "", nil, 400)
		}
	})
	t.Run("TwoKeysMustResolveTheSameTransaction", func(t *testing.T) {
		a := operation(w, p, uuid.NewString(), "BET", "25")
		b := operation(w, p, uuid.NewString(), "BET", "10")
		ka, kb := uuid.NewString(), uuid.NewString()
		first := call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, ka, a, 200)
		call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, kb, b, 200)
		call(t, h.urls[2], "POST", "/wagering/transactions", h.provider, ka, b, 409)
		a["money"] = money("025.0")
		replay := call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, ka, a, 200)
		if replay["transactionId"] != first["transactionId"] || amount(replay) != "75.00" {
			t.Fatal(replay)
		}
		call(t, h.urls[0], "GET", "/wagering/transactions/urn:uuid:"+first["transactionId"].(string), h.provider, "", nil, 200)
		assertSQLCount(t, h, "SELECT count(*) FROM wallet_ledger WHERE wallet_id=$1", 3, w)
	})
	t.Run("PaginationBoundariesAndIsolation", func(t *testing.T) {
		page := call(t, h.urls[0], "GET", "/wallets/"+w+"/ledger?limit=1", h.internal, "", nil, 200)
		cursor := page["nextCursor"].(string)
		other, _ := h.wallet(t, "1")
		call(t, h.urls[0], "GET", "/wallets/"+other+"/ledger?cursor="+url.QueryEscape(cursor), h.internal, "", nil, 400)
		for _, query := range []string{"limit=0", "limit=-1", "limit=101", "limit=x", "cursor=not-a-cursor"} {
			call(t, h.urls[0], "GET", "/wallets/"+w+"/ledger?"+query, h.internal, "", nil, 400)
		}
		ids := map[string]bool{}
		cursor = ""
		for i := 0; i < 4; i++ {
			page = call(t, h.urls[i%3], "GET", "/wallets/"+w+"/ledger?limit=1&cursor="+url.QueryEscape(cursor), h.internal, "", nil, 200)
			for _, v := range page["entries"].([]any) {
				id := v.(map[string]any)["id"].(string)
				if ids[id] {
					t.Fatal("repeated entry")
				}
				ids[id] = true
			}
			next, ok := page["nextCursor"].(string)
			if !ok {
				break
			}
			cursor = next
		}
		if len(ids) != 3 {
			t.Fatal(ids)
		}
	})
}

// A separate database and runtime role prove SQL constraints without HTTP or workers.
func auditDatabase(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	root, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	name := "audit_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := root.Exec(ctx, "DROP DATABASE "+name); err != nil {
			t.Error(err)
		}
		root.Close()
	})
	open := func(raw string) *pgxpool.Pool {
		u, e := url.Parse(raw)
		if e != nil {
			t.Fatal(e)
		}
		u.Path = "/" + name
		pool, e := pgxpool.New(ctx, u.String())
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	admin := open(os.Getenv("TEST_DATABASE_URL"))
	runtime := open(os.Getenv("DATABASE_URL"))
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = migrations.Apply(ctx, conn.Conn(), "up")
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	return admin, runtime
}
func TestStorageAudit(t *testing.T) {
	admin, runtime := auditDatabase(t)
	ctx := context.Background()
	s := store.New(runtime)
	initial, _ := domain.ParseMoney("100", "BRL")
	player := uuid.NewString()
	wallet, err := s.Open(ctx, player, initial)
	if err != nil {
		t.Fatal(err)
	}
	assertError := func(pool *pgxpool.Pool, query, code string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, query, args...)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != code {
			t.Fatalf("expected SQLSTATE %s, got %v", code, err)
		}
	}
	t.Run("RuntimePermissionsAndAdminTriggers", func(t *testing.T) {
		for _, query := range []string{"UPDATE wallet_ledger SET amount=amount WHERE wallet_id=$1", "DELETE FROM wallet_ledger WHERE wallet_id=$1"} {
			assertError(runtime, query, "42501", wallet.ID)
			assertError(admin, query, "23514", wallet.ID)
		}
		assertError(runtime, "TRUNCATE wallet_ledger", "42501")
		assertError(runtime, "ALTER TABLE wallet_ledger DISABLE TRIGGER ALL", "42501")
		assertError(runtime, "SET session_replication_role=replica", "42501")
		assertError(admin, "TRUNCATE wallet_ledger CASCADE", "23514")
		assertError(runtime, "UPDATE wallets SET balance=-1,version=version+1 WHERE id=$1", "23514", wallet.ID)
		assertError(runtime, "UPDATE wallets SET balance=balance+1,version=version+1 WHERE id=$1", "23514", wallet.ID)
		assertError(runtime, "UPDATE outbox SET payload='{}' WHERE aggregate_id=$1", "23514", wallet.ID)
		other, e := s.Open(ctx, uuid.NewString(), initial)
		if e != nil {
			t.Fatal(e)
		}
		assertError(admin, `INSERT INTO wallet_ledger SELECT gen_random_uuid(),$1,transaction_id,direction,amount,currency,balance_before,balance_after,wallet_version,created_at FROM wallet_ledger WHERE wallet_id=$2`, "23514", other.ID, wallet.ID)
	})
	t.Run("OutboxInsertFailureRollsBackEveryFinancialWrite", func(t *testing.T) {
		_, err := admin.Exec(ctx, `CREATE FUNCTION audit_reject_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'serialization test' USING ERRCODE='40001'; END $$; CREATE TRIGGER audit_reject_outbox BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION audit_reject_outbox()`)
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Exec(ctx, "DROP TRIGGER audit_reject_outbox ON outbox; DROP FUNCTION audit_reject_outbox()")
		bet, _ := domain.ParseMoney("25", "BRL")
		messageID := uuid.NewString()
		_, err = s.Consume(ctx, domain.Operation{ProviderID: "provider-a", ExternalID: uuid.NewString(), WalletID: wallet.ID, PlayerID: player, RoundID: "audit", GameID: "audit", Kind: domain.Bet, Money: bet}, uuid.NewString(), "audit", messageID, strings.Repeat("a", 64))
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "40001" {
			t.Fatal(err)
		}
		var balance, ledger, transactions int
		err = admin.QueryRow(ctx, `SELECT balance,(SELECT count(*) FROM wallet_ledger WHERE wallet_id=$1),(SELECT count(*) FROM wager_transactions WHERE wallet_id=$1) FROM wallets WHERE id=$1`, wallet.ID).Scan(&balance, &ledger, &transactions)
		if err != nil || balance != 10000 || ledger != 1 || transactions != 1 {
			t.Fatal(balance, ledger, transactions, err)
		}
		var inbox int
		if err = admin.QueryRow(ctx, "SELECT count(*) FROM inbox WHERE message_id=$1", messageID).Scan(&inbox); err != nil || inbox != 0 {
			t.Fatal(inbox, err)
		}
	})
	t.Run("LeaseRecoveryAndStaleWorkerFencing", func(t *testing.T) {
		first, err := s.Claim(ctx)
		if err != nil || len(first) != 4 {
			t.Fatal(len(first), err)
		}
		concurrent, err := store.New(runtime).Claim(ctx)
		if err != nil || len(concurrent) != 0 {
			t.Fatal(concurrent, err)
		}
		old := first[0]
		if _, err = admin.Exec(ctx, "UPDATE outbox SET lease_until=now()-interval '1 second' WHERE event_id=$1", old.ID); err != nil {
			t.Fatal(err)
		}
		second, err := s.Claim(ctx)
		if err != nil || len(second) != 1 || second[0].Lease == old.Lease || second[0].ID != old.ID || string(second[0].Payload) != string(old.Payload) {
			t.Fatal(second, err)
		}
		if err = s.Published(ctx, old); err != nil {
			t.Fatal(err)
		}
		if err = s.RetryPublication(ctx, old); err != nil {
			t.Fatal(err)
		}
		var lease string
		var published *time.Time
		err = admin.QueryRow(ctx, "SELECT lease_token::text,published_at FROM outbox WHERE event_id=$1", old.ID).Scan(&lease, &published)
		if err != nil || lease != second[0].Lease || published != nil {
			t.Fatal(lease, published, err)
		}
		if err = s.Published(ctx, second[0]); err != nil {
			t.Fatal(err)
		}
		err = admin.QueryRow(ctx, "SELECT published_at FROM outbox WHERE event_id=$1", old.ID).Scan(&published)
		if err != nil || published == nil {
			t.Fatal(published, err)
		}
	})
}
