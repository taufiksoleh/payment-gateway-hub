// Command hub runs the payment-gateway-hub HTTP server.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/taufiksoleh/payment-gateway-hub/internal/app"
	"github.com/taufiksoleh/payment-gateway-hub/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, log)
	if err != nil {
		log.Error("startup failed", "error", err)
		os.Exit(1)
	}
	if err := a.Run(ctx); err != nil {
		log.Error("server stopped with error", "error", err)
		os.Exit(1)
	}
}
