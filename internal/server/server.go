// Package server implements the fleet control plane.
package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

// Config configures the server.
type Config struct {
	Addr       string
	DBPath     string
	KeyPath    string
	AdminToken string
	Version    string
}

// Server is the control plane.
type Server struct {
	cfg   Config
	store *Store
	priv  ed25519.PrivateKey
	pub   ed25519.PublicKey
	log   *slog.Logger
	now   func() time.Time
}

// New opens the store and loads (or creates) the policy signing key.
func New(cfg Config, log *slog.Logger) (*Server, error) {
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	pub, priv, err := loadOrCreateKey(cfg.KeyPath)
	if err != nil {
		store.Close()
		return nil, err
	}
	return &Server{
		cfg:   cfg,
		store: store,
		priv:  priv,
		pub:   pub,
		log:   log,
		now:   time.Now,
	}, nil
}

// Close releases resources.
func (s *Server) Close() error { return s.store.Close() }

func loadOrCreateKey(path string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		seed, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if decErr != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, decErr)
		}
		if len(seed) != ed25519.SeedSize {
			return nil, nil, fmt.Errorf("%s: want %d-byte seed, got %d", path, ed25519.SeedSize, len(seed))
		}
		priv := ed25519.NewKeyFromSeed(seed)
		return priv.Public().(ed25519.PublicKey), priv, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}

	pub, priv, err := protocol.GenerateKey()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return nil, nil, err
	}
	enc := base64.StdEncoding.EncodeToString(priv.Seed())
	if err := os.WriteFile(path, []byte(enc+"\n"), 0o600); err != nil {
		return nil, nil, err
	}
	return pub, priv, nil
}

func dirOf(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i > 0 {
		return p[:i]
	}
	return "."
}

// Handler returns every endpoint: agent API, admin API and web UI.
//
// Production uses AgentHandler and AdminHandler on separate listeners, so the
// admin surface - and the UI that manages every machine - is not reachable from
// wherever agents connect. Handler exists for tests and simple single-port
// setups.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", s.AgentHandler())
	mux.Handle("/api/v1/admin/", s.adminMux())
	mux.Handle("/ui/", s.uiHandler())
	return mux
}

// AgentHandler serves only what a managed machine needs: enrolment, checkin,
// report and a health check. This is the listener exposed to the fleet.
func (s *Server) AgentHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.cfg.Version})
	})
	mux.HandleFunc("POST /api/v1/enroll", s.handleEnroll)
	mux.HandleFunc("POST /api/v1/checkin", s.handleCheckin)
	mux.HandleFunc("POST /api/v1/report", s.handleReport)
	return mux
}

// AdminHandler serves the admin API and the web UI.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/admin/", s.adminMux())
	mux.Handle("/ui/", s.uiHandler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.cfg.Version})
	})
	return mux
}

func (s *Server) adminMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/devices", s.admin(s.handleListDevices))
	mux.HandleFunc("PUT /api/v1/admin/devices/{id}", s.admin(s.handleSetDevicePolicy))
	mux.HandleFunc("GET /api/v1/admin/groups", s.admin(s.handleListGroups))
	mux.HandleFunc("PUT /api/v1/admin/groups/{name}", s.admin(s.handleSetGroupPolicy))
	mux.HandleFunc("POST /api/v1/admin/enrollment-tokens", s.admin(s.handleCreateToken))
	mux.HandleFunc("GET /api/v1/admin/audit", s.admin(s.handleAudit))
	return mux
}

// --- middleware ---

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken == "" {
			writeErr(w, http.StatusServiceUnavailable, "admin API disabled: FLEET_ADMIN_TOKEN is not set")
			return
		}
		tok := bearer(r)
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.AdminToken)) != 1 {
			writeErr(w, http.StatusUnauthorized, "invalid admin token")
			return
		}
		next(w, r)
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if v, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *Server) device(r *http.Request) (*Device, error) {
	secret := bearer(r)
	if secret == "" {
		return nil, fmt.Errorf("missing device secret")
	}
	return s.store.DeviceBySecretHash(r.Context(), sha256hex(secret))
}

// --- device endpoints ---

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req protocol.EnrollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Token == "" {
		writeErr(w, http.StatusBadRequest, "token is required")
		return
	}

	ok, err := s.store.ConsumeToken(r.Context(), sha256hex(req.Token))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		s.log.Warn("enrollment rejected", "remote", r.RemoteAddr, "hostname", req.Hostname)
		writeErr(w, http.StatusForbidden, "invalid, expired or exhausted enrollment token")
		return
	}

	secret := NewSecret()
	d := &Device{
		ID:         NewID("dev"),
		Hostname:   truncate(req.Hostname, 128),
		OS:         truncate(req.OS, 32),
		Arch:       truncate(req.Arch, 32),
		Version:    truncate(req.Version, 64),
		SecretHash: sha256hex(secret),
	}
	if err := s.store.CreateDevice(r.Context(), d); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.Audit(r.Context(), "device:"+d.ID, "enroll", d.ID, d.Hostname+" "+d.OS+"/"+d.Arch)
	s.log.Info("device enrolled", "device_id", d.ID, "hostname", d.Hostname, "os", d.OS)

	writeJSON(w, http.StatusOK, protocol.EnrollResponse{
		DeviceID:     d.ID,
		DeviceSecret: secret,
		SigningKey:   protocol.EncodePublicKey(s.pub),
		ServerTime:   s.now().UTC(),
	})
}

