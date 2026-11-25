package modules

import (
	"encoding/json"
	"testing"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

func packagesItem(t *testing.T, cfg string) protocol.Item {
	t.Helper()
	return protocol.Item{ID: "pk", Type: policy.TypePackages, Class: string(policy.Enforce),
		Enabled: true, Config: json.RawMessage(cfg)}
}

func TestPackagesInstallsWhenMissing(t *testing.T) {
	installed := false
	r := &fakeRunner{
		lookPath: map[string]bool{"brew": true},
		handler: func(name string, args []string) (string, error) {
			if name != "brew" || len(args) < 1 {
				return "", nil
			}
			switch args[0] {
			case "list":
				if installed {
					return "firefox 131.0\n", nil
				}
				return "wget 1.21\n", nil
			case "install":
				installed = true
				return "installed", nil
			}
			return "", nil
		},
	}
	e := testEnv("darwin", t.TempDir(), r)
	item := packagesItem(t, `{"manager":"brew","install":[{"name":"firefox"}]}`)

	out := Packages{}.Apply(ctxWith(e), item)
	if out.Status != protocol.StatusChanged {
		t.Fatalf("want changed, got %s (%s)", out.Status, out.Message)
	}
	if !r.called(t, "brew", "install", "firefox") {
		t.Fatalf("brew install not called: %v", r.calls)
	}

	out = Packages{}.Apply(ctxWith(e), item)
	if out.Status != protocol.StatusCompliant {
		t.Fatalf("want compliant, got %s (%s)", out.Status, out.Message)
	}
}

func TestPackagesDryRunDoesNotInstall(t *testing.T) {
	r := &fakeRunner{
		lookPath: map[string]bool{"brew": true},
		handler:  func(string, []string) (string, error) { return "", nil },
	}
	e := testEnv("darwin", t.TempDir(), r)
	e.DryRun = true
	out := Packages{}.Apply(ctxWith(e), packagesItem(t, `{"install":[{"name":"firefox"}]}`))
	if out.Status != protocol.StatusDrifted {
		t.Fatalf("want drifted, got %s", out.Status)
	}
	if r.called(t, "brew", "install") {
		t.Fatal("dry run must not install")
	}
}

func TestPackagesUnsupportedManager(t *testing.T) {
	e := testEnv("darwin", t.TempDir(), &fakeRunner{lookPath: map[string]bool{}})
	out := Packages{}.Apply(ctxWith(e), packagesItem(t, `{"install":[{"name":"firefox"}]}`))
	if out.Status != protocol.StatusUnsupported {
		t.Fatalf("want unsupported, got %s (%s)", out.Status, out.Message)
	}
}

func TestPackagesNoopOnEmptyList(t *testing.T) {
	e := testEnv("linux", t.TempDir(), &fakeRunner{})
	out := Packages{}.Apply(ctxWith(e), packagesItem(t, `{"install":[]}`))
	if out.Status != protocol.StatusCompliant {
		t.Fatalf("want compliant, got %s", out.Status)
	}
}

func TestCheckNamesRejectsOptionInjection(t *testing.T) {
	// "apt-get install -y -oDPkg::Pre-Invoke::=/bin/sh -c id" runs a shell.
	bad := []Package{{Name: "-oDPkg::Pre-Invoke::=/bin/sh -c id"}}
	if err := checkNames(bad); err == nil {
		t.Fatal("a package name starting with '-' must be rejected")
	}
	good := []Package{{Name: "git"}, {Name: "ublock-origin"}}
	if err := checkNames(good); err != nil {
		t.Fatalf("normal names must pass: %v", err)
	}

	ctrl := []Package{{Name: "git\nrm -rf /"}}
	if err := checkNames(ctrl); err == nil {
		t.Fatal("control characters must be rejected")
	}
}

func TestInstallBatchesWingetIsOnePerCall(t *testing.T) {
	pkgs := []Package{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	if got := len(installBatches("apt-get", pkgs)); got != 1 {
		t.Fatalf("apt-get should install in one call, got %d", got)
	}
	if got := len(installBatches("winget", pkgs)); got != 3 {
		t.Fatalf("winget needs one call per package, got %d", got)
	}
}
