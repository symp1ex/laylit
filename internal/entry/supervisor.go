package entry

import (
	"context"
	"fmt"
	"time"
)

type helperProcess interface {
	SessionID() uint32
	Done() <-chan struct{}
	ExitError() error
	Stop(context.Context) error
}

type sessionSupervisor struct {
	activeConsoleSession func() (uint32, bool, error)
	startHelper          func(context.Context, uint32) (helperProcess, error)
	reportError          func(error)
	restartBackoff       time.Duration
	stopTimeout          time.Duration
}

func (supervisor sessionSupervisor) Run(ctx context.Context, notifications <-chan struct{}) error {
	restartBackoff := supervisor.restartBackoff
	if restartBackoff <= 0 {
		restartBackoff = 2 * time.Second
	}
	stopTimeout := supervisor.stopTimeout
	if stopTimeout <= 0 {
		stopTimeout = 10 * time.Second
	}

	var helper helperProcess
	var retryTimer *time.Timer
	var retry <-chan time.Time
	var retrySession uint32
	var retryAll bool
	reconcile := true

	clearRetry := func() {
		if retryTimer != nil {
			if !retryTimer.Stop() {
				select {
				case <-retryTimer.C:
				default:
				}
			}
		}
		retryTimer = nil
		retry = nil
		retryAll = false
	}
	scheduleRetry := func(sessionID uint32, all bool) {
		clearRetry()
		retrySession = sessionID
		retryAll = all
		retryTimer = time.NewTimer(restartBackoff)
		retry = retryTimer.C
	}
	report := func(err error) {
		if err != nil && supervisor.reportError != nil {
			supervisor.reportError(err)
		}
	}

	for {
		if reconcile {
			reconcile = false
			sessionID, usable, err := supervisor.activeConsoleSession()
			if err != nil {
				report(fmt.Errorf("determine active console session: %w", err))
				scheduleRetry(0, true)
			} else if !usable {
				clearRetry()
				if helper != nil {
					stopped, stopErr := stopSupervisedHelper(ctx, helper, stopTimeout)
					report(stopErr)
					if stopped {
						helper = nil
					} else {
						scheduleRetry(helper.SessionID(), true)
					}
				}
			} else if helper != nil && helper.SessionID() == sessionID {
				clearRetry()
			} else {
				if retry != nil && (retryAll || retrySession == sessionID) && helper == nil {
					// A notification for the same session must not bypass restart backoff.
				} else {
					clearRetry()
					if helper != nil {
						stopped, stopErr := stopSupervisedHelper(ctx, helper, stopTimeout)
						report(stopErr)
						if !stopped {
							scheduleRetry(helper.SessionID(), true)
							continue
						}
						helper = nil
					}
					if ctx.Err() == nil {
						started, startErr := supervisor.startHelper(ctx, sessionID)
						if startErr != nil {
							report(fmt.Errorf("start helper for console session %d: %w", sessionID, startErr))
							scheduleRetry(sessionID, false)
						} else {
							helper = started
							reconcile = true
							continue
						}
					}
				}
			}
		}

		var helperDone <-chan struct{}
		if helper != nil {
			helperDone = helper.Done()
		}
		select {
		case <-ctx.Done():
			clearRetry()
			if helper == nil {
				return nil
			}
			stopped, err := stopSupervisedHelper(context.Background(), helper, stopTimeout)
			report(err)
			if !stopped {
				return fmt.Errorf("stop helper for console session %d: %w", helper.SessionID(), err)
			}
			return nil
		case <-notifications:
			reconcile = true
		case <-helperDone:
			sessionID := helper.SessionID()
			exitErr := helper.ExitError()
			if exitErr == nil {
				exitErr = fmt.Errorf("process exited without an error")
			}
			report(fmt.Errorf("helper for console session %d exited unexpectedly: %w", sessionID, exitErr))
			helper = nil
			scheduleRetry(sessionID, false)
		case <-retry:
			clearRetry()
			reconcile = true
		}
	}
}

func stopSupervisedHelper(parent context.Context, helper helperProcess, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	err := helper.Stop(ctx)
	select {
	case <-helper.Done():
		return true, err
	default:
		if err == nil {
			err = fmt.Errorf("helper stop returned before process exit")
		}
		return false, err
	}
}
