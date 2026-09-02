//go:build windows

package entry

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestServiceHandlerCancelsAndWaitsForRuntime(t *testing.T) {
	for _, command := range []svc.Cmd{svc.Stop, svc.Shutdown} {
		t.Run(serviceCommandName(command), func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			handler := serviceHandler{run: func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
				return ctx.Err()
			}}

			requests := make(chan svc.ChangeRequest, 1)
			statuses := make(chan svc.Status, 4)
			result := make(chan serviceResult, 1)
			go func() {
				specific, code := handler.Execute(nil, requests, statuses)
				result <- serviceResult{specific: specific, code: code}
			}()

			assertServiceStatus(t, statuses, svc.StartPending)
			assertServiceStatus(t, statuses, svc.Running)
			<-started
			requests <- svc.ChangeRequest{Cmd: command}
			assertServiceStatus(t, statuses, svc.StopPending)
			<-canceled

			select {
			case got := <-result:
				t.Fatalf("Execute returned before runtime shutdown completed: %+v", got)
			case <-time.After(50 * time.Millisecond):
			}

			close(release)
			select {
			case got := <-result:
				if got != (serviceResult{}) {
					t.Fatalf("Execute result = %+v, want clean service exit", got)
				}
			case <-time.After(time.Second):
				t.Fatal("Execute did not return after runtime shutdown completed")
			}
		})
	}
}

func TestServiceHandlerReportsRuntimeFailure(t *testing.T) {
	handler := serviceHandler{run: func(context.Context) error {
		return context.DeadlineExceeded
	}}
	statuses := make(chan svc.Status, 2)
	specific, code := handler.Execute(nil, make(chan svc.ChangeRequest), statuses)
	if !specific || code != 1 {
		t.Fatalf("Execute result = (%t, %d), want service-specific exit code 1", specific, code)
	}
	assertServiceStatus(t, statuses, svc.StartPending)
	assertServiceStatus(t, statuses, svc.Running)
}

func TestRunWithoutArgumentsIsReservedForUI(t *testing.T) {
	if err := Run(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("Run(nil args) error = %v, want nil", err)
	}
}

func TestServiceModeIsExplicitAndExclusive(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"-service"}, want: true},
		{args: nil, want: false},
		{args: []string{"--service"}, want: false},
		{args: []string{"-service", "info"}, want: false},
	} {
		if got := isServiceMode(test.args); got != test.want {
			t.Errorf("isServiceMode(%q) = %t, want %t", test.args, got, test.want)
		}
	}
}

type serviceResult struct {
	specific bool
	code     uint32
}

func assertServiceStatus(t *testing.T, statuses <-chan svc.Status, want svc.State) {
	t.Helper()
	select {
	case got := <-statuses:
		if got.State != want {
			t.Fatalf("service state = %v, want %v", got.State, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for service state %v", want)
	}
}

func serviceCommandName(command svc.Cmd) string {
	if command == svc.Stop {
		return "Stop"
	}
	return "Shutdown"
}
