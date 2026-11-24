package modules

import (
	"context"
	"encoding/json"
	"fmt"
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
		out, err := e.Run.Run(ctx, "defaults", "read",
			"com.apple.LaunchServices/com.apple.launchservices.secure", "LSHandlers")
		if err != nil {
			return "", fmt.Errorf("defaults read: %w", err)
		}
		return parseDarwinHandlers(out), nil
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
	case strings.Contains(l, "chrome"):
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

// parseDarwinHandlers finds the handler registered for the https scheme.
// The `defaults` output is an old-style plist; blocks look like:
//
//	{
//	    LSHandlerContentType = "public.html";
//	    LSHandlerRoleAll = "com.google.chrome";
//	},
//
// and the scheme entry uses lsHandlerURLScheme = https.
func parseDarwinHandlers(out string) string {
	idx := strings.Index(out, "lsHandlerURLScheme = https")
	if idx < 0 {
		return "unknown"
	}
	rest := out[idx:]
	if end := strings.Index(rest, "}"); end > 0 {
		rest = rest[:end]
	}
	if i := strings.Index(rest, "LSHandlerRoleAll = "); i >= 0 {
		val := strings.TrimSpace(rest[i+len("LSHandlerRoleAll = "):])
		val = strings.Trim(val, `"; `)
		return normalizeBrowser(val)
	}
	return "unknown"
}
