// Command hub runs the payment-gateway-hub HTTP server.
//
// hub -healthcheck exits 0 if the local server answers /healthz; container
// health checks use it because the runtime image has no shell or curl.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/app"
	"github.com/taufiksoleh/payment-gateway-hub/internal/config"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local server's /healthz and exit")
	flag.Parse()
	if *healthcheck {
		os.Exit(probe(os.Getenv("HTTP_ADDR")))
	}

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

func probe(addr string) int {
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthz answered", resp.Status)
		return 1
	}
	return 0
}
