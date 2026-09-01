// Package wire holds the three request/response dialects and the translation
// between them. Gemini's shape is the pivot: it is what the CLI speaks, so
// every other dialect is translated to and from it.
package wire

import "encoding/json"

// ---------- Gemini (generativelanguage) ----------

// GeminiRequest is a GenerateContentRequest.
type GeminiRequest struct {
	Contents          []GeminiContent   `json:"contents"`
	SystemInstruction *GeminiContent    `json:"systemInstruction,omitempty"`
	Tools             []GeminiTool      `json:"tools,omitempty"`
	ToolConfig        *GeminiToolConfig `json:"toolConfig,omitempty"`
	GenerationConfig  *GeminiGenConfig  `json:"generationConfig,omitempty"`
	SafetySettings    json.RawMessage   `json:"safetySettings,omitempty"`
	CachedContent     string            `json:"cachedContent,omitempty"`
}

// GeminiContent is one turn.
type GeminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts,omitempty"`
}

// GeminiPart is one piece of a turn. Exactly one field is set.
type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	InlineData       *GeminiBlob             `json:"inlineData,omitempty"`
	FileData         *GeminiFileData         `json:"fileData,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
}

// GeminiBlob is an inline attachment.
type GeminiBlob struct {
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

// GeminiFileData is an attachment by reference.
type GeminiFileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri,omitempty"`
}

// GeminiFunctionCall is a tool call the model made.
type GeminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
	ID   string          `json:"id,omitempty"`
}

// GeminiFunctionResponse is the result handed back for a call.
type GeminiFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
	ID       string          `json:"id,omitempty"`
}

// GeminiTool is a group of declarations.
type GeminiTool struct {
	FunctionDeclarations []GeminiFunctionDecl `json:"functionDeclarations,omitempty"`
	GoogleSearch         json.RawMessage      `json:"googleSearch,omitempty"`
	CodeExecution        json.RawMessage      `json:"codeExecution,omitempty"`
}

// GeminiFunctionDecl declares one callable tool.
type GeminiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// GeminiToolConfig carries the function-calling mode.
type GeminiToolConfig struct {
	FunctionCallingConfig *GeminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

// GeminiFunctionCallingConfig is AUTO, ANY, NONE or a name allowlist.
type GeminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// GeminiGenConfig is the sampling and output configuration.
type GeminiGenConfig struct {
	Temperature      *float64              `json:"temperature,omitempty"`
	TopP             *float64              `json:"topP,omitempty"`
	TopK             *int                  `json:"topK,omitempty"`
	CandidateCount   *int                  `json:"candidateCount,omitempty"`
	MaxOutputTokens  *int                  `json:"maxOutputTokens,omitempty"`
	StopSequences    []string              `json:"stopSequences,omitempty"`
	ResponseMimeType string                `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage       `json:"responseSchema,omitempty"`
	ThinkingConfig   *GeminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

// GeminiThinkingConfig is the reasoning budget.
type GeminiThinkingConfig struct {
	ThinkingBudget  *int `json:"thinkingBudget,omitempty"`
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
}

// GeminiResponse is a GenerateContentResponse, whole or one stream chunk.
type GeminiResponse struct {
	Candidates     []GeminiCandidate `json:"candidates,omitempty"`
	UsageMetadata  *GeminiUsage      `json:"usageMetadata,omitempty"`
	ModelVersion   string            `json:"modelVersion,omitempty"`
	ResponseID     string            `json:"responseId,omitempty"`
	PromptFeedback json.RawMessage   `json:"promptFeedback,omitempty"`
}

// GeminiCandidate is one completion.
type GeminiCandidate struct {
	Content      GeminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
	Index        int           `json:"index"`
}

// GeminiUsage is the token accounting.
type GeminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount int `json:"candidatesTokenCount,omitempty"`
	ThoughtsTokenCount   int `json:"thoughtsTokenCount,omitempty"`
	TotalTokenCount      int `json:"totalTokenCount,omitempty"`
}

// GeminiError is the error envelope every Google API returns.
type GeminiError struct {
	Error GeminiErrorBody `json:"error"`
}

// GeminiErrorBody is the body of that envelope.
type GeminiErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status,omitempty"`
}

// Gemini finish reasons this package emits.
const (
	FinishStop      = "STOP"
	FinishMaxTokens = "MAX_TOKENS"
	FinishSafety    = "SAFETY"
	FinishOther     = "OTHER"
)

// Gemini roles.
const (
	RoleUser  = "user"
	RoleModel = "model"
)
