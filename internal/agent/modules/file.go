package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	path := e.Resolve(cfg.Path)

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
