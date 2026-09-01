package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToAnthropicShapesTheTranscript(t *testing.T) {
	got, err := ToAnthropic(transcript(), "claude-sonnet-4-5", true)
	if err != nil {
		t.Fatalf("ToAnthropic: %v", err)
	}

	if len(got.System) != 1 || got.System[0].Text != "You are terse." {
		t.Errorf("system wrong: %+v", got.System)
	}

	// Two user turns in a row are legal in Gemini's transcript and rejected by
	// Anthropic, so the tool result and the follow-up must merge.
	if len(got.Messages) != 3 {
		t.Fatalf("want user, assistant, user; got %d: %+v", len(got.Messages), got.Messages)
	}
	roles := []string{got.Messages[0].Role, got.Messages[1].Role, got.Messages[2].Role}
	if roles[0] != "user" || roles[1] != "assistant" || roles[2] != "user" {
		t.Fatalf("roles must alternate, got %v", roles)
	}

	assistant := got.Messages[1].Content
	if len(assistant) != 2 || assistant[0].Type != "text" || assistant[1].Type != "tool_use" {
		t.Fatalf("assistant blocks wrong: %+v", assistant)
	}
	if assistant[0].Text != "Looking." {
		t.Errorf("a thought part must not reach the wire: %q", assistant[0].Text)
	}

	merged := got.Messages[2].Content
	if len(merged) != 2 {
		t.Fatalf("want the tool result and the follow-up merged, got %+v", merged)
	}
	if merged[0].Type != "tool_result" {
		t.Errorf("a tool result has to lead its turn, got %q", merged[0].Type)
	}
	if merged[0].ToolUseID != assistant[1].ID {
		t.Errorf("tool_use_id %q does not match the call %q", merged[0].ToolUseID, assistant[1].ID)
	}
	if merged[1].Type != "text" || merged[1].Text != "and now?" {
		t.Errorf("follow-up wrong: %+v", merged[1])
	}

	if got.MaxTokens != 4096 {
		t.Errorf("max_tokens wrong: %d", got.MaxTokens)
	}
	// The budget has to leave room for the reply, and sampling must be left
	// alone while thinking is on.
	if got.Thinking == nil || got.Thinking.BudgetTokens != 4095 {
		t.Errorf("thinking budget should clamp under max_tokens: %+v", got.Thinking)
	}
	if got.Temperature != nil || got.TopP != nil || got.TopK != nil {
		t.Error("Anthropic rejects an explicit temperature while thinking is enabled")
	}

	schema := string(got.Tools[0].InputSchema)
	if strings.Contains(schema, "OBJECT") || strings.Contains(schema, "nullable") {
		t.Errorf("schema was not normalized: %s", schema)
	}
}

func TestToAnthropicAlwaysSendsMaxTokens(t *testing.T) {
	got, err := ToAnthropic(&GeminiRequest{
		Contents: []GeminiContent{{Role: RoleUser, Parts: []GeminiPart{{Text: "hi"}}}},
	}, "claude-sonnet-4-5", false)
	if err != nil {
		t.Fatalf("ToAnthropic: %v", err)
	}
	if got.MaxTokens != DefaultAnthropicMaxTokens {
		t.Errorf("max_tokens is required by the API; want the default, got %d", got.MaxTokens)
	}
}

func TestToAnthropicOpensOnTheUser(t *testing.T) {
	got, err := ToAnthropic(&GeminiRequest{
		Contents: []GeminiContent{{Role: RoleModel, Parts: []GeminiPart{{Text: "carrying on"}}}},
	}, "claude-sonnet-4-5", false)
	if err != nil {
		t.Fatalf("ToAnthropic: %v", err)
	}
	if got.Messages[0].Role != "user" {
		t.Fatalf("a budgeted transcript can start on the assistant; the first turn must still be the user's: %+v", got.Messages)
	}
	if got.Messages[1].Role != "assistant" {
		t.Errorf("the original turn should follow: %+v", got.Messages)
	}
}

func TestToAnthropicDropsToolsWhenCallingIsOff(t *testing.T) {
	g := transcript()
	g.ToolConfig = &GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{Mode: "NONE"}}
	got, err := ToAnthropic(g, "claude-sonnet-4-5", false)
	if err != nil {
		t.Fatalf("ToAnthropic: %v", err)
	}
	if len(got.Tools) != 0 || got.ToolChoice != nil {
		t.Errorf("NONE should leave nothing to call: %+v %+v", got.Tools, got.ToolChoice)
	}
}

func TestToAnthropicCarriesAPDFAsADocument(t *testing.T) {
	g := &GeminiRequest{Contents: []GeminiContent{{Role: RoleUser, Parts: []GeminiPart{
		{InlineData: &GeminiBlob{MimeType: "application/pdf", Data: "JVBER"}},
		{InlineData: &GeminiBlob{MimeType: "image/jpeg", Data: "AAA"}},
	}}}}
	got, err := ToAnthropic(g, "claude-sonnet-4-5", false)
	if err != nil {
		t.Fatalf("ToAnthropic: %v", err)
	}
	blocks := got.Messages[0].Content
	if blocks[0].Type != "document" || blocks[1].Type != "image" {
		t.Errorf("attachment types wrong: %q %q", blocks[0].Type, blocks[1].Type)
	}
	if blocks[0].Source.Type != "base64" || blocks[0].Source.MediaType != "application/pdf" {
		t.Errorf("source wrong: %+v", blocks[0].Source)
	}
}

