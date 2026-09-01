package wire

import "encoding/json"

// ---------- Anthropic (messages) ----------

// DefaultAnthropicMaxTokens is what a request goes out with when the CLI did not
// ask for a limit. Anthropic requires the field, and 8192 is the one value every
// model in the family accepts.
const DefaultAnthropicMaxTokens = 8192

// AnthropicRequest is a /v1/messages body.
type AnthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        []AnthropicBlock   `json:"system,omitempty"`
	Messages      []AnthropicMessage `json:"messages"`
	Tools         []AnthropicTool    `json:"tools,omitempty"`
	ToolChoice    map[string]any     `json:"tool_choice,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	TopK          *int               `json:"top_k,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Thinking      *AnthropicThinking `json:"thinking,omitempty"`
}

// AnthropicThinking is the extended-thinking switch and its budget.
type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// AnthropicMessage is one turn; content is always the block form here.
type AnthropicMessage struct {
	Role    string           `json:"role"`
	Content []AnthropicBlock `json:"content"`
}

// AnthropicBlock is one content block of any type.
type AnthropicBlock struct {
	Type      string            `json:"type"`
	Text      string            `json:"text,omitempty"`
	Thinking  string            `json:"thinking,omitempty"`
	Signature string            `json:"signature,omitempty"`
	Source    *AnthropicSource  `json:"source,omitempty"`
	ID        string            `json:"id,omitempty"`
	Name      string            `json:"name,omitempty"`
	Input     json.RawMessage   `json:"input,omitempty"`
	ToolUseID string            `json:"tool_use_id,omitempty"`
	Content   any               `json:"content,omitempty"`
	IsError   bool              `json:"is_error,omitempty"`
	Cache     map[string]string `json:"cache_control,omitempty"`
}

// AnthropicSource is an attachment's bytes or its URL.
type AnthropicSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// AnthropicTool declares a callable tool.
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// AnthropicResponse is a whole reply, and also the `message` of a message_start
// event.
type AnthropicResponse struct {
	ID         string           `json:"id,omitempty"`
	Type       string           `json:"type,omitempty"`
	Role       string           `json:"role,omitempty"`
	Model      string           `json:"model,omitempty"`
	Content    []AnthropicBlock `json:"content,omitempty"`
	StopReason string           `json:"stop_reason,omitempty"`
	Usage      *AnthropicUsage  `json:"usage,omitempty"`
	Error      *AnthropicError  `json:"error,omitempty"`
}

// AnthropicUsage is the token accounting.
type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// Prompt is every token that counted as input, however it was billed.
func (u *AnthropicUsage) Prompt() int {
	if u == nil {
		return 0
	}
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// AnthropicError is the error envelope.
type AnthropicError struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

// AnthropicEvent is one event of a streamed reply.
type AnthropicEvent struct {
	Type         string             `json:"type"`
	Index        int                `json:"index"`
	Message      *AnthropicResponse `json:"message,omitempty"`
	ContentBlock *AnthropicBlock    `json:"content_block,omitempty"`
	Delta        *AnthropicDelta    `json:"delta,omitempty"`
	Usage        *AnthropicUsage    `json:"usage,omitempty"`
	Error        *AnthropicError    `json:"error,omitempty"`
}

// AnthropicDelta is the payload of a content_block_delta or message_delta.
type AnthropicDelta struct {
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Signature   string `json:"signature,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// AnthropicModels is a /v1/models listing.
type AnthropicModels struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}
