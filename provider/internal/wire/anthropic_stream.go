package wire

import "encoding/json"

type blockState struct {
	kind string
	id   string
	name string
	args []byte
}

// AnthropicStream decodes a messages stream.
type AnthropicStream struct {
	blocks  map[int]*blockState
	model   string
	id      string
	finish  string
	input   int
	output  int
	flushed bool
}

// NewAnthropicStream returns a decoder for one response.
func NewAnthropicStream() *AnthropicStream {
	return &AnthropicStream{blocks: map[int]*blockState{}}
}

// Event feeds one SSE payload.
func (s *AnthropicStream) Event(data []byte) ([]*GeminiResponse, error) {
	var ev AnthropicEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, nil
	}
	if ev.Error != nil && ev.Error.Message != "" {
		return nil, &UpstreamError{Message: ev.Error.Message}
	}

	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			s.model = ev.Message.Model
			s.id = ev.Message.ID
			s.input = ev.Message.Usage.Prompt()
		}

	case "content_block_start":
		if ev.ContentBlock == nil {
			return nil, nil
		}
		s.blocks[ev.Index] = &blockState{
			kind: ev.ContentBlock.Type,
			id:   ev.ContentBlock.ID,
			name: ev.ContentBlock.Name,
		}
		// A non-streaming host can deliver a whole block here rather than in
		// deltas, and text that arrives this way is easy to lose.
		if ev.ContentBlock.Text != "" {
			return []*GeminiResponse{s.chunk(GeminiPart{Text: ev.ContentBlock.Text})}, nil
		}

	case "content_block_delta":
		if ev.Delta == nil {
			return nil, nil
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				return []*GeminiResponse{s.chunk(GeminiPart{Text: ev.Delta.Text})}, nil
			}
		case "thinking_delta":
			if ev.Delta.Thinking != "" {
				return []*GeminiResponse{s.chunk(GeminiPart{Text: ev.Delta.Thinking, Thought: true})}, nil
			}
		case "input_json_delta":
			if b := s.blocks[ev.Index]; b != nil {
				b.args = append(b.args, ev.Delta.PartialJSON...)
			}
		}

	case "content_block_stop":
		b := s.blocks[ev.Index]
		if b == nil || b.kind != "tool_use" || b.name == "" {
			return nil, nil
		}
		delete(s.blocks, ev.Index)
		args := b.args
		if len(args) == 0 || !json.Valid(args) {
			args = []byte(`{}`)
		}
		return []*GeminiResponse{s.chunk(GeminiPart{FunctionCall: &GeminiFunctionCall{
			Name: b.name,
			Args: json.RawMessage(args),
			ID:   b.id,
		}})}, nil

	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			s.finish = ev.Delta.StopReason
		}
		if ev.Usage != nil {
			s.output = ev.Usage.OutputTokens
			if p := ev.Usage.Prompt(); p > 0 {
				s.input = p
			}
		}
	}
	return nil, nil
}

func (s *AnthropicStream) chunk(parts ...GeminiPart) *GeminiResponse {
	return &GeminiResponse{
		ModelVersion: s.model,
		ResponseID:   s.id,
		Candidates: []GeminiCandidate{{
			Content: GeminiContent{Role: RoleModel, Parts: parts},
		}},
	}
}

// Done emits the finish reason and the usage record.
func (s *AnthropicStream) Done() []*GeminiResponse {
	if s.flushed {
		return nil
	}
	s.flushed = true

	final := s.chunk()
	final.Candidates[0].FinishReason = geminiFinish(s.finish)
	if final.Candidates[0].FinishReason == "" {
		final.Candidates[0].FinishReason = FinishStop
	}
	if s.input > 0 || s.output > 0 {
		final.UsageMetadata = &GeminiUsage{
			PromptTokenCount:     s.input,
			CandidatesTokenCount: s.output,
			TotalTokenCount:      s.input + s.output,
		}
	}
	return []*GeminiResponse{final}
}
