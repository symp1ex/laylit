package entry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSupervisorReconcilesPhysicalConsoleSession(t *testing.T) {
	sessions := &fakeConsoleSessions{}
	launcher := newFakeHelperLauncher()
	notifications := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := runFakeSupervisor(ctx, sessions, launcher, notifications, 40*time.Millisecond)
	defer func() {
		cancel()
		<-done
	}()

	assertNoHelperStart(t, launcher.starts)
	sessions.set(10, true, nil)
	triggerReconcile(notifications)
	first := expectHelperStart(t, launcher.starts, 10)

	triggerReconcile(notifications)
	assertNoHelperStart(t, launcher.starts)

	first.blockStop()
	sessions.set(20, true, nil)
	triggerReconcile(notifications)
	expectHelperStop(t, first)
	assertNoHelperStart(t, launcher.starts)
	first.releaseStop()
	second := expectHelperStart(t, launcher.starts, 20)
	if launcher.maximumRunning() != 1 {
		t.Fatalf("maximum simultaneous helpers = %d, want 1", launcher.maximumRunning())
	}

	triggerReconcile(notifications) // An RDP/session event did not change the physical console.
	assertNoHelperStart(t, launcher.starts)

	sessions.set(10, true, nil)
	triggerReconcile(notifications)
	expectHelperStop(t, second)
	third := expectHelperStart(t, launcher.starts, 10)

	sessions.set(0, false, nil)
	triggerReconcile(notifications)
	expectHelperStop(t, third)
	assertNoHelperStart(t, launcher.starts)
}

