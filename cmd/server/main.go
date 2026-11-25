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
		addr      = flag.String("addr", envOr("FLEET_ADDR", ":8080"), "agent listen address")
		dbPath    = flag.String("db", envOr("FLEET_DB", "fleet.db"), "SQLite database path")
		keyPath   = flag.String("key", envOr("FLEET_KEY", "fleet-signing.key"), "policy signing key path")
		adminTok  = flag.String("admin-token", os.Getenv("FLEET_ADMIN_TOKEN"), "admin API bearer token (or FLEET_ADMIN_TOKEN)")
		adminAddr = flag.String("admin-addr", envOr("FLEET_ADMIN_ADDR", "127.0.0.1:8081"),
			"admin API and web UI listen address; empty serves them on the agent port too")
		logLevel = flag.String("log-level", envOr("FLEET_LOG_LEVEL", "info"), "debug|info|warn|error")
	)
	flag.Parse()

	log := newLogger(*logLevel)
	if *adminTok == "" {
		log.Warn("FLEET_ADMIN_TOKEN is empty: the admin API and web UI are disabled")
	}
	if *adminAddr == *addr {
		log.Warn("admin and agent share a listener; the web UI is reachable from the agent network",
			"addr", *addr)
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

	agentSrv := newHTTPServer(*addr, srv.AgentHandler())
	var adminSrv *http.Server
	switch {
	case *adminAddr == "" || *adminAddr == *addr:
		// One port for everything, as before 1.0. The UI is then reachable from
		// the agent network; only do this behind a private network.
		agentSrv.Handler = srv.Handler()
	default:
		adminSrv = newHTTPServer(*adminAddr, srv.AdminHandler())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening for agents", "addr", *addr, "version", version)
		if err := agentSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("agent listener error", "error", err)
			stop()
		}
	}()

	if adminSrv != nil {
		go func() {
			log.Info("listening for admin and the web UI", "addr", *adminAddr, "ui", "http://"+*adminAddr+"/ui/")
			if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("admin listener error", "error", err)
				stop()
			}
		}()
	}

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = agentSrv.Shutdown(shutdownCtx)
	if adminSrv != nil {
		_ = adminSrv.Shutdown(shutdownCtx)
	}
}

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
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
