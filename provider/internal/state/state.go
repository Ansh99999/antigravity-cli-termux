// Package state holds what changes as the proxy runs: which key rotation is up
// to, which keys are resting after a rejection, and where the proxy is
// listening. It is deliberately a separate file from the registry — this one is
// rewritten while requests are in flight, and losing it costs nothing but a
// rotation cursor.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
)

// KeyHealth is what rotation and `agy provider status` know about one key.
type KeyHealth struct {
	Uses          int       `json:"uses,omitempty"`
	Errors        int       `json:"errors,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
	LastErrorAt   time.Time `json:"lastErrorAt,omitzero"`
	CooldownUntil time.Time `json:"cooldownUntil,omitzero"`
	LastUsedAt    time.Time `json:"lastUsedAt,omitzero"`
}

// Resting reports whether the key is still inside its cooldown.
func (h KeyHealth) Resting(now time.Time) bool {
	return !h.CooldownUntil.IsZero() && h.CooldownUntil.After(now)
}

// Proxy records the running translating server so a second launch reuses it
// instead of starting another.
type Proxy struct {
	PID       int       `json:"pid,omitempty"`
	Port      int       `json:"port,omitempty"`
	Token     string    `json:"token,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	StartedAt time.Time `json:"startedAt,omitzero"`
}

// Data is the whole state file.
type Data struct {
	Cursor map[string]int        `json:"cursor,omitempty"`
	Keys   map[string]*KeyHealth `json:"keys,omitempty"`
	Proxy  *Proxy                `json:"proxy,omitempty"`
	// PriorModelProvider is whatever the CLI's own modelProvider setting said
	// before a provider of ours was first switched on, so turning them off can
	// put the setting back rather than guessing at a default.
	PriorModelProvider *string `json:"priorModelProvider,omitempty"`
}

// Health returns the record for a key, creating it if new.
func (d *Data) Health(provider, keyID string) *KeyHealth {
	if d.Keys == nil {
		d.Keys = map[string]*KeyHealth{}
	}
	k := provider + "/" + keyID
	if d.Keys[k] == nil {
		d.Keys[k] = &KeyHealth{}
	}
	return d.Keys[k]
}

// Path is the state file, beside the registry.
func Path() string { return filepath.Join(config.Dir(), "state.json") }

func lockPath() string { return filepath.Join(config.Dir(), "state.lock") }

// Load reads the state file. A missing or corrupt file reads as empty state:
// this holds nothing worth failing a launch over.
func Load() *Data {
	raw, err := os.ReadFile(Path())
	if err != nil {
		return &Data{}
	}
	var d Data
	if err := json.Unmarshal(raw, &d); err != nil {
		return &Data{}
	}
	return &d
}

// Save writes the state file under an advisory lock, so the proxy flushing a
// rotation cursor cannot land on top of the CLI clearing a cooldown.
func Save(d *Data) error {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(Path(), append(raw, '\n'), 0o600)
}

// Update reads, mutates and writes the state file with the lock held for the
// whole cycle.
func Update(mutate func(*Data)) error {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()
	d := Load()
	mutate(d)
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(Path(), append(raw, '\n'), 0o600)
}

func lock() (func(), error) {
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
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

// ErrNoProxy means no proxy is recorded as running.
var ErrNoProxy = errors.New("no proxy recorded")

// Live reports whether the recorded proxy process still exists. It answers the
// only question a launch has: reuse, or start one?
func (p *Proxy) Live() bool {
	if p == nil || p.PID <= 0 || p.Port <= 0 {
		return false
	}
	return syscall.Kill(p.PID, 0) == nil
}

// Store is the proxy's own view of the state: authoritative in memory, flushed
// to disk on a timer so a busy stream is not one file rewrite per token.
type Store struct {
	mu    sync.Mutex
	data  *Data
	dirty bool
}

// NewStore loads the state for a proxy run.
func NewStore() *Store { return &Store{data: Load()} }

// With runs fn against the state under the store's lock and marks it dirty.
func (s *Store) With(fn func(*Data)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.data)
	s.dirty = true
}

// Read runs fn against the state without marking it dirty.
func (s *Store) Read(fn func(*Data)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.data)
}

// Flush writes the state if anything changed since the last write.
func (s *Store) Flush() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	snapshot := *s.data
	s.dirty = false
	s.mu.Unlock()
	// Merge onto whatever is on disk so a cooldown the CLI cleared meanwhile is
	// not resurrected wholesale.
	_ = Update(func(d *Data) {
		d.Cursor = snapshot.Cursor
		d.Keys = snapshot.Keys
		if snapshot.Proxy != nil {
			d.Proxy = snapshot.Proxy
		}
	})
}

// FlushEvery flushes on a ticker until done closes, then flushes once more.
func (s *Store) FlushEvery(interval time.Duration, done <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.Flush()
		case <-done:
			s.Flush()
			return
		}
	}
}
