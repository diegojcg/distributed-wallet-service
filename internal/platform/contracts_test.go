package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"
	"jungle-wallet-service/internal/domain"
	"jungle-wallet-service/internal/requestmeta"
	"jungle-wallet-service/internal/store"
)

func TestModuleGraphWithoutInfrastructure(t *testing.T) {
	if err := fx.ValidateApp(Module(), fx.NopLogger); err != nil {
		t.Fatal(err)
	}
}
func TestDecodeMessageContract(t *testing.T) {
	m, _ := domain.ParseMoney("1", "BRL")
	base := RequestEnvelope{MessageID: "message", Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC(), Data: RequestData{Operation: domain.Operation{ProviderID: "provider-a", ExternalID: "external", PlayerID: uuid.NewString(), WalletID: uuid.NewString(), RoundID: "round", GameID: "game", Kind: domain.Bet, Money: m}, IdempotencyKey: "key"}}
	raw, _ := json.Marshal(base)
	if _, hash, err := decodeMessage(string(raw), "provider-a"); err != nil || len(hash) != 64 {
		t.Fatal(hash, err)
	}
	for _, tc := range []struct {
		name     string
		body     string
		provider string
	}{
		{"extra", string(raw[:len(raw)-1]) + `,"extra":true}`, "provider-a"},
		{"identity", string(raw), "provider-b"},
		{"missing key", strings.Replace(string(raw), `"idempotencyKey":"key"`, `"idempotencyKey":""`, 1), "provider-a"},
		{"trailing", string(raw) + ` {}`, "provider-a"},
		{"opening", strings.Replace(string(raw), `"BET"`, `"OPENING"`, 1), "provider-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := decodeMessage(tc.body, tc.provider); err == nil {
				t.Fatal("invalid message accepted")
			}
		})
	}
}
func TestHTTPErrorContractAndDiagnosticIDs(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrNotFound, 404, "NOT_FOUND"}, {store.ErrConflict, 409, "CONFLICT"}, {store.ErrOwnership, 422, "WALLET_OWNER_MISMATCH"},
		{domain.ErrInvalidOperation, 400, "INVALID_INPUT"}, {domain.ErrOverflow, 400, "INVALID_INPUT"},
		{context.DeadlineExceeded, 503, "TEMPORARILY_UNAVAILABLE"}, {errors.New("secret internal detail"), 503, "TEMPORARILY_UNAVAILABLE"},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			var logs bytes.Buffer
			r := httptest.NewRequest("POST", "/wagering/transactions", nil)
			r = r.WithContext(requestmeta.With(r.Context(), requestmeta.Metadata{CorrelationID: "corr", WalletID: "wallet", ProviderID: "provider", TransactionID: "tx"}))
			w := httptest.NewRecorder()
			handleError(w, r, tc.err, slog.New(slog.NewJSONHandler(&logs, nil)))
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || body["code"] != tc.code {
				t.Fatal(w.Code, body)
			}
			if strings.Contains(w.Body.String()+logs.String(), "secret internal detail") {
				t.Fatal("internal error leaked")
			}
			if tc.status == 503 {
				if w.Header().Get("Retry-After") != "1" {
					t.Fatal(w.Header())
				}
				var fields map[string]any
				if err := json.Unmarshal(logs.Bytes(), &fields); err != nil {
					t.Fatal(err)
				}
				for key, value := range map[string]string{"correlationId": "corr", "walletId": "wallet", "providerId": "provider", "transactionId": "tx"} {
					if fields[key] != value {
						t.Fatal(fields)
					}
				}
			}
		})
	}
}
