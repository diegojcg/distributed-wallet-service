//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"jungle-wallet-service/internal/domain"
	"jungle-wallet-service/internal/store"
)

func TestConcurrentClaims(t *testing.T) {
	_, runtime := auditDatabase(t)
	s := store.New(runtime)
	ctx := context.Background()
	m, _ := domain.ParseMoney("1", "BRL")
	for i := 0; i < 12; i++ {
		if _, err := s.Open(ctx, uuid.NewString(), m); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	type result struct {
		events []store.Publication
		err    error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; events, err := store.New(runtime).Claim(ctx); results <- result{events, err} }()
	}
	close(start)
	ids := map[string]bool{}
	leases := map[string]bool{}
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil || len(r.events) != 10 {
			t.Fatal(len(r.events), r.err)
		}
		leases[r.events[0].Lease] = true
		for _, event := range r.events {
			if ids[event.ID] {
				t.Fatal("overlapping claims", event.ID)
			}
			ids[event.ID] = true
		}
	}
	if len(ids) != 20 || len(leases) != 2 {
		t.Fatal(len(ids), len(leases))
	}
	rest, err := s.Claim(ctx)
	if err != nil || len(rest) != 4 {
		t.Fatal(rest, err)
	}
	for _, event := range rest {
		if ids[event.ID] {
			t.Fatal("active lease claimed again")
		}
	}
}

