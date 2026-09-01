package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/state"
	"github.com/wallentx/antigravity-cli-termux/provider/internal/wire"
)

const testToken = "agy-test-token"

// capture records what an upstream stand-in was asked.
type capture struct {
	mu      sync.Mutex
	bodies  []map[string]any
	raw     [][]byte
	paths   []string
	authKey []string
}

func (c *capture) record(r *http.Request) map[string]any {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	key := r.Header.Get("x-api-key")
	if key == "" {
		key = r.Header.Get("x-goog-api-key")
	}
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, body)
	c.raw = append(c.raw, raw)
	c.paths = append(c.paths, r.URL.Path)
	c.authKey = append(c.authKey, key)
	return body
}

func (c *capture) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

// serve wires a provider to an upstream stand-in and returns a proxy to talk to.
func serve(t *testing.T, p *config.Provider, upstream http.Handler) *httptest.Server {
	t.Helper()
	t.Setenv("AGY_PROVIDER_HOME", t.TempDir())

	host := httptest.NewServer(upstream)
	t.Cleanup(host.Close)

	p.BaseURL = host.URL
	if err := config.Save(&config.File{Active: p.Name, Providers: []*config.Provider{p}}); err != nil {
		t.Fatalf("saving the registry: %v", err)
	}

	front := httptest.NewServer(New(testToken, state.NewStore()).Handler())
	t.Cleanup(front.Close)
	return front
}

// post sends a Gemini-shaped request at the proxy.
func post(t *testing.T, front *httptest.Server, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, front.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("x-goog-api-key", testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatalf("posting: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

const helloRequest = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],
  "systemInstruction":{"parts":[{"text":"be brief"}]},
  "generationConfig":{"maxOutputTokens":100,"temperature":0.4}}`

func TestOpenAIProviderAnswersAWholeReply(t *testing.T) {
	seen := &capture{}
	p := &config.Provider{
		Name: "router", Kind: config.KindOpenAI, Model: "openai/gpt-5.1",
		Keys:    []config.Key{{ID: "k1", Value: "sk-one"}},
		Headers: []config.Header{{Name: "HTTP-Referer", Value: "https://example.test"}},
	}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		if got := r.Header.Get("HTTP-Referer"); got != "https://example.test" {
			t.Errorf("a configured header did not reach the host: %q", got)
		}
		_, _ = w.Write([]byte(`{"id":"c","model":"openai/gpt-5.1","choices":[{"index":0,
          "message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],
          "usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
	}))

	resp := post(t, front, "/v1beta/models/gemini-3-pro:generateContent", helloRequest)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	if seen.paths[0] != "/v1/chat/completions" {
		t.Errorf("wrong upstream path: %s", seen.paths[0])
	}
	if seen.authKey[0] != "sk-one" {
		t.Errorf("wrong key sent: %q", seen.authKey[0])
	}
	// The model the engine asked for is not the model the host serves.
	if got := seen.bodies[0]["model"]; got != "openai/gpt-5.1" {
		t.Errorf("model was not rewritten, got %v", got)
	}
	messages, _ := seen.bodies[0]["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("want a system and a user message, got %v", messages)
	}

	var out wire.GeminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("the reply is not a GenerateContentResponse: %v", err)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].Content.Parts[0].Text != "hi there" {
		t.Fatalf("reply wrong: %+v", out)
	}
	if out.Candidates[0].FinishReason != wire.FinishStop {
		t.Errorf("finish reason wrong: %q", out.Candidates[0].FinishReason)
	}
	if out.UsageMetadata == nil || out.UsageMetadata.TotalTokenCount != 7 {
		t.Errorf("usage did not cross over: %+v", out.UsageMetadata)
	}
}

