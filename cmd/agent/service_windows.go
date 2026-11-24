//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sys/windows/svc"
)

// serviceName must match the name used by scripts/install-windows.ps1.
const serviceName = "FleetAgent"

// runAsService reports whether we were started by the Service Control Manager,
// and if so runs the agent loop as a Windows service until SCM asks it to stop.
//
// Without this, a service whose binary never calls StartServiceCtrlDispatcher
// fails to start with error 1053 ("did not respond to the start or control
// request in a timely fashion") after a 30 second timeout.
func runAsService(log *slog.Logger, fn func(context.Context) error) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false, fmt.Errorf("detect service context: %w", err)
	}
	if !isService {
		return false, nil
	}
	return true, svc.Run(serviceName, &handler{log: log, fn: fn})
}

type handler struct {
	log *slog.Logger
	fn  func(context.Context) error
}

func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- h.fn(ctx) }()

	status <- svc.Status{State: svc.Running, Accepts: accepted}
	h.log.Info("running as a windows service", "service", serviceName)

	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				h.log.Info("stop requested")
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		case err := <-done:
			// The loop returned on its own: let SCM restart the service.
			if err != nil {
				h.log.Error("agent loop exited", "error", err)
				return false, 1
			}
			return false, 0
		}
	}
}
