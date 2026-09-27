package platform

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"jungle-wallet-service/internal/auth"
	"jungle-wallet-service/internal/requestmeta"
	"jungle-wallet-service/internal/store"
	"log/slog"
	"os"
	"time"
)

func NewPool(lc fx.Lifecycle, c Config) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if err := db.Ping(ctx); err != nil {
			db.Close()
			return err
		}
		return nil
	}, OnStop: func(context.Context) error { db.Close(); return nil }})
	return db, nil
}
func NewAuth(lc fx.Lifecycle, c Config) *auth.Verifier {
	v := auth.New(c.Issuer, c.JWKSURL, c.Audience)
	lc.Append(fx.Hook{OnStart: v.Ready, OnStop: func(context.Context) error { v.Close(); return nil }})
	return v
}
func Module() fx.Option {
	return fx.Module("wallet-service",
		fx.Provide(ReadConfig, NewPool, NewBroker, NewAuth, func(db *pgxpool.Pool, m *Metrics, logger *slog.Logger) *store.Store {
			return store.NewObserved(db, func(ctx context.Context, r store.Result, err error, elapsed time.Duration) {
				m.Observe(ctx, r, err, elapsed)
				meta := requestmeta.From(ctx)
				if meta.Transport == "reference" && !r.Replay {
					logger.Info("reference retry result", "correlationId", meta.CorrelationID, "messageId", meta.CausationID, "transactionId", r.TransactionID, "walletId", meta.WalletID, "providerId", meta.ProviderID, "status", r.Status, "failureCode", r.FailureCode, "failed", err != nil)
				}
			})
		}, NewHTTP, NewWorkers, NewMetrics, prometheus.NewRegistry, func() *slog.Logger { return slog.New(slog.NewJSONHandler(os.Stdout, nil)) }),
		fx.Invoke(func(*Workers) {}, func(*HTTPServer) {}),
	)
}
func NewApp(options ...fx.Option) *fx.App {
	defaults := []fx.Option{Module(), fx.StartTimeout(30 * time.Second), fx.StopTimeout(20 * time.Second), fx.WithLogger(func(l *slog.Logger) fxevent.Logger { return &fxevent.SlogLogger{Logger: l} })}
	return fx.New(append(defaults, options...)...)
}
