package wire

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToAnthropic translates the CLI's Gemini request into a messages body.
func ToAnthropic(g *GeminiRequest, model string, stream bool) (*AnthropicRequest, error) {
	out := &AnthropicRequest{Model: model, Stream: stream, MaxTokens: DefaultAnthropicMaxTokens}
	ids := newCallIDs("toolu_")

	if g.SystemInstruction != nil {
		if text := splitText(g.SystemInstruction.Parts); text != "" {
			out.System = []AnthropicBlock{{Type: "text", Text: text}}
		}
	}

	for _, c := range g.Contents {
		role := "user"
		var blocks []AnthropicBlock
		if c.Role == RoleModel {
			role = "assistant"
			blocks = assistantAnthropic(c, ids)
		} else {
			blocks = userAnthropic(c, ids)
		}
		if len(blocks) == 0 {
			continue
		}
		// Anthropic requires the roles to alternate, while Gemini is happy with
		// two user turns in a row — which is exactly what a tool result followed
		// by the next instruction looks like.
		if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == role {
			out.Messages[n-1].Content = append(out.Messages[n-1].Content, blocks...)
			continue
		}
		out.Messages = append(out.Messages, AnthropicMessage{Role: role, Content: blocks})
	}

	if len(out.Messages) == 0 {
		out.Messages = []AnthropicMessage{{Role: "user", Content: []AnthropicBlock{{Type: "text", Text: "Continue."}}}}
	} else if out.Messages[0].Role != "user" {
		// The first turn has to be the user's. A transcript that opens on the
		// assistant is a budgeted one, so say so rather than dropping the turn.
		out.Messages = append([]AnthropicMessage{{
			Role:    "user",
			Content: []AnthropicBlock{{Type: "text", Text: "Continue the conversation."}},
		}}, out.Messages...)
	}

	if g.ToolConfig == nil || g.ToolConfig.FunctionCallingConfig == nil ||
		!strings.EqualFold(g.ToolConfig.FunctionCallingConfig.Mode, "NONE") {
		for _, t := range g.Tools {
			for _, d := range t.FunctionDeclarations {
				schema := normalizeSchema(d.Parameters)
				if len(schema) == 0 {
					schema = json.RawMessage(`{"type":"object","properties":{}}`)
				}
				out.Tools = append(out.Tools, AnthropicTool{
					Name:        d.Name,
					Description: d.Description,
					InputSchema: schema,
				})
			}
		}
		out.ToolChoice = anthropicToolChoice(g.ToolConfig)
	}

	applyAnthropicGenConfig(out, g.GenerationConfig)
	return out, nil
}

func assistantAnthropic(c GeminiContent, ids *callIDs) []AnthropicBlock {
	var blocks []AnthropicBlock
	if text := splitText(c.Parts); text != "" {
		blocks = append(blocks, AnthropicBlock{Type: "text", Text: text})
	}
	for _, p := range c.Parts {
		if p.FunctionCall == nil {
			continue
		}
		input := p.FunctionCall.Args
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		blocks = append(blocks, AnthropicBlock{
			Type:  "tool_use",
			ID:    ids.issue(p.FunctionCall.Name, p.FunctionCall.ID),
			Name:  p.FunctionCall.Name,
			Input: input,
		})
	}
	return blocks
}

func userAnthropic(c GeminiContent, ids *callIDs) []AnthropicBlock {
	var blocks []AnthropicBlock
	// Tool results have to come first in the turn that carries them; Anthropic
	// rejects a user turn whose tool_result blocks trail its text.
	for _, p := range c.Parts {
		if p.FunctionResponse == nil {
			continue
		}
		text := responseText(p.FunctionResponse.Response)
		if text == "" {
			text = "(no output)"
		}
		blocks = append(blocks, AnthropicBlock{
			Type:      "tool_result",
			ToolUseID: ids.match(p.FunctionResponse.Name, p.FunctionResponse.ID),
			Content:   []AnthropicBlock{{Type: "text", Text: text}},
		})
	}
	for _, p := range c.Parts {
		switch {
		case p.InlineData != nil:
			blocks = append(blocks, AnthropicBlock{
				Type:   attachmentType(p.InlineData.MimeType),
				Source: &AnthropicSource{Type: "base64", MediaType: p.InlineData.MimeType, Data: p.InlineData.Data},
			})
		case p.FileData != nil:
			blocks = append(blocks, AnthropicBlock{
				Type:   attachmentType(p.FileData.MimeType),
				Source: &AnthropicSource{Type: "url", URL: p.FileData.FileURI},
			})
		case p.Text != "" && !p.Thought:
			blocks = append(blocks, AnthropicBlock{Type: "text", Text: p.Text})
		}
	}
	return blocks
}

