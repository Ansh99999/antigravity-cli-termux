// Package discover asks a provider what it can do: which models it serves, and
// whether a given key actually works.
package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/wire"
)

// Models lists the model ids the provider serves, sorted.
func Models(ctx context.Context, client *http.Client, p *config.Provider, key string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.ModelsURL(), nil)
	if err != nil {
		return nil, err
	}
	Authorize(req, p, key)
	for _, h := range p.Headers {
		req.Header.Set(h.Name, h.Value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &HTTPError{Status: resp.StatusCode, Message: Message(raw, resp.Status)}
	}

	ids, err := parseModels(p.Kind, raw)
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

func parseModels(kind config.Kind, raw []byte) ([]string, error) {
	switch kind {
	case config.KindGemini:
		var list struct {
			Models []struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("model list: %w", err)
		}
		var ids []string
		for _, m := range list.Models {
			// A host that says which methods it supports is telling us which of
			// its models can hold a conversation; embedding models cannot.
			if len(m.SupportedGenerationMethods) > 0 && !supportsGenerate(m.SupportedGenerationMethods) {
				continue
			}
			ids = append(ids, strings.TrimPrefix(m.Name, "models/"))
		}
		return ids, nil

	case config.KindAnthropic:
		var list wire.AnthropicModels
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("model list: %w", err)
		}
		var ids []string
		for _, m := range list.Data {
			ids = append(ids, m.ID)
		}
		return ids, nil

	default:
		var list wire.OpenAIModels
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("model list: %w", err)
		}
		var ids []string
		for _, m := range list.Data {
			ids = append(ids, m.ID)
		}
		return ids, nil
	}
}

func supportsGenerate(methods []string) bool {
	for _, m := range methods {
		// Case-folded, because the method may be named generateContent or
		// streamGenerateContent and only one of those contains the other.
		if strings.Contains(strings.ToLower(m), "generatecontent") {
			return true
		}
	}
	return false
}

// Authorize applies the provider's dialect of authentication to a request.
func Authorize(req *http.Request, p *config.Provider, key string) {
	if key == "" {
		return
	}
	switch p.Kind {
	case config.KindGemini:
		req.Header.Set("x-goog-api-key", key)
	case config.KindAnthropic:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

// HTTPError is a rejection from the provider, kept whole so the caller can tell
// a bad key from a bad URL.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// Message digs the human-readable part out of any of the three error shapes.
func Message(raw []byte, fallback string) string {
	var probe struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil {
		if probe.Error.Message != "" {
			return probe.Error.Message
		}
		if probe.Message != "" {
			return probe.Message
		}
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return fallback
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}

// KeyResult is how one key fared against the provider.
type KeyResult struct {
	KeyID   string
	Label   string
	OK      bool
	Status  int
	Detail  string
	Latency time.Duration
	Models  int
}

// TestKeys tries every enabled key against the model listing, which is the
// cheapest call that still proves the credential works. It costs no tokens.
func TestKeys(ctx context.Context, client *http.Client, p *config.Provider) []KeyResult {
	var out []KeyResult
	for _, k := range p.EnabledKeys() {
		started := time.Now()
		models, err := Models(ctx, client, p, k.Value)
		res := KeyResult{KeyID: k.ID, Label: k.Label, Latency: time.Since(started), Models: len(models)}
		switch e := err.(type) {
		case nil:
			res.OK = true
		case *HTTPError:
			res.Status, res.Detail = e.Status, e.Message
		default:
			res.Detail = err.Error()
		}
		out = append(out, res)
	}
	return out
}
