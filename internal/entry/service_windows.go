//go:build windows

package entry

import (
	"context"
	"errors"
	"io"
	"time"

	"golang.org/x/sys/windows/svc"
)

const (
	serviceWaitHint           = 10_000
	serviceCheckpointInterval = time.Second
)

type serviceHandler struct {
	run func(context.Context) error
}

func runService() error {
	return svc.Run(serviceName, serviceHandler{
		run: func(ctx context.Context) error {
			return runAutomatic(ctx, false, io.Discard)
		},
	})
}

func (handler serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	statuses <- svc.Status{State: svc.StartPending, WaitHint: serviceWaitHint}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- handler.run(ctx)
	}()

	status := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	statuses <- status
	for {
		select {
		case err := <-runDone:
			return serviceExitCode(err)
		case request, ok := <-requests:
			if !ok {
				return handler.stop(cancel, runDone, statuses)
			}
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- status
			case svc.Stop, svc.Shutdown:
				return handler.stop(cancel, runDone, statuses)
			}
		}
	}
}

func (handler serviceHandler) stop(cancel context.CancelFunc, runDone <-chan error, statuses chan<- svc.Status) (bool, uint32) {
	status := svc.Status{State: svc.StopPending, CheckPoint: 1, WaitHint: serviceWaitHint}
	statuses <- status
	cancel()

	ticker := time.NewTicker(serviceCheckpointInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-runDone:
			return serviceExitCode(err)
		case <-ticker.C:
			status.CheckPoint++
			statuses <- status
		}
	}
}

func serviceExitCode(err error) (bool, uint32) {
	if err == nil || errors.Is(err, context.Canceled) {
		return false, 0
	}
	return true, 1
}
