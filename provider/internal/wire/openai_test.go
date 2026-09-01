package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

func ptrInt(v int) *int { return &v }

// transcript is the shape the engine actually sends: a system instruction, a
// user turn, a tool call, its result, and a follow-up.
func transcript() *GeminiRequest {
	return &GeminiRequest{
		SystemInstruction: &GeminiContent{Parts: []GeminiPart{{Text: "You are terse."}}},
		Contents: []GeminiContent{
			{Role: RoleUser, Parts: []GeminiPart{{Text: "what is in main.go?"}}},
			{Role: RoleModel, Parts: []GeminiPart{
				{Text: "Looking.", Thought: false},
				{Text: "I should read it first.", Thought: true},
				{FunctionCall: &GeminiFunctionCall{Name: "read_file", Args: json.RawMessage(`{"path":"main.go"}`)}},
			}},
			{Role: RoleUser, Parts: []GeminiPart{
				{FunctionResponse: &GeminiFunctionResponse{Name: "read_file", Response: json.RawMessage(`{"output":"package main"}`)}},
			}},
			{Role: RoleUser, Parts: []GeminiPart{{Text: "and now?"}}},
		},
		Tools: []GeminiTool{{FunctionDeclarations: []GeminiFunctionDecl{{
			Name:        "read_file",
			Description: "read a file",
			Parameters:  json.RawMessage(`{"type":"OBJECT","properties":{"path":{"type":"STRING","nullable":true}},"required":["path"]}`),
		}}}},
		GenerationConfig: &GeminiGenConfig{
			MaxOutputTokens: ptrInt(4096),
			ThinkingConfig:  &GeminiThinkingConfig{ThinkingBudget: ptrInt(4096), IncludeThoughts: true},
		},
	}
}