func TestOpenAIProviderStreams(t *testing.T) {
	p := &config.Provider{
		Name: "router", Kind: config.KindOpenAI, Model: "gpt-4o",
		Keys: []config.Key{{ID: "k1", Value: "sk-one"}},
	}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if body["stream"] != true {
			t.Errorf("a streaming request should ask the host to stream: %v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"choices":[{"delta":{"content":"one "}}]}`,
			`{"choices":[{"delta":{"content":"two"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
			`[DONE]`,
		} {
			_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
			w.(http.Flusher).Flush()
		}
	}))

	resp := post(t, front, "/v1beta/models/gemini-3-pro:streamGenerateContent?alt=sse", helloRequest)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("the proxy must answer a stream with a stream, got %q", ct)
	}

	text, finish, usage := readGeminiStream(t, resp)
	if text != "one two" {
		t.Errorf("streamed text wrong: %q", text)
	}
	if finish != wire.FinishStop {
		t.Errorf("finish reason wrong: %q", finish)
	}
	if usage == nil || usage.TotalTokenCount != 5 {
		t.Errorf("usage wrong: %+v", usage)
	}
}

// readGeminiStream collects the text, finish reason and usage out of the SSE the
// proxy produced.
func readGeminiStream(t *testing.T, resp *http.Response) (string, string, *wire.GeminiUsage) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the stream: %v", err)
	}
	var text, finish string
	var usage *wire.GeminiUsage
	for _, line := range strings.Split(string(raw), "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var chunk wire.GeminiResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("chunk %q is not a GenerateContentResponse: %v", payload, err)
		}
		if chunk.UsageMetadata != nil {
			usage = chunk.UsageMetadata
		}
		for _, cand := range chunk.Candidates {
			if cand.FinishReason != "" {
				finish = cand.FinishReason
			}
			for _, part := range cand.Content.Parts {
				if !part.Thought {
					text += part.Text
				}
			}
		}
	}
	return text, finish, usage
}

func TestAnthropicProviderStreams(t *testing.T) {
	seen := &capture{}
	p := &config.Provider{
		Name: "claude", Kind: config.KindAnthropic, Model: "claude-sonnet-4-5",
		Keys: []config.Key{{ID: "k1", Value: "sk-ant-one"}},
	}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := seen.record(r)
		if r.Header.Get("anthropic-version") == "" {
			t.Error("the version header is required")
		}
		if _, ok := body["max_tokens"]; !ok {
			t.Error("max_tokens is required")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"type":"message_start","message":{"id":"m","model":"claude-sonnet-4-5","usage":{"input_tokens":4}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"all "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"good"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = io.WriteString(w, "event: x\ndata: "+chunk+"\n\n")
			w.(http.Flusher).Flush()
		}
	}))

	resp := post(t, front, "/v1beta/models/gemini-3-pro:streamGenerateContent?alt=sse", helloRequest)
	text, finish, usage := readGeminiStream(t, resp)
	if text != "all good" {
		t.Errorf("streamed text wrong: %q", text)
	}
	if finish != wire.FinishStop {
		t.Errorf("finish reason wrong: %q", finish)
	}
	if usage == nil || usage.PromptTokenCount != 4 || usage.CandidatesTokenCount != 3 {
		t.Errorf("usage wrong: %+v", usage)
	}
	if seen.paths[0] != "/v1/messages" {
		t.Errorf("wrong upstream path: %s", seen.paths[0])
	}
}

func TestGeminiProviderPassesThroughUntouched(t *testing.T) {
	seen := &capture{}
	reply := `{"candidates":[{"content":{"role":"model","parts":[{"text":"verbatim"}]},"finishReason":"STOP"}]}`
	p := &config.Provider{
		Name: "relay", Kind: config.KindGemini, Model: "gemini-3-pro-preview",
		Keys: []config.Key{{ID: "k1", Value: "AIza-one"}},
	}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		_, _ = w.Write([]byte(reply))
	}))

	resp := post(t, front, "/v1beta/models/gemini-3-pro:generateContent", helloRequest)
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != reply {
		t.Errorf("a Gemini host's reply should arrive verbatim, got %s", body)
	}
	// The path carries the model, so the rewrite happens there.
	if seen.paths[0] != "/v1beta/models/gemini-3-pro-preview:generateContent" {
		t.Errorf("model was not rewritten in the path: %s", seen.paths[0])
	}
	if seen.authKey[0] != "AIza-one" {
		t.Errorf("wrong key sent: %q", seen.authKey[0])
	}
	// The transcript itself must not be re-encoded on the way through.
	if !json.Valid(seen.raw[0]) || !strings.Contains(string(seen.raw[0]), `"be brief"`) {
		t.Errorf("request body was mangled: %s", seen.raw[0])
	}
}

func TestARateLimitRotatesOntoTheNextKey(t *testing.T) {
	seen := &capture{}
	p := &config.Provider{
		Name: "router", Kind: config.KindOpenAI, Model: "gpt-4o",
		Strategy: config.StrategyRotate,
		Keys:     []config.Key{{ID: "k1", Value: "sk-one"}, {ID: "k2", Value: "sk-two"}},
	}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		if seen.calls() == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"second key"},"finish_reason":"stop"}]}`))
	}))

	resp := post(t, front, "/v1beta/models/gemini-3-pro:generateContent", helloRequest)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("the rotation should have hidden the 429; got %d: %s", resp.StatusCode, body)
	}
	if seen.calls() != 2 {
		t.Fatalf("want two upstream attempts, got %d", seen.calls())
	}
	if seen.authKey[0] == seen.authKey[1] {
		t.Errorf("the retry reused the rejected key: %q", seen.authKey[1])
	}

	var out wire.GeminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if out.Candidates[0].Content.Parts[0].Text != "second key" {
		t.Errorf("wrong reply relayed: %+v", out.Candidates[0].Content.Parts)
	}
}

