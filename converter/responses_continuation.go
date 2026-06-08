// responses_continuation.go
// OpenAI Responses API previous_response_id 续传机制
//
// 原理：
//   OpenAI Responses API 支持通过 previous_response_id 续传对话。
//   当设置了该字段后，只需发送"增量"的 input（新增的 tool result + user message），
//   上游会自动将 previous response 的上下文拼接上来。
//
//   这样做的好处：
//   1. 请求体大幅缩小（从 300KB → 几 KB）
//   2. 上游不需要重新 prefix match，缓存命中率接近 100%
//   3. 减少网络传输和序列化开销
//
// 流程：
//   1. 首次请求：正常发送全量 input → 保存 (responseID, inputJSON, itemCount)
//   2. 后续请求：对比前次 input 是否是当前 input 的前缀
//      - 是：设置 previous_response_id，只发送 delta 部分
//      - 否：全量发送（system prompt 变了等情况）
//   3. 上游返回 400 时：回退到全量发送，清理存储

package converter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"

	json "github.com/goccy/go-json"
)

// ResponsesContinuationState 保存续传所需的状态（由调用方管理存储）
type ResponsesContinuationState struct {
	ConversationID string
	ResponseID     string // 上一次的 response_id
	InputJSON      string // 上一次的完整 input JSON
	ItemCount      int    // 上一次 input 的 item 数量
}

// ResponsesContinuationResult 续传处理结果
type ResponsesContinuationResult struct {
	Body             []byte // 最终请求体（可能带 previous_response_id + delta input）
	UsedContinuation bool   // 是否使用了续传
	FullInputJSON    string // 完整 input JSON（用于后续存储）
	InputItemCount   int    // 完整 input 的 item 数量
	FallbackBody     []byte // 续传被拒时的回退全量 body（等于原始 body）
}

// responsesRequestForContinuation 用于解析/修改 Responses 请求
type responsesRequestForContinuation struct {
	Model              string            `json:"model"`
	Instructions       string            `json:"instructions,omitempty"`
	Input              []json.RawMessage `json:"input"`
	Tools              []ReqTool         `json:"tools,omitempty"`
	Reasoning          *OpenAIReasoning  `json:"reasoning,omitempty"`
	Stream             bool              `json:"stream"`
	Store              bool              `json:"store"`
	PromptCacheKey     string            `json:"prompt_cache_key,omitempty"`
	PreviousResponseID string            `json:"previous_response_id,omitempty"`
}

// ApplyResponsesContinuation 尝试对 Responses API 请求应用续传优化
//
// 如果 prevState 为 nil 或 ResponseID 为空，直接返回原始 body（首次请求）。
// 如果前次 input 是当前 input 的严格前缀，则：
//   - 设置 previous_response_id
//   - input 只保留 delta 部分（新增的 items）
func ApplyResponsesContinuation(body []byte, prevState *ResponsesContinuationState) (*ResponsesContinuationResult, error) {
	// 解析当前请求
	var req responsesRequestForContinuation
	if err := json.Unmarshal(body, &req); err != nil {
		return &ResponsesContinuationResult{Body: body, FullInputJSON: "", InputItemCount: 0}, nil
	}

	// 记录完整 input（用于后续存储对比）
	fullInputJSON, _ := json.Marshal(req.Input)
	fullItemCount := len(req.Input)

	result := &ResponsesContinuationResult{
		FullInputJSON:  string(fullInputJSON),
		InputItemCount: fullItemCount,
		FallbackBody:   body,
	}

	// 无前次状态 → 首次请求，全量发送
	if prevState == nil || prevState.ResponseID == "" || prevState.InputJSON == "" {
		result.Body = body
		return result, nil
	}

	// 解析前次 input
	var prevInput []json.RawMessage
	if err := json.Unmarshal([]byte(prevState.InputJSON), &prevInput); err != nil {
		result.Body = body
		return result, nil
	}

	// 检查前次 input 是否是当前 input 的严格前缀
	if len(req.Input) <= len(prevInput) {
		// 当前 input 没有增长（或更短了），不能续传
		result.Body = body
		return result, nil
	}

	// 逐项比对前缀。使用 bytes.Equal 避免把每个 RawMessage 临时转成 string。
	for i := range prevInput {
		if !bytes.Equal(prevInput[i], req.Input[i]) {
			// 前缀不匹配，全量发送
			result.Body = body
			return result, nil
		}
	}

	// 前缀匹配！构建续传请求
	delta := req.Input[len(prevInput):]
	req.PreviousResponseID = prevState.ResponseID
	req.Input = delta

	continuationBody, err := json.Marshal(req)
	if err != nil {
		result.Body = body
		return result, nil
	}

	result.Body = continuationBody
	result.UsedContinuation = true
	return result, nil
}

// HashInput 计算 input JSON 的短 hash（用于快速比对是否可能匹配）
func HashInput(inputJSON string) string {
	if inputJSON == "" {
		return ""
	}
	h := sha256.Sum256([]byte(inputJSON))
	return hex.EncodeToString(h[:8]) // 16 chars 够用
}

// RebuildResponsesWithoutContinuation 当续传被上游拒绝时，用完整 input 重建请求
func RebuildResponsesWithoutContinuation(continuationBody []byte, fullInputJSON string) ([]byte, error) {
	var req responsesRequestForContinuation
	if err := json.Unmarshal(continuationBody, &req); err != nil {
		return nil, err
	}

	// 恢复完整 input
	var fullInput []json.RawMessage
	if err := json.Unmarshal([]byte(fullInputJSON), &fullInput); err != nil {
		return nil, err
	}

	req.PreviousResponseID = ""
	req.Input = fullInput
	return json.Marshal(req)
}
