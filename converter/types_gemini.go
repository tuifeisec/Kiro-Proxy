package converter

// ── Gemini API 格式 ──────────────────────────────────────────────

type GeminiRequest struct {
	SystemInstruction *GeminiContent   `json:"systemInstruction,omitempty"`
	Contents          []GeminiContent  `json:"contents"`
	Tools             []GeminiTool     `json:"tools,omitempty"`
	GenerationConfig  *GeminiGenConfig `json:"generationConfig,omitempty"`
}

type GeminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text             string                `json:"text,omitempty"`
	FunctionCall     *GeminiFunctionCall   `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResp   `json:"functionResponse,omitempty"`
	InlineData       *GeminiInlineData     `json:"inlineData,omitempty"`
}

type GeminiFunctionCall struct {
	Name string      `json:"name"`
	Args interface{} `json:"args"`
}

type GeminiFunctionResp struct {
	Name     string      `json:"name"`
	Response interface{} `json:"response"`
}

type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type GeminiTool struct {
	FunctionDeclarations []GeminiFuncDecl `json:"functionDeclarations"`
}

type GeminiFuncDecl struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

type GeminiGenConfig struct {
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	Temperature     *float64              `json:"temperature,omitempty"`
	TopP            *float64              `json:"topP,omitempty"`
	ThinkingConfig  *GeminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type GeminiThinkingConfig struct {
	ThinkingBudget int `json:"thinkingBudget,omitempty"`
}
