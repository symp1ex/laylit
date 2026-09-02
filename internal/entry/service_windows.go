//go:build windows

package entry

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"

	"laylit/internal/logger"
	"laylit/internal/logsettings"
)

const (
	serviceWaitHint           = 10_000
	serviceCheckpointInterval = time.Second
)

type serviceHandler struct {
	run func(context.Context, <-chan struct{}) error
}

func runService() error {
	events, _ := eventlog.Open(serviceName)
	if events != nil {
		defer events.Close()
	}
	settings, settingsPath, err := logsettings.LoadOrCreate()
	if err != nil {
		if events != nil {
			_ = events.Error(1, err.Error())
		}
		return err
	}
	logDirectory, err := applicationLogDirectory()
	if err != nil {
		if events != nil {
			_ = events.Error(1, err.Error())
		}
		return err
	}
	logger.Configure(logDirectory, settings.LogLevel, settings.LogRetainDays)
	defer logger.Shutdown(2 * time.Second)
	logger.Laylit.Infof("service starting; settings=%s", settingsPath)
	reportError := func(err error) {
		logger.Laylit.Warnf("service warning: %v", err)
		if events != nil {
			_ = events.Error(1, err.Error())
		}
	}
	err = svc.Run(serviceName, serviceHandler{run: func(ctx context.Context, notifications <-chan struct{}) error {
		supervisor := sessionSupervisor{
			activeConsoleSession: activePhysicalConsoleSession,
			startHelper:          startSessionHelper,
			reportError:          reportError,
		}
		return supervisor.Run(ctx, notifications)
	}})
	if err != nil {
		logger.Laylit.Errorf("service stopped: %v", err)
		return err
	}
	logger.Laylit.Infof("service stopped")
	return nil
}

func (handler serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	statuses <- svc.Status{State: svc.StartPending, WaitHint: serviceWaitHint}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifications := make(chan struct{}, 1)
	runDone := make(chan error, 1)
	go func() {
		runDone <- handler.run(ctx, notifications)
	}()

	status := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptSessionChange}
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
			case svc.SessionChange:
				select {
				case notifications <- struct{}{}:
				default:
				}
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
