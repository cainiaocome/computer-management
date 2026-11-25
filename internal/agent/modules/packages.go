package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

// Package is one desired package.
type Package struct {
	Name string `json:"name"` // manager-visible name, e.g. "firefox" or "Mozilla.Firefox"
	ID   string `json:"id"`   // winget id / cask name; falls back to Name
}

// PackagesConfig is the config of a "packages" item.
type PackagesConfig struct {
	// Manager is one of auto, brew, winget, apt. auto picks by platform.
	Manager string    `json:"manager"`
	Install []Package `json:"install"`
}

// Packages ensures packages are installed via the platform package manager.
type Packages struct{}

func (Packages) Type() string { return policy.TypePackages }

func (Packages) Apply(ctx context.Context, item protocol.Item) Outcome {
	e := env(ctx)
	var cfg PackagesConfig
	if err := json.Unmarshal(item.Config, &cfg); err != nil {
		return failed(fmt.Errorf("decode config: %w", err))
	}
	if len(cfg.Install) == 0 {
		return compliant("no packages requested")
	}

	mgr, err := resolveManager(e, cfg.Manager)
	if err != nil {
		return unsupported(err.Error())
	}

	missing, err := missingPackages(ctx, e, mgr, cfg.Install)
	if err != nil {
		return failed(err)
	}
	if len(missing) == 0 {
		return compliant(fmt.Sprintf("all %d packages present via %s", len(cfg.Install), mgr))
	}

	names := make([]string, 0, len(missing))
	for _, p := range missing {
		names = append(names, p.Name)
	}

	if err := checkNames(missing); err != nil {
		return failed(err)
	}

	if e.DryRun {
		return Outcome{
			Status:  protocol.StatusDrifted,
			Message: fmt.Sprintf("would install via %s: %s", mgr, strings.Join(names, ", ")),
			Detail:  map[string]any{"missing": names, "manager": mgr},
		}
	}

	// winget takes one package per invocation, so a single call would silently
	// install only the first missing package.
	for _, batch := range installBatches(mgr, missing) {
		if _, err := e.Run.Run(ctx, mgr, installArgs(mgr, batch)...); err != nil {
			return failed(fmt.Errorf("%s install: %w", mgr, err))
		}
	}

	stillMissing, err := missingPackages(ctx, e, mgr, missing)
	if err != nil {
		return failed(err)
	}
	if len(stillMissing) > 0 {
		names = names[:0]
		for _, p := range stillMissing {
			names = append(names, p.Name)
		}
		return Outcome{
			Status:  protocol.StatusError,
			Message: "packages still missing after install: " + strings.Join(names, ", "),
			Detail:  map[string]any{"missing": names, "manager": mgr},
		}
	}
	return changed(fmt.Sprintf("installed via %s: %s", mgr, strings.Join(names, ", ")))
}

func resolveManager(e *Env, want string) (string, error) {
	if want == "" || want == "auto" {
		switch e.GOOS {
		case "darwin":
			want = "brew"
		case "windows":
			want = "winget"
		case "linux":
			want = "apt-get"
		default:
			return "", fmt.Errorf("no package manager for %s", e.GOOS)
		}
	}
	if _, err := e.Run.LookPath(want); err != nil {
		return "", fmt.Errorf("package manager %q not available", want)
	}
	return want, nil
}

// installBatches splits the work so that each invocation installs something
// meaningful: winget only accepts one package at a time.
func installBatches(mgr string, pkgs []Package) [][]Package {
	if mgr != "winget" {
		return [][]Package{pkgs}
	}
	out := make([][]Package, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, []Package{p})
	}
	return out
}

// checkNames rejects package identifiers that would be read as options.
//
// There is no shell involved, but "apt-get install -oDPkg::Pre-Invoke::=..."
// is still a way to run a command, and a name beginning with "-" can never be a
// real package. See docs/design.md: the agent must not become an exec channel.
func checkNames(pkgs []Package) error {
	for _, p := range pkgs {
		for _, ref := range []string{p.Name, p.ID} {
			if ref == "" {
				continue
			}
			if strings.HasPrefix(ref, "-") {
				return fmt.Errorf("package %q: identifiers may not start with '-'", ref)
			}
			if strings.ContainsAny(ref, "\x00\n\r") {
				return fmt.Errorf("package %q: control characters are not allowed", ref)
			}
		}
	}
	return nil
}

func installArgs(mgr string, pkgs []Package) []string {
	switch mgr {
	case "brew":
		out := []string{"install"}
		for _, p := range pkgs {
			out = append(out, pkgRef(mgr, p))
		}
		return out
	case "apt-get":
		out := []string{"install", "-y"}
		for _, p := range pkgs {
			out = append(out, pkgRef(mgr, p))
		}
		return out
	case "winget":
		return []string{"install", "--silent", "--accept-package-agreements", "--accept-source-agreements", pkgRef(mgr, pkgs[0])}
	}
	return nil
}

func pkgRef(mgr string, p Package) string {
	switch mgr {
	case "winget":
		if p.ID != "" {
			return p.ID
		}
	case "brew":
		if p.ID != "" {
			return p.ID
		}
	}
	return p.Name
}

func missingPackages(ctx context.Context, e *Env, mgr string, want []Package) ([]Package, error) {
	var missing []Package
	for _, p := range want {
		present, err := hasPackage(ctx, e, mgr, p)
		if err != nil {
			return nil, err
		}
		if !present {
			missing = append(missing, p)
		}
	}
	return missing, nil
}

func hasPackage(ctx context.Context, e *Env, mgr string, p Package) (bool, error) {
	ref := pkgRef(mgr, p)
	switch mgr {
	case "brew":
		out, _ := e.Run.Run(ctx, "brew", "list", "--formula", "--versions")
		if strings.Contains(out, ref+" ") || strings.HasSuffix(strings.TrimSpace(out), ref) {
			return true, nil
		}
		out, _ = e.Run.Run(ctx, "brew", "list", "--cask", "--versions")
		return strings.Contains(out, ref+" ") || strings.HasSuffix(strings.TrimSpace(out), ref), nil
	case "apt-get":
		out, err := e.Run.Run(ctx, "dpkg-query", "-W", "-f=${Status}", ref)
		if err != nil {
			return false, nil
		}
		return strings.Contains(out, "install ok installed"), nil
	case "winget":
		out, err := e.Run.Run(ctx, "winget", "list", "--id", ref, "--exact",
			"--accept-source-agreements", "--disable-interactivity")
		if err != nil {
			return false, nil
		}
		return strings.Contains(strings.ToLower(out), strings.ToLower(ref)), nil
	}
	return false, fmt.Errorf("unsupported package manager %q", mgr)
}
