// Package launch is what the bootstrapper calls on every start: it decides
// whether the engine talks to a provider of yours, starts the translating proxy
// if one is needed, and hands back the environment to hand off with.
package launch

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/keys"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/proxy"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/settings"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/state"
)

// Env is the outcome of a launch decision.
type Env struct {
	// Vars are the variables to export, in order, as NAME then VALUE.
	Vars [][2]string
	// Provider is the active provider, or nil when the engine should sign in to
	// Google as usual.
	Provider *config.Provider
	// Notes are lines worth telling the user, on stderr.
	Notes []string
	// ProxyPort is non-zero when a translating proxy is serving this launch.
	ProxyPort int
}

// LogPath is where a detached proxy's output goes.
func LogPath() string { return filepath.Join(config.Dir(), "proxy.log") }

// Up prepares the environment for a launch. It never fails the launch: a
// provider that cannot be served returns a note and no variables, so the engine
// starts on its own sign-in rather than not at all.
func Up(writeSettings bool) *Env {
	out := &Env{}

	cfg, err := config.Load()
	if err != nil {
		out.Notes = append(out.Notes, fmt.Sprintf("ignoring the provider registry: %v", err))
		return out
	}
	p := cfg.ActiveProvider()
	if p == nil {
		return out
	}
	if err := p.Validate(); err != nil {
		out.Notes = append(out.Notes, fmt.Sprintf("provider %q is not usable: %v", p.Name, err))
		return out
	}
	if len(p.EnabledKeys()) == 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("provider %q has no enabled key; add one with `agy provider key add %s`", p.Name, p.Name))
		return out
	}
	out.Provider = p

	if !p.NeedsProxy() {
		// A Gemini-shaped host with one key needs no translation and no process:
		// the engine can hold the conversation itself.
		store := state.NewStore()
		key, err := keys.New(store, nil).Pick(p, nil)
		if err != nil {
			out.Notes = append(out.Notes, err.Error())
			return out
		}
		store.Flush()
		out.Vars = [][2]string{
			{"GEMINI_API_KEY", key.Value},
			{"GOOGLE_GEMINI_BASE_URL", p.BaseURL},
		}
	} else {
		port, token, err := EnsureProxy(&cfg.Proxy)
		if err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("could not start the provider proxy: %v", err))
			out.Provider = nil
			return out
		}
		out.ProxyPort = port
		out.Vars = [][2]string{
			{"GEMINI_API_KEY", token},
			{"GOOGLE_GEMINI_BASE_URL", fmt.Sprintf("http://127.0.0.1:%d", port)},
		}
	}

	if writeSettings {
		if previous, err := ActivateSettings(); err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("could not set %s in %s: %v", settings.Key, settings.Path(), err))
		} else if previous != settings.ValueGemini {
			out.Notes = append(out.Notes, fmt.Sprintf("set %s to %q in %s", settings.Key, settings.ValueGemini, settings.Path()))
		}
	}
	return out
}

// ActivateSettings points the CLI's own settings at the Gemini API route, which
// is the only route that reads GEMINI_API_KEY and GOOGLE_GEMINI_BASE_URL. It
// remembers what the setting said the first time it changes it, so switching
// providers off restores that value rather than guessing at a default.
func ActivateSettings() (previous string, err error) {
	previous, err = settings.Set(settings.ValueGemini)
	if err != nil {
		return previous, err
	}
	if previous == settings.ValueGemini {
		return previous, nil
	}
	return previous, state.Update(func(d *state.Data) {
		if d.PriorModelProvider == nil {
			d.PriorModelProvider = &previous
		}
	})
}

// RestoreSettings puts the CLI's modelProvider back to whatever it said before a
// provider of ours was switched on, and reports the value written.
func RestoreSettings() (string, error) {
	prior := ""
	if err := state.Update(func(d *state.Data) {
		if d.PriorModelProvider != nil {
			prior = *d.PriorModelProvider
			d.PriorModelProvider = nil
		}
	}); err != nil {
		return "", err
	}
	_, err := settings.Set(prior)
	return prior, err
}

// EnsureProxy returns a live proxy's port and token, starting one if the
// recorded process is gone.
func EnsureProxy(cfg *config.Proxy) (int, string, error) {
	current := state.Load().Proxy
	if current.Live() && healthy(current.Port) {
		return current.Port, current.Token, nil
	}

	host := "127.0.0.1"
	port := 0
	if cfg != nil {
		if cfg.Host != "" {
			host = cfg.Host
		}
		port = cfg.Port
	}

	// The listener is bound here and handed to the child, so by the time this
	// returns the port is already accepting connections — the engine cannot
	// out-race the start-up.
	ln, err := proxy.Listen(host, port)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = ln.Close() }()

	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return 0, "", fmt.Errorf("listener is not TCP")
	}
	handoff, err := tcp.File()
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = handoff.Close() }()

	token, err := newToken()
	if err != nil {
		return 0, "", err
	}
	bound := tcp.Addr().(*net.TCPAddr).Port

	self, err := os.Executable()
	if err != nil {
		return 0, "", err
	}
	logFile, err := openLog()
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(self, "serve", "--inherit-fd")
	cmd.Env = append(os.Environ(), "AGY_PROVIDER_TOKEN="+token)
	cmd.ExtraFiles = []*os.File{handoff}
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// A new session, so closing the terminal the CLI was started from does not
	// take the proxy down with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	if err := state.Update(func(d *state.Data) {
		d.Proxy = &state.Proxy{PID: cmd.Process.Pid, Port: bound, Token: token, StartedAt: time.Now()}
	}); err != nil {
		return 0, "", err
	}
	// Do not wait on the child: it outlives this process on purpose.
	go func() { _ = cmd.Wait() }()
	return bound, token, nil
}

func openLog() (*os.File, error) {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return nil, err
	}
	// Keep the log from growing without bound across launches.
	if info, err := os.Stat(LogPath()); err == nil && info.Size() > 1<<20 {
		_ = os.Remove(LogPath())
	}
	return os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func newToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "agy-" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// healthy asks a recorded proxy whether it is ours and answering.
func healthy(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/__agy/health", port))
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return false
	}
	var body struct {
		Agy bool `json:"agy"`
	}
	return json.Unmarshal(raw, &body) == nil && body.Agy
}

// Stop ends the recorded proxy.
func Stop() (bool, error) {
	current := state.Load().Proxy
	if !current.Live() {
		err := state.Update(func(d *state.Data) { d.Proxy = nil })
		return false, err
	}
	if err := syscall.Kill(current.PID, syscall.SIGTERM); err != nil {
		return false, err
	}
	for range 20 {
		if !current.Live() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true, state.Update(func(d *state.Data) { d.Proxy = nil })
}
