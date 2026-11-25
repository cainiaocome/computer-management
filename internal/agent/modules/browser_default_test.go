package modules

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

func browserItem(t *testing.T, cfg string) protocol.Item {
	t.Helper()
	return protocol.Item{ID: "br", Type: policy.TypeBrowserDefault, Class: string(policy.Nudge),
		Enabled: true, Config: json.RawMessage(cfg)}
}

// Real `plutil -convert json` output. Note that every handler contains a nested
// LSHandlerPreferredVersions object, and that keys are capitalised: the old
// parser searched for a lower-case key and stopped at the nested "}".
const darwinDefaultsFirefox = `{
  "LSHandlers": [
    {
      "LSHandlerPreferredVersions": {"LSHandlerRoleAll": "-"},
      "LSHandlerRoleAll": "us.zoom.xos",
      "LSHandlerURLScheme": "zoomphonecall"
    },
    {
      "LSHandlerPreferredVersions": {"LSHandlerRoleAll": "-"},
      "LSHandlerRoleAll": "org.mozilla.firefox",
      "LSHandlerURLScheme": "https"
    }
  ]
}`

const darwinDefaultsChrome = `{
  "LSHandlers": [
    {
      "LSHandlerPreferredVersions": {"LSHandlerRoleAll": "-"},
      "LSHandlerRoleAll": "com.google.chrome",
      "LSHandlerURLScheme": "https"
    }
  ]
}`

const darwinDefaultsUnset = `{"LSHandlers": [{"LSHandlerURLScheme": "mailto", "LSHandlerRoleAll": "com.apple.mail"}]}`

func TestBrowserDefaultCompliant(t *testing.T) {
	r := &fakeRunner{handler: func(name string, args []string) (string, error) {
		return darwinDefaultsFirefox, nil
	}}
	e := testEnv("darwin", t.TempDir(), r)
	out := BrowserDefault{}.Apply(ctxWith(e), browserItem(t, `{"browser":"firefox"}`))
	if out.Status != protocol.StatusCompliant {
		t.Fatalf("want compliant, got %s (%s)", out.Status, out.Message)
	}
}

func TestBrowserDefaultNudgesInsteadOfWriting(t *testing.T) {
	r := &fakeRunner{handler: func(name string, args []string) (string, error) {
		return darwinDefaultsChrome, nil
	}}
	e := testEnv("darwin", t.TempDir(), r)
	out := BrowserDefault{}.Apply(ctxWith(e), browserItem(t, `{"browser":"firefox"}`))
	if out.Status != protocol.StatusActionRequired {
		t.Fatalf("want action_required, got %s", out.Status)
	}
	if out.Detail["observed"] != "chrome" {
		t.Fatalf("observed = %v", out.Detail["observed"])
	}
	for _, c := range r.calls {
		if c[0] == "defaults" && len(c) > 1 && c[1] == "write" {
			t.Fatal("browser.default must never write the default handler")
		}
	}
}

func TestBrowserDefaultDetectionFailure(t *testing.T) {
	r := &fakeRunner{handler: func(string, []string) (string, error) {
		return "", errors.New("command not found")
	}}
	e := testEnv("linux", t.TempDir(), r)
	out := BrowserDefault{}.Apply(ctxWith(e), browserItem(t, `{"browser":"firefox"}`))
	if out.Status != protocol.StatusActionRequired {
		t.Fatalf("want action_required, got %s", out.Status)
	}
}

func TestParseDarwinHandlers(t *testing.T) {
	cases := map[string]string{
		darwinDefaultsFirefox: "firefox",
		darwinDefaultsChrome:  "chrome",
		"no handlers here":    "unknown",
		darwinDefaultsUnset:   "unset",
	}
	edge := `{"LSHandlers":[{"LSHandlerURLScheme":"https","LSHandlerRoleAll":"com.microsoft.edgemac"}]}`
	cases[edge] = "edge"
	for in, want := range cases {
		if got := parseDarwinHandlers(in); got != want {
			t.Errorf("parse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeBrowser(t *testing.T) {
	cases := map[string]string{
		"FirefoxURL-308046B0AF4A39CB": "firefox",
		"ChromeHTML":                  "chrome",
		"org.mozilla.firefox":         "firefox",
		"MSEdgeHTM":                   "edge",
		"somethingelse":               "unknown",
	}
	for in, want := range cases {
		if got := normalizeBrowser(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
