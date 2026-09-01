package wire

import (
	"encoding/json"
	"fmt"
	"strings"
)

// callIDs bridges the one structural difference between Gemini's transcript and
// the other two: Gemini pairs a function response with its call **by name**,
// while OpenAI and Anthropic pair them by an id the model issued. The transcript
// arrives whole on every request, so walking it in order and issuing ids as the
// calls appear reproduces a consistent pairing without any stored session.
type callIDs struct {
	prefix  string
	seq     int
	pending map[string][]string
}

func newCallIDs(prefix string) *callIDs {
	return &callIDs{prefix: prefix, pending: map[string][]string{}}
}

// issue returns the id to use for a call the model made, preferring an id
// Gemini already carried.
func (c *callIDs) issue(name, existing string) string {
	id := existing
	if id == "" {
		c.seq++
		id = fmt.Sprintf("%s%d_%s", c.prefix, c.seq, sanitizeID(name))
	}
	c.pending[name] = append(c.pending[name], id)
	return id
}

// match returns the id of the call a response belongs to: the oldest call of
// that name still waiting for one.
func (c *callIDs) match(name, existing string) string {
	if existing != "" {
		return existing
	}
	if queue := c.pending[name]; len(queue) > 0 {
		c.pending[name] = queue[1:]
		return queue[0]
	}
	// A response with no call before it: the transcript was trimmed by the
	// budgeter. Issue something stable so the message is still well formed.
	c.seq++
	return fmt.Sprintf("%s%d_%s", c.prefix, c.seq, sanitizeID(name))
}

func sanitizeID(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "tool"
	}
	return s
}

// normalizeSchema rewrites a Gemini tool schema into plain JSON Schema. Gemini's
// is an OpenAPI subset and the SDKs serialize its type as a proto enum name, so
// a declaration can arrive with "type":"STRING" — which OpenAI and Anthropic
// both reject.
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(walkSchema(v))
	if err != nil {
		return raw
	}
	return out
}

func walkSchema(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			switch k {
			case "type":
				if s, ok := val.(string); ok {
					out[k] = strings.ToLower(s)
					continue
				}
				out[k] = walkSchema(val)
			case "nullable":
				// OpenAPI 3.0's spelling; JSON Schema says so with a type union,
				// and neither of the other two dialects needs it stated.
				continue
			case "propertyOrdering", "example":
				continue
			default:
				out[k] = walkSchema(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = walkSchema(t[i])
		}
		return out
	}
	return v
}

// argsJSON renders call arguments as the JSON object string OpenAI wants.
func argsJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// objectJSON coerces a value into a JSON object, since Gemini's
// functionResponse.response is an object while the other two carry free text.
func objectJSON(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	if strings.HasPrefix(trimmed, "{") {
		return json.RawMessage(trimmed)
	}
	wrapped, err := json.Marshal(map[string]json.RawMessage{"output": json.RawMessage(trimmed)})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return wrapped
}

// responseText renders a function response as the flat text OpenAI and
// Anthropic tool results carry.
func responseText(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	// A response that is only {"output": "..."} reads far better to a model as
	// the string itself than as a wrapper it has to see through.
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper) == 1 {
		for _, key := range []string{"output", "result", "content", "text"} {
			if inner, ok := wrapper[key]; ok {
				var s string
				if err := json.Unmarshal(inner, &s); err == nil {
					return s
				}
				return string(inner)
			}
		}
	}
	return trimmed
}

// splitText joins the plain text of a content's parts, ignoring thoughts.
func splitText(parts []GeminiPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Text != "" && !p.Thought {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// dataURL renders an inline attachment the way OpenAI's image_url wants it.
func dataURL(b *GeminiBlob) string {
	mime := b.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	return "data:" + mime + ";base64," + b.Data
}
