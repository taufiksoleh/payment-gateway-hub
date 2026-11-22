// Package app is the composition root: the only place that knows every
// concrete type and wires them together. Read this file top to bottom to see
// how the whole hub fits.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/config"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/doku"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/midtrans"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/xendit"
	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/secret"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/postgres"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/httpapi"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
	"github.com/taufiksoleh/payment-gateway-hub/migrations"
)

// App is a fully wired hub.
type App struct {
	cfg      config.Config
	log      *slog.Logger
	server   *http.Server
	notifier *notify.Async
	db       *sql.DB // nil when running in memory
}

// New builds every dependency from cfg.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	a := &App{cfg: cfg, log: log}

	// 1. Gateways. To add a provider, add one line here.
	providerHTTP := &http.Client{Timeout: 30 * time.Second}
	gateways := gateway.NewRegistry(
		midtrans.New(midtrans.Options{HTTPClient: providerHTTP}),
		xendit.New(xendit.Options{HTTPClient: providerHTTP}),
		doku.New(doku.Options{HTTPClient: providerHTTP}),
	)

	// 2. Storage.
	repos, err := a.buildRepositories(ctx)
	if err != nil {
		return nil, err
	}

	// 3. Outbound merchant webhooks: 4 attempts, 2s/4s/6s apart.
	a.notifier = notify.NewAsync(notify.NewHTTPNotifier(nil), log, 4, 2*time.Second)

	// 4. Use cases.
	merchants := usecase.NewMerchantService(repos.merchants, repos.creds, gateways)
	payments := usecase.NewPaymentService(usecase.PaymentServiceDeps{
		Payments:  repos.payments,
		Merchants: repos.merchants,
		Creds:     repos.creds,
		Gateways:  gateways,
		Notifier:  a.notifier,
		Logger:    log,
	})

	// 5. HTTP.
	a.server = &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Payments:   payments,
			Merchants:  merchants,
			Providers:  gateways.Providers(),
			AdminToken: cfg.AdminToken,
			Logger:     log,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second, // provider calls can be slow
		IdleTimeout:       120 * time.Second,
	}
	return a, nil
}

type repositories struct {
	payments  usecase.PaymentRepository
	merchants usecase.MerchantRepository
	creds     usecase.CredentialRepository
}

func (a *App) buildRepositories(ctx context.Context) (*repositories, error) {
	if a.cfg.DatabaseURL == "" {
		a.log.Warn("DATABASE_URL not set: using in-memory storage, data is lost on restart")
		return &repositories{
			payments:  memory.NewPaymentRepo(),
			merchants: memory.NewMerchantRepo(),
			creds:     memory.NewCredentialRepo(),
		}, nil
	}

	cipher, err := secret.NewCipherFromBase64(a.cfg.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("ENCRYPTION_KEY: %w", err)
	}
	db, err := postgres.Open(ctx, a.cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := postgres.Migrate(ctx, db, migrations.FS); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	a.db = db
	return &repositories{
		payments:  postgres.NewPaymentRepo(db),
		merchants: postgres.NewMerchantRepo(db),
		creds:     postgres.NewCredentialRepo(db, cipher),
	}, nil
}

// Run serves HTTP until ctx is cancelled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		a.log.Info("http server listening", "addr", a.cfg.HTTPAddr)
		errCh <- a.server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	a.log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()

	err := a.server.Shutdown(shutdownCtx)
	a.notifier.Wait(shutdownCtx)
	if a.db != nil {
		if cerr := a.db.Close(); cerr != nil {
			a.log.Warn("close database", "error", cerr)
		}
	}
	return err
}