func attachmentType(mime string) string {
	if strings.HasPrefix(mime, "application/pdf") {
		return "document"
	}
	return "image"
}

func anthropicToolChoice(tc *GeminiToolConfig) map[string]any {
	if tc == nil || tc.FunctionCallingConfig == nil {
		return nil
	}
	cfg := tc.FunctionCallingConfig
	if len(cfg.AllowedFunctionNames) == 1 {
		return map[string]any{"type": "tool", "name": cfg.AllowedFunctionNames[0]}
	}
	switch strings.ToUpper(cfg.Mode) {
	case "ANY":
		return map[string]any{"type": "any"}
	case "AUTO":
		return map[string]any{"type": "auto"}
	}
	return nil
}

func applyAnthropicGenConfig(out *AnthropicRequest, cfg *GeminiGenConfig) {
	if cfg == nil {
		return
	}
	if cfg.MaxOutputTokens != nil && *cfg.MaxOutputTokens > 0 {
		out.MaxTokens = *cfg.MaxOutputTokens
	}
	out.StopSequences = cfg.StopSequences
	out.Temperature = cfg.Temperature
	out.TopP = cfg.TopP
	out.TopK = cfg.TopK

	if cfg.ThinkingConfig == nil || cfg.ThinkingConfig.ThinkingBudget == nil {
		return
	}
	budget := *cfg.ThinkingConfig.ThinkingBudget
	if budget == 0 {
		return
	}
	// Anthropic's floor is 1024, the budget must leave room for the reply, and
	// sampling has to be left alone while thinking is on — it rejects an
	// explicit temperature outright.
	if budget < 1024 {
		budget = 1024
	}
	if out.MaxTokens <= 1024 {
		return
	}
	if budget >= out.MaxTokens {
		budget = out.MaxTokens - 1
	}
	out.Thinking = &AnthropicThinking{Type: "enabled", BudgetTokens: budget}
	out.Temperature = nil
	out.TopP = nil
	out.TopK = nil
}

// FromAnthropic translates a whole reply back into the CLI's dialect.
func FromAnthropic(body []byte) (*GeminiResponse, error) {
	var in AnthropicResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("decoding upstream reply: %w", err)
	}
	if in.Error != nil && in.Error.Message != "" {
		return nil, fmt.Errorf("%s", in.Error.Message)
	}
	cand := GeminiCandidate{
		Content:      GeminiContent{Role: RoleModel},
		FinishReason: geminiFinish(in.StopReason),
	}
	for _, b := range in.Content {
		if part, ok := anthropicPart(b); ok {
			cand.Content.Parts = append(cand.Content.Parts, part)
		}
	}
	out := &GeminiResponse{
		ModelVersion: in.Model,
		ResponseID:   in.ID,
		Candidates:   []GeminiCandidate{cand},
	}
	if in.Usage != nil {
		out.UsageMetadata = &GeminiUsage{
			PromptTokenCount:     in.Usage.Prompt(),
			CandidatesTokenCount: in.Usage.OutputTokens,
			TotalTokenCount:      in.Usage.Prompt() + in.Usage.OutputTokens,
		}
	}
	return out, nil
}

func anthropicPart(b AnthropicBlock) (GeminiPart, bool) {
	switch b.Type {
	case "text":
		return GeminiPart{Text: b.Text}, b.Text != ""
	case "thinking":
		return GeminiPart{Text: b.Thinking, Thought: true, ThoughtSignature: b.Signature}, b.Thinking != ""
	case "redacted_thinking":
		return GeminiPart{}, false
	case "tool_use":
		input := b.Input
		if len(input) == 0 || !json.Valid(input) {
			input = json.RawMessage(`{}`)
		}
		return GeminiPart{FunctionCall: &GeminiFunctionCall{Name: b.Name, Args: input, ID: b.ID}}, b.Name != ""
	}
	return GeminiPart{}, false
}
