package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

// Store is the persistence layer. SQLite is enough for tens of devices; every
// method is behind this type so Postgres is a later swap, not a rewrite.
type Store struct {
	db *sql.DB
}

// Device is one enrolled machine.
type Device struct {
	ID           string    `json:"id"`
	Hostname     string    `json:"hostname"`
	OS           string    `json:"os"`
	OSVersion    string    `json:"os_version"`
	Arch         string    `json:"arch"`
	Version      string    `json:"version"`
	Group        string    `json:"group"`
	Generation   int64     `json:"generation"`
	PolicyHash   string    `json:"policy_hash"`
	SecretHash   string    `json:"-"` // never leaves the server
	OverrideJSON string    `json:"override,omitempty"`
	ReportsJSON  string    `json:"-"`
	EnrolledAt   time.Time `json:"enrolled_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

// OpenStore opens (and migrates) the database.
func OpenStore(path string) (*Store, error) {
	if d := filepath.Dir(path); d != "" && d != "." && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	// A single writer keeps SQLite simple and avoids SQLITE_BUSY under load.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS devices (
			id            TEXT PRIMARY KEY,
			hostname      TEXT NOT NULL DEFAULT '',
			os            TEXT NOT NULL DEFAULT '',
			os_version    TEXT NOT NULL DEFAULT '',
			arch          TEXT NOT NULL DEFAULT '',
			version       TEXT NOT NULL DEFAULT '',
			group_name    TEXT NOT NULL DEFAULT '',
			generation    INTEGER NOT NULL DEFAULT 0,
			policy_hash   TEXT NOT NULL DEFAULT '',
			secret_hash   TEXT NOT NULL UNIQUE,
			override_json TEXT NOT NULL DEFAULT '[]',
			reports_json  TEXT NOT NULL DEFAULT '[]',
			enrolled_at   TEXT NOT NULL,
			last_seen_at  TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS groups (
			name        TEXT PRIMARY KEY,
			policy_json TEXT NOT NULL,
			updated_at  TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS enrollment_tokens (
			token_hash TEXT PRIMARY KEY,
			label      TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL,
			max_uses   INTEGER NOT NULL DEFAULT 1,
			uses       INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id     INTEGER PRIMARY KEY AUTOINCREMENT,
			at     TEXT NOT NULL,
			actor  TEXT NOT NULL,
			action TEXT NOT NULL,
			target TEXT NOT NULL,
			detail TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q[:20], err)
		}
	}
	return nil
}

// sqliteDSN turns a filesystem path into a DSN SQLite understands on every
// platform. A bare Windows path such as `C:\data\fleet.db` is parsed as a URI
// with `C:` as the scheme by modernc's driver ("unable to open database file"),
// so convert it to the documented `file:/C:/data/fleet.db` form.
func sqliteDSN(path string) string {
	if strings.HasPrefix(path, "file:") {
		return path
	}
	// ReplaceAll instead of filepath.ToSlash so a Windows path is handled the
	// same way when this code is compiled on Linux (tests, CI).
	p := strings.ReplaceAll(path, `\`, "/")
	if len(p) >= 2 && p[1] == ':' {
		p = "/" + p
	}
	return "file:" + p
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// --- enrollment tokens ---

// CreateToken stores a hashed, single-use enrollment token.
func (s *Store) CreateToken(ctx context.Context, tokenHash, label string, expires time.Time, maxUses int) error {
	if maxUses < 1 {
		maxUses = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO enrollment_tokens(token_hash,label,expires_at,max_uses,uses,created_at)
		 VALUES(?,?,?,?,0,?)`,
		tokenHash, label, ts(expires), maxUses, ts(time.Now()))
	return err
}

// ConsumeToken atomically burns one use of a token. Returns false when the
// token does not exist, is expired, or has no uses left.
// Token is an enrolment token as shown in the admin UI: the secret is not
// stored, so it can never be displayed twice.
type Token struct {
	Label     string    `json:"label"`
	ExpiresAt time.Time `json:"expires_at"`
	MaxUses   int       `json:"max_uses"`
	Uses      int       `json:"uses"`
	CreatedAt time.Time `json:"created_at"`
}

