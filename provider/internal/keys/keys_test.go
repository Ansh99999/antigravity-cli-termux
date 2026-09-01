package keys

import (
	"net/http"
	"testing"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/state"
)

func provider(strategy config.Strategy, ids ...string) *config.Provider {
	p := &config.Provider{Name: "p", Kind: config.KindOpenAI, BaseURL: "https://x.test/v1", Strategy: strategy}
	for _, id := range ids {
		p.Keys = append(p.Keys, config.Key{ID: id, Value: "value-" + id})
	}
	return p
}

// picker builds a picker over a store rooted in a temp directory, with a clock
// the test drives.
func picker(t *testing.T, now *time.Time) *Picker {
	t.Helper()
	t.Setenv("AGY_PROVIDER_HOME", t.TempDir())
	return New(state.NewStore(), func() time.Time { return *now })
}

func pick(t *testing.T, pk *Picker, p *config.Provider) string {
	t.Helper()
	k, err := pk.Pick(p, nil)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	return k.ID
}

func TestRotateWalksEveryKeyInOrder(t *testing.T) {
	now := time.Now()
	pk := picker(t, &now)
	p := provider(config.StrategyRotate, "k1", "k2", "k3")

	var got []string
	for range 7 {
		got = append(got, pick(t, pk, p))
	}
	want := []string{"k1", "k2", "k3", "k1", "k2", "k3", "k1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rotation went %v, want %v", got, want)
		}
	}
}

func TestFirstStaysOnTheFirstKey(t *testing.T) {
	now := time.Now()
	pk := picker(t, &now)
	p := provider(config.StrategyFirst, "k1", "k2")
	for range 3 {
		if got := pick(t, pk, p); got != "k1" {
			t.Fatalf("want k1 every time, got %s", got)
		}
	}
}

func TestARestingKeyIsSkipped(t *testing.T) {
	now := time.Now()
	pk := picker(t, &now)
	p := provider(config.StrategyRotate, "k1", "k2", "k3")

	pk.Failed(p, "k2", http.StatusTooManyRequests, "rate limited")
	var got []string
	for range 4 {
		got = append(got, pick(t, pk, p))
	}
	for _, id := range got {
		if id == "k2" {
			t.Fatalf("a rate-limited key should rest, got %v", got)
		}
	}

	// Once the cooldown has passed it comes back into the rotation.
	now = now.Add(time.Duration(p.Cooldown()+1) * time.Second)
	seen := map[string]bool{}
	for range 3 {
		seen[pick(t, pk, p)] = true
	}
	if !seen["k2"] {
		t.Error("the key should return once its cooldown has passed")
	}
}

func TestEveryKeyRestingStillAnswers(t *testing.T) {
	now := time.Now()
	pk := picker(t, &now)
	p := provider(config.StrategyRotate, "k1", "k2")

	pk.Failed(p, "k1", http.StatusTooManyRequests, "limited")
	now = now.Add(30 * time.Second)
	pk.Failed(p, "k2", http.StatusTooManyRequests, "limited")

	// k1 rests until now+30s, k2 until now+60s, so k1 frees up first.
	got, err := pk.Pick(p, nil)
	if err != nil {
		t.Fatalf("a cooldown is a hint, not an outage: %v", err)
	}
	if got.ID != "k1" {
		t.Errorf("want the key that frees up soonest, got %s", got.ID)
	}
}

func TestLeastErrorsPrefersTheHealthiest(t *testing.T) {
	now := time.Now()
	pk := picker(t, &now)
	p := provider(config.StrategyLeastErrors, "k1", "k2", "k3")

	// Two failures each that do not rest the key, so the choice is about the
	// error count alone.
	for range 2 {
		pk.Failed(p, "k1", http.StatusBadRequest, "bad request")
	}
	pk.Failed(p, "k2", http.StatusBadRequest, "bad request")

	if got := pick(t, pk, p); got != "k3" {
		t.Errorf("want the key with no failures, got %s", got)
	}
	pk.Succeeded(p, "k1")
	if got := pick(t, pk, p); got != "k1" {
		t.Errorf("a success clears the record, so k1 should lead now, got %s", got)
	}
}

func TestExcludedKeysAreNotOfferedTwice(t *testing.T) {
	now := time.Now()
	pk := picker(t, &now)
	p := provider(config.StrategyRotate, "k1", "k2")

	first, err := pk.Pick(p, nil)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	second, err := pk.Pick(p, map[string]bool{first.ID: true})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("a key already tried for this request must not come back")
	}
	if _, err := pk.Pick(p, map[string]bool{"k1": true, "k2": true}); err == nil {
		t.Fatal("with every key tried, Pick has to fail")
	}
}

func TestNoUsableKey(t *testing.T) {
	now := time.Now()
	pk := picker(t, &now)
	off := false
	p := &config.Provider{Name: "p", Keys: []config.Key{
		{ID: "k1", Value: ""},
		{ID: "k2", Value: "v", Enabled: &off},
	}}
	if _, err := pk.Pick(p, nil); err == nil {
		t.Fatal("a provider with nothing usable must say so")
	}
}

func TestCooldownDependsOnWhatWentWrong(t *testing.T) {
	base := 60
	for _, tc := range []struct {
		status int
		want   time.Duration
		reason string
	}{
		{http.StatusTooManyRequests, 60 * time.Second, "a rate limit rests the key"},
		{http.StatusUnauthorized, 600 * time.Second, "a dead key rests far longer"},
		{http.StatusForbidden, 600 * time.Second, "so does a forbidden one"},
		{http.StatusPaymentRequired, 600 * time.Second, "and an unfunded one"},
		{http.StatusInternalServerError, 60 * time.Second, "a server fault might be the key's region"},
		{http.StatusBadRequest, 0, "a malformed request is not the key's fault"},
		{http.StatusNotFound, 0, "nor is a wrong path"},
		{0, 0, "nor is the network being down"},
	} {
		if got := CooldownFor(tc.status, base); got != tc.want {
			t.Errorf("%s: CooldownFor(%d) = %v, want %v", tc.reason, tc.status, got, tc.want)
		}
	}
}

func TestRetryable(t *testing.T) {
	for _, status := range []int{429, 401, 403, 402, 408, 500, 502, 503, 0} {
		if !Retryable(status) {
			t.Errorf("%d should be worth another key", status)
		}
	}
	for _, status := range []int{400, 404, 413, 422} {
		if Retryable(status) {
			t.Errorf("%d would fail the same way on every key", status)
		}
	}
}

func TestRotationSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGY_PROVIDER_HOME", dir)
	p := provider(config.StrategyRotate, "k1", "k2", "k3")
	now := time.Now()

	first := state.NewStore()
	pk := New(first, func() time.Time { return now })
	if got := pick(t, pk, p); got != "k1" {
		t.Fatalf("want k1 first, got %s", got)
	}
	first.Flush()

	// A second launch reads the cursor back rather than starting over.
	second := New(state.NewStore(), func() time.Time { return now })
	if got := pick(t, second, p); got != "k2" {
		t.Errorf("rotation should carry across launches, got %s", got)
	}
}