func TestABadRequestIsNotRetried(t *testing.T) {
	seen := &capture{}
	p := &config.Provider{
		Name: "router", Kind: config.KindOpenAI, Model: "gpt-4o",
		Keys: []config.Key{{ID: "k1", Value: "sk-one"}, {ID: "k2", Value: "sk-two"}},
	}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"model does not exist"}}`))
	}))

	resp := post(t, front, "/v1beta/models/gemini-3-pro:generateContent", helloRequest)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("the status should be passed on, got %d", resp.StatusCode)
	}
	if seen.calls() != 1 {
		t.Errorf("a request every key would reject should be tried once, got %d attempts", seen.calls())
	}

	var envelope wire.GeminiError
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("an error must arrive in Google's envelope: %v", err)
	}
	if !strings.Contains(envelope.Error.Message, "model does not exist") {
		t.Errorf("the host's own words should survive: %q", envelope.Error.Message)
	}
	if envelope.Error.Status != "INVALID_ARGUMENT" {
		t.Errorf("status name wrong: %q", envelope.Error.Status)
	}
}

func TestEveryKeyRejectedIsReported(t *testing.T) {
	seen := &capture{}
	p := &config.Provider{
		Name: "router", Kind: config.KindOpenAI, Model: "gpt-4o", MaxAttempts: 2,
		Keys: []config.Key{{ID: "k1", Value: "sk-one"}, {ID: "k2", Value: "sk-two"}},
	}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))

	resp := post(t, front, "/v1beta/models/gemini-3-pro:generateContent", helloRequest)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", resp.StatusCode)
	}
	if seen.calls() != 2 {
		t.Errorf("both keys should have been tried, got %d", seen.calls())
	}
	var envelope wire.GeminiError
	_ = json.NewDecoder(resp.Body).Decode(&envelope)
	if envelope.Error.Status != "UNAUTHENTICATED" {
		t.Errorf("status name wrong: %q", envelope.Error.Status)
	}
}

func TestTheProxyRefusesTheWrongToken(t *testing.T) {
	seen := &capture{}
	p := &config.Provider{Name: "router", Kind: config.KindOpenAI, Model: "gpt-4o",
		Keys: []config.Key{{ID: "k1", Value: "sk-one"}}}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"leaked"}}]}`))
	}))

	req, err := http.NewRequest(http.MethodPost,
		front.URL+"/v1beta/models/gemini-3-pro:generateContent", strings.NewReader(helloRequest))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("x-goog-api-key", "not-the-token")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatalf("posting: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anything else on the device must be turned away, got %d", resp.StatusCode)
	}
	if seen.calls() != 0 {
		t.Error("an unauthorized request must not reach the provider at all")
	}
}