// ListTokens returns the most recent enrolment tokens.
func (s *Store) ListTokens(ctx context.Context, limit int) ([]Token, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT label, expires_at, max_uses, uses, created_at
		FROM enrollment_tokens ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		var exp, created string
		if err := rows.Scan(&t.Label, &exp, &t.MaxUses, &t.Uses, &created); err != nil {
			return nil, err
		}
		t.ExpiresAt, t.CreatedAt = parseTS(exp), parseTS(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteDevice removes a device, which revokes its secret immediately: the
// next checkin fails and the agent stops applying policy.
func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ConsumeToken(ctx context.Context, tokenHash string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE enrollment_tokens SET uses = uses + 1
		 WHERE token_hash = ? AND uses < max_uses AND expires_at > ?`,
		tokenHash, ts(time.Now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// --- devices ---

// CreateDevice inserts a new device and returns it.
func (s *Store) CreateDevice(ctx context.Context, d *Device) error {
	now := ts(time.Now())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO devices(id,hostname,os,os_version,arch,version,group_name,generation,
			policy_hash,secret_hash,override_json,reports_json,enrolled_at,last_seen_at)
		 VALUES(?,?,?,?,?,?,?,0,'',?,'[]','[]',?,?)`,
		d.ID, d.Hostname, d.OS, d.OSVersion, d.Arch, d.Version, d.Group, d.SecretHash, now, now)
	if err != nil {
		return err
	}
	d.EnrolledAt = time.Now()
	d.LastSeenAt = d.EnrolledAt
	return nil
}

const deviceColumns = `id,hostname,os,os_version,arch,version,group_name,generation,
	policy_hash,secret_hash,override_json,reports_json,enrolled_at,last_seen_at`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var enrolled, seen string
	err := row.Scan(&d.ID, &d.Hostname, &d.OS, &d.OSVersion, &d.Arch, &d.Version,
		&d.Group, &d.Generation, &d.PolicyHash, &d.SecretHash, &d.OverrideJSON,
		&d.ReportsJSON, &enrolled, &seen)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.EnrolledAt = parseTS(enrolled)
	d.LastSeenAt = parseTS(seen)
	return &d, nil
}

// DeviceBySecretHash looks up a device by the hash of its secret.
func (s *Store) DeviceBySecretHash(ctx context.Context, hash string) (*Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx,
		`SELECT `+deviceColumns+` FROM devices WHERE secret_hash = ?`, hash))
}

// DeviceByID looks up a device by id.
func (s *Store) DeviceByID(ctx context.Context, id string) (*Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx,
		`SELECT `+deviceColumns+` FROM devices WHERE id = ?`, id))
}

// ListDevices returns all devices, newest first.
func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deviceColumns+` FROM devices ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// TouchDevice records a check-in and refreshes the inventory fields.
func (s *Store) TouchDevice(ctx context.Context, id, hostname, osVersion, arch, version string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET hostname=?, os_version=?, arch=?, version=?, last_seen_at=?
		 WHERE id=?`, hostname, osVersion, arch, version, ts(time.Now()), id)
	return err
}

// SetDeviceGeneration stores the applied policy hash and its generation.
func (s *Store) SetDeviceGeneration(ctx context.Context, id, hash string, gen int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET policy_hash=?, generation=? WHERE id=?`, hash, gen, id)
	return err
}

// SetDeviceReports stores the latest module reports.
func (s *Store) SetDeviceReports(ctx context.Context, id, reportsJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET reports_json=? WHERE id=?`, reportsJSON, id)
	return err
}

// SetDevicePolicy updates group membership and/or per-device overrides.
func (s *Store) SetDevicePolicy(ctx context.Context, id, group, overrideJSON string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET group_name=?, override_json=? WHERE id=?`, group, overrideJSON, id)
	return err
}

// --- groups ---

// UpsertGroup stores a group's policy.
func (s *Store) UpsertGroup(ctx context.Context, name, policyJSON string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO groups(name,policy_json,updated_at) VALUES(?,?,?)
		 ON CONFLICT(name) DO UPDATE SET policy_json=excluded.policy_json, updated_at=excluded.updated_at`,
		name, policyJSON, ts(time.Now()))
	return err
}

// GroupPolicy returns a group's policy JSON, or "[]" when the group is unknown.
func (s *Store) GroupPolicy(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "[]", nil
	}
	var p string
	err := s.db.QueryRowContext(ctx, `SELECT policy_json FROM groups WHERE name=?`, name).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "[]", nil
	}
	return p, err
}

// ListGroups returns group names and their policies.
func (s *Store) ListGroups(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, policy_json FROM groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, p string
		if err := rows.Scan(&n, &p); err != nil {
			return nil, err
		}
		out[n] = p
	}
	return out, rows.Err()
}

// --- audit ---

// Audit appends to the audit log.
func (s *Store) Audit(ctx context.Context, actor, action, target, detail string) {
	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO audit_log(at,actor,action,target,detail) VALUES(?,?,?,?,?)`,
		ts(time.Now()), actor, action, target, detail)
}

// AuditEntry is one audit row.
type AuditEntry struct {
	At     time.Time
	Actor  string
	Action string
	Target string
	Detail string
}

// RecentAudit returns the most recent entries.
func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT at,actor,action,target,detail FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at string
		if err := rows.Scan(&at, &e.Actor, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		e.At = parseTS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- helpers ---

// NewID returns a random identifier with a prefix.
func NewID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
}

// NewSecret returns a 32-byte URL-safe random secret.
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
