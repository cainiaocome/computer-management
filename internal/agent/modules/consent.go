package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

// ConsentConfig is the config of a "macos.consent" item.
//
// The agent cannot grant TCC permissions to itself: only a human in System
// Settings, or an MDM PPPC profile, can. This module therefore *detects* the
// grants and reports them as a bootstrap checklist item.
type ConsentConfig struct {
	// Require lists grants that must be present, e.g.
	// ["app_management", "full_disk_access", "screen_recording"].
	Require []string `json:"require"`
}

// MacConsent reports macOS TCC grant state.
type MacConsent struct{}

func (MacConsent) Type() string { return policy.TypeMacConsent }

func (MacConsent) Apply(ctx context.Context, item protocol.Item) Outcome {
	e := env(ctx)
	if e.GOOS != "darwin" {
		return unsupported("macos.consent only applies to macOS")
	}
	var cfg ConsentConfig
	if len(item.Config) > 0 {
		if err := json.Unmarshal(item.Config, &cfg); err != nil {
			return failed(fmt.Errorf("decode config: %w", err))
		}
	}
	required := cfg.Require
	if len(required) == 0 {
		required = []string{"app_management"}
	}

	granted := map[string]bool{}
	for _, g := range required {
		switch g {
		case "app_management":
			granted[g] = probeAppManagement(e)
		case "full_disk_access":
			granted[g] = probeFullDiskAccess(e)
		case "screen_recording":
			// Not probeable without actually capturing the screen.
			granted[g] = false
		default:
			return failed(fmt.Errorf("unknown consent grant %q", g))
		}
	}

	missing := make([]string, 0, len(required))
	for _, g := range required {
		if !granted[g] {
			missing = append(missing, g)
		}
	}
	detail := map[string]any{
		"required": required,
		"granted":  granted,
		"missing":  missing,
		"open":     "x-apple.systempreferences:com.apple.preference.security?Privacy_AppBundles",
	}
	if len(missing) == 0 {
		return Outcome{Status: protocol.StatusCompliant, Message: "all required consent grants present", Detail: detail}
	}
	return Outcome{
		Status:  protocol.StatusActionRequired,
		Message: "missing consent grants: " + strings.Join(missing, ", "),
		Detail:  detail,
	}
}

// probeAppManagement attempts a write inside the Firefox bundle, which is what
// the Firefox policy module needs. A permission error means App Management has
// not been granted; a missing bundle means "not applicable".
func probeAppManagement(e *Env) bool {
	dir := e.Resolve("/Applications/Firefox.app/Contents/Resources/distribution")
	if !fileExists(filepath.Join(dir, "policies.json")) && !fileExists(e.Resolve("/Applications/Firefox.app")) {
		return true // no Firefox, nothing to grant
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".fleet-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// probeFullDiskAccess tries to open TCC-protected user directories. Only the
// ability to open is tested; no content is read or reported.
func probeFullDiskAccess(e *Env) bool {
	candidates := []string{
		"/Library/Application Support/com.apple.TCC/TCC.db",
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, "Library", "Mail"),
			filepath.Join(home, "Library", "Messages"),
		)
	}
	for _, p := range candidates {
		p = e.Resolve(p)
		f, err := os.Open(p)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				return false
			}
			continue // missing path says nothing about consent
		}
		f.Close()
		return true
	}
	return false
}
