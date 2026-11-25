package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

// FileConfig is the config of a "file" item.
type FileConfig struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode"`   // octal string, e.g. "0644"
	Ensure  string `json:"ensure"` // present (default) or absent
}

// File manages a single file declaratively.
type File struct{}

func (File) Type() string { return policy.TypeFile }

func (File) Apply(ctx context.Context, item protocol.Item) Outcome {
	var cfg FileConfig
	if err := json.Unmarshal(item.Config, &cfg); err != nil {
		return failed(fmt.Errorf("decode config: %w", err))
	}
	if cfg.Path == "" {
		return failed(fmt.Errorf("path is required"))
	}
	e := env(ctx)
	if !isRootedPath(cfg.Path) {
		return failed(fmt.Errorf("path %q must be absolute", cfg.Path))
	}
	path := e.Resolve(cfg.Path)
	if err := checkFilePath(e, path); err != nil {
		return failed(err)
	}

	ensure := cfg.Ensure
	if ensure == "" {
		ensure = "present"
	}

	switch ensure {
	case "absent":
		if !fileExists(path) {
			return compliant("absent: " + path)
		}
		if e.DryRun {
			return drifted("would remove " + path)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return failed(err)
		}
		return changed("removed " + path)
	case "present":
	default:
		return failed(fmt.Errorf("unknown ensure %q", ensure))
	}

	mode := os.FileMode(0o644)
	if cfg.Mode != "" {
		v, err := strconv.ParseUint(strings.TrimPrefix(cfg.Mode, "0o"), 8, 32)
		if err != nil {
			return failed(fmt.Errorf("bad mode %q: %w", cfg.Mode, err))
		}
		if v > 0o777 {
			// Special bits (setuid/setgid/sticky) cannot be expressed this way:
			// os.Chmod would silently drop them while the check below compared
			// only Perm(), and the item would report compliance forever.
			return failed(fmt.Errorf("mode %q: special bits are not supported (use 0000-0777)", cfg.Mode))
		}
		mode = os.FileMode(v)
	}

	// Windows has no POSIX permission bits: Go only maps the read-only flag, so
	// comparing modes there would loop forever between "changed" and "drifted".
	checkMode := e.GOOS != "windows"

	current, err := os.ReadFile(path)
	if err == nil && string(current) == cfg.Content {
		if info, statErr := os.Stat(path); !checkMode || (statErr == nil && info.Mode().Perm() == mode.Perm()) {
			return compliant("up to date: " + path)
		}
		if !e.DryRun {
			if err := os.Chmod(path, mode); err != nil {
				return failed(err)
			}
			return changed("fixed permissions on " + path)
		}
		return drifted("would fix permissions on " + path)
	}

	if e.DryRun {
		return drifted("would write " + path)
	}
	if err := writeFileAtomic(path, []byte(cfg.Content), mode); err != nil {
		return failed(err)
	}
	return changed("wrote " + path)
}

// writeDeny describes locations where a policy-driven file write is equivalent
// to arbitrary code execution or credential theft. This is defence in depth,
// not a security boundary: see docs/design.md, "the file module is a write
// primitive". A server that can choose any other path can still plant code.
var writeDeny = []string{
	"/etc/sudoers", "/etc/passwd", "/etc/shadow", "/etc/group",
	"/etc/gshadow", "/etc/ld.so.preload", "/etc/rc.local", "/etc/profile.d",
	"/etc/init.d", "/etc/cron", "/var/spool/cron", "/etc/systemd/system",
	"/usr/lib/systemd/system", "/lib/systemd/system", "/etc/apt/apt.conf.d",
	"/library/launchdaemons", "/library/launchagents", "/system/library",
	"/etc/fleet", "/programdata/fleet", "/program files/fleet",
	"\\windows\\system32", "\\windows\\syswow64", "\\windows\\sysnative",
}

// isRootedPath reports whether p is rooted. A leading separator counts even on
// Windows: "/etc/x" and "\\etc\\x" both mean the volume root, and a policy that
// says "etc/x" would otherwise resolve against the daemon's working directory.
func isRootedPath(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`)
}

// checkFilePath rejects paths the agent must never write on policy command.
func checkFilePath(e *Env, path string) error {
	rel := path
	if e.Root != "" {
		rel = strings.TrimPrefix(rel, e.Root)
	}
	rel = strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	for _, d := range writeDeny {
		if strings.HasPrefix(rel, strings.ReplaceAll(d, "\\", "/")) {
			return fmt.Errorf("path %q is not managed by policy (autostart, credentials and agent state are off limits)", path)
		}
	}
	if exe, err := os.Executable(); err == nil {
		if a, err1 := filepath.Abs(exe); err1 == nil {
			if b, err2 := filepath.Abs(path); err2 == nil && strings.EqualFold(a, b) {
				return fmt.Errorf("refusing to overwrite the running agent (%s)", b)
			}
		}
	}
	return nil
}