func TestConcurrentTransportAndReversals(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	c := sqsClient(t, "provider-a")
	q := queueURL(t, c, "wager-transactions.fifo")
	t.Run("HTTPAndSQSOverlapUnderWalletLock", func(t *testing.T) {
		w, p := h.wallet(t, "100")
		op := operation(w, p, uuid.NewString(), "BET", "25")
		key, id := uuid.NewString(), uuid.NewString()
		lock, err := h.db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback(ctx)
		if _, err = lock.Exec(ctx, "SELECT id FROM wallets WHERE id=$1 FOR UPDATE", w); err != nil {
			t.Fatal(err)
		}
		var blocker int32
		if err = lock.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
			t.Fatal(err)
		}
		// Wait for SQS to reach the held row before starting HTTP. A canceled
		// ReceiveMessage from a prior test can otherwise hide this message for
		// one visibility window; that is unrelated to database concurrency.
		waiters := func() int {
			var n int
			err := h.db.QueryRow(ctx, `WITH RECURSIVE blocked(pid) AS (
                SELECT $1::int
                UNION
                SELECT activity.pid FROM pg_stat_activity activity JOIN blocked parent ON parent.pid=ANY(pg_blocking_pids(activity.pid))
            ) SELECT count(*)-1 FROM blocked`, blocker).Scan(&n)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		send(t, c, q, w, envelope(op, key, id))
		eventually(t, 45*time.Second, func() bool { return waiters() == 1 })
		type response struct {
			code int
			body map[string]any
			err  error
		}
		done := make(chan response, 1)
		go func() {
			code, body, err := request(h.urls[0], "POST", "/wagering/transactions", h.provider, key, op)
			done <- response{code, body, err}
		}()
		// The second requester can queue behind the first (tuple lock), so
		// traverse the full wait chain rooted at our held wallet row.
		eventually(t, 3*time.Second, func() bool { return waiters() == 2 })
		if err = lock.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		r := <-done
		if r.err != nil || r.code != 200 || amount(r.body) != "75.00" {
			t.Fatal(r)
		}
		eventually(t, 5*time.Second, func() bool {
			var n int
			err := h.db.QueryRow(ctx, "SELECT count(*) FROM inbox WHERE message_id=$1", id).Scan(&n)
			return err == nil && n == 1
		})
		assertSQLCount(t, h, "SELECT count(*) FROM wallet_ledger WHERE wallet_id=$1 AND direction='DEBIT'", 1, w)
		assertSQLCount(t, h, "SELECT count(*) FROM wager_transactions WHERE wallet_id=$1 AND kind='BET'", 1, w)
		assertSQLCount(t, h, "SELECT count(*) FROM outbox WHERE aggregate_id=$1", 4, w)
		if out := call(t, h.urls[2], "GET", "/wallets/"+w, h.internal, "", nil, 200); amount(out) != "75.00" {
			t.Fatal(out)
		}
	})
	t.Run("RefundAndRollbackRace", func(t *testing.T) {
		w, p := h.wallet(t, "100")
		bet := uuid.NewString()
		call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), operation(w, p, bet, "BET", "25"), 200)
		start := make(chan struct{})
		var wg sync.WaitGroup
		codes := make([]int, 2)
		bodies := make([]map[string]any, 2)
		for i, kind := range []string{"REFUND", "ROLLBACK"} {
			wg.Add(1)
			go func(i int, kind string) {
				defer wg.Done()
				<-start
				op := operation(w, p, uuid.NewString(), kind, "25")
				op["referenceExternalTransactionId"] = bet
				var err error
				codes[i], bodies[i], err = request(h.urls[i+1], "POST", "/wagering/transactions", h.provider, uuid.NewString(), op)
				if err != nil {
					t.Error(err)
				}
			}(i, kind)
		}
		close(start)
		wg.Wait()
		processed, rejected := 0, 0
		for i, code := range codes {
			if code == 200 {
				processed++
			} else if code == 422 && bodies[i]["failureCode"] == "ALREADY_REVERSED" {
				rejected++
			} else {
				t.Fatal(code, bodies[i])
			}
		}
		if processed != 1 || rejected != 1 {
			t.Fatal(codes)
		}
		assertSQLCount(t, h, "SELECT count(*) FROM wallet_ledger WHERE wallet_id=$1", 3, w)
		out := call(t, h.urls[0], "GET", "/wallets/"+w, h.internal, "", nil, 200)
		if amount(out) != "100.00" || out["version"] != float64(3) {
			t.Fatal(out)
		}
		rec := call(t, h.urls[0], "POST", "/wallets/"+w+"/reconciliation", h.internal, "", nil, 200)
		if rec["consistent"] != true {
			t.Fatal(rec)
		}
	})
	t.Run("DuplicateOpeningAndMissingKey", func(t *testing.T) {
		w, p := h.wallet(t, "100")
		call(t, h.urls[1], "POST", "/wallets", h.internal, "", map[string]any{"playerId": p, "initialBalance": money("100")}, 409)
		call(t, h.urls[2], "POST", "/wagering/transactions", h.provider, "", operation(w, p, uuid.NewString(), "BET", "1"), 400)
		assertSQLCount(t, h, "SELECT count(*) FROM wager_transactions WHERE wallet_id=$1", 1, w)
	})
}

