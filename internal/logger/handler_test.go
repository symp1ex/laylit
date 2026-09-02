package logger

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestPlainHandlerFormatMatchesRDAgent(t *testing.T) {
	var output bytes.Buffer
	handler := NewPlainHandler(&output, slog.LevelDebug)
	when := time.Date(2026, time.September, 2, 13, 14, 15, 987654321, time.Local)
	if err := handler.Handle(context.Background(), slog.NewRecord(when, slog.LevelInfo, "hello", 0)); err != nil {
		t.Fatal(err)
	}
	if !handler.Flush(time.Second) {
		t.Fatal("logger did not flush")
	}
	if got, want := output.String(), "[2026-09-02 13:14:15,987] [INFO] hello\n"; got != want {
		t.Fatalf("log line = %q, want %q", got, want)
	}
	if !handler.Shutdown(time.Second) {
		t.Fatal("logger did not shut down")
	}
}

func TestPlainHandlerLevelFiltering(t *testing.T) {
	tests := []struct {
		name  string
		level slog.Level
		want  []string
	}{
		{name: "DEBUG", level: slog.LevelDebug, want: []string{"[DEBUG] debug", "[INFO] info", "[WARN] warn", "[ERROR] error"}},
		{name: "INFO", level: slog.LevelInfo, want: []string{"[INFO] info", "[WARN] warn", "[ERROR] error"}},
		{name: "WARN", level: slog.LevelWarn, want: []string{"[WARN] warn", "[ERROR] error"}},
		{name: "ERROR", level: slog.LevelError, want: []string{"[ERROR] error"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			handler := NewPlainHandler(&output, test.level)
			log := Logger{Logger: slog.New(handler), handler: handler}
			log.Debugf("debug")
			log.Infof("info")
			log.Warnf("warn")
			log.Errorf("error")
			if !handler.Flush(time.Second) {
				t.Fatal("logger did not flush")
			}
			for _, message := range test.want {
				if !strings.Contains(output.String(), message) {
					t.Errorf("output %q does not contain %q", output.String(), message)
				}
			}
			if got := strings.Count(output.String(), "\n"); got != len(test.want) {
				t.Errorf("output has %d lines, want %d: %q", got, len(test.want), output.String())
			}
			if !handler.Shutdown(time.Second) {
				t.Fatal("logger did not shut down")
			}
		})
	}
}

func TestPlainHandlerShutdownDrainsQueuedRecords(t *testing.T) {
	var output bytes.Buffer
	handler := NewPlainHandler(&output, slog.LevelInfo)
	log := Logger{Logger: slog.New(handler), handler: handler}
	for index := 0; index < 100; index++ {
		log.Infof("message %d", index)
	}
	if !handler.Shutdown(time.Second) {
		t.Fatal("logger did not shut down")
	}
	if got := strings.Count(output.String(), "\n"); got != 100 {
		t.Fatalf("shutdown wrote %d records, want 100", got)
	}
}

func TestLocalQueueSizeMatchesRDAgent(t *testing.T) {
	handler := NewPlainHandler(io.Discard, slog.LevelInfo)
	if got := cap(handler.queue); got != 4096 {
		t.Fatalf("local queue capacity = %d, want 4096", got)
	}
	if !handler.Shutdown(time.Second) {
		t.Fatal("logger did not shut down")
	}
}
