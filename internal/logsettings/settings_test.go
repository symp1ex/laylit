package logsettings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingSettingsUseRDAgentDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Settings{LogLevel: "INFO", LogRetainDays: 14}) {
		t.Fatalf("defaults = %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only settings load created a file: %v", err)
	}
}

func TestSettingsReadLevelAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(path, []byte("{\"log_level\":\"debug\",\"log_retain_days\":21}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.LogLevel != "DEBUG" || got.LogRetainDays != 21 || !got.DebugEnabled() {
		t.Fatalf("settings = %+v", got)
	}
}

func TestSettingsPathIsNextToExecutable(t *testing.T) {
	executable := filepath.Join(`C:\Program Files`, "Laylit", "Laylit.exe")
	want := filepath.Join(`C:\Program Files`, "Laylit", fileName)
	if got := PathForExecutable(executable); got != want {
		t.Fatalf("settings path = %q, want %q", got, want)
	}
}

func TestSettingsAreLoadedFromExecutableDirectory(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "bin", "Laylit.exe")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	path := PathForExecutable(executable)
	if err := os.WriteFile(path, []byte("{\"log_level\":\"ERROR\",\"log_retain_days\":3}"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, gotPath, err := loadForExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path || settings != (Settings{LogLevel: "ERROR", LogRetainDays: 3}) {
		t.Fatalf("loaded path/settings = %q %+v", gotPath, settings)
	}
}

func TestMissingSettingsAreCreatedNextToExecutable(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "Laylit.exe")
	settings, path, err := loadOrCreateForExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	if settings != Default() || path != filepath.Join(directory, fileName) {
		t.Fatalf("created path/settings = %q %+v", path, settings)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"log_level\": \"INFO\",\n  \"log_retain_days\": 14\n}\n"
	if string(data) != want {
		t.Fatalf("created settings = %q, want %q", data, want)
	}
}

func TestSettingsRejectLogDirectoryAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(path, []byte("{\"log_level\":\"INFO\",\"log_retain_days\":14,\"log_directory\":\"other\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown log directory setting error = %v", err)
	}
}

func TestSettingsRejectNonPositiveRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(path, []byte("{\"log_retain_days\":0}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("zero retention was accepted")
	}
}
