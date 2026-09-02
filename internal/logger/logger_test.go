package logger

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"laylit/internal/sessionipc"
)

func TestRemoteLevelsAndMultilineNormalizationReachServiceLogger(t *testing.T) {
	directory := t.TempDir()
	Configure(directory, "DEBUG", 14)
	for _, level := range []string{"debug", "info", "warn", "error"} {
		LogSessionHelperRemote(level, "first\r\nsecond\rthird\nfourth", 123, 7, sessionHelperRoleForTest)
	}
	if !Flush(time.Second) {
		t.Fatal("logger did not flush")
	}
	if !Shutdown(time.Second) {
		t.Fatal("logger did not shut down")
	}
	data, err := os.ReadFile(filepath.Join(directory, "laylit.log"))
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, level := range []string{"DEBUG", "INFO", "WARN", "ERROR"} {
		if !strings.Contains(output, "["+level+"] Remote session-helper log: pid=123 windows_session_id=7 role=session-helper message=first second third fourth") {
			t.Errorf("missing %s remote record in %q", level, output)
		}
	}
	if got := strings.Count(output, "\n"); got != 4 {
		t.Fatalf("remote multiline records occupy %d physical lines, want 4", got)
	}
}

func TestServiceIsOnlyProductionFileWriter(t *testing.T) {
	directory := t.TempDir()
	Configure(directory, "INFO", 14)
	path := filepath.Join(directory, "laylit.log")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("service logger did not open production file: %v", err)
	}
	if !Shutdown(time.Second) {
		t.Fatal("local logger did not shut down")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ConfigureRemote("DEBUG", func(string, string) {})
	Laylit.Infof("helper message")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("remote logger opened production file: %v", err)
	}
}

const sessionHelperRoleForTest = "session-helper"

func TestRemoteSenderRecordsReachServiceFileWithAllLevels(t *testing.T) {
	directory := t.TempDir()
	Configure(directory, "DEBUG", 14)
	service, helper := net.Pipe()
	serviceConn := sessionipc.NewConn(service, func(_ context.Context, frame sessionipc.Frame) {
		meta, err := sessionipc.DecodeMetadata[sessionipc.LogMeta](frame)
		if err == nil && frame.Type == sessionipc.TypeLog {
			LogSessionHelperRemote(meta.Level, meta.Message, 44, 9, sessionHelperRoleForTest)
		}
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- serviceConn.Serve(context.Background()) }()
	helperConn := sessionipc.NewConn(helper, nil)
	sender := NewRemoteLogSender()
	sender.SetConnection(helperConn, true)
	for _, level := range []string{"debug", "info", "warn", "error"} {
		sender.Enqueue(level, level+" message")
	}

	deadline := time.Now().Add(time.Second)
	for {
		if Flush(time.Second) {
			data, err := os.ReadFile(filepath.Join(directory, "laylit.log"))
			if err == nil && strings.Count(string(data), "Remote session-helper log:") == 4 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("remote records did not reach service logger")
		}
		time.Sleep(10 * time.Millisecond)
	}
	sender.Close()
	_ = helperConn.Close()
	_ = serviceConn.Close()
	<-serveDone
	if !Shutdown(time.Second) {
		t.Fatal("logger did not shut down")
	}
	data, err := os.ReadFile(filepath.Join(directory, "laylit.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"DEBUG", "INFO", "WARN", "ERROR"} {
		if !strings.Contains(string(data), "["+level+"] Remote session-helper log:") {
			t.Errorf("missing remote %s record in %q", level, data)
		}
	}
}
