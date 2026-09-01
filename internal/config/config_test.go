package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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
