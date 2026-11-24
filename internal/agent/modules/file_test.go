package modules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

func fileItem(t *testing.T, cfg string) protocol.Item {
	t.Helper()
	return protocol.Item{ID: "f", Type: policy.TypeFile, Class: string(policy.Enforce),
		Enabled: true, Config: json.RawMessage(cfg)}
}

func TestFileApplyLifecycle(t *testing.T) {
	root := t.TempDir()
	e := testEnv(runtime.GOOS, root, &fakeRunner{})
	item := fileItem(t, `{"path":"/etc/fleet/hello.conf","content":"a=1\n","mode":"0640"}`)

	out := File{}.Apply(ctxWith(e), item)
	if out.Status != protocol.StatusChanged {
		t.Fatalf("want changed, got %s (%s)", out.Status, out.Message)
	}
	path := filepath.Join(root, "etc/fleet/hello.conf")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o640 {
			t.Fatalf("mode = %o, want 640", perm)
		}
	}

	out = File{}.Apply(ctxWith(e), item)
	if out.Status != protocol.StatusCompliant {
		t.Fatalf("want compliant, got %s (%s)", out.Status, out.Message)
	}

	// Wrong permission with correct content is repaired in place.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		out = File{}.Apply(ctxWith(e), item)
		if out.Status != protocol.StatusChanged {
			t.Fatalf("want changed after chmod, got %s (%s)", out.Status, out.Message)
		}
	}
}

func TestFileEnsureAbsent(t *testing.T) {
	root := t.TempDir()
	e := testEnv(runtime.GOOS, root, &fakeRunner{})
	write := fileItem(t, `{"path":"/tmp/f","content":"x"}`)
	File{}.Apply(ctxWith(e), write)

	out := File{}.Apply(ctxWith(e), fileItem(t, `{"path":"/tmp/f","ensure":"absent"}`))
	if out.Status != protocol.StatusChanged {
		t.Fatalf("want changed, got %s", out.Status)
	}
	if fileExists(filepath.Join(root, "tmp/f")) {
		t.Fatal("file should be gone")
	}
}

func TestFileRejectsBadMode(t *testing.T) {
	e := testEnv(runtime.GOOS, t.TempDir(), &fakeRunner{})
	out := File{}.Apply(ctxWith(e), fileItem(t, `{"path":"/tmp/x","content":"","mode":"nope"}`))
	if out.Status != protocol.StatusError {
		t.Fatalf("want error, got %s", out.Status)
	}
}
