package modules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

func firefoxItem(t *testing.T, cfg string) protocol.Item {
	t.Helper()
	return protocol.Item{
		ID: "ff", Type: policy.TypeFirefoxPolicy, Class: string(policy.Enforce),
		Enabled: true, Config: json.RawMessage(cfg),
	}
}

func TestRenderFirefoxPolicyIsDeterministicAndLocked(t *testing.T) {
	cfg := FirefoxConfig{
		Extensions: []FirefoxExtension{
			{ID: "uBlock0@raymondhill.net", URL: "https://addons.mozilla.org/firefox/downloads/latest/ublock-origin/latest.xpi"},
		},
		Preferences: map[string]any{
			"browser.startup.page":       float64(3),
			"browser.newtabpage.enabled": false,
		},
		BlockOtherExtensions: true,
	}
	a, err := RenderFirefoxPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RenderFirefoxPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("render must be deterministic (it is signed and hashed)")
	}

	var doc struct {
		Policies map[string]any `json:"policies"`
	}
	if err := json.Unmarshal(a, &doc); err != nil {
		t.Fatalf("rendered policy is not valid JSON: %v\n%s", err, a)
	}
	ext, ok := doc.Policies["ExtensionSettings"].(map[string]any)
	if !ok {
		t.Fatalf("ExtensionSettings missing:\n%s", a)
	}
	ublock, ok := ext["uBlock0@raymondhill.net"].(map[string]any)
	if !ok {
		t.Fatalf("extension entry missing:\n%s", a)
	}
	if ublock["installation_mode"] != "force_installed" {
		t.Fatalf("force_installed missing: %v", ublock)
	}
	if _, ok := ext["*"]; !ok {
		t.Fatal("wildcard block entry missing")
	}
	if doc.Policies["DisableTelemetry"] != true {
		t.Fatal("telemetry should be disabled by default")
	}
}

func TestRenderFirefoxPolicyRejectsIncompleteExtension(t *testing.T) {
	_, err := RenderFirefoxPolicy(FirefoxConfig{
		Extensions: []FirefoxExtension{{ID: "x@y", URL: ""}},
	})
	if err == nil {
		t.Fatal("expected an error for a missing install URL")
	}
}

func TestFirefoxApplyWritesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	e := testEnv("darwin", root, &fakeRunner{})
	item := firefoxItem(t, `{
		"extensions": [{"id":"a@b","url":"https://example.com/a.xpi"}],
		"homepage": "https://example.com"
	}`)

	out := FirefoxPolicy{}.Apply(ctxWith(e), item)
	if out.Status != protocol.StatusChanged {
		t.Fatalf("first apply: want changed, got %s (%s)", out.Status, out.Message)
	}

	path := filepath.Join(root, "Applications/Firefox.app/Contents/Resources/distribution/policies.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("policy file not written: %v", err)
	}
	if !strings.Contains(string(raw), "force_installed") {
		t.Fatalf("unexpected content:\n%s", raw)
	}

	out = FirefoxPolicy{}.Apply(ctxWith(e), item)
	if out.Status != protocol.StatusCompliant {
		t.Fatalf("second apply: want compliant, got %s (%s)", out.Status, out.Message)
	}
}

func TestFirefoxApplyDryRunDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	e := testEnv("darwin", root, &fakeRunner{})
	e.DryRun = true
	item := firefoxItem(t, `{"extensions":[{"id":"a@b","url":"https://example.com/a.xpi"}]}`)

	out := FirefoxPolicy{}.Apply(ctxWith(e), item)
	if out.Status != protocol.StatusDrifted {
		t.Fatalf("want drifted, got %s", out.Status)
	}
	if fileExists(filepath.Join(root, "Applications/Firefox.app/Contents/Resources/distribution/policies.json")) {
		t.Fatal("dry run must not write")
	}
}

func TestFirefoxPolicyPathPerPlatform(t *testing.T) {
	cases := map[string]string{
		"linux":   "/etc/firefox/policies/policies.json",
		"darwin":  "/Applications/Firefox.app/Contents/Resources/distribution/policies.json",
		"windows": `C:\Program Files\Mozilla Firefox\distribution\policies.json`,
	}
	for goos, want := range cases {
		got, ok := FirefoxPolicyPath(&Env{GOOS: goos}, FirefoxConfig{})
		if !ok {
			t.Fatalf("%s: no path", goos)
		}
		if got != filepath.FromSlash(want) {
			t.Errorf("%s: got %q, want %q", goos, got, filepath.FromSlash(want))
		}
	}
}

func TestFirefoxRejectsBadConfig(t *testing.T) {
	e := testEnv("linux", t.TempDir(), &fakeRunner{})
	out := FirefoxPolicy{}.Apply(ctxWith(e), firefoxItem(t, `{`))
	if out.Status != protocol.StatusError {
		t.Fatalf("want error, got %s", out.Status)
	}
}
