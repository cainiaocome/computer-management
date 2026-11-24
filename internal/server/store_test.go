package server

import (
	"path/filepath"
	"testing"
)

func TestSQLiteDSN(t *testing.T) {
	cases := map[string]string{
		"fleet.db":                      "file:fleet.db",
		"/var/lib/fleet/fleet.db":       "file:/var/lib/fleet/fleet.db",
		`C:\ProgramData\fleet\fleet.db`: "file:/C:/ProgramData/fleet/fleet.db",
		"file:/tmp/x.db?_pragma=wal":    "file:/tmp/x.db?_pragma=wal",
	}
	for in, want := range cases {
		if got := sqliteDSN(in); got != want {
			t.Errorf("sqliteDSN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenStoreCreatesMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	s, err := OpenStore(filepath.Join(dir, "fleet.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
}
