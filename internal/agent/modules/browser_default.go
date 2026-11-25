package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

// BrowserDefaultConfig is the config of a "browser.default" item.
type BrowserDefaultConfig struct {
	Browser string `json:"browser"` // firefox, chrome, edge, safari
}

// BrowserDefault detects the default browser and nudges.
//
// This module NEVER writes: on Windows 11 the UCPD kernel driver denies
// programmatic changes to the UserChoice keys to any non-Microsoft binary
// (including SYSTEM), and on macOS the default handler is per-user session
// state. Reporting plus a one-click instruction is the only durable approach.
type BrowserDefault struct{}

func (BrowserDefault) Type() string { return policy.TypeBrowserDefault }

func (BrowserDefault) Apply(ctx context.Context, item protocol.Item) Outcome {
	e := env(ctx)
	var cfg BrowserDefaultConfig
	if err := json.Unmarshal(item.Config, &cfg); err != nil {
		return failed(fmt.Errorf("decode config: %w", err))
	}
	want := strings.ToLower(cfg.Browser)
	if want == "" {
		return failed(fmt.Errorf("browser is required"))
	}

	current, err := currentDefaultBrowser(ctx, e)
	if err != nil {
		return actionRequired(
			"could not determine the default browser: "+err.Error(),
			map[string]any{"wanted": want, "open": settingsDeepLink(e.GOOS)},
		)
	}

	detail := map[string]any{
		"wanted":   want,
		"observed": current,
		"open":     settingsDeepLink(e.GOOS),
	}
	if current == want {
		return Outcome{Status: protocol.StatusCompliant,
			Message: "default browser is " + want, Detail: detail}
	}
	return actionRequired(
		fmt.Sprintf("default browser is %q, wanted %q: open settings and set it once", current, want),
		detail)
}

func settingsDeepLink(goos string) string {
	switch goos {
	case "windows":
		return "ms-settings:defaultapps"
	case "darwin":
		return "x-apple.systempreferences:com.apple.Desktop-Settings.extension"
	default:
		return "xdg-settings set default-web-browser firefox.desktop"
	}
}

// currentDefaultBrowser normalises the OS answer to firefox/chrome/edge/safari.
func currentDefaultBrowser(ctx context.Context, e *Env) (string, error) {
	switch e.GOOS {
	case "windows":
		out, err := e.Run.Run(ctx, "reg", "query",
			`HKCU\Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice`,
			"/v", "ProgId")
		if err != nil {
			return "", fmt.Errorf("reg query: %w", err)
		}
		return normalizeBrowser(out), nil
	case "darwin":
		return darwinDefaultBrowser(ctx, e)
	default:
		out, err := e.Run.Run(ctx, "xdg-settings", "get", "default-web-browser")
		if err != nil {
			return "", fmt.Errorf("xdg-settings: %w", err)
		}
		return normalizeBrowser(out), nil
	}
}

// normalizeBrowser maps a handler identifier to a browser name.
func normalizeBrowser(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "firefox"):
		return "firefox"
	case strings.Contains(l, "chrome"), strings.Contains(l, "chromium"):
		return "chrome"
	case strings.Contains(l, "edge"):
		return "edge"
	case strings.Contains(l, "safari"):
		return "safari"
	case strings.Contains(l, "brave"):
		return "brave"
	}
	return "unknown"
}

// darwinDefaultBrowser reads the LaunchServices handler for the https scheme.
//
// The file is a per-user binary plist, so it is converted to JSON rather than
// scraped. Scraping the `defaults read` text was wrong twice over: the real key
// is "LSHandlerURLScheme" (not "lsHandlerURLScheme"), and every handler block
// contains a nested "LSHandlerPreferredVersions" whose closing brace ended the
// scan early. The result was always "unknown".
func darwinDefaultBrowser(ctx context.Context, e *Env) (string, error) {
	plist, err := darwinLaunchServicesPlist(ctx, e)
	if err != nil {
		return "", err
	}
	out, err := e.Run.Run(ctx, "plutil", "-convert", "json", "-o", "-", plist)
	if err != nil {
		return "", fmt.Errorf("plutil: %w", err)
	}
	return parseDarwinHandlers(out), nil
}

// darwinLaunchServicesPlist returns the console user's LaunchServices plist.
// A LaunchDaemon runs as root, and root's own preferences say nothing about the
// person using the machine.
func darwinLaunchServicesPlist(ctx context.Context, e *Env) (string, error) {
	home := ""
	if out, err := e.Run.Run(ctx, "stat", "-f", "%Su", "/dev/console"); err == nil {
		if u := strings.TrimSpace(out); u != "" && u != "root" && u != "loginwindow" {
			if h, err := e.Run.Run(ctx, "dscl", ".", "-read", "/Users/"+u, "NFSHomeDirectory"); err == nil {
				if i := strings.Index(h, ":"); i >= 0 {
					home = strings.TrimSpace(h[i+1:])
				}
			}
		}
	}
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = h
	}
	return filepath.Join(home, "Library", "Preferences",
		"com.apple.LaunchServices", "com.apple.launchservices.secure.plist"), nil
}

// parseDarwinHandlers finds the handler registered for the https scheme in
// `plutil -convert json` output. "unset" means the file has no explicit https
// handler, which is a real answer: nobody has chosen a default browser.
func parseDarwinHandlers(out string) string {
	var doc struct {
		LSHandlers []struct {
			LSHandlerURLScheme string `json:"LSHandlerURLScheme"`
			LSHandlerRoleAll   string `json:"LSHandlerRoleAll"`
		} `json:"LSHandlers"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return "unknown"
	}
	for _, h := range doc.LSHandlers {
		if !strings.EqualFold(h.LSHandlerURLScheme, "https") {
			continue
		}
		if h.LSHandlerRoleAll == "" || h.LSHandlerRoleAll == "-" {
			continue
		}
		return normalizeBrowser(h.LSHandlerRoleAll)
	}
	return "unset"
}
