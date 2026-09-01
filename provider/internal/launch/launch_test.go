package launch

import (
	"testing"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/settings"
)

func sandbox(t *testing.T) {
	t.Helper()
	t.Setenv("AGY_PROVIDER_HOME", t.TempDir())
	t.Setenv("AGY_CLI_SETTINGS", t.TempDir()+"/settings.json")
}

func vars(env *Env) map[string]string {
	out := map[string]string{}
	for _, v := range env.Vars {
		out[v[0]] = v[1]
	}
	return out
}

func TestUpWithNothingConfiguredChangesNothing(t *testing.T) {
	sandbox(t)
	env := Up(true)
	if len(env.Vars) != 0 || env.Provider != nil {
		t.Fatalf("a first run must leave the launch alone: %+v", env)
	}
	if settings.Current() != "" {
		t.Errorf("settings.json should not be touched, got %q", settings.Current())
	}
}

func TestUpPointsTheEngineStraightAtAGeminiHost(t *testing.T) {
	sandbox(t)
	if err := config.Save(&config.File{Active: "direct", Providers: []*config.Provider{{
		Name: "direct", Kind: config.KindGemini,
		BaseURL: "https://gemini.example",
		Keys:    []config.Key{{ID: "k1", Value: "AIza-one"}},
	}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	env := Up(true)
	if env.Provider == nil {
		t.Fatalf("the provider should be in play: %+v", env.Notes)
	}
	if env.ProxyPort != 0 {
		t.Error("a single-key Gemini host needs no proxy")
	}
	got := vars(env)
	if got["GEMINI_API_KEY"] != "AIza-one" {
		t.Errorf("the key should go straight to the engine, got %q", got["GEMINI_API_KEY"])
	}
	if got["GOOGLE_GEMINI_BASE_URL"] != "https://gemini.example" {
		t.Errorf("base url wrong: %q", got["GOOGLE_GEMINI_BASE_URL"])
	}
	if settings.Current() != settings.ValueGemini {
		t.Errorf("the engine needs %s set to reach for those variables at all, got %q", settings.Key, settings.Current())
	}
}

func TestUpLeavesSettingsAloneWhenAsked(t *testing.T) {
	sandbox(t)
	if err := config.Save(&config.File{Active: "direct", Providers: []*config.Provider{{
		Name: "direct", Kind: config.KindGemini, BaseURL: "https://gemini.example",
		Keys: []config.Key{{ID: "k1", Value: "AIza-one"}},
	}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if env := Up(false); len(env.Vars) == 0 {
		t.Fatal("the variables are still wanted")
	}
	if settings.Current() != "" {
		t.Errorf("--no-settings must not write, got %q", settings.Current())
	}
}

func TestUpReportsAProviderItCannotUse(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    *config.Provider
	}{
		{"no keys", &config.Provider{Name: "empty", Kind: config.KindGemini, BaseURL: "https://x.test"}},
		{"a bad base url", &config.Provider{Name: "bad", Kind: config.KindGemini, BaseURL: "not a url",
			Keys: []config.Key{{ID: "k1", Value: "v"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			if err := config.Save(&config.File{Active: tc.p.Name, Providers: []*config.Provider{tc.p}}); err != nil {
				t.Fatalf("Save: %v", err)
			}
			env := Up(true)
			if len(env.Vars) != 0 {
				t.Errorf("nothing should be exported: %+v", env.Vars)
			}
			if len(env.Notes) == 0 {
				t.Error("the reason should be said out loud, not swallowed")
			}
			if settings.Current() != "" {
				t.Error("a launch that cannot use a provider must not redirect the engine")
			}
		})
	}
}

func TestRestoreSettingsPutsBackWhatWasThere(t *testing.T) {
	sandbox(t)
	if _, err := settings.Set("google"); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := config.Save(&config.File{Active: "direct", Providers: []*config.Provider{{
		Name: "direct", Kind: config.KindGemini, BaseURL: "https://gemini.example",
		Keys: []config.Key{{ID: "k1", Value: "AIza-one"}},
	}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	Up(true)
	if settings.Current() != settings.ValueGemini {
		t.Fatalf("expected the setting to change, got %q", settings.Current())
	}

	restored, err := RestoreSettings()
	if err != nil {
		t.Fatalf("RestoreSettings: %v", err)
	}
	if restored != "google" {
		t.Errorf("want the original value back, got %q", restored)
	}
	if settings.Current() != "google" {
		t.Errorf("the file was not restored, got %q", settings.Current())
	}
}

func TestActivateSettingsRemembersOnlyTheOriginalValue(t *testing.T) {
	sandbox(t)
	if _, err := settings.Set("google"); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// Switching on twice must not record "gemini" as the thing to go back to.
	for range 2 {
		if _, err := ActivateSettings(); err != nil {
			t.Fatalf("ActivateSettings: %v", err)
		}
	}
	restored, err := RestoreSettings()
	if err != nil {
		t.Fatalf("RestoreSettings: %v", err)
	}
	if restored != "google" {
		t.Fatalf("want google back, got %q", restored)
	}

	// And a second restore has nothing left to put back.
	again, err := RestoreSettings()
	if err != nil {
		t.Fatalf("RestoreSettings: %v", err)
	}
	if again != "" {
		t.Errorf("want nothing recorded, got %q", again)
	}
}

func TestStopWithNoProxy(t *testing.T) {
	sandbox(t)
	stopped, err := Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped {
		t.Error("nothing was running")
	}
}
