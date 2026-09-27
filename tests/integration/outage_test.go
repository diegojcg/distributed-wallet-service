//go:build integration

package integration

import (
	"context"
	"github.com/google/uuid"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func compose(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compose %v: %v: %s", args, err, out)
	}
}
func metricsText(t *testing.T, h *harness, index int) string {
	t.Helper()
	req, _ := http.NewRequest("GET", h.urls[index]+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+h.metrics)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != 200 {
		t.Fatal(res.StatusCode, err)
	}
	return string(raw)
}
func TestDependencyOutages(t *testing.T) {
	h := setup(t)
	w, p := h.wallet(t, "100")
	t.Run("PostgresUnavailableIsTransient", func(t *testing.T) {
		compose(t, "stop", "postgres")
		stopped := true
		defer func() {
			if stopped {
				compose(t, "up", "-d", "--wait", "postgres")
			}
		}()
		call(t, h.urls[0], "GET", "/health/ready", "", "", nil, 503)
		op := operation(w, p, uuid.NewString(), "BET", "10")
		key := uuid.NewString()
		call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, key, op, 503)
		compose(t, "up", "-d", "--wait", "postgres")
		stopped = false
		call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, key, op, 200)
		replay := call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, key, op, 200)
		if replay["idempotentReplay"] != true || amount(replay) != "90.00" {
			t.Fatal(replay)
		}
		assertSQLCount(t, h, "SELECT count(*) FROM wager_transactions WHERE wallet_id=$1 AND status='FAILED'", 0, w)
	})
	t.Run("BrokerUnavailableKeepsCommittedEvents", func(t *testing.T) {
		compose(t, "stop", "broker")
		stopped := true
		defer func() {
			if stopped {
				compose(t, "up", "-d", "--wait", "broker")
			}
		}()
		call(t, h.urls[0], "GET", "/health/ready", "", "", nil, 503)
		op := operation(w, p, uuid.NewString(), "BET", "5")
		result := call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, uuid.NewString(), op, 200)
		transaction := result["transactionId"].(string)
		assertSQLCount(t, h, "SELECT count(*) FROM outbox WHERE payload->'data'->>'transactionId'=$1 AND published_at IS NULL", 2, transaction)
		eventually(t, 10*time.Second, func() bool {
			var n int
			err := h.db.QueryRow(context.Background(), "SELECT count(*) FROM outbox WHERE payload->'data'->>'transactionId'=$1 AND attempts>0", transaction).Scan(&n)
			return err == nil && n > 0
		})
		// Both data and stable event identities survive the broker outage and process restart.
		h.stop()
		compose(t, "up", "-d", "--wait", "broker")
		stopped = false
		h.restart()
		eventually(t, 45*time.Second, func() bool {
			var n int
			err := h.db.QueryRow(context.Background(), "SELECT count(*) FROM outbox WHERE payload->'data'->>'transactionId'=$1 AND published_at IS NOT NULL", transaction).Scan(&n)
			return err == nil && n == 2
		})
		assertSQLCount(t, h, "SELECT count(*) FROM outbox WHERE payload->'data'->>'transactionId'=$1", 2, transaction)
		rec := call(t, h.urls[2], "POST", "/wallets/"+w+"/reconciliation", h.internal, "", nil, 200)
		if rec["consistent"] != true {
			t.Fatal(rec)
		}
		eventually(t, 8*time.Second, func() bool {
			return strings.Contains(metricsText(t, h, 0), `wallet_dlq_messages{provider="provider-a"}`)
		})
		for _, name := range []string{"wallet_transactions_total", "wallet_idempotent_replays_total", "wallet_retries_total", "wallet_concurrency_conflicts_total", "wallet_outbox_oldest_seconds", "wallet_outbox_pending", "wallet_processing_seconds", "wallet_reconciliation_divergences_total"} {
			if !strings.Contains(metricsText(t, h, 0), name) {
				t.Fatal("missing metric", name)
			}
		}
	})
}
