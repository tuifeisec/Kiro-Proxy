package converter

import json "github.com/goccy/go-json"

// ── Kiro 请求结构 ────────────────────────────────────────────────

// KiroRequest 是 Kiro generateAssistantResponse 的完整请求体
type KiroRequest struct {
	ConversationState            KiroConversationState             `json:"conversationState"`
	AgentMode                    string                            `json:"agentMode,omitempty"`
	ProfileArn                   string                            `json:"profileArn,omitempty"`
	InferenceConfig              *KiroInferenceConfig              `json:"inferenceConfig,omitempty"`
	AdditionalModelRequestFields *KiroAdditionalModelRequestFields `json:"additionalModelRequestFields,omitempty"`
}

type KiroAdditionalModelRequestFields struct {
	ReasoningEffort string                     `json:"reasoningEffort,omitempty"`
	Reasoning       *KiroReasoningRequestField `json:"reasoning,omitempty"`
}

type KiroReasoningRequestField struct {
	Effort string `json:"effort,omitempty"`
}

type KiroConversationState struct {
	ChatTriggerType     string           `json:"chatTriggerType"`
	ConversationID      string           `json:"conversationId"`
	AgentContinuationID string           `json:"agentContinuationId,omitempty"`
	AgentTaskType       string           `json:"agentTaskType,omitempty"`
	CurrentMessage      KiroCurrentMsg   `json:"currentMessage"`
	History             []KiroHistoryMsg `json:"history,omitempty"`
}

type KiroCurrentMsg struct {
	UserInputMessage KiroUserInputMsg `json:"userInputMessage"`
}

type KiroUserInputMsg struct {
	Content                 string          `json:"content"`
	ModelID                 string          `json:"modelId,omitempty"`
	Origin                  string          `json:"origin,omitempty"`
	UserInputMessageContext *KiroMsgContext `json:"userInputMessageContext,omitempty"`
	Images                  []KiroImage     `json:"images,omitempty"`
}

type KiroMsgContext struct {
	Tools       []json.RawMessage `json:"tools,omitempty"`
	ToolResults []KiroToolResult  `json:"toolResults,omitempty"`
}

type KiroHistoryMsg struct {
	UserInputMessage     *KiroUserInputMsg `json:"userInputMessage,omitempty"`
	AssistantResponseMsg *KiroAssistantMsg `json:"assistantResponseMessage,omitempty"`
}

type KiroAssistantMsg struct {
	Content  string        `json:"content"`
	ToolUses []KiroToolUse `json:"toolUses,omitempty"`
}

type KiroToolUse struct {
	ToolUseID string      `json:"toolUseId"`
	Name      string      `json:"name"`
	Input     interface{} `json:"input"`
}

type KiroToolResult struct {
	ToolUseID string            `json:"toolUseId"`
	Content   []KiroToolContent `json:"content"`
	Status    string            `json:"status"` // "success" | "error"
}

type KiroToolContent struct {
	Text string          `json:"text,omitempty"`
	Raw  json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps the original block so provider adapters can preserve
// non-text tool output without changing the existing text fast path.
func (c *KiroToolContent) UnmarshalJSON(data []byte) error {
	c.Raw = append(c.Raw[:0], data...)
	c.Text = ""

	var value struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &value); err == nil {
		c.Text = value.Text
	}
	return nil
}

type KiroImage struct {
	Format string          `json:"format"` // png, jpeg, gif, webp
	Source KiroImageSource `json:"source"`
}

type KiroImageSource struct {
	Bytes string `json:"bytes"` // base64
}

type KiroInferenceConfig struct {
	MaxTokens       int                  `json:"maxTokens,omitempty"`
	Temperature     *float64             `json:"temperature,omitempty"`
	TopP            *float64             `json:"topP,omitempty"`
	ReasoningConfig *KiroReasoningConfig `json:"reasoningConfig,omitempty"`
}

type KiroReasoningConfig struct {
	Type         string `json:"type"` // "enabled" | "adaptive"
	BudgetTokens int    `json:"budgetTokens,omitempty"`
}

// ── 解析后的结构化会话 ────────────────────────────────────────────

// ParsedConversation 是一次解析后的结构化会话，后续所有转换都基于此
type ParsedConversation struct {
	ModelID                     string
	ConversationID              string
	ProfileArn                  string
	ChatTriggerType             string
	AgentTaskType               string
	AgentMode                   string
	AgentContinuationID         string
	AgentContinuationProvenance string
	WorkflowStage               string
	WorkflowStageSource         string
	WorkflowStageProvided       bool
	WorkflowID                  string
	WorkflowMetadataVersion     int
	WorkflowNodeID              string
	WorkflowDefinitionID        string
	WorkflowDefinitionRevision  string
	WorkflowNodePath            string
	WorkflowTaskType            string
	WorkflowExecutionID         string
	Origin                      string
	PromptAffinityKey           string
	ContinuationStateKey        string
	IsClassifier                bool   // intent-classification 请求
	SystemText                  string // 从 history[0] 提取的系统提示

	// 当前消息
	CurrentContent          string
	CurrentToolResults      []ParsedToolResult
	CurrentImages           []KiroImage
	CurrentToolContinuation *bool
	RawResponsesHasImages   bool

	// 历史消息（已去除系统提示那一轮）
	History []ParsedHistoryEntry

	// 工具定义（原始 JSON，用于缓存）
	RawTools  []json.RawMessage
	ToolsHash string // sha256(tools_json)，用于缓存命中

	// 推理配置
	MaxTokens       int
	Temperature     *float64
	TopP            *float64
	ReasoningEffort string
}

