package entry

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	appLogger "laylit/internal/logger"
	"laylit/internal/logsettings"
)

func TestCLIArgumentValidationBeforeHardwareAccess(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"info", "extra"}, "info does not accept arguments"},
		{[]string{"set"}, "set requires one color argument"},
		{[]string{"set", "not-a-color"}, "invalid color"},
		{[]string{"off", "extra"}, "off does not accept arguments"},
		{[]string{"unknown"}, "unknown command"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), test.args, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("Run(%v) error = %v, want substring %q", test.args, err, test.want)
		}
	}
}

func TestDebugSettingsEnableAutomaticDiagnosticProducers(t *testing.T) {
	var messages []string
	appLogger.ConfigureRemote("DEBUG", func(level, message string) {
		messages = append(messages, level+":"+message)
	})
	settings := logsettings.Settings{LogLevel: "DEBUG", LogRetainDays: 14}
	tracef, writer := automaticDiagnostics(settings.DebugEnabled())
	if tracef == nil || writer == io.Discard {
		t.Fatal("DEBUG settings did not enable diagnostic producers")
	}
	tracef("layout trace")
	if _, err := writer.Write([]byte("DEBUG evision trace\n")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(messages, "|"); got != "debug:layout trace|debug:evision trace" {
		t.Fatalf("diagnostic messages = %q", got)
	}
}

func TestApplicationConfigPathKeepsUserConfigLocation(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("APPDATA", directory)
	got, err := applicationConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(directory, "laylit", "config.json")
	if got != want {
		t.Fatalf("config path = %q, want %q", got, want)
	}
}

func TestApplicationLogDirectoryIsNextToExecutable(t *testing.T) {
	executable := filepath.Join(`C:\Program Files`, "Laylit", "Laylit.exe")
	got := applicationLogDirectoryForExecutable(executable)
	want := filepath.Join(`C:\Program Files`, "Laylit", "logs")
	if got != want {
		t.Fatalf("log directory = %q, want %q", got, want)
	}
}
