package converter

// ReasoningContentFrame 构建思考文本增量帧，payload: {"text":"<thinking>"}。
//
// Kiro 按到达顺序把同一推理阶段的 text 拼接后展示（与官方 reasoningContentEvent 一致）。
// 这里刻意不带 signature：Kiro 只把带签名的推理写回历史，不带签名时只展示、不回传，
// 历史不会膨胀；工具轮推理回放由代理自己的 ReasoningTrace 负责，与这条展示通道无关。
func ReasoningContentFrame(text string) []byte {
	if text == "" {
		return nil
	}
	buf := make([]byte, 0, 16+len(text)*2)
	buf = append(buf, `{"text":`...)
	buf = appendJSONString(buf, text)
	buf = append(buf, '}')
	return buildFrameFast(getCachedHeaders("reasoningContentEvent"), buf)
}