const (
	AgentContinuationProvenanceNativeRaw   = "native_raw"
	AgentContinuationProvenanceProxyBridge = "proxy_bridge"
)

type ParsedHistoryEntry struct {
	// 用户侧
	UserContent string
	ToolResults []ParsedToolResult
	Images      []KiroImage
	IsSystem    bool

	// 助手侧
	HasAssistant     bool
	AssistantContent string
	ToolUses         []ParsedToolUse
	Reasoning        *ReasoningTrace
}

// ReasoningTrace preserves provider-issued thinking metadata outside Kiro's
// visible assistant content so tool-call turns can be replayed losslessly.
type ReasoningTrace struct {
	Present     bool
	Text        string
	Signature   string
	ItemID      string
	Status      string
	WireField   string
	Protocol    string
	ToolCallIDs []string
}

type ParsedToolResult struct {
	ToolUseID string
	Content   string
	Blocks    []ParsedToolResultBlock
	IsError   bool
}

// ParsedToolResultBlock is the provider-neutral representation of one Kiro
// tool-result block. Raw is retained for non-text blocks and unknown fields.
type ParsedToolResultBlock struct {
	Kind string
	Text string
	Raw  json.RawMessage
}

type ParsedToolUse struct {
	ToolUseID string
	Name      string
	Input     json.RawMessage // 保留原始 JSON 字节，避免 unmarshal → marshal 往返开销
}

// ── OpenAI 格式 ──────────────────────────────────────────────────

type OpenAIRequest struct {
	Model           string              `json:"model"`
	Messages        []OpenAIMessage     `json:"messages"`
	Tools           []OpenAITool        `json:"tools,omitempty"`
	Stream          bool                `json:"stream"`
	StreamOptions   *OpenAIStreamOpts   `json:"stream_options,omitempty"`
	MaxTokens       int                 `json:"max_tokens,omitempty"`
	Temperature     *float64            `json:"temperature,omitempty"`
	TopP            *float64            `json:"top_p,omitempty"`
	ReasoningEffort string              `json:"reasoning_effort,omitempty"`
	PromptCacheKey  string              `json:"prompt_cache_key,omitempty"`
	PromptCacheOpts *PromptCacheOptions `json:"prompt_cache_options,omitempty"`
	User            string              `json:"user,omitempty"`
}

type OpenAIReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type OpenAIStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type PromptCacheOptions struct {
	Mode string `json:"mode"`
	TTL  string `json:"ttl"`
}

type PromptCacheBreakpoint struct {
	Mode string `json:"mode"`
}

type OpenAIMessage struct {
	Role             string           `json:"role"`
	Content          interface{}      `json:"content"` // string 或 []OpenAIContentPart
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	Name             string           `json:"name,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ReasoningText    *string          `json:"reasoning_text,omitempty"`
	Reasoning        *string          `json:"reasoning,omitempty"`
}

type OpenAIContentPart struct {
	Type                  string                 `json:"type"`
	Text                  string                 `json:"text,omitempty"`
	ImageURL              *OpenAIImageURL        `json:"image_url,omitempty"`
	PromptCacheBreakpoint *PromptCacheBreakpoint `json:"prompt_cache_breakpoint,omitempty"`
}

type OpenAIImageURL struct {
	URL string `json:"url"`
}

type OpenAIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function OpenAIFuncCall `json:"function"`
}

type OpenAIFuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OpenAITool struct {
	Type     string        `json:"type"`
	Function OpenAIFuncDef `json:"function"`
}

type OpenAIFuncDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// ── Anthropic 格式 ────────────────────────────────────────────────

type AnthropicRequest struct {
	Model        string             `json:"model"`
	Messages     []AnthropicMessage `json:"messages"`
	System       interface{}        `json:"system,omitempty"` // string 或 []AnthropicBlock
	Tools        []AnthropicTool    `json:"tools,omitempty"`
	MaxTokens    int                `json:"max_tokens"`
	Stream       bool               `json:"stream"`
	Thinking     *AnthropicThinking `json:"thinking,omitempty"`
	CacheControl *CacheControl      `json:"cache_control,omitempty"` // 顶层自动缓存（Anthropic 自动将 breakpoint 放在最后一个可缓存 block）
}

type AnthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string 或 []AnthropicBlock
}

type AnthropicBlock struct {
	Type         string                `json:"type"`
	Text         string                `json:"text,omitempty"`
	Thinking     string                `json:"thinking,omitempty"`
	Signature    string                `json:"signature,omitempty"`
	ID           string                `json:"id,omitempty"`
	Name         string                `json:"name,omitempty"`
	Input        interface{}           `json:"input,omitempty"`
	ToolUseID    string                `json:"tool_use_id,omitempty"`
	Content      interface{}           `json:"content,omitempty"`
	IsError      bool                  `json:"is_error,omitempty"`
	Source       *AnthropicImageSource `json:"source,omitempty"`
	CacheControl *CacheControl         `json:"cache_control,omitempty"`
}

type CacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type AnthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type AnthropicTool struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	InputSchema  interface{}   `json:"input_schema"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}
