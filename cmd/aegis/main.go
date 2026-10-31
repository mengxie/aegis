// Command aegis runs the AI governance gateway.
//
//	aegis serve [-config aegis.json]   run the gateway
//	aegis new-key                      create a team API key and its config entry
//	aegis mock-upstream [-listen :9090] run an OpenAI-compatible mock provider
//	aegis healthcheck [-url ...]       probe a running gateway (for container health checks)
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mengxie/aegis/internal/config"
	"github.com/mengxie/aegis/internal/gateway"
	"github.com/mengxie/aegis/internal/mock"
	"github.com/mengxie/aegis/internal/usage"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "aegis:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "serve":
		return serve(args)
	case "new-key":
		return newKey()
	case "mock-upstream":
		return mockUpstream(args)
	case "healthcheck":
		return healthcheck(args)
	default:
		return fmt.Errorf("unknown command %q (use serve, new-key, mock-upstream or healthcheck)", command)
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	path := flags.String("config", envOr("AEGIS_CONFIG", "aegis.json"), "path to the policy config file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(envOr("AEGIS_LOG_LEVEL", "info")))
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(*path, os.LookupEnv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var store usage.Store = usage.NewMemoryStore()
	if dsn := os.Getenv("AEGIS_DATABASE_URL"); dsn != "" {
		pg, err := usage.OpenPostgres(ctx, dsn)
		if err != nil {
			return err
		}
		defer pg.Close()
		store = pg
	} else {
		logger.Warn("AEGIS_DATABASE_URL is not set: budgets and the audit log are in memory and per instance")
	}

	upstreamClient := &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment, MaxIdleConnsPerHost: 100, IdleConnTimeout: 90 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
	}}
	gw := gateway.New(gateway.Options{Config: cfg, Store: store, HTTPClient: upstreamClient, Logger: logger})
	server := &http.Server{Addr: cfg.Listen, Handler: gw.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}

	logger.Info("aegis listening", "addr", cfg.Listen, "models", len(cfg.Models), "teams", len(cfg.Teams))
	return listenUntil(ctx, server, logger)
}

// listenUntil serves until a signal, then drains: in-flight requests and streams get time to finish.
func listenUntil(ctx context.Context, server *http.Server, logger *slog.Logger) error {
	failed := make(chan error, 1)
	go func() { failed <- server.ListenAndServe() }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-failed; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newKey() error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	key := "aeg_" + base64.RawURLEncoding.EncodeToString(b)
	fmt.Printf("API key (give it to the team; it is shown only now):\n  %s\n\n", key)
	fmt.Printf("Add this to the team's \"keys\" in the config:\n  \"sha256:%s\"\n", config.HashKey(key))
	return nil
}

func mockUpstream(args []string) error {
	flags := flag.NewFlagSet("mock-upstream", flag.ContinueOnError)
	listen := flags.String("listen", ":9090", "address to listen on")
	if err := flags.Parse(args); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Info("mock provider listening", "addr", *listen)
	return listenUntil(ctx, &http.Server{Addr: *listen, Handler: &mock.Server{Log: logger}, ReadHeaderTimeout: 10 * time.Second}, logger)
}

func healthcheck(args []string) error {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	url := flags.String("url", "http://127.0.0.1:8080/healthz", "health endpoint")
	if err := flags.Parse(args); err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(*url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %s", resp.Status)
	}
	return nil
}

func envOr(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}
