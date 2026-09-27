//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var client = &http.Client{Timeout: 12 * time.Second}

type harness struct {
	urls                               []string
	internal, provider, other, metrics string
	db                                 *pgxpool.Pool
	stop                               func()
	restart                            func()
}

func setup(t *testing.T, failpoints ...string) *harness {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("run scripts/integration.sh after make infra")
	}
	temp := t.TempDir()
	if len(failpoints) > 0 {
		t.Cleanup(func() {
			if _, err := os.Stat(filepath.Join(temp, failpoints[0])); err != nil {
				t.Errorf("failpoint was not exercised: %s", failpoints[0])
			}
		})
	}
	binary := filepath.Join(temp, "server")
	cmd := exec.Command("go", "build", "-race", "-tags=failpoints", "-o", binary, "./cmd/server")
	cmd.Dir = "../.."
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build: %s: %v", out, e)
	}
	h := &harness{internal: token(t, "wallet-internal"), provider: token(t, "provider-a"), other: token(t, "provider-b"), metrics: token(t, "metrics-reader")}
	var err error
	h.db, err = pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.db.Close)
	var stops []func()
	h.stop = func() {
		for _, stop := range stops {
			stop()
		}
		stops = nil
	}
	h.restart = func() {
		h.urls = nil
		for i := 0; i < 3; i++ {
			l, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			addr := l.Addr().String()
			l.Close()
			logPath := filepath.Join(temp, fmt.Sprintf("server-%d.log", i))
			log, e := os.Create(logPath)
			if e != nil {
				t.Fatal(e)
			}
			command := exec.Command(binary)
			command.Env = append(os.Environ(), "HTTP_ADDR="+addr)
			if len(failpoints) > 0 {
				command.Env = append(command.Env, "FAILPOINT="+failpoints[0], "FAILPOINT_DIR="+temp)
			}
			command.Stdout = log
			command.Stderr = log
			if e = command.Start(); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			var once sync.Once
			stop := func() {
				once.Do(func() {
					command.Process.Signal(os.Interrupt)
					select {
					case e := <-done:
						if e != nil {
							var exit *exec.ExitError
							if len(failpoints) == 0 || !errors.As(e, &exit) || exit.ExitCode() != 86 {
								t.Errorf("server shutdown: %v", e)
							}
						}
					case <-time.After(25 * time.Second):
						command.Process.Kill()
						t.Error("server did not stop")
					}
					log.Close()
					if t.Failed() {
						b, _ := os.ReadFile(logPath)
						t.Log(string(b))
					}
				})
			}
			stops = append(stops, stop)
			t.Cleanup(stop)
			base := "http://" + addr
			deadline := time.Now().Add(15 * time.Second)
			for {
				res, e := client.Get(base + "/health/ready")
				if e == nil {
					res.Body.Close()
					if res.StatusCode == 200 {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("server readiness timeout")
				}
				time.Sleep(100 * time.Millisecond)
			}
			h.urls = append(h.urls, base)
		}
	}
	h.restart()
	return h
}
func token(t *testing.T, id string) string {
	t.Helper()
	res, e := client.PostForm(os.Getenv("OIDC_ISSUER")+"/protocol/openid-connect/token", url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {id + "-local-secret"}})
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	var data struct {
		Token string `json:"access_token"`
	}
	if e = json.NewDecoder(res.Body).Decode(&data); e != nil || res.StatusCode != 200 || data.Token == "" {
		t.Fatal("cannot acquire local test token", res.StatusCode, e)
	}
	return data.Token
}
func request(base, method, path, token, key string, body any) (int, map[string]any, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	out := map[string]any{}
	err = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out, err
}
func call(t *testing.T, base, method, path, token, key string, body any, want int) map[string]any {
	t.Helper()
	code, out, e := request(base, method, path, token, key, body)
	if e != nil || code != want {
		t.Fatalf("%s %s: status=%d want=%d body=%v error=%v", method, path, code, want, out, e)
	}
	return out
}
func money(amount string) map[string]string {
	return map[string]string{"amount": amount, "currency": "BRL"}
}
func (h *harness) wallet(t *testing.T, amount string) (string, string) {
	t.Helper()
	player := uuid.NewString()
	out := call(t, h.urls[0], "POST", "/wallets", h.internal, "", map[string]any{"playerId": player, "initialBalance": money(amount)}, 201)
	return out["id"].(string), player
}
func operation(wallet, player, id, kind, amount string) map[string]any {
	return map[string]any{"providerId": "provider-a", "externalTransactionId": id, "playerId": player, "walletId": wallet, "roundId": "round-1", "gameId": "game-1", "kind": kind, "money": money(amount)}
}
func amount(out map[string]any) string { return out["balance"].(map[string]any)["amount"].(string) }
func TestService(t *testing.T) {
	h := setup(t)
	t.Run("AuthAndProviderIsolation", func(t *testing.T) {
		call(t, h.urls[0], "POST", "/wallets", "", "", nil, 401)
		call(t, h.urls[0], "POST", "/wallets", "invalid", "", nil, 401)
		call(t, h.urls[0], "POST", "/wallets", h.provider, "", nil, 403)
		wallet, player := h.wallet(t, "100")
		op := operation(wallet, player, uuid.NewString(), "BET", "25")
		call(t, h.urls[0], "POST", "/wagering/transactions", h.other, "key", op, 403)
		result := call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), op, 200)
		call(t, h.urls[1], "GET", "/wagering/transactions/"+result["transactionId"].(string), h.other, "", nil, 404)
		call(t, h.urls[1], "GET", "/providers/provider-a/wagering/transactions/"+op["externalTransactionId"].(string), h.other, "", nil, 403)
		call(t, h.urls[1], "GET", "/metrics", h.provider, "", nil, 403)
		req, _ := http.NewRequest("GET", h.urls[1]+"/metrics", nil)
		req.Header.Set("Authorization", "Bearer "+h.metrics)
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || !strings.Contains(string(b), "wallet_transactions_total") {
			t.Fatal("metrics not available")
		}
	})
	t.Run("Two80BetsAcrossProcesses", func(t *testing.T) {
		wallet, player := h.wallet(t, "100")
		start := make(chan struct{})
		codes := make([]int, 2)
		bodies := make([]map[string]any, 2)
		keys := []string{uuid.NewString(), uuid.NewString()}
		ops := []map[string]any{operation(wallet, player, uuid.NewString(), "BET", "80"), operation(wallet, player, uuid.NewString(), "BET", "80")}
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				var err error
				codes[i], bodies[i], err = request(h.urls[i], "POST", "/wagering/transactions", h.provider, keys[i], ops[i])
				if err != nil {
					t.Error(err)
				}
			}(i)
		}
		close(start)
		wg.Wait()
		counts := map[int]int{}
		for _, code := range codes {
			counts[code]++
		}
		if counts[200] != 1 || counts[422] != 1 {
			t.Fatal(counts)
		}
		for i := range codes {
			replay := call(t, h.urls[(i+1)%3], "POST", "/wagering/transactions", h.provider, keys[i], ops[i], codes[i])
			if replay["idempotentReplay"] != true || replay["transactionId"] != bodies[i]["transactionId"] || replay["status"] != bodies[i]["status"] || amount(replay) != amount(bodies[i]) || replay["failureCode"] != bodies[i]["failureCode"] {
				t.Fatal("replay changed original result", bodies[i], replay)
			}
		}
		out := call(t, h.urls[2], "GET", "/wallets/"+wallet, h.internal, "", nil, 200)
		if amount(out) != "20.00" {
			t.Fatal(out)
		}
		var debits int
		if e := h.db.QueryRow(context.Background(), "SELECT count(*) FROM wallet_ledger WHERE wallet_id=$1 AND direction='DEBIT'", wallet).Scan(&debits); e != nil || debits != 1 {
			t.Fatal(debits, e)
		}
		rec := call(t, h.urls[2], "POST", "/wallets/"+wallet+"/reconciliation", h.internal, "", nil, 200)
		if rec["consistent"] != true {
			t.Fatal(rec)
		}
	})
	t.Run("50DuplicatesAndHistoricalReplay", func(t *testing.T) {
		wallet, player := h.wallet(t, "100")
		id := uuid.NewString()
		key := uuid.NewString()
		op := operation(wallet, player, id, "BET", "25")
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make(chan map[string]any, 50)
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				code, out, e := request(h.urls[i%3], "POST", "/wagering/transactions", h.provider, key, op)
				if e != nil || code != 200 {
					t.Errorf("duplicate code=%d err=%v body=%v", code, e, out)
				}
				results <- out
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		original := 0
		ids := map[any]bool{}
		for out := range results {
			if out["idempotentReplay"] == false {
				original++
			}
			ids[out["transactionId"]] = true
		}
		if original != 1 || len(ids) != 1 {
			t.Fatal(original, ids)
		}
		call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, uuid.NewString(), operation(wallet, player, uuid.NewString(), "WIN", "10"), 200)
		replay := call(t, h.urls[2], "POST", "/wagering/transactions", h.provider, key, op, 200)
		if amount(replay) != "75.00" || replay["idempotentReplay"] != true {
			t.Fatal(replay)
		}
		call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), op, 200)
		op["money"] = money("26")
		call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, key, op, 409)
		out := call(t, h.urls[2], "GET", "/wallets/"+wallet, h.internal, "", nil, 200)
		if amount(out) != "85.00" {
			t.Fatal(out)
		}
		page := call(t, h.urls[2], "GET", "/wallets/"+wallet+"/ledger?limit=1", h.internal, "", nil, 200)
		if len(page["entries"].([]any)) != 1 || page["nextCursor"] == nil {
			t.Fatal(page)
		}
	})
	t.Run("ReversalPolicyAndLoss", func(t *testing.T) {
		wallet, player := h.wallet(t, "100")
		betID := uuid.NewString()
		call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), operation(wallet, player, betID, "BET", "25"), 200)
		refundID := uuid.NewString()
		refund := operation(wallet, player, refundID, "REFUND", "25")
		refund["referenceExternalTransactionId"] = betID
		call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, uuid.NewString(), refund, 200)
		rollback := operation(wallet, player, uuid.NewString(), "ROLLBACK", "25")
		rollback["referenceExternalTransactionId"] = betID
		out := call(t, h.urls[2], "POST", "/wagering/transactions", h.provider, uuid.NewString(), rollback, 422)
		if out["failureCode"] != "ALREADY_REVERSED" {
			t.Fatal(out)
		}
		rollback["externalTransactionId"] = uuid.NewString()
		rollback["referenceExternalTransactionId"] = refundID
		call(t, h.urls[2], "POST", "/wagering/transactions", h.provider, uuid.NewString(), rollback, 200)
		before := call(t, h.urls[0], "GET", "/wallets/"+wallet, h.internal, "", nil, 200)
		call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, uuid.NewString(), operation(wallet, player, uuid.NewString(), "LOSS", "0"), 200)
		after := call(t, h.urls[0], "GET", "/wallets/"+wallet, h.internal, "", nil, 200)
		if before["version"] != after["version"] || amount(after) != "75.00" {
			t.Fatal(before, after)
		}
	})
	t.Run("PendingReferenceRecovery", func(t *testing.T) {
		wallet, player := h.wallet(t, "100")
		betID := uuid.NewString()
		refund := operation(wallet, player, uuid.NewString(), "REFUND", "25")
		refund["referenceExternalTransactionId"] = betID
		out := call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, uuid.NewString(), refund, 202)
		call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, uuid.NewString(), operation(wallet, player, betID, "BET", "25"), 200)
		deadline := time.Now().Add(8 * time.Second)
		for {
			r := call(t, h.urls[2], "GET", "/wagering/transactions/"+out["transactionId"].(string), h.provider, "", nil, 200)
			if r["status"] == "PROCESSED" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal(r)
			}
			time.Sleep(100 * time.Millisecond)
		}
		balance := call(t, h.urls[2], "GET", "/wallets/"+wallet, h.internal, "", nil, 200)
		if amount(balance) != "100.00" {
			t.Fatal(balance)
		}
	})
	t.Run("DatabaseGuards", func(t *testing.T) {
		wallet, _ := h.wallet(t, "100")
		for _, sql := range []string{"UPDATE wallets SET player_id=gen_random_uuid() WHERE id=$1", "UPDATE wallets SET version=version+1 WHERE id=$1", "UPDATE wallet_ledger SET amount=amount WHERE wallet_id=$1", "DELETE FROM wallet_ledger WHERE wallet_id=$1", "UPDATE wallets SET balance=balance+1 WHERE id=$1", "UPDATE wager_transactions SET status='REJECTED',failure_code='tampered' WHERE wallet_id=$1"} {
			tx, e := h.db.Begin(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			_, e = tx.Exec(context.Background(), sql, wallet)
			if e == nil {
				e = tx.Commit(context.Background())
			}
			tx.Rollback(context.Background())
			if e == nil {
				t.Fatal("database accepted invalid mutation", sql)
			}
		}
	})
	t.Run("IndependentWallets", func(t *testing.T) {
		wallet, _ := h.wallet(t, "100")
		other, player := h.wallet(t, "100")
		tx, e := h.db.Begin(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(context.Background())
		if _, e = tx.Exec(context.Background(), "SELECT id FROM wallets WHERE id=$1 FOR UPDATE", wallet); e != nil {
			t.Fatal(e)
		}
		started := time.Now()
		call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, uuid.NewString(), operation(other, player, uuid.NewString(), "BET", "25"), 200)
		if time.Since(started) > 2*time.Second {
			t.Fatal("independent wallet stalled")
		}
	})
	t.Run("SQSAndHTTPIdempotency", func(t *testing.T) { testSQS(t, h) })
	t.Run("OutboxPublishedByConcurrentWorkers", func(t *testing.T) {
		wallet, _ := h.wallet(t, "50")
		deadline := time.Now().Add(10 * time.Second)
		for {
			var pending int
			e := h.db.QueryRow(context.Background(), "SELECT count(*) FROM outbox WHERE aggregate_id=$1 AND published_at IS NULL", wallet).Scan(&pending)
			if e != nil {
				t.Fatal(e)
			}
			if pending == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("outbox did not drain")
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
}

func TestMigrationRollback(t *testing.T) {
	ctx := context.Background()
	admin, e := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close(ctx)
	name := "migration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.Exec(ctx, "CREATE DATABASE "+name); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+name)
	u, e := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	u.Path = "/" + name
	for _, direction := range []string{"up", "down", "up"} {
		cmd := exec.Command("go", "run", "./cmd/migrate", direction)
		cmd.Dir = "../.."
		cmd.Env = append(os.Environ(), "DATABASE_URL="+u.String())
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%s: %s %v", direction, out, e)
		}
	}
}
