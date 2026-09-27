//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
)

func assertSQLCount(t *testing.T, h *harness, sql string, want int, args ...any) {
	t.Helper()
	var n int
	if err := h.db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil || n != want {
		t.Fatalf("count=%d want=%d err=%v", n, want, err)
	}
}
func TestContracts(t *testing.T) {
	h := setup(t)
	t.Run("RealExpiredTokenAndAudience", func(t *testing.T) {
		w, p := h.wallet(t, "100")
		op := operation(w, p, uuid.NewString(), "BET", "1")
		expired := token(t, "expired-token")
		wrong := token(t, "wrong-audience")
		time.Sleep(2100 * time.Millisecond)
		for _, credential := range []string{expired, wrong, "", h.other} {
			want := 401
			if credential == h.other {
				want = 403
			}
			call(t, h.urls[0], "POST", "/wagering/transactions", credential, uuid.NewString(), op, want)
		}
		assertSQLCount(t, h, "SELECT count(*) FROM wager_transactions WHERE wallet_id=$1 AND kind<>'OPENING'", 0, w)
		if out := call(t, h.urls[1], "GET", "/wallets/"+w, h.internal, "", nil, 200); amount(out) != "100.00" {
			t.Fatal(out)
		}
	})
	t.Run("ExternalOpeningZerosAndOverflow", func(t *testing.T) {
		w, p := h.wallet(t, "92233720368547758.07")
		for _, kind := range []string{"OPENING", "BET", "WIN", "REFUND", "ROLLBACK"} {
			op := operation(w, p, uuid.NewString(), kind, "0")
			op["referenceExternalTransactionId"] = "missing"
			call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), op, 400)
		}
		out := call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), operation(w, p, uuid.NewString(), "WIN", "0.01"), 422)
		if out["failureCode"] != "BALANCE_OVERFLOW" {
			t.Fatal(out)
		}
		assertSQLCount(t, h, "SELECT count(*) FROM wallet_ledger WHERE wallet_id=$1", 1, w)
		zero, _ := h.wallet(t, "0")
		assertSQLCount(t, h, "SELECT count(*) FROM wager_transactions WHERE wallet_id=$1", 0, zero)
		assertSQLCount(t, h, "SELECT count(*) FROM outbox WHERE aggregate_id=$1", 0, zero)
	})
	t.Run("ReferenceExpiryAndRejectedReference", func(t *testing.T) {
		w, p := h.wallet(t, "10")
		op := operation(w, p, uuid.NewString(), "REFUND", "1")
		op["referenceExternalTransactionId"] = uuid.NewString()
		out := call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), op, 202)
		id := out["transactionId"].(string)
		// Advance only retry metadata; no business state or balances are forged.
		if _, err := h.db.Exec(context.Background(), "UPDATE wager_transactions SET attempts=9,next_attempt_at=now() WHERE id=$1 AND status='PENDING_REFERENCE'", id); err != nil {
			t.Fatal(err)
		}
		eventually(t, 5*time.Second, func() bool {
			var code string
			err := h.db.QueryRow(context.Background(), "SELECT coalesce(failure_code,'') FROM wager_transactions WHERE id=$1", id).Scan(&code)
			return err == nil && code == "REFERENCE_NOT_FOUND"
		})
		bad := operation(w, p, uuid.NewString(), "BET", "20")
		call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), bad, 422)
		op["externalTransactionId"] = uuid.NewString()
		op["referenceExternalTransactionId"] = bad["externalTransactionId"]
		op["money"] = money("20")
		out = call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, uuid.NewString(), op, 422)
		if out["failureCode"] != "REFERENCE_UNSUCCESSFUL" {
			t.Fatal(out)
		}
		assertSQLCount(t, h, "SELECT count(*) FROM outbox WHERE aggregate_id=$1 AND event_type='WagerTransactionRejected'", 3, w)
	})
	t.Run("ReconciliationReportsDriftWithoutRepair", func(t *testing.T) {
		w, _ := h.wallet(t, "100")
		// Only this test's administrative connection bypasses triggers to emulate corruption.
		corrupt := func(value int64) {
			tx, err := h.db.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(context.Background(), "SET LOCAL session_replication_role=replica"); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(context.Background(), "UPDATE wallets SET balance=$2 WHERE id=$1", w, value); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		corrupt(9999)
		defer corrupt(10000)
		rec := call(t, h.urls[0], "POST", "/wallets/"+w+"/reconciliation", h.internal, "", nil, 200)
		if rec["consistent"] != false || rec["difference"].(map[string]any)["amount"] != "-0.01" {
			t.Fatal(rec)
		}
		balance := call(t, h.urls[0], "GET", "/wallets/"+w, h.internal, "", nil, 200)
		if amount(balance) != "99.99" {
			t.Fatal("reconciliation repaired data", balance)
		}
		if !strings.Contains(metricsText(t, h, 0), "wallet_reconciliation_divergences_total 1") {
			t.Fatal("missing divergence metric")
		}
	})

	t.Run("CorrelationAndImmutableEventSnapshot", func(t *testing.T) {
		w, p := h.wallet(t, "100")
		op := operation(w, p, uuid.NewString(), "BET", "1")
		key := uuid.NewString()
		correlation := "test-" + uuid.NewString()
		raw, _ := json.Marshal(op)
		req, _ := http.NewRequest("POST", h.urls[0]+"/wagering/transactions", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+h.provider)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("X-Correlation-ID", correlation)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || res.Header.Get("X-Correlation-ID") != correlation {
			t.Fatal(res.StatusCode)
		}
		assertSQLCount(t, h, "SELECT count(*) FROM outbox WHERE aggregate_id=$1 AND payload->>'correlationId'=$2", 2, w, correlation)
		call(t, h.urls[2], "POST", "/wagering/transactions", h.provider, key, op, 200)
		assertSQLCount(t, h, "SELECT count(*) FROM outbox WHERE aggregate_id=$1", 4, w)
		c := sqsClient(t, "provider-a")
		q := queueURL(t, c, "wager-transactions.fifo")
		msg := uuid.NewString()
		body := envelope(operation(w, p, uuid.NewString(), "LOSS", "0"), uuid.NewString(), msg)
		send(t, c, q, w, body)
		eventually(t, 45*time.Second, func() bool {
			var n int
			err := h.db.QueryRow(context.Background(), "SELECT count(*) FROM outbox WHERE aggregate_id=$1 AND payload->>'causationId'=$2 AND payload->>'correlationId'=$2", w, msg).Scan(&n)
			return err == nil && n == 1
		})
	})
	t.Run("ParallelIndependentWallets", func(t *testing.T) {
		type wallet struct{ id, player string }
		wallets := make([]wallet, 8)
		for i := range wallets {
			wallets[i].id, wallets[i].player = h.wallet(t, "100")
		}
		var wg sync.WaitGroup
		for i, w := range wallets {
			for j := 0; j < 10; j++ {
				wg.Add(1)
				go func(i int, w wallet) {
					defer wg.Done()
					code, out, err := request(h.urls[i%3], "POST", "/wagering/transactions", h.provider, uuid.NewString(), operation(w.id, w.player, uuid.NewString(), "BET", "1"))
					if err != nil || code != 200 {
						t.Errorf("%d %v %v", code, out, err)
					}
				}(i, w)
			}
		}
		wg.Wait()
		for _, w := range wallets {
			out := call(t, h.urls[0], "GET", "/wallets/"+w.id, h.internal, "", nil, 200)
			if amount(out) != "90.00" {
				t.Fatal(out)
			}
			rec := call(t, h.urls[1], "POST", "/wallets/"+w.id+"/reconciliation", h.internal, "", nil, 200)
			if rec["consistent"] != true {
				t.Fatal(rec)
			}
		}
	})
	t.Run("AllProcessesRestart", func(t *testing.T) {
		w, p := h.wallet(t, "100")
		betID := uuid.NewString()
		bet := operation(w, p, betID, "BET", "25")
		key := uuid.NewString()
		call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, key, bet, 200)
		futureID := uuid.NewString()
		pending := operation(w, p, uuid.NewString(), "REFUND", "10")
		pending["referenceExternalTransactionId"] = futureID
		result := call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, uuid.NewString(), pending, 202)
		h.stop()
		h.restart()
		out := call(t, h.urls[2], "POST", "/wagering/transactions", h.provider, key, bet, 200)
		if out["idempotentReplay"] != true || amount(out) != "75.00" {
			t.Fatal(out)
		}
		call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), operation(w, p, futureID, "BET", "10"), 200)
		eventually(t, 10*time.Second, func() bool {
			var status string
			err := h.db.QueryRow(context.Background(), "SELECT status FROM wager_transactions WHERE id=$1", result["transactionId"]).Scan(&status)
			return err == nil && status == "PROCESSED"
		})
		rec := call(t, h.urls[1], "POST", "/wallets/"+w+"/reconciliation", h.internal, "", nil, 200)
		if rec["consistent"] != true {
			t.Fatal(rec)
		}
	})
}

