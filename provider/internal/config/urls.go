package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ParseKind accepts the dialect names and the obvious aliases a user is likely
// to type at a prompt.
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "openai", "oai", "openai-compatible", "compatible":
		return KindOpenAI, nil
	case "anthropic", "claude":
		return KindAnthropic, nil
	case "gemini", "google", "generativelanguage":
		return KindGemini, nil
	}
	return "", fmt.Errorf("unknown provider style %q (want openai, anthropic or gemini)", s)
}

// ParseStrategy accepts the rotation strategy names.
func ParseStrategy(s string) (Strategy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "first", "none", "fixed":
		return StrategyFirst, nil
	case "rotate", "round-robin", "roundrobin", "rr":
		return StrategyRotate, nil
	case "random", "rand":
		return StrategyRandom, nil
	case "least-errors", "least", "healthiest":
		return StrategyLeastErrors, nil
	}
	return "", fmt.Errorf("unknown key strategy %q (want first, rotate, random or least-errors)", s)
}

var nameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Validate reports why a provider cannot be used, if it cannot.
func (p *Provider) Validate() error {
	if !nameOK.MatchString(p.Name) {
		return fmt.Errorf("provider name %q must start alphanumeric and hold only letters, digits, dot, dash or underscore", p.Name)
	}
	if _, err := ParseKind(string(p.Kind)); err != nil {
		return err
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return fmt.Errorf("base url %q: %w", p.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base url %q must be http or https", p.BaseURL)
	}
	if u.Host == "" {
		return fmt.Errorf("base url %q has no host", p.BaseURL)
	}
	if p.Strategy != "" {
		if _, err := ParseStrategy(string(p.Strategy)); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for i := range p.Keys {
		if p.Keys[i].ID == "" {
			return fmt.Errorf("key %d has no id", i+1)
		}
		if seen[p.Keys[i].ID] {
			return fmt.Errorf("duplicate key id %q", p.Keys[i].ID)
		}
		seen[p.Keys[i].ID] = true
	}
	for _, h := range p.Headers {
		if strings.TrimSpace(h.Name) == "" {
			return fmt.Errorf("a custom header has no name")
		}
	}
	return nil
}

var versionSegment = regexp.MustCompile(`^v\d+(alpha|beta)?\d*$`)

// versioned returns the base URL with an API version segment, adding "/v1" only
// when the URL the user pasted does not already end in one. Both
// "https://api.openai.com" and "https://api.openai.com/v1" are things people
// paste, and OpenRouter's is "/api/v1" — all three have to land on the same
// endpoint.
func versioned(base, fallback string) string {
	trimmed := strings.TrimRight(base, "/")
	segments := strings.Split(trimmed, "/")
	if len(segments) > 0 && versionSegment.MatchString(segments[len(segments)-1]) {
		return trimmed
	}
	return trimmed + "/" + fallback
}

// ChatURL is where a completion request goes for this provider's dialect.
// Gemini is absent on purpose: its path carries the model and the method, so
// GeminiURL takes both.
func (p *Provider) ChatURL() string {
	switch p.Kind {
	case KindAnthropic:
		return versioned(p.BaseURL, "v1") + "/messages"
	case KindOpenAI:
		return versioned(p.BaseURL, "v1") + "/chat/completions"
	case KindGemini:
		return p.GeminiURL("", "generateContent")
	}
	return strings.TrimRight(p.BaseURL, "/")
}

// GeminiURL builds a generativelanguage-shaped URL for a model and method.
func (p *Provider) GeminiURL(model, method string) string {
	base := versioned(p.BaseURL, "v1beta")
	if model == "" {
		return base + "/models"
	}
	return base + "/models/" + url.PathEscape(model) + ":" + method
}

// ModelsURL is where the model list lives for this dialect.
func (p *Provider) ModelsURL() string {
	switch p.Kind {
	case KindGemini:
		return p.GeminiURL("", "")
	default:
		return versioned(p.BaseURL, "v1") + "/models"
	}
}

// TokenCountURL is Anthropic's token counter. Nothing else has one.
func (p *Provider) TokenCountURL() string {
	return versioned(p.BaseURL, "v1") + "/messages/count_tokens"
}
