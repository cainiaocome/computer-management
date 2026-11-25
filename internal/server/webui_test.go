package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cainiaocome/computer-management/internal/protocol"
)

// newUITestServer returns a server whose admin/UI handler is under test.
func newUITestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(Config{
		DBPath:     filepath.Join(dir, "fleet.db"),
		KeyPath:    filepath.Join(dir, "signing.key"),
		AdminToken: adminToken,
		Version:    "test",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(s.AdminHandler())
	t.Cleanup(func() {
		ts.Close()
		s.Close()
	})
	return s, ts
}

// browser is a client with a cookie jar and a fixed Origin, so requests look
// like a real form submission rather than a bare curl.
type browser struct {
	t    *testing.T
	ts   *httptest.Server
	http *http.Client
}

func newBrowser(t *testing.T, ts *httptest.Server) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, ts: ts, http: &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // assert on the redirect itself
		},
	}}
}

func (b *browser) do(method, path string, form url.Values, origin string) *http.Response {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.ts.URL+path, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	} else {
		req.Header.Set("Origin", b.ts.URL)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	return resp
}

func (b *browser) login() {
	b.t.Helper()
	resp := b.do("POST", "/ui/login", url.Values{"token": {adminToken}}, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("login: want 303, got %d", resp.StatusCode)
	}
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUIRequiresASession(t *testing.T) {
	_, ts := newUITestServer(t)
	b := newBrowser(t, ts)

	resp := b.do("GET", "/ui/", nil, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous dashboard: want 303 to login, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/ui/login" {
		t.Fatalf("redirected to %q, want /ui/login", loc)
	}
	if got := body(t, resp); strings.Contains(got, "Sign in") {
		t.Fatalf("redirect response leaked the login page: %q", got)
	}

	// A login page must load without a session.
	resp = b.do("GET", "/ui/login", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login page: want 200, got %d", resp.StatusCode)
	}
	html := body(t, resp)
	if !strings.Contains(html, `name="token"`) {
		t.Fatal("login page has no token field")
	}
	// Regression: the layout used to render the nav and a "Sign out" button to
	// an anonymous visitor.
	for _, chrome := range []string{"/ui/logout", "Sign out", "/ui/audit"} {
		if strings.Contains(html, chrome) {
			t.Fatalf("login page must not offer authenticated chrome (%q)", chrome)
		}
	}
}

func TestUILoginRejectsTheWrongToken(t *testing.T) {
	_, ts := newUITestServer(t)
	b := newBrowser(t, ts)

	resp := b.do("POST", "/ui/login", url.Values{"token": {"nope"}}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bad token: want 200 with an error page, got %d", resp.StatusCode)
	}
	if html := body(t, resp); !strings.Contains(html, "not correct") {
		t.Fatalf("expected a visible error, got: %s", html)
	}
	// Still anonymous.
	resp = b.do("GET", "/ui/", nil, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("session must not exist after a failed login, got %d", resp.StatusCode)
	}
}

func TestUISessionCookieIsHttpOnlyAndStrict(t *testing.T) {
	_, ts := newUITestServer(t)
	b := newBrowser(t, ts)
	resp := b.do("POST", "/ui/login", url.Values{"token": {adminToken}}, "")
	defer resp.Body.Close()
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login set no cookie")
	}
	c := cookies[0]
	if c.Name != sessionCookie {
		t.Fatalf("cookie name %q", c.Name)
	}
	if !c.HttpOnly {
		t.Fatal("session cookie must be HttpOnly")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie must be SameSite=Strict")
	}
	// The admin token itself must never be in the cookie.
	if strings.Contains(c.Value, adminToken) {
		t.Fatal("cookie contains the admin token")
	}
}

