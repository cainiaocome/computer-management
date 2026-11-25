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
	item := fileItem(t, `{"path":"/etc/example/hello.conf","content":"a=1\n","mode":"0640"}`)

	out := File{}.Apply(ctxWith(e), item)
	if out.Status != protocol.StatusChanged {
		t.Fatalf("want changed, got %s (%s)", out.Status, out.Message)
	}
	path := filepath.Join(root, "etc/example/hello.conf")
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

func TestFileRejectsPathsOutsidePolicy(t *testing.T) {
	root := t.TempDir()
	e := testEnv(runtime.GOOS, root, &fakeRunner{})
	for _, p := range []string{
		"/etc/sudoers.d/boom",
		"/etc/cron.d/boom",
		"/Library/LaunchDaemons/boom.plist",
		"/etc/fleet/agent.json",
		`C:\Windows\System32\drivers\etc\hosts`,
	} {
		out := File{}.Apply(ctxWith(e), fileItem(t, `{"path":"`+p+`","content":"x"}`))
		if out.Status != protocol.StatusError {
			t.Fatalf("%s: want error, got %s", p, out.Status)
		}
	}
}

func TestFileRejectsRelativePathAndSpecialBits(t *testing.T) {
	root := t.TempDir()
	e := testEnv(runtime.GOOS, root, &fakeRunner{})
	out := File{}.Apply(ctxWith(e), fileItem(t, `{"path":"etc/x","content":"x"}`))
	if out.Status != protocol.StatusError {
		t.Fatalf("relative path: want error, got %s", out.Status)
	}
	// 4755 would silently lose the setuid bit and then compare equal forever.
	out = File{}.Apply(ctxWith(e), fileItem(t, `{"path":"/etc/example/x","content":"x","mode":"4755"}`))
	if out.Status != protocol.StatusError {
		t.Fatalf("setuid mode: want error, got %s", out.Status)
	}
	if fileExists(filepath.Join(root, "etc/example/x")) {
		t.Fatal("nothing may be written when the mode is rejected")
	}
}
