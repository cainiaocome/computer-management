//go:build !windows

package main

import (
	"context"
	"log/slog"
)

// runAsService is a no-op on platforms without the Windows service model;
// macOS uses launchd and Linux uses systemd, both of which run the process in
// the foreground.
func runAsService(_ *slog.Logger, _ func(context.Context) error) (bool, error) {
	return false, nil
}