func TestUICrossOriginPostIsRefused(t *testing.T) {
	s, ts := newUITestServer(t)
	b := newBrowser(t, ts)
	b.login()

	resp := b.do("POST", "/ui/groups/parents",
		url.Values{"policies": {`[]`}}, "https://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin post: want 403, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Nothing was written.
	groups, err := s.store.ListGroups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := groups["parents"]; ok {
		t.Fatal("cross-origin request must not change state")
	}
}

func TestUIGroupPolicyRoundTrip(t *testing.T) {
	s, ts := newUITestServer(t)
	b := newBrowser(t, ts)
	b.login()

	good := `[{"id":"inv","type":"inventory","class":"report_only","enabled":true,"config":{"note":"x"}}]`
	resp := b.do("POST", "/ui/groups/parents", url.Values{"policies": {good}}, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save group: want 303, got %d: %s", resp.StatusCode, body(t, resp))
	}
	resp.Body.Close()

	stored, err := s.store.GroupPolicy(t.Context(), "parents")
	if err != nil {
		t.Fatalf("group not stored: %v", err)
	}
	if !strings.Contains(stored, `"inventory"`) {
		t.Fatalf("stored policy looks wrong: %s", stored)
	}

	// The edit page renders it back.
	resp = b.do("GET", "/ui/groups/parents", nil, "")
	html := body(t, resp)
	if !strings.Contains(html, "inventory") {
		t.Fatalf("edit page did not show the stored policy: %s", html)
	}
}

func TestUIGroupPolicyRejectsWhatTheAPICannotShip(t *testing.T) {
	s, ts := newUITestServer(t)
	b := newBrowser(t, ts)
	b.login()

	// browser.default can never be "enforce": policy.Validate owns that rule.
	bad := `[{"id":"br","type":"browser.default","class":"enforce","enabled":true,"config":{"browser":"firefox"}}]`
	resp := b.do("POST", "/ui/groups/parents", url.Values{"policies": {bad}}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("invalid policy: want 200 with an error page, got %d", resp.StatusCode)
	}
	html := body(t, resp)
	if !strings.Contains(html, "tier") {
		t.Fatalf("expected the tier validation error to be shown, got: %s", html)
	}
	groups, err := s.store.ListGroups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := groups["parents"]; ok {
		t.Fatal("an invalid policy must not be stored")
	}
}

func TestUIEnrolmentTokenIsShownOnce(t *testing.T) {
	s, ts := newUITestServer(t)
	b := newBrowser(t, ts)
	b.login()

	resp := b.do("POST", "/ui/enrol",
		url.Values{"label": {"mom-laptop"}, "ttl_seconds": {"3600"}, "max_uses": {"1"}}, "")
	html := body(t, resp)
	if !strings.Contains(html, "copy it now") {
		t.Fatal("token page does not warn that the secret is shown once")
	}
	secret := extractSecret(html)
	if secret == "" {
		t.Fatalf("no token rendered: %s", html)
	}

	// It works for enrolment (which is served on the agent listener, not this one).
	if got := enrollOnAgent(t, s, secret); got == "" {
		t.Fatal("the created token did not enrol a device")
	}

	// ...and a reload does not show it again.
	html = body(t, b.do("GET", "/ui/enrol", nil, ""))
	if strings.Contains(html, secret) {
		t.Fatal("the token secret is still rendered on the list page")
	}
	if !strings.Contains(html, "mom-laptop") {
		t.Fatal("the token label is not listed")
	}
}

// enrollOnAgent posts to the agent listener, proving the token created in the UI
// is the same object the agent API consumes.
func enrollOnAgent(t *testing.T, s *Server, token string) string {
	t.Helper()
	ts := httptest.NewServer(s.AgentHandler())
	defer ts.Close()
	var out protocol.EnrollResponse
	code := do(t, http.MethodPost, ts.URL+"/api/v1/enroll", "", protocol.EnrollRequest{
		Token: token, Hostname: "mom-laptop", OS: "darwin", Arch: "arm64", Version: "test",
	}, &out)
	if code != 200 {
		return ""
	}
	return out.DeviceID
}

func extractSecret(html string) string {
	const marker = `<pre class="secret">`
	i := strings.Index(html, marker)
	if i < 0 {
		return ""
	}
	rest := html[i+len(marker):]
	j := strings.Index(rest, "</pre>")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

func TestUIDashboardShowsDevicesAndTheirHonestState(t *testing.T) {
	s, ts := newUITestServer(t)
	b := newBrowser(t, ts)
	b.login()

	tok := NewSecret()
	if err := s.store.CreateToken(t.Context(), sha256hex(tok), "t", s.now().Add(3600e9), 1); err != nil {
		t.Fatal(err)
	}
	resp := b.do("GET", "/ui/", nil, "")
	if html := body(t, resp); !strings.Contains(html, "No devices have enrolled") {
		t.Fatalf("empty dashboard should explain how to enrol: %s", html)
	}
}
