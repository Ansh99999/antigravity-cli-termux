package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionedBaseURLs(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		kind       Kind
		chat       string
		models     string
	}{
		{"openai without a version", "https://api.openai.com", KindOpenAI,
			"https://api.openai.com/v1/chat/completions", "https://api.openai.com/v1/models"},
		{"openai with one", "https://api.openai.com/v1", KindOpenAI,
			"https://api.openai.com/v1/chat/completions", "https://api.openai.com/v1/models"},
		{"openai with a trailing slash", "https://api.openai.com/v1/", KindOpenAI,
			"https://api.openai.com/v1/chat/completions", "https://api.openai.com/v1/models"},
		{"a gateway that nests it", "https://openrouter.ai/api/v1", KindOpenAI,
			"https://openrouter.ai/api/v1/chat/completions", "https://openrouter.ai/api/v1/models"},
		{"anthropic", "https://api.anthropic.com", KindAnthropic,
			"https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1/models"},
		{"a local relay on a port", "http://127.0.0.1:8080", KindOpenAI,
			"http://127.0.0.1:8080/v1/chat/completions", "http://127.0.0.1:8080/v1/models"},
	} {
		p := &Provider{Name: "x", Kind: tc.kind, BaseURL: tc.base}
		if got := p.ChatURL(); got != tc.chat {
			t.Errorf("%s: chat URL = %q, want %q", tc.name, got, tc.chat)
		}
		if got := p.ModelsURL(); got != tc.models {
			t.Errorf("%s: models URL = %q, want %q", tc.name, got, tc.models)
		}
	}
}

func TestGeminiURLs(t *testing.T) {
	p := &Provider{Name: "g", Kind: KindGemini, BaseURL: "https://generativelanguage.googleapis.com"}
	want := "https://generativelanguage.googleapis.com/v1beta/models/gemini-3-pro:streamGenerateContent"
	if got := p.GeminiURL("gemini-3-pro", "streamGenerateContent"); got != want {
		t.Errorf("GeminiURL = %q, want %q", got, want)
	}
	if got := p.ModelsURL(); got != "https://generativelanguage.googleapis.com/v1beta/models" {
		t.Errorf("ModelsURL = %q", got)
	}
	// A base that already names the version must not gain a second one.
	p.BaseURL = "https://gateway.example/v1beta"
	if got := p.GeminiURL("m", "generateContent"); got != "https://gateway.example/v1beta/models/m:generateContent" {
		t.Errorf("GeminiURL = %q", got)
	}
}

func TestParseKindAndStrategy(t *testing.T) {
	for input, want := range map[string]Kind{
		"openai": KindOpenAI, "OpenAI": KindOpenAI, "oai": KindOpenAI,
		"anthropic": KindAnthropic, "claude": KindAnthropic,
		"gemini": KindGemini, "google": KindGemini,
	} {
		got, err := ParseKind(input)
		if err != nil || got != want {
			t.Errorf("ParseKind(%q) = %q, %v", input, got, err)
		}
	}
	if _, err := ParseKind("ollama"); err == nil {
		t.Error("an unknown style should be reported, not guessed at")
	}

	for input, want := range map[string]Strategy{
		"rotate": StrategyRotate, "round-robin": StrategyRotate, "rr": StrategyRotate,
		"first": StrategyFirst, "random": StrategyRandom, "least-errors": StrategyLeastErrors,
	} {
		got, err := ParseStrategy(input)
		if err != nil || got != want {
			t.Errorf("ParseStrategy(%q) = %q, %v", input, got, err)
		}
	}
}

