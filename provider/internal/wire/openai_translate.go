package wire

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToOpenAI translates the CLI's Gemini request into a chat-completions body.
func ToOpenAI(g *GeminiRequest, model string, stream bool) (*OpenAIRequest, error) {
	out := &OpenAIRequest{Model: model, Stream: stream}
	if stream {
		out.StreamOptions = &OpenAIStreamOptions{IncludeUsage: true}
	}
	ids := newCallIDs("call_")

	if g.SystemInstruction != nil {
		if text := splitText(g.SystemInstruction.Parts); text != "" {
			out.Messages = append(out.Messages, OpenAIMessage{Role: "system", Content: text})
		}
	}

	for _, c := range g.Contents {
		switch c.Role {
		case RoleModel:
			out.Messages = append(out.Messages, assistantOpenAI(c, ids)...)
		default:
			out.Messages = append(out.Messages, userOpenAI(c, ids)...)
		}
	}

	// A host that is handed an empty transcript answers with a validation error
	// rather than anything useful; one empty user turn is the least surprising
	// thing to send.
	if len(out.Messages) == 0 {
		out.Messages = append(out.Messages, OpenAIMessage{Role: "user", Content: ""})
	}

	for _, t := range g.Tools {
		for _, d := range t.FunctionDeclarations {
			out.Tools = append(out.Tools, OpenAITool{
				Type: "function",
				Function: OpenAIToolSchema{
					Name:        d.Name,
					Description: d.Description,
					Parameters:  normalizeSchema(d.Parameters),
				},
			})
		}
	}
	out.ToolChoice = openAIToolChoice(g.ToolConfig)
	applyOpenAIGenConfig(out, g.GenerationConfig, model)
	return out, nil
}

func assistantOpenAI(c GeminiContent, ids *callIDs) []OpenAIMessage {
	msg := OpenAIMessage{Role: "assistant"}
	if text := splitText(c.Parts); text != "" {
		msg.Content = text
	}
	for _, p := range c.Parts {
		if p.FunctionCall == nil {
			continue
		}
		msg.ToolCalls = append(msg.ToolCalls, OpenAIToolCall{
			ID:   ids.issue(p.FunctionCall.Name, p.FunctionCall.ID),
			Type: "function",
			Function: OpenAIToolFunction{
				Name:      p.FunctionCall.Name,
				Arguments: argsJSON(p.FunctionCall.Args),
			},
		})
	}
	if msg.Content == nil && len(msg.ToolCalls) == 0 {
		return nil
	}
	return []OpenAIMessage{msg}
}

func userOpenAI(c GeminiContent, ids *callIDs) []OpenAIMessage {
	var out []OpenAIMessage
	var parts []OpenAIPart

	for _, p := range c.Parts {
		switch {
		case p.FunctionResponse != nil:
			// A tool result is its own message and must follow the assistant
			// turn that called it, so anything queued goes out first.
			if len(parts) > 0 {
				out = append(out, OpenAIMessage{Role: "user", Content: parts})
				parts = nil
			}
			out = append(out, OpenAIMessage{
				Role:       "tool",
				ToolCallID: ids.match(p.FunctionResponse.Name, p.FunctionResponse.ID),
				Content:    responseText(p.FunctionResponse.Response),
			})
		case p.InlineData != nil:
			parts = append(parts, OpenAIPart{Type: "image_url", ImageURL: &OpenAIImageURL{URL: dataURL(p.InlineData)}})
		case p.FileData != nil:
			parts = append(parts, OpenAIPart{Type: "image_url", ImageURL: &OpenAIImageURL{URL: p.FileData.FileURI}})
		case p.Text != "" && !p.Thought:
			parts = append(parts, OpenAIPart{Type: "text", Text: p.Text})
		}
	}

	if len(parts) == 1 && parts[0].Type == "text" {
		// Keep the common case a plain string: a few gateways only accept the
		// array form for genuinely multimodal turns.
		out = append(out, OpenAIMessage{Role: "user", Content: parts[0].Text})
	} else if len(parts) > 0 {
		out = append(out, OpenAIMessage{Role: "user", Content: parts})
	}
	return out
}

func openAIToolChoice(tc *GeminiToolConfig) any {
	if tc == nil || tc.FunctionCallingConfig == nil {
		return nil
	}
	cfg := tc.FunctionCallingConfig
	if len(cfg.AllowedFunctionNames) == 1 {
		return map[string]any{
			"type":     "function",
			"function": map[string]string{"name": cfg.AllowedFunctionNames[0]},
		}
	}
	switch strings.ToUpper(cfg.Mode) {
	case "ANY":
		return "required"
	case "NONE":
		return "none"
	case "AUTO":
		return "auto"
	}
	return nil
}

