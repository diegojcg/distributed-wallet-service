package platform

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"
	"io"
	"jungle-wallet-service/internal/auth"
	"jungle-wallet-service/internal/domain"
	"jungle-wallet-service/internal/requestmeta"
	"jungle-wallet-service/internal/store"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"
)

type HTTPServer struct {
	server   *http.Server
	Listener net.Listener
	done     chan error
}

func NewHTTP(lc fx.Lifecycle, c Config, shutdown fx.Shutdowner, workers *Workers, db *pgxpool.Pool, broker *Broker, verifier *auth.Verifier, s *store.Store, metrics *Metrics, registry *prometheus.Registry, logger *slog.Logger) *HTTPServer {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "live"}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if db.Ping(ctx) != nil || broker.Ready(ctx) != nil {
			writeJSON(w, 503, map[string]string{"status": "not_ready"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	protect := func(role string, handler func(http.ResponseWriter, *http.Request, auth.Identity)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			identity, err := verifier.Authenticate(r.Context(), r.Header.Get("Authorization"))
			if err != nil {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeError(w, 401, "UNAUTHENTICATED")
				return
			}
			if identity.Role != role {
				writeError(w, 403, "FORBIDDEN")
				return
			}
			handler(w, r, identity)
		}
	}
	mux.Handle("GET /metrics", protect("metrics", func(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
		promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
	}))
	mux.Handle("POST /wallets", protect("internal", func(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
		var input struct {
			PlayerID       string       `json:"playerId"`
			InitialBalance domain.Money `json:"initialBalance"`
		}
		if !decode(w, r, &input) {
			return
		}
		result, err := s.Open(r.Context(), input.PlayerID, input.InitialBalance)
		if err != nil {
			handleError(w, err, logger)
			return
		}
		writeJSON(w, 201, result)
	}))
	mux.Handle("GET /wallets/{id}", protect("internal", func(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
		if !validID(w, r) {
			return
		}
		result, err := s.Wallet(r.Context(), r.PathValue("id"))
		if err != nil {
			handleError(w, err, logger)
			return
		}
		writeJSON(w, 200, result)
	}))
	mux.Handle("GET /wallets/{id}/ledger", protect("internal", func(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
		if !validID(w, r) {
			return
		}
		limit := 50
		var err error
		if value := r.URL.Query().Get("limit"); value != "" {
			limit, err = strconv.Atoi(value)
			if err != nil {
				writeError(w, 400, "INVALID_LIMIT")
				return
			}
		}
		result, err := s.Ledger(r.Context(), r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			handleError(w, err, logger)
			return
		}
		writeJSON(w, 200, result)
	}))
	mux.Handle("POST /wallets/{id}/reconciliation", protect("internal", func(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
		if !validID(w, r) {
			return
		}
		result, err := s.Reconcile(r.Context(), r.PathValue("id"))
		if err != nil {
			handleError(w, err, logger)
			return
		}
		if !result.Consistent {
			metrics.Divergences.Inc()
			logger.Error("reconciliation divergence", "walletId", result.WalletID)
		}
		writeJSON(w, 200, result)
	}))
	mux.Handle("POST /wagering/transactions", protect("provider", func(w http.ResponseWriter, r *http.Request, identity auth.Identity) {

		var input domain.Operation
		if !decode(w, r, &input) {
			return
		}
		if err := input.Validate(); err != nil {
			handleError(w, err, logger)
			return
		}
		if input.ProviderID != identity.ProviderID {
			writeError(w, 403, "PROVIDER_MISMATCH")
			return
		}
		result, err := s.Apply(r.Context(), input, r.Header.Get("Idempotency-Key"))
		if err != nil {
			handleError(w, err, logger)
			return
		}
		code := 200
		if result.Status == domain.Failed {
			code = 500
		}
		if result.Status == domain.Rejected {
			code = 422
		}
		if result.Status == domain.Pending || result.Status == domain.PendingReference {
			code = 202
		}
		logger.Info("transaction result", "correlationId", w.Header().Get("X-Correlation-ID"), "transactionId", result.TransactionID, "walletId", input.WalletID, "providerId", identity.ProviderID, "status", result.Status, "replay", result.Replay, "failureCode", result.FailureCode)
		writeJSON(w, code, result)
	}))
	mux.Handle("GET /wagering/transactions/{id}", protect("provider", func(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
		if !validID(w, r) {
			return
		}
		result, err := s.Transaction(r.Context(), identity.ProviderID, r.PathValue("id"), false)
		if err != nil {
			handleError(w, err, logger)
			return
		}
		writeJSON(w, 200, result)
	}))
	mux.Handle("GET /providers/{provider}/wagering/transactions/{id}", protect("provider", func(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
		if r.PathValue("provider") != identity.ProviderID {
			writeError(w, 403, "PROVIDER_MISMATCH")
			return
		}
		result, err := s.Transaction(r.Context(), identity.ProviderID, r.PathValue("id"), true)
		if err != nil {
			handleError(w, err, logger)
			return
		}
		writeJSON(w, 200, result)
	}))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlation := r.Header.Get("X-Correlation-ID")
		if correlation == "" {
			correlation = uuid.NewString()
		}
		if !requestmeta.ValidID(correlation, 128) {
			writeError(w, 400, "INVALID_CORRELATION_ID")
			return
		}
		w.Header().Set("X-Correlation-ID", correlation)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		ctx = requestmeta.With(ctx, requestmeta.Metadata{CorrelationID: correlation, Transport: "http"})
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	h := &HTTPServer{server: &http.Server{Addr: c.HTTPAddr, Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}, done: make(chan error, 1)}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		var err error
		h.Listener, err = net.Listen("tcp", c.HTTPAddr)
		if err != nil {
			return err
		}
		go func() {
			err := h.server.Serve(h.Listener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			h.done <- err
			if err != nil {
				logger.Error("HTTP serve failed")
				_ = shutdown.Shutdown(fx.ExitCode(1))
			}
		}()
		logger.Info("http started", "address", h.Listener.Addr().String())
		return nil
	}, OnStop: func(ctx context.Context) error {
		workers.cancel()
		err := h.server.Shutdown(ctx)
		if err != nil {
			h.server.Close()
		}
		select {
		case serveErr := <-h.done:
			if err == nil {
				err = serveErr
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		return err
	}})
	return h
}
func validID(w http.ResponseWriter, r *http.Request) bool {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil || id == uuid.Nil {
		writeError(w, 400, "INVALID_ID")
		return false
	}
	r.SetPathValue("id", id.String())
	return true
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeError(w, 400, "INVALID_INPUT")
		return false
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, 400, "INVALID_INPUT")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"code": code})
}
func handleError(w http.ResponseWriter, err error, logger *slog.Logger) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "NOT_FOUND")
	case errors.Is(err, store.ErrConflict):
		writeError(w, 409, "CONFLICT")
	case errors.Is(err, store.ErrOwnership):
		writeError(w, 422, "WALLET_OWNER_MISMATCH")
	case errors.Is(err, domain.ErrInvalidMoney), errors.Is(err, domain.ErrInvalidOperation), errors.Is(err, domain.ErrInvalidState), errors.Is(err, domain.ErrCurrency), errors.Is(err, domain.ErrOverflow):
		writeError(w, 400, "INVALID_INPUT")
	default:
		logger.Error("operation unavailable", "errorType", fmtErrorType(err))
		w.Header().Set("Retry-After", "1")
		writeError(w, 503, "TEMPORARILY_UNAVAILABLE")
	}
}
func fmtErrorType(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return "postgres:" + pg.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	return "infrastructure"
}
