package wire

import "encoding/json"

// ---------- OpenAI (chat completions) ----------

// OpenAIRequest is a /chat/completions body.
type OpenAIRequest struct {
	Model               string                `json:"model"`
	Messages            []OpenAIMessage       `json:"messages"`
	Tools               []OpenAITool          `json:"tools,omitempty"`
	ToolChoice          any                   `json:"tool_choice,omitempty"`
	Stream              bool                  `json:"stream,omitempty"`
	StreamOptions       *OpenAIStreamOptions  `json:"stream_options,omitempty"`
	Temperature         *float64              `json:"temperature,omitempty"`
	TopP                *float64              `json:"top_p,omitempty"`
	MaxTokens           *int                  `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                  `json:"max_completion_tokens,omitempty"`
	Stop                []string              `json:"stop,omitempty"`
	ResponseFormat      *OpenAIResponseFormat `json:"response_format,omitempty"`
	ReasoningEffort     string                `json:"reasoning_effort,omitempty"`
}

// OpenAIStreamOptions asks for the usage record at the end of a stream.
type OpenAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// OpenAIResponseFormat carries JSON mode and structured output.
type OpenAIResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}

// OpenAIMessage is one message. Content is a string or a part array.
type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// OpenAIPart is one piece of a multimodal message.
type OpenAIPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *OpenAIImageURL `json:"image_url,omitempty"`
}

// OpenAIImageURL is an attachment, inline as a data URL or by link.
type OpenAIImageURL struct {
	URL string `json:"url"`
}

// OpenAIToolCall is a call the model made.
type OpenAIToolCall struct {
	Index    *int               `json:"index,omitempty"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function OpenAIToolFunction `json:"function"`
}

// OpenAIToolFunction is the name and the argument JSON, as a string.
type OpenAIToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// OpenAITool declares a callable tool.
type OpenAITool struct {
	Type     string           `json:"type"`
	Function OpenAIToolSchema `json:"function"`
}

// OpenAIToolSchema is the declaration body.
type OpenAIToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// OpenAIResponse is a whole completion or one stream chunk.
type OpenAIResponse struct {
	ID      string         `json:"id,omitempty"`
	Model   string         `json:"model,omitempty"`
	Choices []OpenAIChoice `json:"choices,omitempty"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
	Error   *OpenAIError   `json:"error,omitempty"`
}

// OpenAIChoice is one completion, whole (Message) or partial (Delta).
type OpenAIChoice struct {
	Index        int             `json:"index"`
	Message      *OpenAIDelta    `json:"message,omitempty"`
	Delta        *OpenAIDelta    `json:"delta,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

// OpenAIDelta covers both a whole message and a streamed fragment of one.
// ReasoningContent and Reasoning are the two spellings gateways use for
// thinking text; neither is in OpenAI's own schema.
type OpenAIDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          any              `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	Reasoning        string           `json:"reasoning,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
}

// Text renders the delta's content, which is a string on every real gateway but
// occasionally a part array.
func (d *OpenAIDelta) Text() string {
	if d == nil {
		return ""
	}
	switch c := d.Content.(type) {
	case string:
		return c
	case []any:
		var out string
		for _, item := range c {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := m["text"].(string); ok {
				out += s
			}
		}
		return out
	}
	return ""
}

// Thinking renders whichever reasoning field the gateway used.
func (d *OpenAIDelta) Thinking() string {
	if d == nil {
		return ""
	}
	if d.ReasoningContent != "" {
		return d.ReasoningContent
	}
	return d.Reasoning
}

// OpenAIUsage is the token accounting.
type OpenAIUsage struct {
	PromptTokens     int                 `json:"prompt_tokens,omitempty"`
	CompletionTokens int                 `json:"completion_tokens,omitempty"`
	TotalTokens      int                 `json:"total_tokens,omitempty"`
	Details          *OpenAIUsageDetails `json:"completion_tokens_details,omitempty"`
}

// OpenAIUsageDetails carries the reasoning token count when a host reports one.
type OpenAIUsageDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// OpenAIError is the error envelope.
type OpenAIError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    any    `json:"code,omitempty"`
}

// OpenAIModels is a /models listing.
type OpenAIModels struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}
