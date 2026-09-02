package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotatingWriterRotatesExistingLogByMTime(t *testing.T) {
	directory := t.TempDir()
	withWriterSettings(t, directory, 14)
	currentPath := filepath.Join(directory, "laylit.log")
	if err := os.WriteFile(currentPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().AddDate(0, 0, -1)
	if err := os.Chtimes(currentPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	writer := NewRotatingWriter("laylit")
	if _, err := writer.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(directory, "laylit.log."+oldTime.Format("2006-01-02"))
	if data, err := os.ReadFile(backupPath); err != nil || string(data) != "old\n" {
		t.Fatalf("rotated log = %q, %v", data, err)
	}
	if data, err := os.ReadFile(currentPath); err != nil || string(data) != "new\n" {
		t.Fatalf("current log = %q, %v", data, err)
	}
}

func TestRotatingWriterLazilyRotatesOnWrite(t *testing.T) {
	directory := t.TempDir()
	withWriterSettings(t, directory, 14)
	writer := NewRotatingWriter("laylit")
	if _, err := writer.Write([]byte("old\n")); err != nil {
		t.Fatal(err)
	}
	oldDay := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	writer.currentDay = oldDay
	if _, err := writer.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(directory, "laylit.log."+oldDay)); err != nil || string(data) != "old\n" {
		t.Fatalf("rotated log = %q, %v", data, err)
	}
}

func TestRotatingWriterRetentionUsesMTime(t *testing.T) {
	directory := t.TempDir()
	withWriterSettings(t, directory, 14)
	oldPath := filepath.Join(directory, "laylit.log.old")
	freshPath := filepath.Join(directory, "laylit.log.fresh")
	for _, path := range []string{oldPath, freshPath} {
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().AddDate(0, 0, -15)
	freshTime := time.Now().AddDate(0, 0, -13)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(freshPath, freshTime, freshTime); err != nil {
		t.Fatal(err)
	}

	writer := NewRotatingWriter("laylit")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired log still exists: %v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("fresh log was removed: %v", err)
	}
}

func withWriterSettings(t *testing.T, directory string, retention int) {
	t.Helper()
	oldDirectory, oldRetention := logDir, retainDays
	logDir, retainDays = directory, retention
	t.Cleanup(func() { logDir, retainDays = oldDirectory, oldRetention })
}