func TestNeedsProxy(t *testing.T) {
	oneKey := []Key{{ID: "k1", Value: "v"}}
	twoKeys := []Key{{ID: "k1", Value: "v"}, {ID: "k2", Value: "w"}}

	for _, tc := range []struct {
		name string
		p    *Provider
		want bool
	}{
		{"a plain gemini host needs nothing", &Provider{Kind: KindGemini, Keys: oneKey}, false},
		{"two keys need rotation", &Provider{Kind: KindGemini, Keys: twoKeys}, true},
		{"a pinned model needs rewriting", &Provider{Kind: KindGemini, Keys: oneKey, Model: "x"}, true},
		{"a mapped model needs rewriting", &Provider{Kind: KindGemini, Keys: oneKey, ModelMap: map[string]string{"a": "b"}}, true},
		{"extra headers need adding", &Provider{Kind: KindGemini, Keys: oneKey, Headers: []Header{{Name: "X", Value: "1"}}}, true},
		{"openai always needs translating", &Provider{Kind: KindOpenAI, Keys: oneKey}, true},
		{"anthropic always needs translating", &Provider{Kind: KindAnthropic, Keys: oneKey}, true},
	} {
		if got := tc.p.NeedsProxy(); got != tc.want {
			t.Errorf("%s: NeedsProxy = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveModel(t *testing.T) {
	p := &Provider{Model: "pinned", ModelMap: map[string]string{"gemini-3-pro": "claude-opus-4-5"}}
	if got := p.ResolveModel("gemini-3-pro"); got != "claude-opus-4-5" {
		t.Errorf("a mapping should win over the pin, got %q", got)
	}
	if got := p.ResolveModel("gemini-3-flash"); got != "pinned" {
		t.Errorf("an unmapped name should fall to the pin, got %q", got)
	}
	bare := &Provider{}
	if got := bare.ResolveModel("whatever"); got != "whatever" {
		t.Errorf("with nothing configured the name passes through, got %q", got)
	}
}

func TestEffectiveStrategyDefaultsByKeyCount(t *testing.T) {
	one := &Provider{Keys: []Key{{ID: "k1", Value: "v"}}}
	if one.EffectiveStrategy() != StrategyFirst {
		t.Errorf("one key needs no rotation, got %q", one.EffectiveStrategy())
	}
	two := &Provider{Keys: []Key{{ID: "k1", Value: "v"}, {ID: "k2", Value: "w"}}}
	if two.EffectiveStrategy() != StrategyRotate {
		t.Errorf("a second key should start rotating, got %q", two.EffectiveStrategy())
	}
	pinned := &Provider{Strategy: StrategyFirst, Keys: two.Keys}
	if pinned.EffectiveStrategy() != StrategyFirst {
		t.Error("an explicit strategy must not be overridden")
	}
}

func TestEnabledKeysSkipsBlanksAndDisabled(t *testing.T) {
	off := false
	p := &Provider{Keys: []Key{
		{ID: "k1", Value: "one"},
		{ID: "k2", Value: "", Enabled: nil},
		{ID: "k3", Value: "three", Enabled: &off},
		{ID: "k4", Value: "  "},
	}}
	got := p.EnabledKeys()
	if len(got) != 1 || got[0].ID != "k1" {
		t.Errorf("only k1 is usable, got %+v", got)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGY_PROVIDER_HOME", dir)

	file := &File{
		Active: "router",
		Providers: []*Provider{{
			Name:     "router",
			Kind:     KindOpenAI,
			BaseURL:  "https://openrouter.ai/api/v1",
			Keys:     []Key{{ID: "k1", Value: "sk-one"}, {ID: "k2", Value: "sk-two", Label: "spare"}},
			Model:    "anthropic/claude-sonnet-4.5",
			Strategy: StrategyRotate,
			Headers:  []Header{{Name: "HTTP-Referer", Value: "https://example.test"}},
			ModelMap: map[string]string{"gemini-3-pro": "openai/gpt-5.1"},
		}},
	}
	if err := Save(file); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "providers.json"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("a file holding API keys must be 0600, got %o", perm)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != Version || loaded.Active != "router" || len(loaded.Providers) != 1 {
		t.Fatalf("round trip lost the shape: %+v", loaded)
	}
	p, err := loaded.Find("ROUTER")
	if err != nil {
		t.Fatalf("Find should not care about case: %v", err)
	}
	if len(p.Keys) != 2 || p.Keys[1].Label != "spare" {
		t.Errorf("keys did not survive: %+v", p.Keys)
	}
	if p.ModelMap["gemini-3-pro"] != "openai/gpt-5.1" {
		t.Errorf("model map did not survive: %+v", p.ModelMap)
	}
	if p.Headers[0].Name != "HTTP-Referer" {
		t.Errorf("headers did not survive: %+v", p.Headers)
	}
}

func TestLoadWithNoFileIsEmptyNotAnError(t *testing.T) {
	t.Setenv("AGY_PROVIDER_HOME", filepath.Join(t.TempDir(), "missing"))
	loaded, err := Load()
	if err != nil {
		t.Fatalf("a first run must not fail: %v", err)
	}
	if loaded.ActiveProvider() != nil {
		t.Error("nothing should be active")
	}
}

func TestRemoveClearsActive(t *testing.T) {
	f := &File{Active: "one", Providers: []*Provider{{Name: "one"}, {Name: "two"}}}
	if err := f.Remove("one"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if f.Active != "" {
		t.Errorf("removing the active provider must clear it, got %q", f.Active)
	}
	if len(f.Providers) != 1 || f.Providers[0].Name != "two" {
		t.Errorf("the other provider should remain: %+v", f.Providers)
	}
	if err := f.Remove("nope"); err == nil {
		t.Error("removing something absent should say so")
	}
}

func TestValidate(t *testing.T) {
	good := &Provider{Name: "ok-1", Kind: KindOpenAI, BaseURL: "https://x.test/v1", Keys: []Key{{ID: "k1", Value: "v"}}}
	if err := good.Validate(); err != nil {
		t.Fatalf("a plain provider should validate: %v", err)
	}
	for _, tc := range []struct {
		name string
		p    *Provider
	}{
		{"empty name", &Provider{Kind: KindOpenAI, BaseURL: "https://x.test"}},
		{"a name with a space", &Provider{Name: "my host", Kind: KindOpenAI, BaseURL: "https://x.test"}},
		{"unknown style", &Provider{Name: "x", Kind: "ollama", BaseURL: "https://x.test"}},
		{"no scheme", &Provider{Name: "x", Kind: KindOpenAI, BaseURL: "x.test/v1"}},
		{"a file URL", &Provider{Name: "x", Kind: KindOpenAI, BaseURL: "file:///etc/passwd"}},
		{"duplicate key ids", &Provider{Name: "x", Kind: KindOpenAI, BaseURL: "https://x.test",
			Keys: []Key{{ID: "k1", Value: "a"}, {ID: "k1", Value: "b"}}}},
	} {
		if err := tc.p.Validate(); err == nil {
			t.Errorf("%s should not validate", tc.name)
		}
	}
}
