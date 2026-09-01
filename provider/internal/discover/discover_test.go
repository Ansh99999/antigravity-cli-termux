package discover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
)

func TestModelsReadsEachDialectsListing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     config.Kind
		path     string
		body     string
		wantIDs  []string
		authHead string
	}{
		{
			name: "openai", kind: config.KindOpenAI, path: "/v1/models",
			body:     `{"data":[{"id":"gpt-4o"},{"id":"anthropic/claude-sonnet-4.5"}]}`,
			wantIDs:  []string{"anthropic/claude-sonnet-4.5", "gpt-4o"},
			authHead: "Authorization",
		},
		{
			name: "anthropic", kind: config.KindAnthropic, path: "/v1/models",
			body:     `{"data":[{"id":"claude-sonnet-4-5"},{"id":"claude-haiku-4-5"}]}`,
			wantIDs:  []string{"claude-haiku-4-5", "claude-sonnet-4-5"},
			authHead: "x-api-key",
		},
		{
			name: "gemini", kind: config.KindGemini, path: "/v1beta/models",
			body: `{"models":[
                {"name":"models/gemini-3-pro","supportedGenerationMethods":["generateContent"]},
                {"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]},
                {"name":"models/gemini-3-flash","supportedGenerationMethods":["streamGenerateContent"]}]}`,
			wantIDs:  []string{"gemini-3-flash", "gemini-3-pro"},
			authHead: "x-goog-api-key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sawPath, sawAuth, sawHeader string
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawPath = r.URL.Path
				sawAuth = r.Header.Get(tc.authHead)
				sawHeader = r.Header.Get("X-Tenant")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer host.Close()

			p := &config.Provider{
				Name: "p", Kind: tc.kind, BaseURL: host.URL,
				Headers: []config.Header{{Name: "X-Tenant", Value: "acme"}},
			}
			got, err := Models(context.Background(), host.Client(), p, "the-key")
			if err != nil {
				t.Fatalf("Models: %v", err)
			}
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("got %v, want %v", got, tc.wantIDs)
			}
			for i := range got {
				if got[i] != tc.wantIDs[i] {
					t.Errorf("got %v, want %v (sorted)", got, tc.wantIDs)
					break
				}
			}
			if sawPath != tc.path {
				t.Errorf("asked %q, want %q", sawPath, tc.path)
			}
			if sawAuth == "" {
				t.Errorf("the key was not sent in %s", tc.authHead)
			}
			if sawHeader != "acme" {
				t.Errorf("a configured header should reach the listing too, got %q", sawHeader)
			}
		})
	}
}

func TestModelsReportsARejection(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"incorrect api key"}}`))
	}))
	defer host.Close()

	p := &config.Provider{Name: "p", Kind: config.KindOpenAI, BaseURL: host.URL}
	_, err := Models(context.Background(), host.Client(), p, "bad")
	if err == nil {
		t.Fatal("a rejection must be reported")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("want an HTTPError so the caller can tell a bad key from a bad URL, got %T", err)
	}
	if httpErr.Status != http.StatusUnauthorized {
		t.Errorf("status = %d", httpErr.Status)
	}
	if httpErr.Message != "incorrect api key" {
		t.Errorf("the host's own words should survive: %q", httpErr.Message)
	}
}

func TestTestKeysChecksEveryEnabledKey(t *testing.T) {
	var seen []string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		seen = append(seen, key)
		if key == "Bearer good" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
	}))
	defer host.Close()

	off := false
	p := &config.Provider{Name: "p", Kind: config.KindOpenAI, BaseURL: host.URL, Keys: []config.Key{
		{ID: "k1", Value: "good"},
		{ID: "k2", Value: "bad"},
		{ID: "k3", Value: "skipped", Enabled: &off},
	}}

	results := TestKeys(context.Background(), host.Client(), p)
	if len(results) != 2 {
		t.Fatalf("a disabled key should not be tried: %+v", results)
	}
	if !results[0].OK || results[0].Models != 1 {
		t.Errorf("k1 should pass: %+v", results[0])
	}
	if results[1].OK || results[1].Status != http.StatusForbidden {
		t.Errorf("k2 should fail with its status: %+v", results[1])
	}
	if len(seen) != 2 {
		t.Errorf("want two calls, got %v", seen)
	}
}