func TestPermanentFailureAndPoisonMessages(t *testing.T) {
	h := setup(t)
	w, p := h.wallet(t, "100")
	ctx := context.Background()
	// A wallet-specific server error, after the transaction row has been updated,
	// proves rollback of all financial work before the independent FAILED audit.
	fn := "test_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	sql := fmt.Sprintf("CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.wallet_id='%s'::uuid THEN RAISE EXCEPTION 'test permanent failure' USING ERRCODE='0A000'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON wallet_ledger FOR EACH ROW EXECUTE FUNCTION %s()", fn, w, fn, fn)
	if _, err := h.db.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := h.db.Exec(ctx, "DROP TRIGGER "+fn+" ON wallet_ledger; DROP FUNCTION "+fn+"()"); err != nil {
			t.Error(err)
		}
	}()
	op := operation(w, p, uuid.NewString(), "BET", "1")
	key := uuid.NewString()
	out := call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, key, op, 500)
	if out["status"] != "FAILED" || out["failureCode"] != "INFRASTRUCTURE_PERMANENT" {
		t.Fatal(out)
	}
	replay := call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, key, op, 500)
	if replay["idempotentReplay"] != true {
		t.Fatal(replay)
	}
	ref := operation(w, p, uuid.NewString(), "REFUND", "1")
	ref["referenceExternalTransactionId"] = op["externalTransactionId"]
	rejected := call(t, h.urls[2], "POST", "/wagering/transactions", h.provider, uuid.NewString(), ref, 422)
	if rejected["failureCode"] != "REFERENCE_UNSUCCESSFUL" {
		t.Fatal(rejected)
	}
	c := sqsClient(t, "provider-a")
	q := queueURL(t, c, "wager-transactions.fifo")
	root := sqsClient(t, "root")
	dlq := queueURL(t, root, "wager-transactions-dlq.fifo")
	id := uuid.NewString()
	failedOp := operation(w, p, uuid.NewString(), "BET", "2")
	bodies := [][]byte{envelope(failedOp, uuid.NewString(), id)}
	for _, kind := range []string{"OPENING", "BET"} {
		invalid := operation(w, p, uuid.NewString(), kind, "1")
		if kind == "BET" {
			invalid["providerId"] = "provider-b"
		}
		bodies = append(bodies, envelope(invalid, uuid.NewString(), uuid.NewString()))
	}
	// Separate FIFO groups allow invalid messages to reach their retry limits concurrently.
	for i, body := range bodies {
		send(t, c, q, fmt.Sprintf("%s-%d", w, i), body)
	}
	eventually(t, 45*time.Second, func() bool {
		var n int
		err := h.db.QueryRow(ctx, "SELECT count(*) FROM inbox i JOIN wager_transactions t ON t.external_id=$2 WHERE i.message_id=$1 AND t.status='FAILED'", id, failedOp["externalTransactionId"]).Scan(&n)
		return err == nil && n == 1
	})
	found := map[string]bool{}
	eventually(t, 60*time.Second, func() bool {
		r, err := root.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: &dlq, MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		if err != nil {
			return false
		}
		for _, m := range r.Messages {
			for _, body := range bodies {
				if aws.ToString(m.Body) == string(body) {
					found[string(body)] = true
				}
			}
			root.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: &dlq, ReceiptHandle: m.ReceiptHandle})
		}
		return len(found) == len(bodies)
	})
	assertSQLCount(t, h, "SELECT count(*) FROM wager_transactions WHERE wallet_id=$1 AND status='FAILED'", 2, w)
	assertSQLCount(t, h, "SELECT count(*) FROM wallet_ledger WHERE wallet_id=$1", 1, w)
	assertSQLCount(t, h, "SELECT count(*) FROM outbox WHERE aggregate_id=$1", 3, w) // opening pair + reference rejection
	balance := call(t, h.urls[2], "GET", "/wallets/"+w, h.internal, "", nil, 200)
	if amount(balance) != "100.00" {
		t.Fatal(balance)
	}
}
