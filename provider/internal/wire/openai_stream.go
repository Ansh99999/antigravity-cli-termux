package wire

import (
	"encoding/json"
	"sort"
)

// StreamDecoder turns one upstream dialect's stream events into Gemini stream
// chunks. Event is handed the payload of one `data:` line; Done is called once
// the upstream stream ends.
type StreamDecoder interface {
	Event(data []byte) ([]*GeminiResponse, error)
	Done() []*GeminiResponse
}

type partialCall struct {
	id   string
	name string
	args []byte
}

// OpenAIStream decodes a chat-completions stream.
type OpenAIStream struct {
	calls   map[int]*partialCall
	usage   *OpenAIUsage
	finish  string
	model   string
	id      string
	flushed bool
}

// NewOpenAIStream returns a decoder for one response.
func NewOpenAIStream() *OpenAIStream {
	return &OpenAIStream{calls: map[int]*partialCall{}}
}

// Event feeds one SSE payload.
func (s *OpenAIStream) Event(data []byte) ([]*GeminiResponse, error) {
	if isDone(data) {
		return nil, nil
	}
	var chunk OpenAIResponse
	if err := json.Unmarshal(data, &chunk); err != nil {
		// A comment, a keep-alive or a fragment this dialect does not define:
		// dropping it is better than failing a stream mid-reply.
		return nil, nil
	}
	if chunk.Error != nil && chunk.Error.Message != "" {
		return nil, &UpstreamError{Message: chunk.Error.Message}
	}
	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.ID != "" {
		s.id = chunk.ID
	}
	if chunk.Usage != nil {
		s.usage = chunk.Usage
	}

	var out []*GeminiResponse
	for _, ch := range chunk.Choices {
		if ch.FinishReason != "" {
			s.finish = ch.FinishReason
		}
		d := ch.Delta
		if d == nil {
			d = ch.Message
		}
		if d == nil {
			continue
		}
		if thinking := d.Thinking(); thinking != "" {
			out = append(out, s.chunk(GeminiPart{Text: thinking, Thought: true}))
		}
		if text := d.Text(); text != "" {
			out = append(out, s.chunk(GeminiPart{Text: text}))
		}
		s.accumulate(d.ToolCalls)
	}
	return out, nil
}

// accumulate merges tool-call fragments. The index is what ties fragments of the
// same call together; a gateway that omits it is treating each fragment as a
// whole call, so the count stands in for it.
func (s *OpenAIStream) accumulate(calls []OpenAIToolCall) {
	for _, tc := range calls {
		idx := len(s.calls)
		if tc.Index != nil {
			idx = *tc.Index
		} else if tc.ID != "" {
			if found, ok := s.byID(tc.ID); ok {
				idx = found
			}
		}
		if s.calls[idx] == nil {
			s.calls[idx] = &partialCall{}
		}
		c := s.calls[idx]
		if tc.ID != "" {
			c.id = tc.ID
		}
		if tc.Function.Name != "" {
			c.name = tc.Function.Name
		}
		c.args = append(c.args, tc.Function.Arguments...)
	}
}

func (s *OpenAIStream) byID(id string) (int, bool) {
	for idx, c := range s.calls {
		if c.id == id {
			return idx, true
		}
	}
	return 0, false
}

func (s *OpenAIStream) chunk(parts ...GeminiPart) *GeminiResponse {
	return &GeminiResponse{
		ModelVersion: s.model,
		ResponseID:   s.id,
		Candidates: []GeminiCandidate{{
			Content: GeminiContent{Role: RoleModel, Parts: parts},
		}},
	}
}

// Done emits the tool calls, the finish reason and the usage record, which is
// everything that can only be known once the stream has ended.
func (s *OpenAIStream) Done() []*GeminiResponse {
	if s.flushed {
		return nil
	}
	s.flushed = true

	final := s.chunk()
	for _, idx := range sortedKeys(s.calls) {
		c := s.calls[idx]
		if c.name == "" {
			continue
		}
		final.Candidates[0].Content.Parts = append(final.Candidates[0].Content.Parts,
			toolCallPart(OpenAIToolCall{
				ID:       c.id,
				Function: OpenAIToolFunction{Name: c.name, Arguments: string(c.args)},
			}))
	}
	final.Candidates[0].FinishReason = geminiFinish(s.finish)
	if final.Candidates[0].FinishReason == "" {
		final.Candidates[0].FinishReason = FinishStop
	}
	if s.usage != nil {
		final.UsageMetadata = &GeminiUsage{
			PromptTokenCount:     s.usage.PromptTokens,
			CandidatesTokenCount: s.usage.CompletionTokens,
			TotalTokenCount:      s.usage.TotalTokens,
		}
		if s.usage.Details != nil {
			final.UsageMetadata.ThoughtsTokenCount = s.usage.Details.ReasoningTokens
		}
	}
	return []*GeminiResponse{final}
}

func sortedKeys(m map[int]*partialCall) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// UpstreamError is an error the host reported inside an otherwise healthy
// stream, which has to be surfaced rather than swallowed.
type UpstreamError struct {
	Message string
}

func (e *UpstreamError) Error() string { return e.Message }

func isDone(data []byte) bool {
	trimmed := string(data)
	return trimmed == "[DONE]" || trimmed == "\"[DONE]\""
}