func TestToOpenAIShapesTheTranscript(t *testing.T) {
	got, err := ToOpenAI(transcript(), "gpt-4o", true)
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}

	if len(got.Messages) != 5 {
		t.Fatalf("want 5 messages, got %d: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content != "You are terse." {
		t.Errorf("system message wrong: %+v", got.Messages[0])
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content != "what is in main.go?" {
		t.Errorf("user message wrong: %+v", got.Messages[1])
	}

	assistant := got.Messages[2]
	if assistant.Role != "assistant" {
		t.Fatalf("want assistant third, got %q", assistant.Role)
	}
	if assistant.Content != "Looking." {
		t.Errorf("a thought part must not reach the wire as text: %v", assistant.Content)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("tool call missing: %+v", assistant.ToolCalls)
	}
	if assistant.ToolCalls[0].Function.Arguments != `{"path":"main.go"}` {
		t.Errorf("arguments wrong: %q", assistant.ToolCalls[0].Function.Arguments)
	}

	// The pairing Gemini does by name has to survive as an id pair.
	result := got.Messages[3]
	if result.Role != "tool" {
		t.Fatalf("want the tool result fourth, got %q", result.Role)
	}
	if result.ToolCallID != assistant.ToolCalls[0].ID {
		t.Errorf("tool_call_id %q does not match the call's id %q", result.ToolCallID, assistant.ToolCalls[0].ID)
	}
	if result.Content != "package main" {
		t.Errorf("a one-key output wrapper should unwrap to its text, got %v", result.Content)
	}

	if got.Messages[4].Role != "user" {
		t.Errorf("want the follow-up last, got %q", got.Messages[4].Role)
	}

	if !got.Stream || got.StreamOptions == nil || !got.StreamOptions.IncludeUsage {
		t.Error("a streaming request should ask for the usage record")
	}
	if got.MaxTokens == nil || *got.MaxTokens != 4096 || got.MaxCompletionTokens != nil {
		t.Errorf("gpt-4o wants max_tokens: %+v %+v", got.MaxTokens, got.MaxCompletionTokens)
	}
	if got.ReasoningEffort != "medium" {
		t.Errorf("a 4096-token budget should be medium effort, got %q", got.ReasoningEffort)
	}

	// The schema must arrive as JSON Schema, not as proto enum names.
	schema := string(got.Tools[0].Function.Parameters)
	if strings.Contains(schema, "OBJECT") || strings.Contains(schema, "STRING") {
		t.Errorf("schema types were not lowercased: %s", schema)
	}
	if strings.Contains(schema, "nullable") {
		t.Errorf("nullable should be dropped: %s", schema)
	}
	if !strings.Contains(schema, `"required":["path"]`) {
		t.Errorf("required was lost: %s", schema)
	}
}

func TestToOpenAIPicksTheRightTokenField(t *testing.T) {
	for _, tc := range []struct {
		model      string
		completion bool
	}{
		{"gpt-4o", false},
		{"gpt-5.1-codex", true},
		{"o3-mini", true},
		{"anthropic/claude-sonnet-4.5", false},
		{"openai/gpt-5-mini", true},
	} {
		g := &GeminiRequest{
			Contents:         []GeminiContent{{Role: RoleUser, Parts: []GeminiPart{{Text: "hi"}}}},
			GenerationConfig: &GeminiGenConfig{MaxOutputTokens: ptrInt(64)},
		}
		got, err := ToOpenAI(g, tc.model, false)
		if err != nil {
			t.Fatalf("%s: %v", tc.model, err)
		}
		if tc.completion && (got.MaxCompletionTokens == nil || got.MaxTokens != nil) {
			t.Errorf("%s should send max_completion_tokens", tc.model)
		}
		if !tc.completion && (got.MaxTokens == nil || got.MaxCompletionTokens != nil) {
			t.Errorf("%s should send max_tokens", tc.model)
		}
	}
}

func TestToOpenAICarriesAnAttachment(t *testing.T) {
	g := &GeminiRequest{Contents: []GeminiContent{{Role: RoleUser, Parts: []GeminiPart{
		{Text: "what is this?"},
		{InlineData: &GeminiBlob{MimeType: "image/png", Data: "AAAA"}},
	}}}}
	got, err := ToOpenAI(g, "gpt-4o", false)
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	parts, ok := got.Messages[0].Content.([]OpenAIPart)
	if !ok {
		t.Fatalf("a multimodal turn must use the part array, got %T", got.Messages[0].Content)
	}
	if len(parts) != 2 || parts[1].ImageURL == nil {
		t.Fatalf("attachment missing: %+v", parts)
	}
	if parts[1].ImageURL.URL != "data:image/png;base64,AAAA" {
		t.Errorf("data url wrong: %q", parts[1].ImageURL.URL)
	}
}

func TestFromOpenAIReadsAWholeReply(t *testing.T) {
	body := []byte(`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant",
      "reasoning_content":"thinking about it","content":"done",
      "tool_calls":[{"id":"call_9","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"a\"}"}}]},
      "finish_reason":"tool_calls"}],
      "usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"completion_tokens_details":{"reasoning_tokens":3}}}`)

	got, err := FromOpenAI(body)
	if err != nil {
		t.Fatalf("FromOpenAI: %v", err)
	}
	parts := got.Candidates[0].Content.Parts
	if len(parts) != 3 {
		t.Fatalf("want thought, text and call, got %d: %+v", len(parts), parts)
	}
	if !parts[0].Thought || parts[0].Text != "thinking about it" {
		t.Errorf("reasoning should arrive as a thought part: %+v", parts[0])
	}
	if parts[1].Text != "done" || parts[1].Thought {
		t.Errorf("text part wrong: %+v", parts[1])
	}
	if parts[2].FunctionCall == nil || parts[2].FunctionCall.Name != "write_file" {
		t.Fatalf("function call missing: %+v", parts[2])
	}
	if got.Candidates[0].FinishReason != FinishStop {
		t.Errorf("a tool call finishes as STOP in Gemini's vocabulary, got %q", got.Candidates[0].FinishReason)
	}
	if got.UsageMetadata.PromptTokenCount != 10 || got.UsageMetadata.ThoughtsTokenCount != 3 {
		t.Errorf("usage wrong: %+v", got.UsageMetadata)
	}
}

func TestFromOpenAIRepairsTruncatedArguments(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"c","function":{"name":"x","arguments":"{\"a\":"}}]},"finish_reason":"length"}]}`)
	got, err := FromOpenAI(body)
	if err != nil {
		t.Fatalf("FromOpenAI: %v", err)
	}
	call := got.Candidates[0].Content.Parts[0].FunctionCall
	if string(call.Args) != "{}" {
		t.Errorf("half a JSON object would make the whole reply unreadable; want {}, got %s", call.Args)
	}
	if got.Candidates[0].FinishReason != FinishMaxTokens {
		t.Errorf("want MAX_TOKENS, got %q", got.Candidates[0].FinishReason)
	}
}

func TestOpenAIStreamAssemblesFragments(t *testing.T) {
	s := NewOpenAIStream()
	events := []string{
		`{"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"hmm"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"run","arguments":"{\"cmd\""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"ls\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
		`[DONE]`,
	}

	var text, thoughts string
	for _, ev := range events {
		chunks, err := s.Event([]byte(ev))
		if err != nil {
			t.Fatalf("event %q: %v", ev, err)
		}
		for _, c := range chunks {
			for _, p := range c.Candidates[0].Content.Parts {
				if p.Thought {
					thoughts += p.Text
				} else {
					text += p.Text
				}
			}
		}
	}

	if text != "Hello" {
		t.Errorf("text should concatenate to Hello, got %q", text)
	}
	if thoughts != "hmm" {
		t.Errorf("thoughts wrong: %q", thoughts)
	}

	final := s.Done()
	if len(final) != 1 {
		t.Fatalf("want one closing chunk, got %d", len(final))
	}
	cand := final[0].Candidates[0]
	if len(cand.Content.Parts) != 1 || cand.Content.Parts[0].FunctionCall == nil {
		t.Fatalf("the assembled call is missing: %+v", cand.Content.Parts)
	}
	call := cand.Content.Parts[0].FunctionCall
	if call.Name != "run" || string(call.Args) != `{"cmd":"ls"}` {
		t.Errorf("fragments did not reassemble: %s %s", call.Name, call.Args)
	}
	if cand.FinishReason != FinishStop {
		t.Errorf("finish reason wrong: %q", cand.FinishReason)
	}
	if final[0].UsageMetadata == nil || final[0].UsageMetadata.TotalTokenCount != 9 {
		t.Errorf("usage wrong: %+v", final[0].UsageMetadata)
	}
	if s.Done() != nil {
		t.Error("Done twice must not emit the reply twice")
	}
}

func TestOpenAIStreamSurfacesAnError(t *testing.T) {
	s := NewOpenAIStream()
	if _, err := s.Event([]byte(`{"error":{"message":"rate limited"}}`)); err == nil {
		t.Fatal("an error inside a stream must not be swallowed")
	}
}

func TestToolChoiceCrossesOver(t *testing.T) {
	anyMode := &GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{Mode: "ANY"}}
	got, err := ToOpenAI(&GeminiRequest{ToolConfig: anyMode}, "gpt-4o", false)
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	if got.ToolChoice != "required" {
		t.Errorf("ANY should become required, got %v", got.ToolChoice)
	}

	named := &GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{AllowedFunctionNames: []string{"read_file"}}}
	got, err = ToOpenAI(&GeminiRequest{ToolConfig: named}, "gpt-4o", false)
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	choice, ok := got.ToolChoice.(map[string]any)
	if !ok || choice["type"] != "function" {
		t.Fatalf("a single allowed name should pin the function: %+v", got.ToolChoice)
	}
}
