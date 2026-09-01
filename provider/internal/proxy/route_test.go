package proxy

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/wallentx/antigravity-cli-termux/provider/internal/config"
)

// TestUnrecognizedPathsAreReportedAndLogged covers the one assumption the whole
// proxy rests on that cannot be checked without the engine: the shape of the
// path it asks for. If a build ever asks for something else, the log line is the
// only record of what it wanted.
func TestUnrecognizedPathsAreReportedAndLogged(t *testing.T) {
	p := &config.Provider{Name: "router", Kind: config.KindOpenAI, Model: "gpt-4o",
		Keys: []config.Key{{ID: "k1", Value: "sk-one"}}}
	front := serve(t, p, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("nothing should reach the host")
	}))

	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/v1beta/models/gemini-3-pro:embedContent", http.StatusNotImplemented},
		{"/v1internal:loadCodeAssist", http.StatusNotFound},
		{"/", http.StatusNotFound},
	} {
		resp := post(t, front, tc.path, `{}`)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d", tc.path, resp.StatusCode, tc.status)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"error"`) {
			t.Errorf("%s: the engine only reads Google's error envelope, got %s", tc.path, body)
		}
	}
}

func TestRouteLogsWhatItCouldNotServe(t *testing.T) {
	var logged bytes.Buffer
	server := New("", nil)
	server.Logger = log.New(&logged, "", 0)

	rec := &recorder{header: http.Header{}}
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/v1internal:loadCodeAssist", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	server.route(rec, req)

	if !strings.Contains(logged.String(), "/v1internal:loadCodeAssist") {
		t.Errorf("the path should be in the log, got %q", logged.String())
	}
}

// recorder is a minimal ResponseWriter; httptest's own would do, but this keeps
// the test to the routing decision alone.
type recorder struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(p []byte) (int, error) { return r.body.Write(p) }
func (r *recorder) WriteHeader(status int)      { r.status = status }