func TestFromAnthropicReadsAWholeReply(t *testing.T) {
	body := []byte(`{"id":"msg_1","model":"claude-sonnet-4-5","content":[
      {"type":"thinking","thinking":"weighing it","signature":"sig"},
      {"type":"text","text":"here you go"},
      {"type":"tool_use","id":"toolu_1","name":"run","input":{"cmd":"ls"}}],
      "stop_reason":"tool_use","usage":{"input_tokens":10,"cache_read_input_tokens":90,"output_tokens":5}}`)

	got, err := FromAnthropic(body)
	if err != nil {
		t.Fatalf("FromAnthropic: %v", err)
	}
	parts := got.Candidates[0].Content.Parts
	if len(parts) != 3 {
		t.Fatalf("want three parts, got %d: %+v", len(parts), parts)
	}
	if !parts[0].Thought || parts[0].ThoughtSignature != "sig" {
		t.Errorf("thinking block wrong: %+v", parts[0])
	}
	if parts[2].FunctionCall == nil || string(parts[2].FunctionCall.Args) != `{"cmd":"ls"}` {
		t.Errorf("tool use wrong: %+v", parts[2])
	}
	// Cached input still counted as input.
	if got.UsageMetadata.PromptTokenCount != 100 {
		t.Errorf("cache reads belong in the prompt count, got %d", got.UsageMetadata.PromptTokenCount)
	}
	if got.UsageMetadata.TotalTokenCount != 105 {
		t.Errorf("total wrong: %d", got.UsageMetadata.TotalTokenCount)
	}
}

func TestAnthropicStreamAssemblesEvents(t *testing.T) {
	s := NewAnthropicStream()
	events := []string{
		`{"type":"message_start","message":{"id":"msg_2","model":"claude-sonnet-4-5","usage":{"input_tokens":12}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Rea"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"dy"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_7","name":"run","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":6}}`,
		`{"type":"message_stop"}`,
	}

	var text, thoughts string
	var calls []*GeminiFunctionCall
	for _, ev := range events {
		chunks, err := s.Event([]byte(ev))
		if err != nil {
			t.Fatalf("event %q: %v", ev, err)
		}
		for _, c := range chunks {
			for _, p := range c.Candidates[0].Content.Parts {
				switch {
				case p.FunctionCall != nil:
					calls = append(calls, p.FunctionCall)
				case p.Thought:
					thoughts += p.Text
				default:
					text += p.Text
				}
			}
		}
	}

	if text != "Ready" {
		t.Errorf("text wrong: %q", text)
	}
	if thoughts != "let me" {
		t.Errorf("thoughts wrong: %q", thoughts)
	}
	if len(calls) != 1 || string(calls[0].Args) != `{"cmd":"ls"}` {
		t.Fatalf("the tool call did not reassemble: %+v", calls)
	}
	if calls[0].ID != "toolu_7" {
		t.Errorf("the call's id should survive: %q", calls[0].ID)
	}

	final := s.Done()
	if final[0].Candidates[0].FinishReason != FinishStop {
		t.Errorf("finish reason wrong: %q", final[0].Candidates[0].FinishReason)
	}
	if final[0].UsageMetadata == nil || final[0].UsageMetadata.PromptTokenCount != 12 ||
		final[0].UsageMetadata.CandidatesTokenCount != 6 {
		t.Errorf("usage wrong: %+v", final[0].UsageMetadata)
	}
}

func TestAnthropicStreamSurfacesAnError(t *testing.T) {
	s := NewAnthropicStream()
	if _, err := s.Event([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`)); err == nil {
		t.Fatal("an error event must not be swallowed")
	}
}

func TestFunctionResponsesPairInOrder(t *testing.T) {
	// Two calls of the same name, answered in order: Gemini pairs them by
	// position, and both dialects have to reproduce that pairing.
	g := &GeminiRequest{Contents: []GeminiContent{
		{Role: RoleModel, Parts: []GeminiPart{
			{FunctionCall: &GeminiFunctionCall{Name: "read", Args: json.RawMessage(`{"p":"a"}`)}},
			{FunctionCall: &GeminiFunctionCall{Name: "read", Args: json.RawMessage(`{"p":"b"}`)}},
		}},
		{Role: RoleUser, Parts: []GeminiPart{
			{FunctionResponse: &GeminiFunctionResponse{Name: "read", Response: json.RawMessage(`{"output":"A"}`)}},
			{FunctionResponse: &GeminiFunctionResponse{Name: "read", Response: json.RawMessage(`{"output":"B"}`)}},
		}},
	}}

	oa, err := ToOpenAI(g, "gpt-4o", false)
	if err != nil {
		t.Fatalf("ToOpenAI: %v", err)
	}
	firstID := oa.Messages[0].ToolCalls[0].ID
	secondID := oa.Messages[0].ToolCalls[1].ID
	if firstID == secondID {
		t.Fatal("two calls need two ids")
	}
	if oa.Messages[1].ToolCallID != firstID || oa.Messages[2].ToolCallID != secondID {
		t.Errorf("results paired out of order: %q then %q", oa.Messages[1].ToolCallID, oa.Messages[2].ToolCallID)
	}

	an, err := ToAnthropic(g, "claude-sonnet-4-5", false)
	if err != nil {
		t.Fatalf("ToAnthropic: %v", err)
	}
	// The prepended user turn shifts everything by one.
	uses := an.Messages[1].Content
	results := an.Messages[2].Content
	if results[0].ToolUseID != uses[0].ID || results[1].ToolUseID != uses[1].ID {
		t.Errorf("results paired out of order: %+v vs %+v", results, uses)
	}
}
