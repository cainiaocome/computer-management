package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cainiaocome/computer-management/internal/protocol"
)

const adminToken = "test-admin-token"

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
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
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		s.Close()
	})
	return s, ts
}

func do(t *testing.T, method, url, bearer string, body any, out any) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			t.Fatalf("decode %s %s: %v", method, url, err)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode
}

func newToken(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	if code := do(t, http.MethodPost, ts.URL+"/api/v1/admin/enrollment-tokens", adminToken,
		map[string]any{"label": "test", "ttl_seconds": 600, "max_uses": 1}, &out); code != 200 {
		t.Fatalf("create token: %d", code)
	}
	if out.Token == "" {
		t.Fatal("empty token")
	}
	return out.Token
}

func enroll(t *testing.T, ts *httptest.Server, token string) protocol.EnrollResponse {
	t.Helper()
	var out protocol.EnrollResponse
	code := do(t, http.MethodPost, ts.URL+"/api/v1/enroll", "", protocol.EnrollRequest{
		Token: token, Hostname: "mom-laptop", OS: "darwin", Arch: "arm64", Version: "test",
	}, &out)
	if code != 200 {
		t.Fatalf("enroll: %d", code)
	}
	return out
}

func checkin(t *testing.T, ts *httptest.Server, secret string, gen int64) *protocol.Bundle {
	t.Helper()
	var out protocol.CheckinResponse
	code := do(t, http.MethodPost, ts.URL+"/api/v1/checkin", secret, protocol.CheckinRequest{
		Hostname: "mom-laptop", OS: "darwin", Arch: "arm64", Version: "test", Generation: gen,
	}, &out)
	if code != 200 {
		t.Fatalf("checkin: %d", code)
	}
	return out.Bundle
}

func firefoxItem() protocol.Item {
	return protocol.Item{
		ID: "ff", Type: "firefox.policy", Class: "enforce", Enabled: true,
		Config: json.RawMessage(`{"extensions":[{"id":"a@b","url":"https://example.com/a.xpi"}]}`),
	}
}

func TestEnrollCheckinReportFlow(t *testing.T) {
	_, ts := newTestServer(t)
	tok := newToken(t, ts)
	dev := enroll(t, ts, tok)

	if dev.DeviceID == "" || dev.DeviceSecret == "" || dev.SigningKey == "" {
		t.Fatalf("incomplete enrollment response: %+v", dev)
	}
	pub, err := protocol.DecodePublicKey(dev.SigningKey)
	if err != nil {
		t.Fatal(err)
	}

	// Before any policy is assigned there is no bundle.
	if b := checkin(t, ts, dev.DeviceSecret, 0); b != nil {
		t.Fatalf("expected no bundle before policy assignment, got %+v", b)
	}

	// Assign a group policy.
	if code := do(t, http.MethodPut, ts.URL+"/api/v1/admin/groups/parents", adminToken,
		map[string]any{"policies": []protocol.Item{firefoxItem()}}, nil); code != 200 {
		t.Fatalf("set group policy: %d", code)
	}
	if code := do(t, http.MethodPut, ts.URL+"/api/v1/admin/devices/"+dev.DeviceID, adminToken,
		map[string]any{"group": "parents"}, nil); code != 200 {
		t.Fatalf("assign group: %d", code)
	}

	b := checkin(t, ts, dev.DeviceSecret, 0)
	if b == nil {
		t.Fatal("expected a bundle")
	}
	if err := protocol.Verify(b, pub); err != nil {
		t.Fatalf("bundle signature: %v", err)
	}
	if b.Generation != 1 {
		t.Fatalf("generation = %d, want 1", b.Generation)
	}
	if len(b.Policies) != 1 || b.Policies[0].ID != "ff" {
		t.Fatalf("unexpected policies: %+v", b.Policies)
	}

	// Unchanged policy keeps the generation stable, so the agent does not
	// re-apply on every poll.
	if b2 := checkin(t, ts, dev.DeviceSecret, b.Generation); b2 == nil || b2.Generation != b.Generation {
		t.Fatalf("generation should be stable: %+v", b2)
	}

	// A policy change bumps the generation.
	if code := do(t, http.MethodPut, ts.URL+"/api/v1/admin/groups/parents", adminToken,
		map[string]any{"policies": []protocol.Item{
			firefoxItem(),
			{ID: "inv", Type: "inventory", Class: "report_only", Enabled: true, Config: json.RawMessage(`{}`)},
		}}, nil); code != 200 {
		t.Fatalf("update group policy: %d", code)
	}
	b3 := checkin(t, ts, dev.DeviceSecret, b.Generation)
	if b3 == nil || b3.Generation != b.Generation+1 {
		t.Fatalf("generation should bump: %+v", b3)
	}

	// Reports round-trip.
	code := do(t, http.MethodPost, ts.URL+"/api/v1/report", dev.DeviceSecret, map[string]any{
		"generation": b3.Generation,
		"reports": []protocol.ModuleReport{
			{ID: "ff", Type: "firefox.policy", Class: "enforce", Status: protocol.StatusCompliant},
			{ID: "inv", Type: "inventory", Class: "report_only", Status: protocol.StatusCompliant},
		},
	}, nil)
	if code != 200 {
		t.Fatalf("report: %d", code)
	}

	var list struct {
		Devices []struct {
			ID         string                  `json:"id"`
			Generation int64                   `json:"generation"`
			Compliance map[string]int          `json:"compliance"`
			Reports    []protocol.ModuleReport `json:"reports"`
		} `json:"devices"`
	}
	if code := do(t, http.MethodGet, ts.URL+"/api/v1/admin/devices", adminToken, nil, &list); code != 200 {
		t.Fatalf("list devices: %d", code)
	}
	if len(list.Devices) != 1 {
		t.Fatalf("want 1 device, got %d", len(list.Devices))
	}
	if list.Devices[0].Compliance[protocol.StatusCompliant] != 2 {
		t.Fatalf("compliance = %v", list.Devices[0].Compliance)
	}
	if len(list.Devices[0].Reports) != 2 {
		t.Fatalf("reports = %+v", list.Devices[0].Reports)
	}
}

