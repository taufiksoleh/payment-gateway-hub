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
	"github.com/taufiksoleh/payment-gateway-hub/internal/idempotency"
	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/secret"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/postgres"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/httpapi"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
	"github.com/taufiksoleh/payment-gateway-hub/migrations"
)

// App is a fully wired hub.
type App struct {
	cfg         config.Config
	log         *slog.Logger
	server      *http.Server
	dispatcher  *notify.Dispatcher
	outbox      outbox.Store
	idempotency idempotency.Store
	db          *sql.DB // nil when running in memory
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
	a.idempotency = repos.idempotency
	a.outbox = repos.outbox

	// 3. Merchant webhooks: payment changes land in the outbox (written by
	//    the payment repository); the dispatcher delivers them with retries.
	a.dispatcher = notify.NewDispatcher(repos.outbox, repos.merchants, notify.NewSender(nil), log)

	// 4. Use cases.
	merchants := usecase.NewMerchantService(repos.merchants, repos.creds, gateways)
	payments := usecase.NewPaymentService(usecase.PaymentServiceDeps{
		Payments: repos.payments,
		Creds:    repos.creds,
		Gateways: gateways,
		Logger:   log,
	})

	// 5. HTTP.
	a.server = &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Payments:    payments,
			Merchants:   merchants,
			Providers:   gateways.Providers(),
			AdminToken:  cfg.AdminToken,
			Idempotency: repos.idempotency,
			Logger:      log,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second, // provider calls can be slow
		IdleTimeout:       120 * time.Second,
	}
	return a, nil
}

type repositories struct {
	payments    usecase.PaymentRepository
	merchants   usecase.MerchantRepository
	creds       usecase.CredentialRepository
	idempotency idempotency.Store
	outbox      outbox.Store
}

func (a *App) buildRepositories(ctx context.Context) (*repositories, error) {
	if a.cfg.DatabaseURL == "" {
		a.log.Warn("DATABASE_URL not set: using in-memory storage, data is lost on restart")
		ob := memory.NewOutbox(notify.EncodeEvent)
		return &repositories{
			payments:    memory.NewPaymentRepo(ob),
			merchants:   memory.NewMerchantRepo(),
			creds:       memory.NewCredentialRepo(),
			idempotency: memory.NewIdempotencyStore(),
			outbox:      ob,
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
		payments:    postgres.NewPaymentRepo(db, notify.EncodeEvent),
		merchants:   postgres.NewMerchantRepo(db),
		creds:       postgres.NewCredentialRepo(db, cipher),
		idempotency: postgres.NewIdempotencyStore(db),
		outbox:      postgres.NewOutboxStore(db),
	}, nil
}

// Run serves HTTP until ctx is cancelled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		a.dispatcher.Run(ctx)
	}()
	go a.housekeeping(ctx)

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
	// Let the dispatcher finish its current batch. Anything undelivered is
	// safe in the outbox and goes out after the next start.
	select {
	case <-dispatcherDone:
	case <-shutdownCtx.Done():
	}
	if a.db != nil {
		if cerr := a.db.Close(); cerr != nil {
			a.log.Warn("close database", "error", cerr)
		}
	}
	return err
}

// housekeeping deletes expired Idempotency-Key records and old delivered
// webhook messages every hour until ctx ends. It only keeps tables small;
// correctness never depends on it.
func (a *App) housekeeping(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.purge(ctx, "idempotency keys", func() (int64, error) {
				return a.idempotency.Purge(ctx, now.Add(-idempotency.KeyTTL))
			})
			a.purge(ctx, "delivered webhooks", func() (int64, error) {
				return a.outbox.Purge(ctx, now.Add(-outbox.DeliveredRetention))
			})
		}
	}
}

func (a *App) purge(ctx context.Context, what string, fn func() (int64, error)) {
	n, err := fn()
	if err != nil {
		a.log.WarnContext(ctx, "purge "+what, "error", err)
		return
	}
	if n > 0 {
		a.log.InfoContext(ctx, "purged "+what, "count", n)
	}
}
