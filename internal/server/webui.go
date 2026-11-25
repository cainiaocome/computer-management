package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

//go:embed webui/templates/*.html webui/static/*
var uiFS embed.FS

// The UI is server-rendered HTML with no build step and no JavaScript
// framework. That is a deliberate choice for a self-hosted family fleet: one
// binary to deploy, no CORS, no token in localStorage, and the admin surface can
// listen on a different address from the one agents reach.

var uiFuncs = template.FuncMap{
	"ts": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Local().Format("2006-01-02 15:04")
	},
	"ago": func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		d := time.Since(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 48*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		default:
			return fmt.Sprintf("%dd ago", int(d.Hours()/24))
		}
	},
	"pretty": func(raw string) string {
		var v any
		if json.Unmarshal([]byte(raw), &v) != nil {
			return raw
		}
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return raw
		}
		return string(b)
	},
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
	"statusClass": func(status string) string {
		switch status {
		case protocol.StatusCompliant:
			return "ok"
		case protocol.StatusChanged:
			return "warn"
		case protocol.StatusDrifted, protocol.StatusActionRequired:
			return "warn"
		case protocol.StatusError:
			return "bad"
		default:
			return "muted"
		}
	},
}

// uiPages caches one parsed template set per page. Parsing per request would be
// wasteful; parsing every page into one set would collide on "content".
var uiPages map[string]*template.Template

func init() {
	uiPages = make(map[string]*template.Template)
	entries, err := fs.Glob(uiFS, "webui/templates/*.html")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(strings.TrimPrefix(e, "webui/templates/"), ".html")
		if name == "layout" {
			continue
		}
		uiPages[name] = template.Must(
			template.New("layout").Funcs(uiFuncs).ParseFS(uiFS, "webui/templates/layout.html", e))
	}
}

type uiPage struct {
	Title   string
	Nav     string
	Flash   string
	Error   string
	Version string
	Data    any
	// Authed hides the navigation and the sign-out button on the login page:
	// offering "sign out" to an anonymous visitor is nonsense.
	Authed bool
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data uiPage) {
	t, ok := uiPages[page]
	if !ok {
		http.Error(w, "template "+page+" not found", http.StatusInternalServerError)
		return
	}
	data.Version = s.cfg.Version
	if data.Nav != "" {
		data.Authed = true
	}
	if data.Flash == "" {
		data.Flash = r.URL.Query().Get("ok")
	}
	if data.Error == "" {
		data.Error = r.URL.Query().Get("err")
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	// No inline script is used, so a strict CSP costs nothing.
	h.Set("Content-Security-Policy",
		"default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	// ParseFS names templates after their file, so the layout is "layout.html".
	if err := t.ExecuteTemplate(w, "layout.html", data); err != nil {
		s.log.Error("render failed", "page", page, "error", err)
	}
}

// --- view models -----------------------------------------------------------

type uiCount struct {
	Status string
	N      int
}

type uiReport struct {
	ID      string
	Type    string
	Class   string
	Status  string
	Message string
	Detail  string
}

type uiDevice struct {
	Device
	Reports []uiReport
	Counts  []uiCount
	Health  string // ok | attention | stale
}

var reportOrder = []string{
	protocol.StatusError,
	protocol.StatusActionRequired,
	protocol.StatusDrifted,
	protocol.StatusChanged,
	protocol.StatusCompliant,
	protocol.StatusUnsupported,
	protocol.StatusSkipped,
}

func (s *Server) deviceView(d Device) uiDevice {
	v := uiDevice{Device: d}
	if d.ReportsJSON != "" {
		var reps []protocol.ModuleReport
		if json.Unmarshal([]byte(d.ReportsJSON), &reps) == nil {
			counts := map[string]int{}
			for _, r := range reps {
				counts[r.Status]++
				detail := ""
				if len(r.Detail) > 0 {
					if b, err := json.Marshal(r.Detail); err == nil {
						detail = string(b)
					}
				}
				v.Reports = append(v.Reports, uiReport{
					ID: r.ID, Type: r.Type, Class: r.Class, Status: r.Status,
					Message: r.Message, Detail: detail,
				})
			}
			for _, st := range reportOrder {
				if counts[st] > 0 {
					v.Counts = append(v.Counts, uiCount{Status: st, N: counts[st]})
				}
			}
			delete(counts, "")
		}
	}
	v.Health = "ok"
	if len(v.Reports) == 0 {
		v.Health = "stale"
	}
	for _, c := range v.Counts {
		if c.Status == protocol.StatusError || c.Status == protocol.StatusDrifted ||
			c.Status == protocol.StatusActionRequired {
			v.Health = "attention"
		}
	}
	if !d.LastSeenAt.IsZero() && time.Since(d.LastSeenAt) > 7*24*time.Hour {
		v.Health = "stale"
	}
	return v
}

// --- handlers --------------------------------------------------------------

func (s *Server) uiHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ui/static/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		b, err := fs.ReadFile(uiFS, "webui/static/"+name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasSuffix(name, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(name, ".svg"):
			w.Header().Set("Content-Type", "image/svg+xml")
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(b)
	})

	mux.HandleFunc("GET /ui/login", func(w http.ResponseWriter, r *http.Request) {
		if s.authed(r) {
			http.Redirect(w, r, "/ui/", http.StatusSeeOther)
			return
		}
		s.render(w, r, "login", uiPage{Title: "Sign in"})
	})

	mux.HandleFunc("POST /ui/login", func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			s.render(w, r, "login", uiPage{Title: "Sign in", Error: "cross-origin form submission refused"})
			return
		}
		if s.cfg.AdminToken == "" {
			s.render(w, r, "login", uiPage{Title: "Sign in", Error: "the admin API is disabled: set FLEET_ADMIN_TOKEN"})
			return
		}
		if !s.tokenMatches(r.FormValue("token")) {
			s.store.Audit(r.Context(), "ui", "login.failed", r.RemoteAddr, "")
			s.render(w, r, "login", uiPage{Title: "Sign in", Error: "that token is not correct"})
			return
		}
		s.issueSession(w, r)
		s.store.Audit(r.Context(), "ui", "login", r.RemoteAddr, "")
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /ui/logout", func(w http.ResponseWriter, r *http.Request) {
		s.clearSession(w)
		http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
	})

	// Everything below requires a session.
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !s.authed(r) {
				http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
				return
			}
			if r.Method != http.MethodGet && !sameOrigin(r) {
				http.Error(w, "cross-origin form submission refused", http.StatusForbidden)
				return
			}
			h(w, r)
		}
	}

	mux.HandleFunc("GET /ui/", guard(s.uiDashboard))
	mux.HandleFunc("GET /ui/devices/{id}", guard(s.uiDevice))
	mux.HandleFunc("POST /ui/devices/{id}", guard(s.uiDeviceUpdate))
	mux.HandleFunc("POST /ui/devices/{id}/revoke", guard(s.uiDeviceRevoke))
	mux.HandleFunc("GET /ui/groups", guard(s.uiGroups))
	mux.HandleFunc("GET /ui/groups/{name}", guard(s.uiGroup))
	mux.HandleFunc("POST /ui/groups/{name}", guard(s.uiGroupSave))
	mux.HandleFunc("GET /ui/enrol", guard(s.uiEnrol))
	mux.HandleFunc("POST /ui/enrol", guard(s.uiEnrolCreate))
	mux.HandleFunc("GET /ui/audit", guard(s.uiAudit))

	return mux
}

