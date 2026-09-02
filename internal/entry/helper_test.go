package entry

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"
)

func TestSessionHelperStopCancelsAndWaitsForAutomaticRuntime(t *testing.T) {
	service, helper := net.Pipe()
	defer service.Close()
	canceled := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveSessionHelper(helper, "token", func(ctx context.Context, ready func() error) error {
			if err := ready(); err != nil {
				return err
			}
			<-ctx.Done()
			close(canceled)
			<-release
			return nil
		})
	}()

	reader := bufio.NewReader(service)
	if hello, err := readProtocolLine(reader); err != nil || hello != helperHelloPrefix+"token" {
		t.Fatalf("hello = %q, %v", hello, err)
	}
	if ready, err := readProtocolLine(reader); err != nil || ready != helperReady {
		t.Fatalf("ready = %q, %v", ready, err)
	}
	if err := writeProtocolLine(service, helperStop); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("STOP did not cancel automatic runtime")
	}
	select {
	case err := <-done:
		t.Fatalf("helper returned before automatic runtime shutdown completed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("helper did not exit after automatic runtime shutdown")
	}
}

func TestSessionHelperModeIsInternalAndExclusive(t *testing.T) {
	if !isSessionHelperMode([]string{sessionHelperArgument, `\\.\pipe\test`, "token"}) {
		t.Fatal("valid internal helper invocation was not recognized")
	}
	for _, args := range [][]string{
		nil,
		{sessionHelperArgument},
		{sessionHelperArgument, `\\.\pipe\test`},
		{sessionHelperArgument, `\\.\pipe\test`, "token", "extra"},
		{"--debug"},
		{"info"},
	} {
		if isSessionHelperMode(args) {
			t.Fatalf("isSessionHelperMode(%q) = true", args)
		}
	}
}
