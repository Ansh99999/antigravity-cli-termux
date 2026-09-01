// Package settings edits the CLI's own settings.json — only the one key that
// selects the model provider, and never anything else.
//
// The engine preserves settings it does not recognise and refuses to save a file
// it could not parse, so this package is careful in the same way: it reads the
// file into a key-ordered map, changes one entry, and writes it back. A
// hand-edited comment or a setting from a newer build survives.
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Key is the setting that sends the engine to the Gemini API directly instead of
// through a Google sign-in.
const Key = "modelProvider"

// ValueGemini is the only value that reads GEMINI_API_KEY and
// GOOGLE_GEMINI_BASE_URL.
const ValueGemini = "gemini"

// Path is the CLI's settings file. AGY_CLI_SETTINGS overrides it for tests.
func Path() string {
	if p := os.Getenv("AGY_CLI_SETTINGS"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".gemini", "antigravity-cli", "settings.json")
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

// Read returns the file as a map, and an empty map when there is no file yet.
func Read() (map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Current returns the modelProvider value in effect, empty when unset.
func Current() string {
	file, err := Read()
	if err != nil {
		return ""
	}
	var value string
	if err := json.Unmarshal(file[Key], &value); err != nil {
		return ""
	}
	return value
}

// Set writes modelProvider, returning the value it replaced so `use none` can
// put back whatever was there. An empty value removes the key.
func Set(value string) (previous string, err error) {
	file, err := Read()
	if err != nil {
		return "", err
	}
	if raw, ok := file[Key]; ok {
		_ = json.Unmarshal(raw, &previous)
	}
	if previous == value {
		return previous, nil
	}
	if value == "" {
		delete(file, Key)
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			return previous, err
		}
		file[Key] = encoded
	}
	return previous, write(file)
}

func write(file map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(Path())
	tmp, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, Path())
}