func TestModelsListingWearsGeminiClothes(t *testing.T) {
	p := &config.Provider{Name: "router", Kind: config.KindOpenAI, Model: "openai/gpt-5.1",
		Keys: []config.Key{{ID: "k1", Value: "sk-one"}}}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("wrong listing path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"zebra/model"},{"id":"openai/gpt-5.1"}]}`))
	}))

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/v1beta/models?key="+testToken, nil)
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}

	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(out.Models) != 2 {
		t.Fatalf("want both models, got %+v", out.Models)
	}
	if out.Models[0].Name != "models/openai/gpt-5.1" {
		t.Errorf("the pinned model should lead the list, got %q", out.Models[0].Name)
	}
	if len(out.Models[0].SupportedGenerationMethods) == 0 {
		t.Error("a picker needs to be told the model can generate")
	}
}

func TestCountTokensEstimatesForOpenAI(t *testing.T) {
	p := &config.Provider{Name: "router", Kind: config.KindOpenAI, Model: "gpt-4o",
		Keys: []config.Key{{ID: "k1", Value: "sk-one"}}}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("OpenAI has no counting endpoint; nothing should be called (%s)", r.URL.Path)
	}))

	resp := post(t, front, "/v1beta/models/gemini-3-pro:countTokens",
		`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"`+strings.Repeat("a", 400)+`"}]}]}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		TotalTokens int `json:"totalTokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if out.TotalTokens != 100 {
		t.Errorf("400 characters should estimate to 100 tokens, got %d", out.TotalTokens)
	}
}

func TestCountTokensAsksAnthropic(t *testing.T) {
	p := &config.Provider{Name: "claude", Kind: config.KindAnthropic, Model: "claude-sonnet-4-5",
		Keys: []config.Key{{ID: "k1", Value: "sk-ant"}}}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))

	resp := post(t, front, "/v1beta/models/gemini-3-pro:countTokens", helloRequest)
	var out struct {
		TotalTokens int `json:"totalTokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if out.TotalTokens != 42 {
		t.Errorf("want the host's own count, got %d", out.TotalTokens)
	}
}

func TestAStreamThatFailsMidReplySaysSo(t *testing.T) {
	p := &config.Provider{Name: "router", Kind: config.KindOpenAI, Model: "gpt-4o",
		Keys: []config.Key{{ID: "k1", Value: "sk-one"}}}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"start\"}}]}\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"upstream exploded\"}}\n\n")
		w.(http.Flusher).Flush()
	}))

	resp := post(t, front, "/v1beta/models/gemini-3-pro:streamGenerateContent?alt=sse", helloRequest)
	text, finish, _ := readGeminiStream(t, resp)
	if !strings.Contains(text, "start") {
		t.Errorf("what was already streamed should still be there: %q", text)
	}
	if !strings.Contains(text, "upstream exploded") {
		t.Errorf("the failure has to be visible once the reply has begun: %q", text)
	}
	if finish != wire.FinishOther {
		t.Errorf("finish reason wrong: %q", finish)
	}
}

func TestParsePath(t *testing.T) {
	for _, tc := range []struct{ path, model, method string }{
		{"/v1beta/models/gemini-3-pro:generateContent", "gemini-3-pro", "generateContent"},
		{"/v1beta/models/gemini-3-pro:streamGenerateContent", "gemini-3-pro", "streamGenerateContent"},
		{"/v1/models/anthropic/claude-4.5:countTokens", "anthropic/claude-4.5", "countTokens"},
		{"/v1beta/models", "", ""},
		{"/nothing/here", "", ""},
	} {
		model, method := parsePath(tc.path)
		if model != tc.model || method != tc.method {
			t.Errorf("parsePath(%q) = %q, %q; want %q, %q", tc.path, model, method, tc.model, tc.method)
		}
	}
}
