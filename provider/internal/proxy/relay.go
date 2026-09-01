package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/keys"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/sse"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/wire"
)

// maxRequestBytes bounds an inbound transcript. A long agent conversation with
// screenshots in it is megabytes; anything past this is not a transcript.
const maxRequestBytes = 64 << 20

// maxErrorBytes bounds how much of an upstream error body is read back.
const maxErrorBytes = 64 << 10

// plan is one dialect's answer to "how do I ask this host the question the
// engine just asked me, and how do I read the reply".
type plan struct {
	url         string
	body        []byte
	auth        func(*http.Request, string)
	whole       func([]byte) (*wire.GeminiResponse, error)
	stream      func() wire.StreamDecoder
	passthrough bool
}

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request, asked string, streaming bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		s.fail(w, http.StatusBadRequest, "INVALID_ARGUMENT", "reading the request: "+err.Error())
		return
	}
	p, err := s.provider()
	if err != nil {
		s.fail(w, http.StatusFailedDependency, "FAILED_PRECONDITION", err.Error())
		return
	}

	var g wire.GeminiRequest
	if err := json.Unmarshal(raw, &g); err != nil {
		s.fail(w, http.StatusBadRequest, "INVALID_ARGUMENT", "this is not a GenerateContentRequest: "+err.Error())
		return
	}

	model := p.ResolveModel(asked)
	pl, err := s.planFor(p, &g, raw, model, streaming, r.URL.RawQuery)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	s.attempt(w, r, p, pl, streaming, model)
}

// planFor builds the upstream call for the active provider's dialect.
func (s *Server) planFor(p *config.Provider, g *wire.GeminiRequest, raw []byte, model string, streaming bool, query string) (*plan, error) {
	switch p.Kind {
	case config.KindGemini:
		method := "generateContent"
		if streaming {
			method = "streamGenerateContent"
		}
		url := p.GeminiURL(model, method)
		if streaming {
			url = withAltSSE(url, query)
		}
		return &plan{
			url:         url,
			body:        raw,
			auth:        func(req *http.Request, key string) { req.Header.Set("x-goog-api-key", key) },
			passthrough: true,
		}, nil

	case config.KindOpenAI:
		req, err := wire.ToOpenAI(g, model, streaming)
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		return &plan{
			url:    p.ChatURL(),
			body:   body,
			auth:   func(r *http.Request, key string) { r.Header.Set("Authorization", "Bearer "+key) },
			whole:  wire.FromOpenAI,
			stream: func() wire.StreamDecoder { return wire.NewOpenAIStream() },
		}, nil

	case config.KindAnthropic:
		req, err := wire.ToAnthropic(g, model, streaming)
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		return &plan{
			url:  p.ChatURL(),
			body: body,
			auth: func(r *http.Request, key string) {
				r.Header.Set("x-api-key", key)
				r.Header.Set("anthropic-version", "2023-06-01")
			},
			whole:  wire.FromAnthropic,
			stream: func() wire.StreamDecoder { return wire.NewAnthropicStream() },
		}, nil
	}
	return nil, fmt.Errorf("provider %q has an unknown style %q", p.Name, p.Kind)
}

func withAltSSE(url, query string) string {
	if strings.Contains(query, "alt=sse") || strings.Contains(url, "alt=sse") {
		if !strings.Contains(url, "alt=sse") {
			return url + "?alt=sse"
		}
		return url
	}
	return url + "?alt=sse"
}

