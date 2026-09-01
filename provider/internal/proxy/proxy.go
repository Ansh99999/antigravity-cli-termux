// Package proxy is the local translating server. The CLI is pointed at it with
// GOOGLE_GEMINI_BASE_URL, so it speaks Gemini inbound and whatever the active
// provider speaks outbound — which is what lets an OpenAI- or Anthropic-shaped
// host answer an engine that only knows how to ask Google.
//
// It binds loopback only and requires the token the launcher handed the engine,
// so nothing else on the device can use it as an open relay to your keys.
package proxy

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/keys"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/state"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/termuxnet"
)

// Server is the running proxy.
type Server struct {
	Token  string
	Logger *log.Logger

	client *http.Client
	store  *state.Store
	picker *keys.Picker

	mu       sync.Mutex
	cfg      *config.File
	loadedAt time.Time

	listings modelCache
}

// New builds a server. The store is shared with the launcher through the state
// file, so a cooldown recorded here survives into the next run.
func New(token string, store *state.Store) *Server {
	return &Server{
		Token:  token,
		Logger: log.New(os.Stderr, "[agy-provider] ", log.LstdFlags),
		client: termuxnet.Client(),
		store:  store,
		picker: keys.New(store, nil),
	}
}

// provider returns the active provider, re-reading the registry when it has
// gone stale. Re-reading is what makes `agy provider use <name>` take effect
// on the next request instead of on the next launch.
func (s *Server) provider() (*config.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg == nil || time.Since(s.loadedAt) > time.Second {
		if cfg, err := config.Load(); err == nil {
			s.cfg, s.loadedAt = cfg, time.Now()
		} else if s.cfg == nil {
			return nil, err
		}
	}
	p := s.cfg.ActiveProvider()
	if p == nil {
		return nil, fmt.Errorf("no provider is active; run `agy provider use <name>`")
	}
	return p, nil
}

// Handler is the routing table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/__agy/health", s.handleHealth)
	mux.HandleFunc("/", s.route)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	name := ""
	if p, err := s.provider(); err == nil {
		name = p.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agy":      true,
		"pid":      os.Getpid(),
		"provider": name,
	})
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		s.fail(w, http.StatusUnauthorized, "UNAUTHENTICATED",
			"this is the agy provider proxy and the API key does not match the one it was started with")
		return
	}

	model, method := parsePath(r.URL.Path)
	switch {
	case method == "" && strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/models"):
		s.handleModels(w, r)
	case method == "generateContent", method == "streamGenerateContent":
		s.handleGenerate(w, r, model, method == "streamGenerateContent")
	case method == "countTokens":
		s.handleCountTokens(w, r, model)
	default:
		// Worth a log line rather than a silent 404: this is the shape of the
		// engine's own request, and if a build ever asks for something else this
		// is the only record of what it asked for.
		s.logf("unrecognized request: %s %s", r.Method, r.URL.RequestURI())
		if method == "" {
			s.fail(w, http.StatusNotFound, "NOT_FOUND", "the agy provider proxy does not serve "+r.URL.Path)
			return
		}
		s.fail(w, http.StatusNotImplemented, "UNIMPLEMENTED",
			"the agy provider proxy does not translate "+method)
	}
}

// authorized checks the token the launcher gave the engine. Both of Gemini's
// spellings are accepted because the engine may use either.
func (s *Server) authorized(r *http.Request) bool {
	if s.Token == "" {
		return true
	}
	presented := r.Header.Get("x-goog-api-key")
	if presented == "" {
		presented = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if presented == "" {
		presented = r.URL.Query().Get("key")
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.Token)) == 1
}

// parsePath pulls the model and the method out of a generativelanguage path.
// The model can hold slashes and dots, and the method is whatever follows the
// last colon.
func parsePath(path string) (model, method string) {
	idx := strings.Index(path, "/models/")
	if idx < 0 {
		return "", ""
	}
	rest := path[idx+len("/models/"):]
	if colon := strings.LastIndex(rest, ":"); colon >= 0 {
		return rest[:colon], rest[colon+1:]
	}
	return rest, ""
}

// Listen binds the server. Port 0 asks the kernel for a free one, which is what
// the launcher normally wants.
func Listen(host string, port int) (net.Listener, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	return net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
}

// Serve runs until the listener closes.
func (s *Server) Serve(ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}
	return srv.Serve(ln)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// fail answers in the shape every Google API uses for an error, because that is
// the only error shape the engine knows how to read.
func (s *Server) fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    status,
			"message": message,
			"status":  code,
		},
	})
}

func (s *Server) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}
