package agent

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/cainiaocome/computer-management/internal/agent/modules"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

// Agent is the on-device daemon.
type Agent struct {
	Cfg     *Config
	CfgPath string
	Client  *Client
	Env     *modules.Env
	Log     *slog.Logger
	Mods    map[string]modules.Module
}

// New wires an agent from config.
func New(cfg *Config, cfgPath string, log *slog.Logger) *Agent {
	return &Agent{
		Cfg:     cfg,
		CfgPath: cfgPath,
		Client:  NewClient(cfg.ServerURL),
		Env: &modules.Env{
			GOOS:    runtime.GOOS,
			GOARCH:  runtime.GOARCH,
			Root:    cfg.Root,
			Run:     modules.ExecRunner{},
			Log:     log,
			DryRun:  cfg.DryRun,
			Version: version,
		},
		Log:  log,
		Mods: modules.All(),
	}
}

var version = "dev"

// SetVersion records the build version for reporting.
func SetVersion(v string) {
	if v != "" {
		version = v
	}
}

// Version returns the agent build version.
func Version() string { return version }

// Enroll performs the one-time token exchange and persists the config.
func (a *Agent) Enroll(ctx context.Context, token string) error {
	hostname := hostnameOrUnknown()
	resp, err := a.Client.Enroll(ctx, protocol.EnrollRequest{
		Token:    token,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  version,
	})
	if err != nil {
		return err
	}
	a.Cfg.ServerURL = strings.TrimRight(a.Cfg.ServerURL, "/")
	a.Cfg.DeviceID = resp.DeviceID
	a.Cfg.DeviceSecret = resp.DeviceSecret
	a.Cfg.SigningKey = resp.SigningKey
	a.Cfg.Generation = 0
	if err := a.Cfg.Save(a.CfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	a.Log.Info("enrolled", "device_id", resp.DeviceID, "config", a.CfgPath)
	return nil
}

// Once performs a single check-in / apply / report cycle and returns the
// module reports it sent (nil when there was nothing to apply).
func (a *Agent) Once(ctx context.Context) ([]protocol.ModuleReport, error) {
	if a.Cfg.DeviceID == "" || a.Cfg.DeviceSecret == "" {
		return nil, fmt.Errorf("not enrolled: run `fleet-agent enroll` first")
	}

	resp, err := a.Client.Checkin(ctx, a.Cfg.DeviceID, a.Cfg.DeviceSecret, protocol.CheckinRequest{
		Hostname:   hostnameOrUnknown(),
		OS:         runtime.GOOS,
		OSVersion:  osVersion(ctx, a.Env),
		Arch:       runtime.GOARCH,
		Version:    version,
		Generation: a.Cfg.Generation,
	})
	if err != nil {
		return nil, fmt.Errorf("checkin: %w", err)
	}
	if resp.Bundle == nil {
		a.Log.Debug("no policy assigned")
		return nil, nil
	}

	pub, err := protocol.DecodePublicKey(a.Cfg.SigningKey)
	if err != nil {
		return nil, fmt.Errorf("pinned signing key is invalid: %w", err)
	}
	if err := protocol.Verify(resp.Bundle, pub); err != nil {
		return nil, fmt.Errorf("rejecting policy bundle: %w", err)
	}
	if resp.Bundle.DeviceID != a.Cfg.DeviceID {
		return nil, fmt.Errorf("bundle is for device %q, not %q", resp.Bundle.DeviceID, a.Cfg.DeviceID)
	}
	if resp.Bundle.Generation < a.Cfg.Generation {
		return nil, fmt.Errorf("%w: bundle %d < applied %d",
			protocol.ErrStaleGeneration, resp.Bundle.Generation, a.Cfg.Generation)
	}
	if resp.Bundle.Generation == a.Cfg.Generation {
		a.Log.Debug("policy unchanged", "generation", a.Cfg.Generation)
		return nil, nil
	}

	reports := a.apply(ctx, resp.Bundle)
	if err := a.Client.Report(ctx, a.Cfg.DeviceID, a.Cfg.DeviceSecret, resp.Bundle.Generation, reports); err != nil {
		// The policy was applied but the server did not hear about it. Do NOT
		// record the generation: the early return above would then suppress all
		// later cycles, and these reports would be lost until the policy
		// changed. Modules are idempotent, so re-applying next cycle is safe.
		a.Log.Warn("report failed; will re-apply and report on the next cycle", "error", err)
		return reports, nil
	}

	a.Cfg.Generation = resp.Bundle.Generation
	if err := a.Cfg.Save(a.CfgPath); err != nil {
		a.Log.Error("could not persist generation", "error", err)
	}
	return reports, nil
}

func (a *Agent) apply(ctx context.Context, b *protocol.Bundle) []protocol.ModuleReport {
	reports := make([]protocol.ModuleReport, 0, len(b.Policies))
	for _, item := range b.Policies {
		reports = append(reports, a.applyItem(ctx, item))
	}
	return reports
}

// applyItem runs one module, converting a panic into a report rather than
// taking the whole daemon down.
func (a *Agent) applyItem(ctx context.Context, item protocol.Item) (rep protocol.ModuleReport) {
	rep = protocol.ModuleReport{ID: item.ID, Type: item.Type, Class: item.Class}

	defer func() {
		if r := recover(); r != nil {
			rep.Status = protocol.StatusError
			rep.Message = fmt.Sprintf("panic: %v", r)
			a.Log.Error("module panicked", "id", item.ID, "type", item.Type, "panic", r)
		}
	}()

	if !item.Enabled {
		rep.Status = protocol.StatusSkipped
		rep.Message = "disabled"
		return rep
	}
	mod, ok := a.Mods[item.Type]
	if !ok {
		rep.Status = protocol.StatusError
		rep.Message = "no module for type " + item.Type
		return rep
	}
	out := mod.Apply(modules.WithEnv(ctx, a.Env), item)
	rep.Status = out.Status
	rep.Message = out.Message
	rep.Detail = out.Detail
	a.Log.Info("policy applied",
		"id", item.ID, "type", item.Type, "status", out.Status, "message", out.Message)
	return rep
}

// Run polls forever until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	wait := a.Cfg.Interval()
	for {
		_, err := a.Once(ctx)
		switch {
		case err != nil:
			a.Log.Error("cycle failed", "error", err)
			wait = minDuration(wait*2, 30*time.Minute)
		default:
			wait = a.Cfg.Interval()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter(wait)):
		}
	}
}

// jitter spreads check-ins so a fleet does not stampede the server.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	delta := time.Duration(rand.Int63n(int64(d)/5 + 1))
	return d - time.Duration(int64(d)/10) + delta
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func hostnameOrUnknown() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

func osVersion(ctx context.Context, env *modules.Env) string {
	var cmd string
	var args []string
	switch env.GOOS {
	case "darwin":
		cmd, args = "sw_vers", []string{"-productVersion"}
	case "windows":
		cmd, args = "cmd", []string{"/c", "ver"}
	default:
		return ""
	}
	out, err := env.Run.Run(ctx, cmd, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
