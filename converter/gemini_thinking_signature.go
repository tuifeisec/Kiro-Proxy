package converter

// Gemini 3 在每一步的第一个 functionCall 上签发 thoughtSignature，下一次请求
// 缺少它会被拒绝（magpie 记录的真实报错："Function call is missing a
// thought_signature in functionCall parts"）。签名按工具轮保存在
// ReasoningTrace.Signature，回放时写回该轮第一个 functionCall。
//
// 没有已保存签名的历史工具轮（功能上线前的会话、切换过模型或中转、上游未签发）
// 按 Google 文档对"非本模型产生的历史"的约定，在每个 functionCall 上写
// skip_thought_signature_validator，与 magpie codeassist.go 的处理一致。
// 该值随请求体进入请求详情日志，可观测。

const (
	geminiReasoningProtocol = "gemini"
	geminiSkipSignature     = "skip_thought_signature_validator"
)

// geminiCallSignatures 返回一个工具轮内每个 functionCall 应携带的签名，
// 下标与 ParsedHistoryEntry.ToolUses 一一对应。
func geminiCallSignatures(trace *ReasoningTrace, callCount int) []string {
	signatures := make([]string, callCount)
	if callCount == 0 {
		return signatures
	}
	if trace != nil && trace.Protocol == geminiReasoningProtocol && trace.Signature != "" {
		// 同一步只有第一个调用带签名，其余保持为空，与上游返回的形态一致。
		signatures[0] = trace.Signature
		return signatures
	}
	for i := range signatures {
		signatures[i] = geminiSkipSignature
	}
	return signatures
}