type dashData struct {
	Devices   []uiDevice
	Total     int
	Healthy   int
	Attention int
	Stale     int
	Groups    int
}

func (s *Server) uiDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		s.render(w, r, "dashboard", uiPage{Title: "Devices", Error: err.Error()})
		return
	}
	d := dashData{}
	for _, dev := range devices {
		v := s.deviceView(dev)
		d.Devices = append(d.Devices, v)
		d.Total++
		switch v.Health {
		case "ok":
			d.Healthy++
		case "attention":
			d.Attention++
		default:
			d.Stale++
		}
	}
	if groups, err := s.store.ListGroups(ctx); err == nil {
		d.Groups = len(groups)
	}
	s.render(w, r, "dashboard", uiPage{Title: "Devices", Nav: "devices", Data: d})
}

type deviceData struct {
	Device   uiDevice
	Groups   []string
	Override string
	Revoked  bool
}

func (s *Server) uiDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	dev, err := s.store.DeviceByID(ctx, id)
	if err != nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	groups, _ := s.store.ListGroups(ctx)
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	s.render(w, r, "device", uiPage{
		Title: dev.Hostname, Nav: "devices",
		Data: deviceData{Device: s.deviceView(*dev), Groups: names, Override: dev.OverrideJSON},
	})
}

func (s *Server) uiDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	dev, err := s.store.DeviceByID(ctx, id)
	if err != nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	group := strings.TrimSpace(r.FormValue("group"))
	raw := strings.TrimSpace(r.FormValue("override"))
	if raw == "" {
		raw = "[]"
	}
	var items []protocol.Item
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		s.renderErrorDevice(w, r, dev, "override is not valid JSON: "+err.Error(), group, raw)
		return
	}
	if err := policy.Validate(items); err != nil {
		s.renderErrorDevice(w, r, dev, err.Error(), group, raw)
		return
	}
	b, _ := json.Marshal(items)
	if err := s.store.SetDevicePolicy(ctx, id, group, string(b)); err != nil {
		s.renderErrorDevice(w, r, dev, err.Error(), group, raw)
		return
	}
	s.store.Audit(ctx, "ui", "device.policy", id,
		fmt.Sprintf("group=%s overrides=%d", group, len(items)))
	http.Redirect(w, r, "/ui/devices/"+id+"?ok=Saved: "+fmt.Sprint(len(items))+" override item(s); applied at the next checkin", http.StatusSeeOther)
}

func (s *Server) renderErrorDevice(w http.ResponseWriter, r *http.Request, dev *Device, msg, group, override string) {
	groups, _ := s.store.ListGroups(r.Context())
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	s.render(w, r, "device", uiPage{
		Title: dev.Hostname, Nav: "devices", Error: msg,
		Data: deviceData{Device: s.deviceView(*dev), Groups: names, Override: override, Revoked: false},
	})
}

