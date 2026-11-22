// Package app wires the concrete implementations together. It is the only
// package that knows them all.
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
	"github.com/taufiksoleh/payment-gateway-hub/internal/metrics"
	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/ratelimit"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/secret"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/postgres"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/httpapi"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
	"github.com/taufiksoleh/payment-gateway-hub/migrations"
)

type App struct {
	cfg           config.Config
	log           *slog.Logger
	server        *http.Server
	metricsServer *http.Server // nil when METRICS_ADDR=off
	dispatcher    *notify.Dispatcher
	repos         *repositories
	db            *sql.DB // nil with in-memory storage
}

func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	a := &App{cfg: cfg, log: log}
	m := metrics.New()

	providerClient := gateway.DefaultHTTPClient()
	gateways := gateway.NewRegistry(
		m.Gateway(midtrans.New(midtrans.Options{
			HTTPClient: providerClient,
			Endpoints:  gateway.Endpoints{Sandbox: cfg.MidtransSandboxURL},
		})),
		m.Gateway(xendit.New(xendit.Options{HTTPClient: providerClient, BaseURL: cfg.XenditBaseURL})),
		m.Gateway(doku.New(doku.Options{
			HTTPClient: providerClient,
			Endpoints:  gateway.Endpoints{Sandbox: cfg.DOKUSandboxURL},
		})),
	)

	repos, err := a.openStorage(ctx)
	if err != nil {
		return nil, err
	}
	a.repos = repos
	if br, ok := repos.outbox.(outbox.BacklogReporter); ok {
		m.WatchBacklog(br, time.Now)
	}
	a.dispatcher = notify.NewDispatcher(m.Outbox(repos.outbox), repos.merchants, notify.NewSender(nil), log)

	router := httpapi.NewRouter(httpapi.Deps{
		Payments: usecase.NewPaymentService(usecase.PaymentServiceDeps{
			Payments: m.Payments(repos.payments),
			Creds:    repos.creds,
			Gateways: gateways,
			Logger:   log,
		}),
		Merchants: usecase.NewMerchantService(usecase.MerchantServiceDeps{
			Merchants: repos.merchants,
			Keys:      repos.apiKeys,
			Creds:     repos.creds,
			Gateways:  gateways,
		}),
		Webhooks:    usecase.NewWebhookService(repos.webhooks),
		Providers:   gateways.Providers(),
		AdminToken:  cfg.AdminToken,
		Idempotency: repos.idempotency,
		RateLimits:  rateLimits(cfg, m),
		Logger:      log,
		Instrument:  m.HTTP,
	})

	a.server = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second, // provider calls can be slow
		IdleTimeout:       120 * time.Second,
	}

	if cfg.MetricsAddr != "off" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", m.Handler())
		a.metricsServer = &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	}
	return a, nil
}

type repositories struct {
	payments    usecase.PaymentRepository
	merchants   usecase.MerchantRepository
	apiKeys     usecase.APIKeyRepository
	creds       usecase.CredentialRepository
	webhooks    usecase.WebhookRepository
	outbox      outbox.Store
	idempotency idempotency.Store
}

func (a *App) openStorage(ctx context.Context) (*repositories, error) {
	if a.cfg.DatabaseURL == "" {
		a.log.Warn("DATABASE_URL not set: using in-memory storage, data is lost on restart")
		ob := memory.NewOutbox(notify.EncodeEvent)
		return &repositories{
			payments:    memory.NewPaymentRepo(ob),
			merchants:   memory.NewMerchantRepo(),
			apiKeys:     memory.NewAPIKeyRepo(),
			creds:       memory.NewCredentialRepo(),
			webhooks:    ob,
			outbox:      ob,
			idempotency: memory.NewIdempotencyStore(),
		}, nil
	}

	cipher, err := secret.NewCipherFromBase64(a.cfg.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("ENCRYPTION_KEY: %w", err)
	}
	db, err := postgres.Open(ctx, a.cfg.DatabaseURL, postgres.PoolConfig{
		MaxOpenConns: a.cfg.DBMaxOpenConns,
		MaxIdleConns: a.cfg.DBMaxIdleConns,
	})
	if err != nil {
		return nil, err
	}
	if err := postgres.Migrate(ctx, db, migrations.FS); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	a.db = db

	ob := postgres.NewOutboxStore(db)
	return &repositories{
		payments:    postgres.NewPaymentRepo(db, notify.EncodeEvent),
		merchants:   postgres.NewMerchantRepo(db),
		apiKeys:     postgres.NewAPIKeyRepo(db),
		creds:       postgres.NewCredentialRepo(db, cipher),
		webhooks:    ob,
		outbox:      ob,
		idempotency: postgres.NewIdempotencyStore(db),
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

	errCh := make(chan error, 2)
	go func() {
		a.log.Info("http server listening", "addr", a.cfg.HTTPAddr)
		errCh <- a.server.ListenAndServe()
	}()
	if a.metricsServer != nil {
		go func() {
			a.log.Info("metrics server listening", "addr", a.cfg.MetricsAddr)
			errCh <- a.metricsServer.ListenAndServe()
		}()
	}

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
	if a.metricsServer != nil {
		_ = a.metricsServer.Shutdown(shutdownCtx)
	}
	select { // undelivered webhooks stay in the outbox for the next start
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

// housekeeping purges expired idempotency keys and old delivered webhooks
// hourly. Correctness does not depend on it.
func (a *App) housekeeping(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.purge(ctx, "idempotency keys", func() (int64, error) {
				return a.repos.idempotency.Purge(ctx, now.Add(-idempotency.KeyTTL))
			})
			a.purge(ctx, "delivered webhooks", func() (int64, error) {
				return a.repos.outbox.Purge(ctx, now.Add(-outbox.DeliveredRetention))
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

func rateLimits(cfg config.Config, m *metrics.Metrics) httpapi.RateLimits {
	limiter := func(perMinute int, scope string) ratelimit.Limiter {
		if perMinute == 0 {
			return nil // disabled
		}
		return m.Limiter(ratelimit.NewTokenBucket(perMinute, ratelimit.BurstFor(perMinute)), scope)
	}
	return httpapi.RateLimits{
		PerIP:       limiter(cfg.RateLimitIPPerMin, "ip"),
		PerMerchant: limiter(cfg.RateLimitMerchantPerMin, "merchant"),
		Admin:       limiter(cfg.RateLimitAdminPerMin, "admin"),
		TrustProxy:  cfg.TrustProxy,
	}
}
