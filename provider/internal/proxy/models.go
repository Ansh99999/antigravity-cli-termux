package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/discover"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/wire"
)

// modelCache keeps a listing briefly: the engine asks for it on every start and
// some hosts rate-limit the endpoint harder than they do completions.
type modelCache struct {
	mu       sync.Mutex
	provider string
	models   []string
	at       time.Time
}

const modelCacheTTL = 60 * time.Second

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	p, err := s.provider()
	if err != nil {
		s.fail(w, http.StatusFailedDependency, "FAILED_PRECONDITION", err.Error())
		return
	}

	s.listings.mu.Lock()
	cached := s.listings.models
	fresh := s.listings.provider == p.Name && time.Since(s.listings.at) < modelCacheTTL
	s.listings.mu.Unlock()

	models := cached
	if !fresh {
		key, err := s.picker.Pick(p, nil)
		if err != nil {
			s.fail(w, http.StatusFailedDependency, "FAILED_PRECONDITION", err.Error())
			return
		}
		found, err := discover.Models(r.Context(), s.client, p, key.Value)
		if err != nil {
			// Falling back to what the registry already knows keeps a start-up
			// listing from being fatal when the endpoint is fussy.
			if len(p.Discovered) > 0 {
				found = p.Discovered
			} else if len(cached) > 0 {
				found = cached
			} else {
				s.fail(w, http.StatusBadGateway, "UNAVAILABLE", err.Error())
				return
			}
		}
		models = found
		s.listings.mu.Lock()
		s.listings.provider, s.listings.models, s.listings.at = p.Name, models, time.Now()
		s.listings.mu.Unlock()
	}

	writeJSON(w, http.StatusOK, map[string]any{"models": geminiModelList(models, p)})
}

// geminiModelList dresses plain model ids up as a Gemini ListModels reply. The
// model the provider is pinned to is listed first so a picker that takes the
// head of the list picks the right thing.
func geminiModelList(ids []string, p *config.Provider) []map[string]any {
	ordered := make([]string, 0, len(ids)+1)
	if p.Model != "" {
		ordered = append(ordered, p.Model)
	}
	for _, id := range ids {
		if id != p.Model {
			ordered = append(ordered, id)
		}
	}

	out := make([]map[string]any, 0, len(ordered))
	for _, id := range ordered {
		out = append(out, map[string]any{
			"name":                       "models/" + id,
			"displayName":                id,
			"description":                "served by " + p.Name,
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
			"inputTokenLimit":            1048576,
			"outputTokenLimit":           65536,
		})
	}
	return out
}

func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request, asked string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		s.fail(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	p, err := s.provider()
	if err != nil {
		s.fail(w, http.StatusFailedDependency, "FAILED_PRECONDITION", err.Error())
		return
	}
	model := p.ResolveModel(asked)

	// A countTokens request wraps the thing to be counted in a
	// generateContentRequest, or is one itself.
	var envelope struct {
		GenerateContentRequest *wire.GeminiRequest `json:"generateContentRequest"`
	}
	g := &wire.GeminiRequest{}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.GenerateContentRequest != nil {
		g = envelope.GenerateContentRequest
	} else if err := json.Unmarshal(raw, g); err != nil {
		s.fail(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}

	switch p.Kind {
	case config.KindGemini:
		s.countUpstream(w, r, p, p.GeminiURL(model, "countTokens"), raw, func(body []byte) (int, error) {
			var out struct {
				TotalTokens int `json:"totalTokens"`
			}
			err := json.Unmarshal(body, &out)
			return out.TotalTokens, err
		})

	case config.KindAnthropic:
		req, err := wire.ToAnthropic(g, model, false)
		if err != nil {
			s.fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		req.Stream = false
		body, err := json.Marshal(map[string]any{
			"model":    req.Model,
			"system":   req.System,
			"messages": req.Messages,
			"tools":    req.Tools,
		})
		if err != nil {
			s.fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		s.countUpstream(w, r, p, p.TokenCountURL(), body, func(body []byte) (int, error) {
			var out struct {
				InputTokens int `json:"input_tokens"`
			}
			err := json.Unmarshal(body, &out)
			return out.InputTokens, err
		})

	default:
		// OpenAI's dialect has no counting endpoint at all. The engine uses this
		// for a context gauge rather than for anything load-bearing, so an
		// estimate is better than an error.
		writeJSON(w, http.StatusOK, map[string]any{"totalTokens": estimateTokens(g)})
	}
}

func (s *Server) countUpstream(w http.ResponseWriter, r *http.Request, p *config.Provider, url string, body []byte, read func([]byte) (int, error)) {
	key, err := s.picker.Pick(p, nil)
	if err != nil {
		s.fail(w, http.StatusFailedDependency, "FAILED_PRECONDITION", err.Error())
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		s.fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	discover.Authorize(req, p, key.Value)
	for _, h := range p.Headers {
		req.Header.Set(h.Name, h.Value)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "UNAVAILABLE", err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	if resp.StatusCode >= 400 {
		s.fail(w, resp.StatusCode, statusName(resp.StatusCode), discover.Message(raw, resp.Status))
		return
	}
	total, err := read(raw)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "UNAVAILABLE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"totalTokens": total})
}

// estimateTokens is the usual four-characters-per-token rule, counting an
// attachment as a flat cost since its size says little about its token count.
func estimateTokens(g *wire.GeminiRequest) int {
	chars := 0
	images := 0
	count := func(c *wire.GeminiContent) {
		if c == nil {
			return
		}
		for _, p := range c.Parts {
			chars += len(p.Text)
			if p.FunctionCall != nil {
				chars += len(p.FunctionCall.Name) + len(p.FunctionCall.Args)
			}
			if p.FunctionResponse != nil {
				chars += len(p.FunctionResponse.Name) + len(p.FunctionResponse.Response)
			}
			if p.InlineData != nil || p.FileData != nil {
				images++
			}
		}
	}
	count(g.SystemInstruction)
	for i := range g.Contents {
		count(&g.Contents[i])
	}
	for _, t := range g.Tools {
		for _, d := range t.FunctionDeclarations {
			chars += len(d.Name) + len(d.Description) + len(d.Parameters)
		}
	}
	return chars/4 + images*800
}
