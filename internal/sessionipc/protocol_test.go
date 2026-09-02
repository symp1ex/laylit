package sessionipc

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestFrameRoundTripThroughPartialReads(t *testing.T) {
	want, err := NewFrame(TypeLog, 3, 17, LogMeta{Level: "info", Message: "message"}, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := WriteFrame(&encoded, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(oneByteReader{reader: bytes.NewReader(encoded.Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != want.Version || got.Type != want.Type || got.Flags != want.Flags || got.RequestID != want.RequestID ||
		!bytes.Equal(got.Metadata, want.Metadata) || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("frame = %+v, want %+v", got, want)
	}
}

func TestFramesImmediatelyAfterHandshakeAreNotLost(t *testing.T) {
	frames := []Frame{
		mustFrame(t, TypeHello, HelloMeta{Nonce: "nonce", PID: 10, SessionID: 7, Role: "session-helper"}),
		mustFrame(t, TypeReady, nil),
		mustFrame(t, TypeLog, LogMeta{Level: "info", Message: "immediate"}),
	}
	var stream bytes.Buffer
	for _, frame := range frames {
		if err := WriteFrame(&stream, frame); err != nil {
			t.Fatal(err)
		}
	}
	for index, want := range frames {
		got, err := ReadFrame(&stream)
		if err != nil {
			t.Fatalf("read frame %d: %v", index, err)
		}
		if got.Type != want.Type || !bytes.Equal(got.Metadata, want.Metadata) {
			t.Fatalf("frame %d = %+v, want %+v", index, got, want)
		}
	}
}

func TestConcurrentWritesDoNotInterleaveLifecycleAndLogFrames(t *testing.T) {
	reader, writer := net.Pipe()
	defer reader.Close()
	conn := NewConn(writer, nil)
	defer conn.Close()
	const logCount = 100
	readDone := make(chan error, 1)
	go func() {
		logs, stops := 0, 0
		for index := 0; index < logCount+1; index++ {
			frame, err := ReadFrame(reader)
			if err != nil {
				readDone <- err
				return
			}
			switch frame.Type {
			case TypeLog:
				logs++
			case TypeStop:
				stops++
			default:
				readDone <- io.ErrUnexpectedEOF
				return
			}
		}
		if logs != logCount || stops != 1 {
			readDone <- io.ErrUnexpectedEOF
			return
		}
		readDone <- nil
	}()

	var writers sync.WaitGroup
	for index := 0; index < logCount; index++ {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			if err := conn.Send(mustFrame(t, TypeLog, LogMeta{Level: "debug", Message: "message"})); err != nil {
				t.Errorf("send log %d: %v", index, err)
			}
		}(index)
	}
	writers.Add(1)
	go func() {
		defer writers.Done()
		if err := conn.Send(mustFrame(t, TypeStop, nil)); err != nil {
			t.Errorf("send STOP: %v", err)
		}
	}()
	writers.Wait()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent frame writes deadlocked")
	}
}

func TestServeReturnsOnDisconnect(t *testing.T) {
	service, helper := net.Pipe()
	conn := NewConn(service, nil)
	done := make(chan error, 1)
	go func() { done <- conn.Serve(context.Background()) }()
	_ = helper.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("disconnect returned nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after disconnect")
	}
}

func TestReadFrameRejectsPartialFrame(t *testing.T) {
	frame := mustFrame(t, TypeLog, LogMeta{Level: "info", Message: "partial"})
	var encoded bytes.Buffer
	if err := WriteFrame(&encoded, frame); err != nil {
		t.Fatal(err)
	}
	raw := encoded.Bytes()
	if _, err := ReadFrame(bytes.NewReader(raw[:len(raw)-1])); err == nil {
		t.Fatal("partial frame was accepted")
	}
}

type oneByteReader struct {
	reader io.Reader
}

func (reader oneByteReader) Read(data []byte) (int, error) {
	if len(data) > 1 {
		data = data[:1]
	}
	return reader.reader.Read(data)
}

func mustFrame(t *testing.T, typ uint8, metadata any) Frame {
	t.Helper()
	frame, err := NewFrame(typ, 0, 0, metadata, nil)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}