func (s *Server) uiDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if err := s.store.DeleteDevice(ctx, id); err != nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	s.store.Audit(ctx, "ui", "device.revoke", id, "secret invalidated")
	http.Redirect(w, r, "/ui/?ok=Device+revoked", http.StatusSeeOther)
}

type groupRow struct {
	Name    string
	Items   int
	Updated string
}

type groupsData struct {
	Groups []groupRow
}

func (s *Server) uiGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		s.render(w, r, "groups", uiPage{Title: "Groups", Nav: "groups", Error: err.Error()})
		return
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	var rows []groupRow
	for _, n := range names {
		var items []protocol.Item
		_ = json.Unmarshal([]byte(groups[n]), &items)
		rows = append(rows, groupRow{Name: n, Items: len(items)})
	}
	s.render(w, r, "groups", uiPage{Title: "Groups", Nav: "groups", Data: groupsData{Groups: rows}})
}

func (s *Server) uiGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	raw := "[]"
	if name != "new" {
		p, err := s.store.GroupPolicy(r.Context(), name)
		if err != nil {
			http.Error(w, "group not found", http.StatusNotFound)
			return
		}
		raw = p
		if pretty, err := indentJSON(p); err == nil {
			raw = pretty
		}
	}
	s.render(w, r, "group", uiPage{
		Title: "Group " + name, Nav: "groups",
		Data: map[string]any{"Name": name, "Policy": raw, "New": name == "new"},
	})
}

func (s *Server) uiGroupSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := strings.TrimSpace(r.PathValue("name"))
	raw := strings.TrimSpace(r.FormValue("policies"))
	if name == "" || name == "new" {
		name = strings.TrimSpace(r.FormValue("name"))
	}
	if name == "" {
		http.Error(w, "group name is required", http.StatusBadRequest)
		return
	}
	if raw == "" {
		raw = "[]"
	}
	var items []protocol.Item
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		s.render(w, r, "group", uiPage{
			Title: "Group " + name, Nav: "groups", Error: "not valid JSON: " + err.Error(),
			Data: map[string]any{"Name": name, "Policy": raw},
		})
		return
	}
	if err := policy.Validate(items); err != nil {
		s.render(w, r, "group", uiPage{
			Title: "Group " + name, Nav: "groups", Error: err.Error(),
			Data: map[string]any{"Name": name, "Policy": raw},
		})
		return
	}
	b, _ := json.Marshal(items)
	if err := s.store.UpsertGroup(ctx, name, string(b)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.store.Audit(ctx, "ui", "group.policy", name, fmt.Sprintf("%d items", len(items)))
	http.Redirect(w, r, "/ui/groups?ok=Saved "+name, http.StatusSeeOther)
}

type enrolData struct {
	Tokens []Token
	New    string // the secret, shown exactly once
	Label  string
}

func (s *Server) uiEnrol(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListTokens(r.Context(), 100)
	if err != nil {
		s.render(w, r, "enrol", uiPage{Title: "Enrolment", Nav: "enrol", Error: err.Error()})
		return
	}
	s.render(w, r, "enrol", uiPage{Title: "Enrolment", Nav: "enrol", Data: enrolData{Tokens: tokens}})
}

func (s *Server) uiEnrolCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	label := strings.TrimSpace(r.FormValue("label"))
	ttl := atoiDefault(r.FormValue("ttl_seconds"), 3600)
	uses := atoiDefault(r.FormValue("max_uses"), 1)
	if ttl < 60 {
		ttl = 60
	}
	if uses < 1 {
		uses = 1
	}
	token := NewSecret()
	expires := s.now().Add(time.Duration(ttl) * time.Second)
	if err := s.store.CreateToken(ctx, sha256hex(token), label, expires, uses); err != nil {
		s.render(w, r, "enrol", uiPage{Title: "Enrolment", Nav: "enrol", Error: err.Error()})
		return
	}
	s.store.Audit(ctx, "ui", "token.create", truncate(label, 64),
		fmt.Sprintf("uses=%d ttl=%ds", uses, ttl))
	tokens, _ := s.store.ListTokens(ctx, 100)
	s.render(w, r, "enrol", uiPage{
		Title: "Enrolment", Nav: "enrol",
		Data: enrolData{Tokens: tokens, New: token, Label: label},
	})
}

func (s *Server) uiAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.RecentAudit(r.Context(), 200)
	if err != nil {
		s.render(w, r, "audit", uiPage{Title: "Audit", Nav: "audit", Error: err.Error()})
		return
	}
	s.render(w, r, "audit", uiPage{Title: "Audit", Nav: "audit", Data: entries})
}

// indentJSON pretty-prints a stored policy so the textarea is readable. The
// stored form stays compact; this is display only.
func indentJSON(raw string) (string, error) {
	// json.Indent only reformats, so the stored key order is preserved: id,
	// type, class, enabled, config. Round-tripping through map[string]any would
	// sort the keys and bury "id" in the middle of the item.
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func atoiDefault(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
}
