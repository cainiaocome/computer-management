// Package agent implements the enrolment, check-in and policy-application loop.
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Config is the on-device agent configuration.
type Config struct {
	ServerURL       string `json:"server_url"`
	DeviceID        string `json:"device_id"`
	DeviceSecret    string `json:"device_secret"`
	SigningKey      string `json:"signing_key"`
	Generation      int64  `json:"generation"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	// Root prefixes every filesystem write. Only for tests; empty in production.
	Root   string `json:"root,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// DefaultPath is the per-platform config location.
func DefaultPath() string {
	switch runtime.GOOS {
	case "windows":
		return `C:\ProgramData\fleet\agent.json`
	case "darwin":
		return "/Library/Application Support/fleet/agent.json"
	default:
		return "/etc/fleet/agent.json"
	}
}

// Load reads the config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Save writes the config atomically with 0600 permissions.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Interval is the poll interval with a sane floor.
func (c *Config) Interval() time.Duration {
	s := c.IntervalSeconds
	if s <= 0 {
		s = 300
	}
	if s < 30 {
		s = 30
	}
	return time.Duration(s) * time.Second
}
