package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"laylit/internal/color"
	"laylit/internal/layouts"
)

func TestReconcileCreatesDefaultsForAllLayouts(t *testing.T) {
	installed := []layouts.Layout{{ID: "en", Name: "English"}, {ID: "ru", Name: "Russian"}}
	got, changed, err := Reconcile(New(), installed)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(got.Layouts) != 2 {
		t.Fatalf("changed=%v layouts=%#v", changed, got.Layouts)
	}
	for _, id := range []string{"en", "ru"} {
		if got.Layouts[id].Color != DefaultColor {
			t.Fatalf("layout %q color = %q", id, got.Layouts[id].Color)
		}
	}
}

func TestV1MigrationPreservesPaletteAndDeviceSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	err := os.WriteFile(path, []byte(`{"version":1,"layouts":{"en":{"name":"English","color":"abcdef"},"old":{"name":"Absent","color":"#112233"}}}`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := NewFileRepository(path)
	legacy, exists, err := repo.Load(ctx)
	if err != nil || !exists {
		t.Fatalf("load v1: exists=%t err=%v", exists, err)
	}
	current, changed, err := Reconcile(legacy, []layouts.Layout{{ID: "en", Name: "English"}})
	if err != nil || !changed || current.Version != 2 {
		t.Fatalf("migrate v1: config=%#v changed=%t err=%v", current, changed, err)
	}
	current.Devices["disabled"] = DeviceSettings{Enabled: false, Colors: map[string]string{"en": "#010203"}}
	current.Devices["absent"] = DeviceSettings{Enabled: true, Colors: map[string]string{"old": "#445566"}}
	if current.AddDevice("disabled") || !current.AddDevice("new") {
		t.Fatal("discovery overwrote an existing device or failed to add a new one")
	}
	if err := repo.Save(ctx, current); err != nil {
		t.Fatal(err)
	}
	got, _, err := repo.Load(ctx)
	if err != nil || !reflect.DeepEqual(got, current) || got.Devices["disabled"].Enabled || !got.Devices["new"].Enabled {
		t.Fatalf("round trip: config=%#v err=%v", got, err)
	}
	if got.Layouts["en"].Color != "abcdef" || got.Layouts["old"].Color != "#112233" {
		t.Fatal("migration changed legacy colors")
	}
}

func TestDeviceColorPriority(t *testing.T) {
	value := New()
	value.Layouts["en"] = LayoutSettings{Color: "#112233"}
	value.Layouts["ru"] = LayoutSettings{Color: "#445566"}
	value.Devices["asus"] = DeviceSettings{Enabled: true, Colors: map[string]string{"en": "#000000", "other": "#070809"}}
	white := color.RGB{R: 255, G: 255, B: 255}
	for _, test := range []struct {
		device, hkl, want string
		known             bool
	}{
		{"asus", "en", "#000000", true},
		{"asus", "ru", "#445566", true},
		{"asus", "other", "#070809", true},
		{"usb", "en", "#112233", true},
		{"usb", "other", "#FFFFFF", false},
	} {
		got, known, err := value.DeviceColor(test.device, test.hkl, white)
		if err != nil || known != test.known || got.String() != test.want {
			t.Errorf("%s/%s: got %s known=%t err=%v, want %s known=%t", test.device, test.hkl, got, known, err, test.want, test.known)
		}
	}
}

func TestReconcilePreservesColorsAddsNewAndRetainsAbsent(t *testing.T) {
	input := Config{Version: CurrentVersion, Layouts: map[string]LayoutSettings{
		"en":     {Name: "Old English", Color: "#123456"},
		"absent": {Name: "Temporarily absent", Color: "abcdef"},
	}}
	got, changed, err := Reconcile(input, []layouts.Layout{{ID: "en", Name: "English"}, {ID: "ru", Name: "Russian"}})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected metadata/addition change")
	}
	if got.Layouts["en"].Color != "#123456" || got.Layouts["absent"].Color != "abcdef" {
		t.Fatalf("user colors changed: %#v", got.Layouts)
	}
	if got.Layouts["en"].Name != "English" || got.Layouts["ru"].Color != DefaultColor {
		t.Fatalf("merge result = %#v", got.Layouts)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	installed := []layouts.Layout{{ID: "en", Name: "English"}, {ID: "ru", Name: "Russian"}}
	first, _, err := Reconcile(New(), installed)
	if err != nil {
		t.Fatal(err)
	}
	second, changed, err := Reconcile(first, installed)
	if err != nil {
		t.Fatal(err)
	}
	if changed || !reflect.DeepEqual(first, second) {
		t.Fatalf("second reconcile changed config: changed=%v\nfirst=%#v\nsecond=%#v", changed, first, second)
	}
}

func TestFileRepositoryMissingThenAtomicSaveAndStableSerialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	repository := NewFileRepository(path)
	value, exists, err := repository.Load(context.Background())
	if err != nil || exists {
		t.Fatalf("Load missing: exists=%v err=%v", exists, err)
	}
	value, _, err = Reconcile(value, []layouts.Layout{{ID: "ru", Name: "Russian"}, {ID: "en", Name: "English"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	loaded, exists, err := repository.Load(context.Background())
	if err != nil || !exists || !reflect.DeepEqual(value, loaded) {
		t.Fatalf("round trip: exists=%v err=%v value=%#v", exists, err, loaded)
	}
	// Saving identical data is deterministic even though map iteration is not.
	if err := repository.Save(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	after, _ := os.Stat(path)
	if string(first) != string(second) {
		t.Fatal("serialization order changed for identical config")
	}
	if !after.ModTime().After(before.ModTime()) && after.ModTime() != before.ModTime() {
		t.Fatal("invalid file timestamp")
	}
}

func TestFileRepositoryRejectsInvalidJSONAndColor(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"invalid JSON", `{`},
		{"trailing JSON", `{"version":1,"layouts":{}} {}`},
		{"invalid color", `{"version":1,"layouts":{"en":{"name":"English","color":"white"}}}`},
		{"invalid device color", `{"version":2,"devices":{"asus":{"enabled":false,"colors":{"en":"white"}}}}`},
		{"invalid device enabled", `{"version":2,"devices":{"asus":{"enabled":"false"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := NewFileRepository(path).Load(context.Background()); err == nil {
				t.Fatal("invalid config was accepted")
			}
		})
	}
}