func TestSupervisorStopWaitsAndPreventsNewHelpers(t *testing.T) {
	sessions := &fakeConsoleSessions{}
	sessions.set(10, true, nil)
	launcher := newFakeHelperLauncher()
	notifications := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := runFakeSupervisor(ctx, sessions, launcher, notifications, 20*time.Millisecond)
	helper := expectHelperStart(t, launcher.starts, 10)
	helper.blockStop()
	cancel()
	expectHelperStop(t, helper)

	sessions.set(20, true, nil)
	triggerReconcile(notifications)
	select {
	case err := <-done:
		t.Fatalf("supervisor returned before helper exited: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	assertNoHelperStart(t, launcher.starts)
	helper.releaseStop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("supervisor did not finish after helper stopped")
	}
	assertNoHelperStart(t, launcher.starts)
}

func TestSupervisorRechecksConsoleSessionAfterHelperStarts(t *testing.T) {
	sessions := &fakeConsoleSessions{}
	sessions.set(10, true, nil)
	launcher := newFakeHelperLauncher()
	notifications := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := sessionSupervisor{
		activeConsoleSession: sessions.active,
		startHelper: func(ctx context.Context, sessionID uint32) (helperProcess, error) {
			helper, err := launcher.start(ctx, sessionID)
			if sessionID == 10 {
				sessions.set(20, true, nil)
			}
			return helper, err
		},
		restartBackoff: 20 * time.Millisecond,
		stopTimeout:    time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, notifications) }()

	first := expectHelperStart(t, launcher.starts, 10)
	expectHelperStop(t, first)
	second := expectHelperStart(t, launcher.starts, 20)
	cancel()
	expectHelperStop(t, second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRestartsUnexpectedExitWithBackoff(t *testing.T) {
	sessions := &fakeConsoleSessions{}
	sessions.set(10, true, nil)
	launcher := newFakeHelperLauncher()
	notifications := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := runFakeSupervisor(ctx, sessions, launcher, notifications, 80*time.Millisecond)
	first := expectHelperStart(t, launcher.starts, 10)
	first.finish(errors.New("crash"))
	triggerReconcile(notifications)
	assertNoHelperStart(t, launcher.starts)
	second := expectHelperStart(t, launcher.starts, 10)
	cancel()
	expectHelperStop(t, second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRetriesLaunchFailureWithBackoff(t *testing.T) {
	sessions := &fakeConsoleSessions{}
	sessions.set(10, true, nil)
	launcher := newFakeHelperLauncher()
	launcher.failStarts = 1
	reported := make(chan error, 2)
	notifications := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := sessionSupervisor{
		activeConsoleSession: sessions.active,
		startHelper:          launcher.start,
		reportError:          func(err error) { reported <- err },
		restartBackoff:       80 * time.Millisecond,
		stopTimeout:          time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, notifications) }()

	expectStartAttempt(t, launcher.attempts, 10)
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("launch failure was not reported")
	}
	triggerReconcile(notifications)
	assertNoStartAttempt(t, launcher.attempts)
	helper := expectHelperStart(t, launcher.starts, 10)
	cancel()
	expectHelperStop(t, helper)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func runFakeSupervisor(ctx context.Context, sessions *fakeConsoleSessions, launcher *fakeHelperLauncher, notifications <-chan struct{}, backoff time.Duration) <-chan error {
	supervisor := sessionSupervisor{
		activeConsoleSession: sessions.active,
		startHelper:          launcher.start,
		restartBackoff:       backoff,
		stopTimeout:          time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, notifications) }()
	return done
}

type fakeConsoleSessions struct {
	mu      sync.Mutex
	session uint32
	usable  bool
	err     error
}

func (sessions *fakeConsoleSessions) set(session uint32, usable bool, err error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.session, sessions.usable, sessions.err = session, usable, err
}

func (sessions *fakeConsoleSessions) active() (uint32, bool, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.session, sessions.usable, sessions.err
}

type fakeHelperLauncher struct {
	mu         sync.Mutex
	failStarts int
	running    int
	maxRunning int
	starts     chan *fakeHelperProcess
	attempts   chan uint32
}

func newFakeHelperLauncher() *fakeHelperLauncher {
	return &fakeHelperLauncher{starts: make(chan *fakeHelperProcess, 16), attempts: make(chan uint32, 16)}
}

func (launcher *fakeHelperLauncher) start(_ context.Context, sessionID uint32) (helperProcess, error) {
	launcher.attempts <- sessionID
	launcher.mu.Lock()
	if launcher.failStarts > 0 {
		launcher.failStarts--
		launcher.mu.Unlock()
		return nil, errors.New("launch failed")
	}
	helper := &fakeHelperProcess{sessionID: sessionID, done: make(chan struct{}), stopCalled: make(chan struct{}), launcher: launcher}
	launcher.running++
	if launcher.running > launcher.maxRunning {
		launcher.maxRunning = launcher.running
	}
	launcher.mu.Unlock()
	launcher.starts <- helper
	return helper, nil
}

func (launcher *fakeHelperLauncher) maximumRunning() int {
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	return launcher.maxRunning
}

type fakeHelperProcess struct {
	sessionID  uint32
	done       chan struct{}
	stopCalled chan struct{}
	launcher   *fakeHelperLauncher

	mu         sync.Mutex
	exitErr    error
	stopBlock  chan struct{}
	stopOnce   sync.Once
	finishOnce sync.Once
}

func (helper *fakeHelperProcess) SessionID() uint32     { return helper.sessionID }
func (helper *fakeHelperProcess) Done() <-chan struct{} { return helper.done }
func (helper *fakeHelperProcess) ExitError() error {
	helper.mu.Lock()
	defer helper.mu.Unlock()
	return helper.exitErr
}
func (helper *fakeHelperProcess) Stop(ctx context.Context) error {
	helper.stopOnce.Do(func() { close(helper.stopCalled) })
	helper.mu.Lock()
	block := helper.stopBlock
	helper.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	helper.finish(nil)
	return nil
}
func (helper *fakeHelperProcess) blockStop() {
	helper.mu.Lock()
	defer helper.mu.Unlock()
	helper.stopBlock = make(chan struct{})
}
func (helper *fakeHelperProcess) releaseStop() {
	helper.mu.Lock()
	defer helper.mu.Unlock()
	close(helper.stopBlock)
}
func (helper *fakeHelperProcess) finish(err error) {
	helper.finishOnce.Do(func() {
		helper.mu.Lock()
		helper.exitErr = err
		helper.mu.Unlock()
		helper.launcher.mu.Lock()
		helper.launcher.running--
		helper.launcher.mu.Unlock()
		close(helper.done)
	})
}

func triggerReconcile(notifications chan<- struct{}) {
	select {
	case notifications <- struct{}{}:
	default:
	}
}

func expectHelperStart(t *testing.T, starts <-chan *fakeHelperProcess, sessionID uint32) *fakeHelperProcess {
	t.Helper()
	select {
	case helper := <-starts:
		if helper.sessionID != sessionID {
			t.Fatalf("started helper for session %d, want %d", helper.sessionID, sessionID)
		}
		return helper
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for helper in session %d", sessionID)
		return nil
	}
}

func expectStartAttempt(t *testing.T, attempts <-chan uint32, sessionID uint32) {
	t.Helper()
	select {
	case got := <-attempts:
		if got != sessionID {
			t.Fatalf("start attempted for session %d, want %d", got, sessionID)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for start attempt in session %d", sessionID)
	}
}

func expectHelperStop(t *testing.T, helper *fakeHelperProcess) {
	t.Helper()
	select {
	case <-helper.stopCalled:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for helper %d stop", helper.sessionID)
	}
}

func assertNoHelperStart(t *testing.T, starts <-chan *fakeHelperProcess) {
	t.Helper()
	select {
	case helper := <-starts:
		t.Fatalf("unexpected helper start for session %d", helper.sessionID)
	case <-time.After(30 * time.Millisecond):
	}
}

func assertNoStartAttempt(t *testing.T, attempts <-chan uint32) {
	t.Helper()
	select {
	case sessionID := <-attempts:
		t.Fatalf("unexpected helper start attempt for session %d", sessionID)
	case <-time.After(30 * time.Millisecond):
	}
}
