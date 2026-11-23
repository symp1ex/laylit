package config

import (
	"fmt"
	"sort"

	"laylit/internal/color"
	"laylit/internal/layouts"
)

const (
	CurrentVersion = 2
	DefaultColor   = "#FFFFFF"
)

type LayoutSettings struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type Config struct {
	Version int                       `json:"version"`
	Layouts map[string]LayoutSettings `json:"layouts"`
	Devices map[string]DeviceSettings `json:"devices"`
}

type DeviceSettings struct {
	Enabled bool              `json:"enabled"`
	Colors  map[string]string `json:"colors"`
}

func New() Config {
	return Config{Version: CurrentVersion, Layouts: make(map[string]LayoutSettings), Devices: make(map[string]DeviceSettings)}
}

// Reconcile adds installed layouts and refreshes display names without ever
// deleting temporarily absent entries or replacing user colors.
func Reconcile(current Config, installed []layouts.Layout) (Config, bool, error) {
	if err := Validate(current); err != nil {
		return Config{}, false, err
	}
	changed := current.Version != CurrentVersion || current.Devices == nil
	current.Version = CurrentVersion
	if current.Devices == nil {
		current.Devices = make(map[string]DeviceSettings)
	}
	if current.Layouts == nil {
		current.Layouts = make(map[string]LayoutSettings)
	}
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
	if value.Version != 0 && value.Version != 1 && value.Version != CurrentVersion {
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
	for deviceID, device := range value.Devices {
		if deviceID == "" {
			return fmt.Errorf("device identifier must not be empty")
		}
		for layoutID, override := range device.Colors {
			if layoutID == "" {
				return fmt.Errorf("device %q: layout identifier must not be empty", deviceID)
			}
			if _, err := color.Parse(override); err != nil {
				return fmt.Errorf("device %q layout %q has invalid color %q: %w", deviceID, layoutID, override, err)
			}
		}
	}
	return nil
}

func (value *Config) AddDevice(id string) bool {
	if _, exists := value.Devices[id]; exists {
		return false
	}
	if value.Devices == nil {
		value.Devices = make(map[string]DeviceSettings)
	}
	value.Devices[id] = DeviceSettings{Enabled: true, Colors: make(map[string]string)}
	return true
}

func (value Config) DeviceColor(deviceID, layoutID string, fallback color.RGB) (color.RGB, bool, error) {
	if override, ok := value.Devices[deviceID].Colors[layoutID]; ok {
		parsed, err := color.Parse(override)
		return parsed, true, err
	}
	selected, known, err := value.Color(layoutID)
	if !known {
		selected = fallback
	}
	return selected, known, err
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
