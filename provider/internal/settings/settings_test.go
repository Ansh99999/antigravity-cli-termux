package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func settingsFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("seeding the file: %v", err)
		}
	}
	t.Setenv("AGY_CLI_SETTINGS", path)
	return path
}

func TestSetKeepsEverythingElse(t *testing.T) {
	// Including a key this build has never heard of: the engine preserves those
	// across versions and so must this.
	path := settingsFile(t, `{
  "colorScheme": "tokyo night",
  "toolPermission": "strict",
  "somethingFromANewerBuild": {"nested": [1, 2, 3]}
}`)

	previous, err := Set(ValueGemini)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if previous != "" {
		t.Errorf("there was no previous value, got %q", previous)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the file must stay valid JSON: %v", err)
	}
	if got[Key] != ValueGemini {
		t.Errorf("%s was not set: %v", Key, got[Key])
	}
	if got["colorScheme"] != "tokyo night" || got["toolPermission"] != "strict" {
		t.Errorf("an existing setting was lost: %+v", got)
	}
	if _, ok := got["somethingFromANewerBuild"]; !ok {
		t.Error("an unrecognized setting was dropped, which is how a config gets silently wiped")
	}

	if perm, err := os.Stat(path); err == nil && perm.Mode().Perm() != 0o600 {
		t.Errorf("want 0600, got %o", perm.Mode().Perm())
	}
}

func TestSetReportsAndRestoresThePreviousValue(t *testing.T) {
	settingsFile(t, `{"modelProvider":"google"}`)

	previous, err := Set(ValueGemini)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if previous != "google" {
		t.Fatalf("want the old value back, got %q", previous)
	}
	if Current() != ValueGemini {
		t.Errorf("Current = %q", Current())
	}

	if _, err := Set(previous); err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if Current() != "google" {
		t.Errorf("the setting was not restored, got %q", Current())
	}
}

func TestSetEmptyRemovesTheKey(t *testing.T) {
	path := settingsFile(t, `{"modelProvider":"gemini","colorScheme":"dark"}`)
	if _, err := Set(""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	raw, _ := os.ReadFile(path)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got[Key]; ok {
		t.Errorf("the key should be gone, got %+v", got)
	}
	if got["colorScheme"] != "dark" {
		t.Error("the rest of the file should be untouched")
	}
}

func TestReadWithNoFile(t *testing.T) {
	t.Setenv("AGY_CLI_SETTINGS", filepath.Join(t.TempDir(), "nothing-here.json"))
	got, err := Read()
	if err != nil {
		t.Fatalf("a missing settings file is a first run, not an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want an empty map, got %+v", got)
	}
	if Current() != "" {
		t.Errorf("Current = %q", Current())
	}

	// Writing into it creates the directory the CLI expects.
	if _, err := Set(ValueGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if Current() != ValueGemini {
		t.Errorf("Current = %q", Current())
	}
}

func TestUnparseableFileIsReported(t *testing.T) {
	settingsFile(t, `{"colorScheme": "dark",,,}`)
	if _, err := Read(); err == nil {
		t.Fatal("a broken file must be reported rather than overwritten")
	}
	if _, err := Set(ValueGemini); err == nil {
		t.Fatal("Set must refuse rather than replace a file it could not read")
	}
}
