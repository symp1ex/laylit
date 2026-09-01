package config

import (
	"fmt"
	"sort"

	"laylit/internal/color"
	"laylit/internal/layouts"
)

const (
	CurrentVersion = 1
	DefaultColor   = "#FFFFFF"
)

type LayoutSettings struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type Config struct {
	Version int                       `json:"version"`
	Layouts map[string]LayoutSettings `json:"layouts"`
}

func New() Config {
	return Config{Version: CurrentVersion, Layouts: make(map[string]LayoutSettings)}
}

// Reconcile adds installed layouts and refreshes display names without ever
// deleting temporarily absent entries or replacing user colors.
func Reconcile(current Config, installed []layouts.Layout) (Config, bool, error) {
	if current.Version == 0 {
		current.Version = CurrentVersion
	}
	if current.Version != CurrentVersion {
		return Config{}, false, fmt.Errorf("unsupported config version %d", current.Version)
	}
	if current.Layouts == nil {
		current.Layouts = make(map[string]LayoutSettings)
	}
	if err := Validate(current); err != nil {
		return Config{}, false, err
	}

	changed := false
	for _, layout := range installed {
		settings, exists := current.Layouts[layout.ID]
		if !exists {
			current.Layouts[layout.ID] = LayoutSettings{Name: layout.Name, Color: DefaultColor}
			changed = true
			continue
		}
		if settings.Name != layout.Name {
			settings.Name = layout.Name
			current.Layouts[layout.ID] = settings
			changed = true
		}
	}
	return current, changed, nil
}

func Validate(value Config) error {
	if value.Version != 0 && value.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d", value.Version)
	}
	ids := make([]string, 0, len(value.Layouts))
	for id := range value.Layouts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("layout identifier must not be empty")
		}
		if _, err := color.Parse(value.Layouts[id].Color); err != nil {
			return fmt.Errorf("layout %q has invalid color %q: %w", id, value.Layouts[id].Color, err)
		}
	}
	return nil
}

func (value Config) Color(layoutID string) (color.RGB, bool, error) {
	settings, ok := value.Layouts[layoutID]
	if !ok {
		return color.RGB{}, false, nil
	}
	parsed, err := color.Parse(settings.Color)
	if err != nil {
		return color.RGB{}, true, fmt.Errorf("layout %q has invalid color %q: %w", layoutID, settings.Color, err)
	}
	return parsed, true, nil
}