func TestEnrollmentTokenIsSingleUse(t *testing.T) {
	_, ts := newTestServer(t)
	tok := newToken(t, ts)
	enroll(t, ts, tok)

	var out map[string]string
	if code := do(t, http.MethodPost, ts.URL+"/api/v1/enroll", "", protocol.EnrollRequest{
		Token: tok, Hostname: "second", OS: "windows", Arch: "amd64",
	}, &out); code != http.StatusForbidden {
		t.Fatalf("replayed token: want 403, got %d (%v)", code, out)
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	s, ts := newTestServer(t)
	token := "expired-token-value"
	if err := s.store.CreateToken(t.Context(), sha256hex(token), "expired",
		time.Now().Add(-time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	if code := do(t, http.MethodPost, ts.URL+"/api/v1/enroll", "", protocol.EnrollRequest{
		Token: token, Hostname: "x", OS: "linux", Arch: "amd64",
	}, nil); code != http.StatusForbidden {
		t.Fatalf("expired token: want 403, got %d", code)
	}
}

func TestDeviceAuthIsRequired(t *testing.T) {
	_, ts := newTestServer(t)
	if code := do(t, http.MethodPost, ts.URL+"/api/v1/checkin", "wrong-secret",
		protocol.CheckinRequest{Hostname: "x"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}
	if code := do(t, http.MethodGet, ts.URL+"/api/v1/admin/devices", "wrong", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("want 401 for admin, got %d", code)
	}
}

func TestAdminAPIDisabledWithoutToken(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Config{DBPath: filepath.Join(dir, "f.db"), KeyPath: filepath.Join(dir, "k")},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	if code := do(t, http.MethodGet, ts.URL+"/api/v1/admin/devices", "anything", nil, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", code)
	}
}

func TestInvalidPolicyRejected(t *testing.T) {
	_, ts := newTestServer(t)
	// browser.default must be nudge: enforce would let a root agent pretend it
	// can set the default browser.
	bad := []protocol.Item{{ID: "b", Type: "browser.default", Class: "enforce", Enabled: true,
		Config: json.RawMessage(`{"browser":"firefox"}`)}}
	if code := do(t, http.MethodPut, ts.URL+"/api/v1/admin/groups/x", adminToken,
		map[string]any{"policies": bad}, nil); code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", code)
	}
}

func TestHealthz(t *testing.T) {
	_, ts := newTestServer(t)
	var out map[string]string
	if code := do(t, http.MethodGet, ts.URL+"/healthz", "", nil, &out); code != 200 {
		t.Fatalf("healthz: %d", code)
	}
	if out["status"] != "ok" {
		t.Fatalf("healthz body: %v", out)
	}
}
