package modules

import (
	"context"
	"errors"
	"testing"
)

// fakeRunner lets modules be tested without touching the real OS.
type fakeRunner struct {
	lookPath map[string]bool
	handler  func(name string, args []string) (string, error)
	calls    [][]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.handler == nil {
		return "", nil
	}
	return f.handler(name, args)
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if f.lookPath[name] {
		return "/usr/bin/" + name, nil
	}
	return "", errors.New("not found")
}

func (f *fakeRunner) ReadFile(name string) ([]byte, error) { return nil, errors.New("not implemented") }

func testEnv(goos, root string, r Runner) *Env {
	return &Env{GOOS: goos, GOARCH: "amd64", Root: root, Run: r, Version: "test"}
}

func ctxWith(e *Env) context.Context { return WithEnv(context.Background(), e) }

func (f *fakeRunner) called(t *testing.T, want ...string) bool {
	t.Helper()
	for _, c := range f.calls {
		if len(c) < len(want) {
			continue
		}
		match := true
		for i := range want {
			if c[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
