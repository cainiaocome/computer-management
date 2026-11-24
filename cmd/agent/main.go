// Command fleet-agent is the on-device daemon. It enrols with the server, polls
// for a signed policy bundle, converges the machine to it and reports back.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/cainiaocome/computer-management/internal/agent"
)

var version = "dev"

func main() {
	agent.SetVersion(version)

	cfgPath := flag.String("config", agent.DefaultPath(), "agent config path")
	serverURL := flag.String("server", "", "server base URL (enroll only)")
	token := flag.String("token", "", "enrollment token (enroll only)")
	dryRun := flag.Bool("dry-run", false, "report what would change without changing anything")
	jsonOut := flag.Bool("json", false, "print module reports as JSON on stdout (once only)")
	root := flag.String("root", "", "filesystem root override (testing only)")
	logLevel := flag.String("log-level", "info", "debug|info|warn|error")
	flag.Usage = usage

	// Support `fleet-agent <subcommand> -flags...`: the Go flag package stops at
	// the first non-flag argument, so pull the subcommand off first.
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	if err := flag.CommandLine.Parse(args); err != nil {
		os.Exit(2)
	}

	log := newLogger(*logLevel)
	// Geteuid is -1 on Windows, so only check on platforms that have it.
	if (runtime.GOOS != "windows") && (*dryRun || *root != "") && os.Geteuid() != 0 {
		log.Warn("running unprivileged; policy writes will likely fail")
	}

	switch cmd {
	case "enroll":
		if *serverURL == "" || *token == "" {
			fatalf(log, "enroll requires -server and -token")
		}
		cfg := &agent.Config{ServerURL: *serverURL, IntervalSeconds: 300}
		a := agent.New(cfg, *cfgPath, log)
		if err := a.Enroll(context.Background(), *token); err != nil {
			fatalf(log, "enroll: %v", err)
		}
		fmt.Printf("enrolled device %s; config written to %s\n", cfg.DeviceID, *cfgPath)

	case "run", "once":
		cfg, err := loadConfig(*cfgPath, *dryRun, *root)
		if err != nil {
			fatalf(log, "%v", err)
		}
		a := agent.New(cfg, *cfgPath, log)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if cmd == "once" {
			reports, err := a.Once(ctx)
			if err != nil {
				fatalf(log, "once: %v", err)
			}
			if *jsonOut {
				b, err := json.Marshal(reports)
				if err != nil {
					fatalf(log, "encode reports: %v", err)
				}
				fmt.Println(string(b))
				return
			}
			fmt.Println("cycle complete")
			return
		}
		if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fatalf(log, "run: %v", err)
		}

	case "status":
		cfg, err := loadConfig(*cfgPath, *dryRun, *root)
		if err != nil {
			fatalf(log, "%v", err)
		}
		printStatus(cfg)

	case "version":
		fmt.Println(version)

	default:
		usage()
		os.Exit(2)
	}
}

func loadConfig(path string, dryRun bool, root string) (*agent.Config, error) {
	cfg, err := agent.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w (run `fleet-agent enroll` first)", path, err)
	}
	if dryRun {
		cfg.DryRun = true
	}
	if root != "" {
		cfg.Root = root
	}
	return cfg, nil
}

func printStatus(cfg *agent.Config) {
	fmt.Printf("server:      %s\n", cfg.ServerURL)
	fmt.Printf("device id:   %s\n", cfg.DeviceID)
	fmt.Printf("generation:  %d\n", cfg.Generation)
	fmt.Printf("interval:    %s\n", cfg.Interval())
	fmt.Printf("dry run:     %v\n", cfg.DryRun)
	if cfg.Root != "" {
		fmt.Printf("root:        %s\n", cfg.Root)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `fleet-agent - declarative device agent

usage:
  fleet-agent enroll -server URL -token TOKEN [-config PATH]
  fleet-agent run    [-config PATH] [-dry-run] [-root DIR]
  fleet-agent once   [-config PATH] [-dry-run] [-root DIR]
  fleet-agent status [-config PATH]
  fleet-agent version

flags:
`)
	flag.PrintDefaults()
}

func fatalf(log *slog.Logger, format string, args ...any) {
	log.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
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
