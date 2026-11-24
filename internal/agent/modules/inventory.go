package modules

import (
	"context"
	"os"
	"strings"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

// Inventory collects read-only facts about the machine. It never changes state.
type Inventory struct{}

func (Inventory) Type() string { return policy.TypeInventory }

func (Inventory) Apply(ctx context.Context, item protocol.Item) Outcome {
	e := env(ctx)
	detail := map[string]any{
		"os":            e.GOOS,
		"arch":          e.GOARCH,
		"agent_version": e.Version,
		"firefox":       HasFirefox(e),
	}
	if h, err := os.Hostname(); err == nil {
		detail["hostname"] = h
	}
	if out, err := e.Run.Run(ctx, "uname", "-r"); err == nil && e.GOOS != "windows" {
		detail["kernel"] = strings.TrimSpace(out)
	}
	if out, err := e.Run.Run(ctx, "sw_vers", "-productVersion"); err == nil && e.GOOS == "darwin" {
		detail["os_version"] = strings.TrimSpace(out)
	}
	return Outcome{Status: protocol.StatusCompliant, Message: "inventory collected", Detail: detail}
}
