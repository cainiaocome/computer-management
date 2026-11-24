// Command fleet-server is the control plane: enrolment, signed policy bundles,
// device inventory and audit log.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cainiaocome/computer-management/internal/server"
)

var version = "dev"

func main() {
	var (
		addr     = flag.String("addr", envOr("FLEET_ADDR", ":8080"), "listen address")
		dbPath   = flag.String("db", envOr("FLEET_DB", "fleet.db"), "SQLite database path")
		keyPath  = flag.String("key", envOr("FLEET_KEY", "fleet-signing.key"), "policy signing key path")
		adminTok = flag.String("admin-token", os.Getenv("FLEET_ADMIN_TOKEN"), "admin API bearer token (or FLEET_ADMIN_TOKEN)")
		logLevel = flag.String("log-level", envOr("FLEET_LOG_LEVEL", "info"), "debug|info|warn|error")
	)
	flag.Parse()

	log := newLogger(*logLevel)
	if *adminTok == "" {
		log.Warn("FLEET_ADMIN_TOKEN is empty: the admin API is disabled")
	}

	srv, err := server.New(server.Config{
		Addr:       *addr,
		DBPath:     *dbPath,
		KeyPath:    *keyPath,
		AdminToken: *adminTok,
		Version:    version,
	}, log)
	if err != nil {
		log.Error("startup failed", "error", err)
		os.Exit(1)
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", *addr, "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
