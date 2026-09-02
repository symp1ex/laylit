package entry

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	appLogger "laylit/internal/logger"
	"laylit/internal/sessionipc"
)

func TestSessionHelperStopCancelsAndWaitsForAutomaticRuntime(t *testing.T) {
	service, helper := net.Pipe()
	defer service.Close()
	canceled := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveSessionHelper(helper, "token", 42, nil, func(ctx context.Context, ready func() error) error {
			if err := ready(); err != nil {
				return err
			}
			<-ctx.Done()
			close(canceled)
			<-release
			return nil
		})
	}()

	hello, err := sessionipc.ReadFrame(service)
	if err != nil || hello.Type != sessionipc.TypeHello {
		t.Fatalf("hello = %+v, %v", hello, err)
	}
	helloMeta, err := sessionipc.DecodeMetadata[sessionipc.HelloMeta](hello)
	if err != nil || helloMeta.Nonce != "token" || helloMeta.SessionID != 42 || helloMeta.Role != sessionHelperRole {
		t.Fatalf("hello metadata = %+v, %v", helloMeta, err)
	}
	ready, err := sessionipc.ReadFrame(service)
	if err != nil || ready.Type != sessionipc.TypeReady {
		t.Fatalf("ready = %+v, %v", ready, err)
	}
	stop, err := sessionipc.NewFrame(sessionipc.TypeStop, 0, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionipc.WriteFrame(service, stop); err != nil {
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

func TestSessionHelperMultiplexesReadyLogsAndStop(t *testing.T) {
	service, helper := net.Pipe()
	remoteLogs := appLogger.NewRemoteLogSender()
	defer remoteLogs.Close()
	appLogger.ConfigureRemote("DEBUG", remoteLogs.Enqueue)
	runtimeCanceled := make(chan struct{})
	emitAfterReady := make(chan struct{})
	helperDone := make(chan error, 1)
	go func() {
		helperDone <- serveSessionHelper(helper, "token", 7, remoteLogs, func(ctx context.Context, ready func() error) error {
			loggingDone := make(chan struct{})
			go func() {
				defer close(loggingDone)
				for index := 0; index < 100; index++ {
					appLogger.Laylit.Debugf("concurrent message %d", index)
				}
			}()
			if err := ready(); err != nil {
				return err
			}
			<-loggingDone
			<-emitAfterReady
			appLogger.Laylit.Infof("after ready")
			<-ctx.Done()
			close(runtimeCanceled)
			return nil
		})
	}()

	hello, err := sessionipc.ReadFrame(service)
	if err != nil || hello.Type != sessionipc.TypeHello {
		t.Fatalf("hello = %+v, %v", hello, err)
	}
	ready := make(chan struct{}, 1)
	logs := make(chan sessionipc.LogMeta, 128)
	serviceConn := sessionipc.NewConn(service, func(_ context.Context, frame sessionipc.Frame) {
		switch frame.Type {
		case sessionipc.TypeReady:
			ready <- struct{}{}
		case sessionipc.TypeLog:
			meta, decodeErr := sessionipc.DecodeMetadata[sessionipc.LogMeta](frame)
			if decodeErr == nil {
				logs <- meta
			}
		}
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- serviceConn.Serve(context.Background()) }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("READY was not received")
	}
	close(emitAfterReady)
	logDeadline := time.After(time.Second)
	for {
		select {
		case meta := <-logs:
			if meta.Level == "info" && meta.Message == "after ready" {
				goto logReceived
			}
			if meta.Level != "debug" || !strings.HasPrefix(meta.Message, "concurrent message ") {
				t.Fatalf("remote log = %+v", meta)
			}
		case <-logDeadline:
			t.Fatal("remote log after READY was not received")
		}
	}

logReceived:
	stop, err := sessionipc.NewFrame(sessionipc.TypeStop, 0, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := serviceConn.Send(stop); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtimeCanceled:
	case <-time.After(time.Second):
		t.Fatal("STOP did not cancel runtime while remote logging was active")
	}
	select {
	case err := <-helperDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("helper shutdown deadlocked")
	}
	_ = serviceConn.Close()
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("service reader did not terminate")
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
