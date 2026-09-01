// Package keys chooses which credential a request goes out with, and records
// how that went so the next choice is better informed.
package keys

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/state"
)

// Picker holds the rotation cursor and key health for a run.
type Picker struct {
	store *state.Store
	now   func() time.Time
}

// New returns a picker over the given store. A nil clock means time.Now.
func New(store *state.Store, now func() time.Time) *Picker {
	if now == nil {
		now = time.Now
	}
	return &Picker{store: store, now: now}
}

// ErrNoKey means the provider has nothing usable to send.
type ErrNoKey struct {
	Provider string
	Reason   string
}

func (e *ErrNoKey) Error() string {
	return fmt.Sprintf("provider %q has no usable key: %s", e.Provider, e.Reason)
}

// Pick chooses a key for one attempt. Keys named in exclude have already been
// tried for this request and are not offered again.
func (pk *Picker) Pick(p *config.Provider, exclude map[string]bool) (config.Key, error) {
	enabled := p.EnabledKeys()
	if len(enabled) == 0 {
		return config.Key{}, &ErrNoKey{Provider: p.Name, Reason: "no enabled key with a value"}
	}

	now := pk.now()
	var fresh, resting []config.Key
	pk.store.Read(func(d *state.Data) {
		for _, k := range enabled {
			if exclude[k.ID] {
				continue
			}
			if d.Health(p.Name, k.ID).Resting(now) {
				resting = append(resting, k)
				continue
			}
			fresh = append(fresh, k)
		}
	})

	// Every untried key is resting: use the one whose rest ends soonest rather
	// than refusing outright. A cooldown is a hint, not a contract, and a wrong
	// guess about a rate limit should not take the CLI offline.
	candidates := fresh
	if len(candidates) == 0 {
		if len(resting) == 0 {
			return config.Key{}, &ErrNoKey{Provider: p.Name, Reason: "every key has already been tried for this request"}
		}
		candidates = []config.Key{pk.soonestFree(p, resting)}
	}

	chosen := pk.apply(p, enabled, candidates)
	pk.store.With(func(d *state.Data) {
		h := d.Health(p.Name, chosen.ID)
		h.Uses++
		h.LastUsedAt = now
	})
	return chosen, nil
}

func (pk *Picker) soonestFree(p *config.Provider, resting []config.Key) config.Key {
	best := resting[0]
	var bestUntil time.Time
	pk.store.Read(func(d *state.Data) {
		bestUntil = d.Health(p.Name, best.ID).CooldownUntil
		for _, k := range resting[1:] {
			if until := d.Health(p.Name, k.ID).CooldownUntil; until.Before(bestUntil) {
				best, bestUntil = k, until
			}
		}
	})
	return best
}

func (pk *Picker) apply(p *config.Provider, enabled, candidates []config.Key) config.Key {
	switch p.EffectiveStrategy() {
	case config.StrategyRandom:
		return candidates[rand.IntN(len(candidates))]

	case config.StrategyLeastErrors:
		best := candidates[0]
		pk.store.Read(func(d *state.Data) {
			bestH := d.Health(p.Name, best.ID)
			for _, k := range candidates[1:] {
				h := d.Health(p.Name, k.ID)
				if h.Errors < bestH.Errors || (h.Errors == bestH.Errors && h.Uses < bestH.Uses) {
					best, bestH = k, h
				}
			}
		})
		return best

	case config.StrategyRotate:
		// Walk the full enabled order from the cursor so rotation stays even
		// even when some keys are resting and never become candidates.
		var chosen config.Key
		pk.store.With(func(d *state.Data) {
			if d.Cursor == nil {
				d.Cursor = map[string]int{}
			}
			start := d.Cursor[p.Name]
			for offset := 0; offset < len(enabled); offset++ {
				idx := (start + offset) % len(enabled)
				for _, c := range candidates {
					if c.ID == enabled[idx].ID {
						chosen = c
						d.Cursor[p.Name] = (idx + 1) % len(enabled)
						return
					}
				}
			}
			chosen = candidates[0]
			d.Cursor[p.Name] = 0
		})
		return chosen

	default: // StrategyFirst
		return candidates[0]
	}
}

// Succeeded clears the failure record for a key that just worked.
func (pk *Picker) Succeeded(p *config.Provider, keyID string) {
	pk.store.With(func(d *state.Data) {
		h := d.Health(p.Name, keyID)
		h.Errors = 0
		h.LastError = ""
		h.CooldownUntil = time.Time{}
	})
}

// Failed records a rejection and rests the key. Only statuses that say
// something about the *credential* or the host's willingness to serve it earn a
// cooldown; a 400 for a malformed request would rest every key in turn for a
// fault none of them has.
func (pk *Picker) Failed(p *config.Provider, keyID string, status int, reason string) {
	rest := CooldownFor(status, p.Cooldown())
	pk.store.With(func(d *state.Data) {
		h := d.Health(p.Name, keyID)
		h.Errors++
		h.LastError = reason
		h.LastErrorAt = pk.now()
		if rest > 0 {
			h.CooldownUntil = pk.now().Add(rest)
		}
	})
}

// CooldownFor is how long a key rests after a given status, and 0 for statuses
// that are not the key's fault.
func CooldownFor(status int, base int) time.Duration {
	d := time.Duration(base) * time.Second
	switch status {
	case http.StatusTooManyRequests:
		return d
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired:
		// A dead or unfunded key will still be dead in a minute; rest it longer
		// so rotation stops handing it out on every request.
		return 10 * d
	case 0: // transport failure: the host, not the key
		return 0
	}
	if status >= 500 {
		return d
	}
	return 0
}

// Retryable reports whether another key is worth trying after this status.
func Retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusPaymentRequired, http.StatusRequestTimeout:
		return true
	case 0:
		return true
	}
	return status >= 500
}