func (s *Server) handleCheckin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := s.device(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unknown device")
		return
	}
	var req protocol.CheckinRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.store.TouchDevice(ctx, d.ID, truncate(req.Hostname, 128),
		truncate(req.OSVersion, 128), truncate(req.Arch, 32), truncate(req.Version, 64)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	items, err := s.resolvePolicy(ctx, d)
	if err != nil {
		s.log.Error("policy resolution failed", "device", d.ID, "error", err)
		writeErr(w, http.StatusInternalServerError, "policy resolution failed: "+err.Error())
		return
	}
	if len(items) == 0 {
		writeJSON(w, http.StatusOK, protocol.CheckinResponse{})
		return
	}

	hash := hashItems(items)
	gen := d.Generation
	if hash != d.PolicyHash {
		gen++
		if err := s.store.SetDeviceGeneration(ctx, d.ID, hash, gen); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	b := &protocol.Bundle{
		Schema:     protocol.Schema,
		DeviceID:   d.ID,
		Generation: gen,
		IssuedAt:   s.now().UTC().Truncate(time.Second),
		Policies:   items,
	}
	if err := protocol.Sign(b, s.priv); err != nil {
		writeErr(w, http.StatusInternalServerError, "sign: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, protocol.CheckinResponse{Bundle: b})
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	d, err := s.device(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unknown device")
		return
	}
	var req struct {
		Generation int64                   `json:"generation"`
		Reports    []protocol.ModuleReport `json:"reports"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	b, err := json.Marshal(req.Reports)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetDeviceReports(r.Context(), d.ID, string(b)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "generation": req.Generation})
}

// --- policy resolution ---

func (s *Server) resolvePolicy(ctx context.Context, d *Device) ([]protocol.Item, error) {
	groupJSON, err := s.store.GroupPolicy(ctx, d.Group)
	if err != nil {
		return nil, err
	}
	var group, override []protocol.Item
	if err := json.Unmarshal([]byte(groupJSON), &group); err != nil {
		return nil, fmt.Errorf("group %q policy: %w", d.Group, err)
	}
	if strings.TrimSpace(d.OverrideJSON) != "" {
		if err := json.Unmarshal([]byte(d.OverrideJSON), &override); err != nil {
			return nil, fmt.Errorf("device override: %w", err)
		}
	}
	items := policy.Merge(group, override)
	if err := policy.Validate(items); err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func hashItems(items []protocol.Item) string {
	b, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- admin endpoints ---

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type deviceView struct {
		Device
		Compliance map[string]int          `json:"compliance"`
		Reports    []protocol.ModuleReport `json:"reports"`
	}
	out := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		v := deviceView{Device: d, Compliance: map[string]int{}, Reports: []protocol.ModuleReport{}}
		if d.ReportsJSON != "" {
			_ = json.Unmarshal([]byte(d.ReportsJSON), &v.Reports)
		}
		for _, rep := range v.Reports {
			v.Compliance[rep.Status]++
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (s *Server) handleSetDevicePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Group    *string          `json:"group"`
		Policies *[]protocol.Item `json:"policies"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	d, err := s.store.DeviceByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	group := d.Group
	if req.Group != nil {
		group = *req.Group
	}
	override := d.OverrideJSON
	if req.Policies != nil {
		if err := policy.Validate(*req.Policies); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		b, _ := json.Marshal(*req.Policies)
		override = string(b)
	}
	if err := s.store.SetDevicePolicy(r.Context(), id, group, override); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.Audit(r.Context(), "admin", "device.policy", id, "group="+group)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": group})
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (s *Server) handleSetGroupPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "group name is required")
		return
	}
	var req struct {
		Policies []protocol.Item `json:"policies"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := policy.Validate(req.Policies); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	b, _ := json.Marshal(req.Policies)
	if err := s.store.UpsertGroup(r.Context(), name, string(b)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.Audit(r.Context(), "admin", "group.policy", name, fmt.Sprintf("%d items", len(req.Policies)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": name, "items": len(req.Policies)})
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label      string `json:"label"`
		TTLSeconds int    `json:"ttl_seconds"`
		MaxUses    int    `json:"max_uses"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	if req.TTLSeconds <= 0 {
		req.TTLSeconds = 3600
	}
	if req.MaxUses <= 0 {
		req.MaxUses = 1
	}
	token := NewSecret()
	expires := s.now().Add(time.Duration(req.TTLSeconds) * time.Second)
	if err := s.store.CreateToken(r.Context(), sha256hex(token), req.Label, expires, req.MaxUses); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.Audit(r.Context(), "admin", "token.create", truncate(req.Label, 64),
		fmt.Sprintf("uses=%d ttl=%ds", req.MaxUses, req.TTLSeconds))
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"label":      req.Label,
		"expires_at": expires.UTC(),
		"max_uses":   req.MaxUses,
	})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.RecentAudit(r.Context(), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
