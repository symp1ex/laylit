package logger

import (
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"laylit/internal/sessionipc"
)

func TestRemoteLogSenderQueueIsBoundedAndSilentlyDropsOverflow(t *testing.T) {
	sender := NewRemoteLogSender()
	for index := 0; index < remoteLogQueueSize+100; index++ {
		sender.Enqueue("info", "message")
	}
	if got := len(sender.queue); got != remoteLogQueueSize {
		t.Fatalf("remote queue length = %d, want %d", got, remoteLogQueueSize)
	}
	if remoteLogSendTimeout != 250*time.Millisecond {
		t.Fatalf("remote send timeout = %s, want 250ms", remoteLogSendTimeout)
	}
	sender.Close()
	if got := len(sender.queue); got != remoteLogQueueSize {
		t.Fatalf("remote shutdown drained queue to %d records", got)
	}
}

func TestRemoteLogSenderTruncatesAndSanitizesUTF8(t *testing.T) {
	message := strings.Repeat("я", sessionipc.MaxLogMessageBytes) + string([]byte{0xff})
	got := truncateLogMessage(message)
	if len([]byte(got)) != sessionipc.MaxLogMessageBytes {
		t.Fatalf("truncated message is %d bytes, want %d", len([]byte(got)), sessionipc.MaxLogMessageBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncated message is not valid UTF-8")
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatal("invalid suffix should have been outside the retained prefix")
	}
}

func TestRemoteLogSenderSendsLogFrame(t *testing.T) {
	service, helper := net.Pipe()
	defer service.Close()
	conn := sessionipc.NewConn(helper, nil)
	defer conn.Close()
	sender := NewRemoteLogSender()
	defer sender.Close()
	sender.SetConnection(conn, true)
	sender.Enqueue("warn", "remote message")

	readDone := make(chan sessionipc.Frame, 1)
	go func() {
		frame, _ := sessionipc.ReadFrame(service)
		readDone <- frame
	}()
	select {
	case frame := <-readDone:
		meta, err := sessionipc.DecodeMetadata[sessionipc.LogMeta](frame)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type != sessionipc.TypeLog || meta.Level != "warn" || meta.Message != "remote message" {
			t.Fatalf("remote frame = type %d metadata %+v", frame.Type, meta)
		}
	case <-time.After(time.Second):
		t.Fatal("remote log was not sent")
	}
}

func TestRemoteLogSenderDoesNotClearReplacementConnection(t *testing.T) {
	sender := NewRemoteLogSender()
	defer sender.Close()
	firstService, firstHelper := net.Pipe()
	defer firstService.Close()
	first := sessionipc.NewConn(firstHelper, nil)
	defer first.Close()
	secondService, secondHelper := net.Pipe()
	defer secondService.Close()
	second := sessionipc.NewConn(secondHelper, nil)
	defer second.Close()

	sender.SetConnection(first, true)
	sender.SetConnection(second, true)
	sender.ClearConnection(first)
	got, enabled := sender.connection()
	if got != second || !enabled {
		t.Fatal("stale connection cleanup removed the replacement connection")
	}
}
