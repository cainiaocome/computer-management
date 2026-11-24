package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"

	"context"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

func goos() string   { return runtime.GOOS }
func goarch() string { return runtime.GOARCH }

// FirefoxExtension is one extension to force-install.
type FirefoxExtension struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// FirefoxConfig is the config of a "firefox.policy" item.
type FirefoxConfig struct {
	Extensions            []FirefoxExtension `json:"extensions"`
	Preferences           map[string]any     `json:"preferences"`
	Homepage              string             `json:"homepage"`
	BlockedInstallMessage string             `json:"blocked_install_message"`
	DisableTelemetry      *bool              `json:"disable_telemetry"`
	DisablePocket         *bool              `json:"disable_pocket"`
	DefaultBrowserCheck   *bool              `json:"default_browser_check"`
	BlockOtherExtensions  bool               `json:"block_other_extensions"`
}

// FirefoxPolicy applies a Firefox enterprise policy file.
//
// Policy files are read by Firefox on startup; writes take effect on the next
// launch. Extensions installed via ExtensionSettings with
// installation_mode=force_installed cannot be removed or disabled by the user.
type FirefoxPolicy struct{}

func (FirefoxPolicy) Type() string { return policy.TypeFirefoxPolicy }

func (FirefoxPolicy) Apply(ctx context.Context, item protocol.Item) Outcome {
	var cfg FirefoxConfig
	if err := json.Unmarshal(item.Config, &cfg); err != nil {
		return failed(fmt.Errorf("decode config: %w", err))
	}

	rendered, err := RenderFirefoxPolicy(cfg)
	if err != nil {
		return failed(err)
	}

	path, ok := FirefoxPolicyPath(env(ctx), cfg)
	if !ok {
		return unsupported("no Firefox policy path known for " + env(ctx).GOOS)
	}

	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(rendered) {
		return compliant("Firefox policy up to date at " + path)
	}

	if env(ctx).DryRun {
		return drifted("would write Firefox policy to " + path)
	}

	if err := writeFileAtomic(path, rendered, 0o644); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return actionRequired(
				"cannot write Firefox policy: App Management consent is required",
				map[string]any{
					"path":  path,
					"grant": "System Settings > Privacy & Security > App Management",
				})
		}
		return failed(err)
	}
	return changed("wrote Firefox policy to " + path)
}

// FirefoxPolicyPath returns the policies.json location for this platform.
// The optional config may override it (handy for testing and for ESR layouts).
func FirefoxPolicyPath(e *Env, cfg FirefoxConfig) (string, bool) {
	var p string
	switch e.GOOS {
	case "windows":
		p = `C:\Program Files\Mozilla Firefox\distribution\policies.json`
	case "darwin":
		p = "/Applications/Firefox.app/Contents/Resources/distribution/policies.json"
	case "linux":
		p = "/etc/firefox/policies/policies.json"
	default:
		return "", false
	}
	return e.Resolve(p), true
}

// RenderFirefoxPolicy produces deterministic policies.json bytes.
func RenderFirefoxPolicy(cfg FirefoxConfig) ([]byte, error) {
	pol := map[string]any{}

	prefs := map[string]any{}
	for k, v := range cfg.Preferences {
		prefs[k] = v
	}
	if cfg.Homepage != "" {
		prefs["browser.startup.homepage"] = cfg.Homepage
	}
	if len(prefs) > 0 {
		pol["Preferences"] = prefs
	}

	ext := map[string]any{}
	if cfg.BlockOtherExtensions {
		msg := cfg.BlockedInstallMessage
		if msg == "" {
			msg = "This browser is managed. Extensions can only be installed by your administrator."
		}
		ext["*"] = map[string]any{
			"installation_mode":       "blocked",
			"blocked_install_message": msg,
		}
	}
	for _, e := range cfg.Extensions {
		if e.ID == "" {
			return nil, fmt.Errorf("extension id is required")
		}
		entry := map[string]any{
			"installation_mode": "force_installed",
			"install_url":       e.URL,
		}
		if e.URL == "" {
			return nil, fmt.Errorf("extension %q: install url is required", e.ID)
		}
		ext[e.ID] = entry
	}
	if len(ext) > 0 {
		pol["ExtensionSettings"] = ext
		pol["ExtensionUpdate"] = true
	}

	if cfg.DisableTelemetry == nil || *cfg.DisableTelemetry {
		pol["DisableTelemetry"] = true
	}
	if cfg.DisablePocket == nil || *cfg.DisablePocket {
		pol["DisablePocket"] = true
	}
	if cfg.DefaultBrowserCheck == nil || *cfg.DefaultBrowserCheck {
		pol["DefaultBrowserSettingEnabled"] = true
	}

	out := map[string]any{"policies": pol}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// env pulls the *Env out of the context. Modules are invoked with
// modules.WithEnv.
func env(ctx context.Context) *Env {
	e, _ := ctx.Value(envKey{}).(*Env)
	if e == nil {
		return &Env{GOOS: goos(), GOARCH: goarch(), Run: ExecRunner{}}
	}
	return e
}

type envKey struct{}

// WithEnv attaches the environment to a context.
func WithEnv(ctx context.Context, e *Env) context.Context {
	return context.WithValue(ctx, envKey{}, e)
}

// HasFirefox reports whether a Firefox installation looks present.
func HasFirefox(e *Env) bool {
	for _, p := range firefoxProbePaths(e.GOOS) {
		if fileExists(e.Resolve(p)) {
			return true
		}
	}
	return false
}

func firefoxProbePaths(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"/Applications/Firefox.app", "/Applications/Firefox ESR.app"}
	case "windows":
		return []string{`C:\Program Files\Mozilla Firefox\firefox.exe`, `C:\Program Files (x86)\Mozilla Firefox\firefox.exe`}
	default:
		return []string{"/usr/lib/firefox/firefox", "/usr/bin/firefox", "/snap/bin/firefox"}
	}
}
