package modules

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cainiaocome/computer-management/internal/protocol"
)

// Runner abstracts command execution so modules can be tested without the real
// package manager or OS tooling.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, err error)
	LookPath(name string) (string, error)
	ReadFile(name string) ([]byte, error)
}

// ExecRunner is the production Runner.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (ExecRunner) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// Env is the execution environment handed to every module.
type Env struct {
	GOOS    string
	GOARCH  string
	Root    string // filesystem prefix for tests; "" means the real root
	Run     Runner
	Log     *slog.Logger
	DryRun  bool
	Version string
	// User is the session user for user-tier modules ("" when running as root
	// without a known console session).
	User string
}

// NewEnv builds the production environment.
func NewEnv(log *slog.Logger, version string) *Env {
	return &Env{
		GOOS:    goos(),
		GOARCH:  goarch(),
		Run:     ExecRunner{},
		Log:     log,
		Version: version,
	}
}

// Resolve maps an OS-native path into the environment root. With Root=="" the
// path is returned unchanged.
func (e *Env) Resolve(p string) string {
	if e.Root == "" {
		return filepath.FromSlash(p)
	}
	p = filepath.ToSlash(p)
	if vol := volumeName(p); vol != "" {
		p = strings.TrimPrefix(p, vol)
		p = vol + strings.TrimLeft(p, "/")
	}
	p = strings.TrimLeft(p, "/")
	return filepath.Join(e.Root, filepath.FromSlash(p))
}

func volumeName(p string) string {
	if len(p) >= 2 && p[1] == ':' {
		return p[:2]
	}
	return ""
}

// Logf logs at info level when a logger is configured.
func (e *Env) Logf(format string, args ...any) {
	if e.Log != nil {
		e.Log.Info(fmt.Sprintf(format, args...))
	}
}

// Outcome is the result of applying one policy item.
type Outcome struct {
	Status  string
	Message string
	Detail  map[string]any
}

func compliant(msg string) Outcome { return Outcome{Status: protocol.StatusCompliant, Message: msg} }
func changed(msg string) Outcome   { return Outcome{Status: protocol.StatusChanged, Message: msg} }
func drifted(msg string) Outcome   { return Outcome{Status: protocol.StatusDrifted, Message: msg} }

func actionRequired(msg string, detail map[string]any) Outcome {
	return Outcome{Status: protocol.StatusActionRequired, Message: msg, Detail: detail}
}

func unsupported(msg string) Outcome {
	return Outcome{Status: protocol.StatusUnsupported, Message: msg}
}

func failed(err error) Outcome {
	return Outcome{Status: protocol.StatusError, Message: err.Error()}
}

// Module applies one policy type.
type Module interface {
	Type() string
	Apply(ctx context.Context, item protocol.Item) Outcome
}

// All returns the module registry keyed by policy type.
func All() map[string]Module {
	mods := []Module{
		FirefoxPolicy{},
		Packages{},
		File{},
		BrowserDefault{},
		MacConsent{},
		UserAccount{},
		Inventory{},
	}
	out := make(map[string]Module, len(mods))
	for _, m := range mods {
		out[m.Type()] = m
	}
	return out
}

// writeFileAtomic writes content to path via a temp file + rename so a partial
// write can never leave a corrupt policy file behind.
func writeFileAtomic(path string, content []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fleet-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// fileExists reports whether path exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
