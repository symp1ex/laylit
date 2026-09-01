package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Repository interface {
	Load(ctx context.Context) (Config, bool, error)
	Save(ctx context.Context, value Config) error
}

type FileRepository struct {
	path string
}

func NewFileRepository(path string) *FileRepository {
	return &FileRepository{path: path}
}

func (repository *FileRepository) Path() string { return repository.path }

func (repository *FileRepository) Load(ctx context.Context) (Config, bool, error) {
	if err := ctx.Err(); err != nil {
		return Config{}, false, err
	}
	data, err := os.ReadFile(repository.path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read config %q: %w", repository.path, err)
	}
	var value Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&value); err != nil {
		return Config{}, true, fmt.Errorf("parse config %q: %w", repository.path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON value")
		}
		return Config{}, true, fmt.Errorf("parse config %q: %w", repository.path, err)
	}
	if err := Validate(value); err != nil {
		return Config{}, true, fmt.Errorf("validate config %q: %w", repository.path, err)
	}
	return value, true, nil
}

func (repository *FileRepository) Save(ctx context.Context, value Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := Validate(value); err != nil {
		return fmt.Errorf("validate config before save: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(repository.path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create config directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := replaceFile(temporaryPath, repository.path); err != nil {
		return fmt.Errorf("atomically replace config %q: %w", repository.path, err)
	}
	removeTemporary = false
	return nil
}
