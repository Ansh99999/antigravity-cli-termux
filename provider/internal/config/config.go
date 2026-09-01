// Package config holds the provider registry: the file the `agy provider`
// subcommands edit and the proxy reads on every request.
//
// The registry lives in its own file, never in the CLI's own settings.json —
// the engine rewrites that file and preserves only the keys it knows about, and
// an API key does not belong in a file that syncs with the desktop app.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kind is the wire dialect a provider speaks. It is not the vendor: any host
// that answers OpenAI's /chat/completions is KindOpenAI regardless of whose
// models it serves.
type Kind string

const (
	KindOpenAI    Kind = "openai"
	KindAnthropic Kind = "anthropic"
	KindGemini    Kind = "gemini"
)

// Strategy picks which key of several to use for a request.
type Strategy string

const (
	// StrategyFirst always uses the first enabled key; the rest are failover.
	StrategyFirst Strategy = "first"
	// StrategyRotate walks the enabled keys round-robin, persisting the cursor.
	StrategyRotate Strategy = "rotate"
	// StrategyRandom picks uniformly at random.
	StrategyRandom Strategy = "random"
	// StrategyLeastErrors prefers the key with the fewest recorded failures.
	StrategyLeastErrors Strategy = "least-errors"
)

// Key is one credential. Several keys under one provider are what the rotation
// strategies choose between.
type Key struct {
	ID      string `json:"id"`
	Value   string `json:"value"`
	Label   string `json:"label,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// On reports whether the key may be used. Absent means enabled, so a
// hand-written entry needs nothing but a value.
func (k Key) On() bool { return k.Enabled == nil || *k.Enabled }

// Provider is one endpoint plus the credentials for it.
type Provider struct {
	Name    string   `json:"name"`
	Kind    Kind     `json:"kind"`
	BaseURL string   `json:"baseUrl"`
	Keys    []Key    `json:"keys,omitempty"`
	Model   string   `json:"model,omitempty"`
	Fast    string   `json:"fastModel,omitempty"`
	Headers []Header `json:"headers,omitempty"`

	// Strategy defaults to StrategyRotate once there is more than one key.
	Strategy Strategy `json:"keyStrategy,omitempty"`
	// CooldownSeconds is how long a key rests after a rejection. 0 uses the
	// package default.
	CooldownSeconds int `json:"cooldownSeconds,omitempty"`
	// MaxAttempts caps how many keys one request may burn through before the
	// failure is reported to the CLI. 0 uses the package default.
	MaxAttempts int `json:"maxAttempts,omitempty"`

	// ModelMap rewrites the model the engine asked for into one this host
	// serves, keyed by the name as it arrives on the wire.
	ModelMap map[string]string `json:"modelMap,omitempty"`
	// Discovered is the last model list read from the host, kept only so `list`
	// and the picker have something to show without a round trip.
	Discovered []string `json:"discovered,omitempty"`
}

// Header is a literal request header sent with every call to this provider —
// OpenRouter's attribution pair, a gateway's tenant id, and the like.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// File is the whole registry as it sits on disk.
type File struct {
	Version   int         `json:"version"`
	Active    string      `json:"active,omitempty"`
	Providers []*Provider `json:"providers"`
	Proxy     Proxy       `json:"proxy,omitzero"`
}

// Proxy is where the translating server listens. The host is fixed to loopback
// unless deliberately overridden; see cmd/agy-provider for the warning.
type Proxy struct {
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
}

// Version is the schema version written by this build.
const Version = 1

// Defaults applied when a provider leaves the field at zero.
const (
	DefaultCooldownSeconds = 60
	DefaultMaxAttempts     = 3
)

// Cooldown is the rest period for a rejected key.
func (p *Provider) Cooldown() int {
	if p.CooldownSeconds > 0 {
		return p.CooldownSeconds
	}
	return DefaultCooldownSeconds
}

// Attempts is how many keys one request may try.
func (p *Provider) Attempts() int {
	if p.MaxAttempts > 0 {
		return p.MaxAttempts
	}
	if n := len(p.EnabledKeys()); n > 0 && n < DefaultMaxAttempts {
		return n
	}
	return DefaultMaxAttempts
}

// EnabledKeys is the keys rotation may choose between, in file order.
func (p *Provider) EnabledKeys() []Key {
	out := make([]Key, 0, len(p.Keys))
	for _, k := range p.Keys {
		if k.On() && strings.TrimSpace(k.Value) != "" {
			out = append(out, k)
		}
	}
	return out
}

// EffectiveStrategy is the strategy to apply, defaulted by key count so a
// second key starts rotating without anyone having to say so.
func (p *Provider) EffectiveStrategy() Strategy {
	if p.Strategy != "" {
		return p.Strategy
	}
	if len(p.EnabledKeys()) > 1 {
		return StrategyRotate
	}
	return StrategyFirst
}

// ResolveModel maps the model the engine asked for onto one this host serves.
// An explicit ModelMap entry wins, then the provider's pinned Model, and
// failing both the name is passed through untouched.
func (p *Provider) ResolveModel(asked string) string {
	if to, ok := p.ModelMap[asked]; ok && to != "" {
		return to
	}
	if p.Model != "" {
		return p.Model
	}
	return asked
}

// NeedsProxy reports whether this provider can only be served by the local
// translating proxy. A single-key Gemini-shaped host needs nothing: the engine
// speaks to it directly and the launcher just points it there.
func (p *Provider) NeedsProxy() bool {
	if p.Kind != KindGemini {
		return true
	}
	return len(p.EnabledKeys()) > 1 || len(p.ModelMap) > 0 || len(p.Headers) > 0 || p.Model != ""
}

// Dir is the directory holding the registry and the proxy's state file.
// AGY_PROVIDER_HOME overrides it, which is what the tests use.
func Dir() string {
	if d := os.Getenv("AGY_PROVIDER_HOME"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "agy")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agy"
	}
	return filepath.Join(home, ".config", "agy")
}

// Path is the registry file.
func Path() string { return filepath.Join(Dir(), "providers.json") }

// ErrNoProvider is returned when a name does not name a provider.
var ErrNoProvider = errors.New("no such provider")

// Load reads the registry. A missing file is an empty registry, not an error —
// the launcher runs on every start and must not care.
func Load() (*File, error) {
	raw, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return &File{Version: Version}, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(), err)
	}
	if f.Version == 0 {
		f.Version = Version
	}
	return &f, nil
}

// Save writes the registry atomically at 0600. The temp file is created in the
// destination directory so the rename cannot cross a filesystem.
func Save(f *File) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	f.Version = Version
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeAtomic(Path(), raw, 0o600)
}

func writeAtomic(path string, raw []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
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
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Find returns the named provider.
func (f *File) Find(name string) (*Provider, error) {
	for _, p := range f.Providers {
		if strings.EqualFold(p.Name, name) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrNoProvider, name)
}

// ActiveProvider is the provider requests should go to, or nil when the CLI
// should fall back to its own Google sign-in.
func (f *File) ActiveProvider() *Provider {
	if f.Active == "" {
		return nil
	}
	p, err := f.Find(f.Active)
	if err != nil {
		return nil
	}
	return p
}

// Remove drops a provider, clearing Active if it pointed there.
func (f *File) Remove(name string) error {
	for i, p := range f.Providers {
		if strings.EqualFold(p.Name, name) {
			f.Providers = append(f.Providers[:i], f.Providers[i+1:]...)
			if strings.EqualFold(f.Active, name) {
				f.Active = ""
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrNoProvider, name)
}

// Upsert adds a provider or replaces the one with the same name.
func (f *File) Upsert(p *Provider) {
	for i, existing := range f.Providers {
		if strings.EqualFold(existing.Name, p.Name) {
			f.Providers[i] = p
			return
		}
	}
	f.Providers = append(f.Providers, p)
}