func applyOpenAIGenConfig(out *OpenAIRequest, cfg *GeminiGenConfig, model string) {
	if cfg == nil {
		return
	}
	out.Temperature = cfg.Temperature
	out.TopP = cfg.TopP
	out.Stop = cfg.StopSequences
	if cfg.MaxOutputTokens != nil {
		if wantsCompletionTokens(model) {
			out.MaxCompletionTokens = cfg.MaxOutputTokens
		} else {
			out.MaxTokens = cfg.MaxOutputTokens
		}
	}
	if strings.Contains(cfg.ResponseMimeType, "json") {
		if len(cfg.ResponseSchema) > 0 {
			schema, err := json.Marshal(map[string]any{
				"name":   "response",
				"strict": false,
				"schema": json.RawMessage(normalizeSchema(cfg.ResponseSchema)),
			})
			if err == nil {
				out.ResponseFormat = &OpenAIResponseFormat{Type: "json_schema", JSONSchema: schema}
			}
		}
		if out.ResponseFormat == nil {
			out.ResponseFormat = &OpenAIResponseFormat{Type: "json_object"}
		}
	}
	if cfg.ThinkingConfig != nil {
		out.ReasoningEffort = effortFromBudget(cfg.ThinkingConfig.ThinkingBudget)
	}
}

// wantsCompletionTokens: OpenAI's reasoning-era models reject max_tokens
// outright, while most compatible gateways still only understand max_tokens.
// The model name is the only signal available here.
func wantsCompletionTokens(model string) bool {
	m := strings.ToLower(model)
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	for _, prefix := range []string{"o1", "o3", "o4", "gpt-5", "gpt5"} {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}

// effortFromBudget turns Gemini's token budget into OpenAI's coarse dial.
func effortFromBudget(budget *int) string {
	if budget == nil {
		return ""
	}
	switch b := *budget; {
	case b == 0:
		return "minimal"
	case b < 0: // -1 is "decide for yourself"
		return ""
	case b <= 2048:
		return "low"
	case b <= 12288:
		return "medium"
	default:
		return "high"
	}
}

// FromOpenAI translates a whole completion back into the CLI's dialect.
func FromOpenAI(body []byte) (*GeminiResponse, error) {
	var in OpenAIResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("decoding upstream reply: %w", err)
	}
	if in.Error != nil && in.Error.Message != "" {
		return nil, fmt.Errorf("%s", in.Error.Message)
	}
	out := &GeminiResponse{ModelVersion: in.Model, ResponseID: in.ID}
	for _, ch := range in.Choices {
		msg := ch.Message
		if msg == nil {
			msg = ch.Delta
		}
		cand := GeminiCandidate{
			Index:        ch.Index,
			FinishReason: geminiFinish(ch.FinishReason),
			Content:      GeminiContent{Role: RoleModel},
		}
		if thinking := msg.Thinking(); thinking != "" {
			cand.Content.Parts = append(cand.Content.Parts, GeminiPart{Text: thinking, Thought: true})
		}
		if text := msg.Text(); text != "" {
			cand.Content.Parts = append(cand.Content.Parts, GeminiPart{Text: text})
		}
		for _, tc := range msg.ToolCalls {
			cand.Content.Parts = append(cand.Content.Parts, toolCallPart(tc))
		}
		out.Candidates = append(out.Candidates, cand)
	}
	if in.Usage != nil {
		out.UsageMetadata = &GeminiUsage{
			PromptTokenCount:     in.Usage.PromptTokens,
			CandidatesTokenCount: in.Usage.CompletionTokens,
			TotalTokenCount:      in.Usage.TotalTokens,
		}
		if in.Usage.Details != nil {
			out.UsageMetadata.ThoughtsTokenCount = in.Usage.Details.ReasoningTokens
		}
	}
	return out, nil
}

func toolCallPart(tc OpenAIToolCall) GeminiPart {
	args := strings.TrimSpace(tc.Function.Arguments)
	if args == "" || !json.Valid([]byte(args)) {
		// A truncated or non-JSON argument string would make the whole reply
		// unparseable to the CLI; an empty object at least names the call.
		args = "{}"
	}
	return GeminiPart{FunctionCall: &GeminiFunctionCall{
		Name: tc.Function.Name,
		Args: json.RawMessage(args),
		ID:   tc.ID,
	}}
}

// geminiFinish maps a stop reason into Gemini's vocabulary.
func geminiFinish(reason string) string {
	switch strings.ToLower(reason) {
	case "":
		return ""
	case "stop", "end_turn", "stop_sequence", "tool_calls", "tool_use", "function_call":
		return FinishStop
	case "length", "max_tokens", "model_length":
		return FinishMaxTokens
	case "content_filter", "refusal":
		return FinishSafety
	}
	return FinishOther
}