// attempt sends the request, rotating onto the next key while the failures are
// ones another key could fix. Nothing is written to the client until a host has
// accepted the request, so a rotation is invisible to the CLI.
func (s *Server) attempt(w http.ResponseWriter, r *http.Request, p *config.Provider, pl *plan, streaming bool, model string) {
	tried := map[string]bool{}
	var lastStatus int
	var lastMessage string

	for i := 0; i < p.Attempts(); i++ {
		key, err := s.picker.Pick(p, tried)
		if err != nil {
			var noKey *keys.ErrNoKey
			if errors.As(err, &noKey) && lastStatus != 0 {
				break
			}
			s.fail(w, http.StatusFailedDependency, "FAILED_PRECONDITION", err.Error())
			return
		}
		tried[key.ID] = true

		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, pl.url, bytes.NewReader(pl.body))
		if err != nil {
			s.fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", accept(streaming))
		for _, h := range p.Headers {
			req.Header.Set(h.Name, h.Value)
		}
		pl.auth(req, key.Value)

		resp, err := s.client.Do(req)
		if err != nil {
			if r.Context().Err() != nil {
				return // the CLI hung up; nothing to report and nobody to report it to
			}
			s.picker.Failed(p, key.ID, 0, err.Error())
			lastStatus, lastMessage = http.StatusBadGateway, err.Error()
			s.logf("%s key %s: %v", p.Name, key.ID, err)
			continue
		}

		if resp.StatusCode >= 400 {
			message := upstreamMessage(resp)
			_ = resp.Body.Close()
			s.picker.Failed(p, key.ID, resp.StatusCode, message)
			lastStatus, lastMessage = resp.StatusCode, message
			s.logf("%s key %s: HTTP %d %s", p.Name, key.ID, resp.StatusCode, truncate(message, 200))
			if keys.Retryable(resp.StatusCode) && i+1 < p.Attempts() {
				continue
			}
			s.fail(w, resp.StatusCode, statusName(resp.StatusCode),
				fmt.Sprintf("%s (%s, model %s, key %s)", message, p.Name, model, key.ID))
			return
		}

		s.picker.Succeeded(p, key.ID)
		defer func() { _ = resp.Body.Close() }()
		if streaming {
			s.relayStream(w, resp, pl)
		} else {
			s.relayWhole(w, resp, pl)
		}
		return
	}

	if lastStatus == 0 {
		lastStatus, lastMessage = http.StatusBadGateway, "every key was tried and none answered"
	}
	s.fail(w, lastStatus, statusName(lastStatus),
		fmt.Sprintf("%s (%s, after %d keys)", lastMessage, p.Name, len(tried)))
}

func accept(streaming bool) string {
	if streaming {
		return "text/event-stream"
	}
	return "application/json"
}

func upstreamMessage(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	if len(raw) == 0 {
		return resp.Status
	}
	// All three dialects bury the readable part somewhere different.
	var probe struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
		Detail  any    `json:"detail"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil {
		if probe.Error.Message != "" {
			return probe.Error.Message
		}
		if probe.Message != "" {
			return probe.Message
		}
		if probe.Detail != nil {
			if s, ok := probe.Detail.(string); ok && s != "" {
				return s
			}
		}
	}
	return truncate(strings.TrimSpace(string(raw)), 500)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func statusName(status int) string {
	switch status {
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	}
	if status >= 500 {
		return "UNAVAILABLE"
	}
	return "UNKNOWN"
}

func (s *Server) relayWhole(w http.ResponseWriter, resp *http.Response, pl *plan) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBytes))
	if err != nil {
		s.fail(w, http.StatusBadGateway, "UNAVAILABLE", "reading the reply: "+err.Error())
		return
	}
	if pl.passthrough {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return
	}
	out, err := pl.whole(raw)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "UNAVAILABLE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) relayStream(w http.ResponseWriter, resp *http.Response, pl *plan) {
	out := sse.NewWriter(w)
	reader := sse.NewReader(resp.Body)
	decoder := wire.StreamDecoder(nil)
	if !pl.passthrough {
		decoder = pl.stream()
	}

	for {
		ev, err := reader.Next()
		if err != nil {
			break
		}
		if len(ev.Data) == 0 {
			continue
		}
		if pl.passthrough {
			if err := out.Send(ev.Data); err != nil {
				return
			}
			continue
		}
		chunks, err := decoder.Event(ev.Data)
		if err != nil {
			// The host reported a fault after the reply had started. The bytes
			// already sent cannot be taken back, so the failure is delivered as
			// the last thing the model "said" — visible, rather than a stream
			// that simply stops.
			s.sendFailureChunk(out, err.Error())
			return
		}
		for _, chunk := range chunks {
			payload, err := json.Marshal(chunk)
			if err != nil {
				continue
			}
			if err := out.Send(payload); err != nil {
				return
			}
		}
	}

	if decoder == nil {
		return
	}
	for _, chunk := range decoder.Done() {
		payload, err := json.Marshal(chunk)
		if err != nil {
			continue
		}
		if err := out.Send(payload); err != nil {
			return
		}
	}
}

func (s *Server) sendFailureChunk(out *sse.Writer, message string) {
	chunk := &wire.GeminiResponse{Candidates: []wire.GeminiCandidate{{
		Content: wire.GeminiContent{
			Role:  wire.RoleModel,
			Parts: []wire.GeminiPart{{Text: "\n\n[agy-provider] the provider ended the reply: " + message}},
		},
		FinishReason: wire.FinishOther,
	}}}
	if payload, err := json.Marshal(chunk); err == nil {
		_ = out.Send(payload)
	}
}
