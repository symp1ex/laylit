package logsettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultLogLevel   = "INFO"
	defaultRetainDays = 14
	fileName          = "settings.json"
)

type Settings struct {
	LogLevel      string `json:"log_level"`
	LogRetainDays int    `json:"log_retain_days"`
}

func Default() Settings {
	return Settings{LogLevel: defaultLogLevel, LogRetainDays: defaultRetainDays}
}

func Load() (Settings, string, error) {
	return load(false)
}

func LoadOrCreate() (Settings, string, error) {
	return load(true)
}

func load(create bool) (Settings, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return Settings{}, "", fmt.Errorf("determine executable path: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return Settings{}, "", fmt.Errorf("make executable path absolute: %w", err)
	}
	if create {
		return loadOrCreateForExecutable(executable)
	}
	return loadForExecutable(executable)
}

func loadForExecutable(executable string) (Settings, string, error) {
	path := PathForExecutable(executable)
	settings, err := LoadFile(path)
	return settings, path, err
}

func loadOrCreateForExecutable(executable string) (Settings, string, error) {
	path := PathForExecutable(executable)
	settings, err := LoadFileOrCreate(path)
	return settings, path, err
}

func PathForExecutable(executable string) string {
	return filepath.Join(filepath.Dir(executable), fileName)
}

func LoadFile(path string) (Settings, error) {
	settings := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read logging settings %q: %w", path, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var raw struct {
		LogLevel      string `json:"log_level"`
		LogRetainDays *int   `json:"log_retain_days"`
	}
	if err := decoder.Decode(&raw); err != nil {
		return Settings{}, fmt.Errorf("parse logging settings %q: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON value")
		}
		return Settings{}, fmt.Errorf("parse logging settings %q: %w", path, err)
	}
	if raw.LogLevel != "" {
		settings.LogLevel = strings.ToUpper(raw.LogLevel)
	}
	if raw.LogRetainDays != nil {
		settings.LogRetainDays = *raw.LogRetainDays
	}
	if settings.LogRetainDays <= 0 {
		return Settings{}, fmt.Errorf("validate logging settings %q: log_retain_days must be positive", path)
	}
	return settings, nil
}

func LoadFileOrCreate(path string) (Settings, error) {
	settings := Default()
	if _, err := os.Stat(path); err == nil {
		return LoadFile(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Settings{}, fmt.Errorf("read logging settings %q: %w", path, err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return Settings{}, fmt.Errorf("encode default logging settings: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Settings{}, fmt.Errorf("create default logging settings %q: %w", path, err)
	}
	return settings, nil
}

func (settings Settings) DebugEnabled() bool {
	return strings.EqualFold(settings.LogLevel, "DEBUG")
}