func TestStoredFutureTimestamps(t *testing.T) {
	admin, runtime := auditDatabase(t)
	s := store.New(runtime)
	ctx := context.Background()
	m, _ := domain.ParseMoney("100", "BRL")
	player := uuid.NewString()
	w, err := s.Open(ctx, player, m)
	if err != nil {
		t.Fatal(err)
	}
	var future time.Time
	if err = admin.QueryRow(ctx, "UPDATE wallets SET updated_at=clock_timestamp()+interval '1 hour' WHERE id=$1 RETURNING updated_at", w.ID).Scan(&future); err != nil {
		t.Fatal(err)
	}
	op := func(kind domain.Kind, ref string) domain.Operation {
		v, _ := domain.ParseMoney("1", "BRL")
		return domain.Operation{ProviderID: "provider-a", ExternalID: uuid.NewString(), WalletID: w.ID, PlayerID: player, RoundID: "r", GameID: "g", Kind: kind, Money: v, Reference: ref}
	}
	bet := op(domain.Bet, "")
	r, err := s.Apply(ctx, bet, uuid.NewString())
	if err != nil || r.Status != domain.Processed {
		t.Fatal(r, err)
	}
	if _, err = s.Consume(ctx, op(domain.Win, ""), uuid.NewString(), "clock", uuid.NewString(), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	ref := op(domain.Bet, "")
	pending := op(domain.Refund, ref.ExternalID)
	r, err = s.Apply(ctx, pending, uuid.NewString())
	if err != nil || r.Status != domain.PendingReference {
		t.Fatal(r, err)
	}
	var transactionFuture time.Time
	err = admin.QueryRow(ctx, "UPDATE wager_transactions SET updated_at=$2::timestamptz+interval '1 hour',next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1 RETURNING updated_at", r.TransactionID, future).Scan(&transactionFuture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, ref, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if err = s.RetryReferences(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var updated time.Time
	if err = admin.QueryRow(ctx, "SELECT status,updated_at FROM wager_transactions WHERE id=$1", r.TransactionID).Scan(&status, &updated); err != nil || status != "PROCESSED" || updated.Before(transactionFuture) {
		t.Fatal(status, updated, err)
	}
	// A failed financial attempt must also audit with monotonic persisted timestamps.
	_, err = admin.Exec(ctx, `CREATE FUNCTION clock_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test' USING ERRCODE='0A000'; END $$; CREATE TRIGGER clock_failure BEFORE INSERT ON wallet_ledger FOR EACH ROW EXECUTE FUNCTION clock_failure()`)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := s.Apply(ctx, op(domain.Bet, ""), uuid.NewString())
	if err != nil || failed.Status != domain.Failed {
		t.Fatal(failed, err)
	}
	if err = admin.QueryRow(ctx, "SELECT updated_at FROM wager_transactions WHERE id=$1", failed.TransactionID).Scan(&updated); err != nil || updated.Before(transactionFuture) {
		t.Fatal(updated, err)
	}
}

func TestBlockedReferenceDoesNotStopBatch(t *testing.T) {
	admin, runtime := auditDatabase(t)
	s := store.New(runtime)
	ctx := context.Background()
	m, _ := domain.ParseMoney("100", "BRL")
	var wallets []string
	var pending []store.Result
	for i := 0; i < 2; i++ {
		player := uuid.NewString()
		w, err := s.Open(ctx, player, m)
		if err != nil {
			t.Fatal(err)
		}
		wallets = append(wallets, w.ID)
		v, _ := domain.ParseMoney("1", "BRL")
		bet := domain.Operation{ProviderID: "provider-a", ExternalID: uuid.NewString(), WalletID: w.ID, PlayerID: player, RoundID: "r", GameID: "g", Kind: domain.Bet, Money: v}
		refund := bet
		refund.Kind = domain.Refund
		refund.Reference = bet.ExternalID
		refund.ExternalID = uuid.NewString()
		r, err := s.Apply(ctx, refund, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		pending = append(pending, r)
		if _, err = s.Apply(ctx, bet, uuid.NewString()); err != nil {
			t.Fatal(err)
		}
	}
	for i, p := range pending {
		if _, err := admin.Exec(ctx, "UPDATE wager_transactions SET next_attempt_at=clock_timestamp()-$2::int*interval '1 second' WHERE id=$1", p.TransactionID, 10-i); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, "SELECT id FROM wallets WHERE id=$1 FOR UPDATE", wallets[0]); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err = s.RetryReferences(bounded); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expected per-item timeout", err)
	}
	var status string
	if err = admin.QueryRow(ctx, "SELECT status FROM wager_transactions WHERE id=$1", pending[1].TransactionID).Scan(&status); err != nil || status != "PROCESSED" {
		t.Fatal(status, err)
	}
	if err = lock.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatal(err)
	}
	if err = s.RetryReferences(ctx); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, "SELECT status FROM wager_transactions WHERE id=$1", pending[0].TransactionID).Scan(&status); err != nil || status != "PROCESSED" {
		t.Fatal(status, err)
	}
}
