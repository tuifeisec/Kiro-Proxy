package converter

import "strings"

// MiniMax 的 Chat API 与未开启 reasoning parser 的 vLLM/SGLang 把思考写在正文开头的
// <think>…</think> 中。thinkTagSplitter 只处理正文开头的这一段：标签可能跨 chunk，
// 因此在能判定之前缓冲；正文已经开始或流有独立推理字段后，<think> 是模型的原话，不再拆分。
const (
	thinkOpenTag  = "<think>"
	thinkCloseTag = "</think>"
	// thinkLeadSpace 是 <think> 前允许的空白：部分中转会在开头写 "\r\n"。
	thinkLeadSpace = " \t\r\n"
	// ThinkTagWireField 标记推理文本来自正文 <think> 标签，而不是独立推理字段，
	// 回放层据此决定发回形态（例如重新包回正文）。
	ThinkTagWireField = "think_tag"
)

type thinkSplitState int

const (
	thinkUndecided   thinkSplitState = iota // 还不能判定正文是否以 <think> 开头
	thinkInside                             // 在 <think> 块内
	thinkAfterClose                         // 刚遇到 </think>，跳过紧随的换行
	thinkPassthrough                        // 不再拆分
)

// thinkSplit 是一次输入产生的输出；同一次输入里推理总在正文之前。
type thinkSplit struct {
	Reasoning string
	Content   string
	Unclosed  bool // 流结束时 <think> 仍未闭合，Reasoning 是已缓冲的思考
}

type thinkTagSplitter struct {
	state   thinkSplitState
	pending string // Undecided 时缓冲的开头；Inside 时可能是 </think> 前缀的尾部
}

// markIndependentReasoning 在流出现独立推理字段时调用：尚未进入 <think> 块就不再拆分。
func (s *thinkTagSplitter) markIndependentReasoning() thinkSplit {
	if s.state != thinkUndecided {
		return thinkSplit{}
	}
	out := thinkSplit{Content: s.pending}
	s.pending, s.state = "", thinkPassthrough
	return out
}

func (s *thinkTagSplitter) feed(text string) thinkSplit {
	switch s.state {
	case thinkPassthrough:
		return thinkSplit{Content: text}
	case thinkUndecided:
		s.pending += text
		lead := strings.TrimLeft(s.pending, thinkLeadSpace)
		if len(lead) < len(thinkOpenTag) && strings.HasPrefix(thinkOpenTag, lead) {
			return thinkSplit{}
		}
		if !strings.HasPrefix(lead, thinkOpenTag) {
			out := thinkSplit{Content: s.pending}
			s.pending, s.state = "", thinkPassthrough
			return out
		}
		rest := lead[len(thinkOpenTag):]
		s.pending, s.state = "", thinkInside
		return s.feed(rest)
	case thinkInside:
		buffered := s.pending + text
		if i := strings.Index(buffered, thinkCloseTag); i >= 0 {
			s.pending, s.state = "", thinkAfterClose
			out := s.feed(buffered[i+len(thinkCloseTag):])
			out.Reasoning = buffered[:i]
			return out
		}
		keep := partialSuffixLen(buffered, thinkCloseTag)
		s.pending = buffered[len(buffered)-keep:]
		return thinkSplit{Reasoning: buffered[:len(buffered)-keep]}
	default: // thinkAfterClose
		text = strings.TrimLeft(text, "\r\n")
		if text == "" {
			return thinkSplit{}
		}
		s.state = thinkPassthrough
		return thinkSplit{Content: text}
	}
}

// finish 在正文结束（工具调用开始、finish_reason 或流完成）时调用，交还缓冲内容。
func (s *thinkTagSplitter) finish() thinkSplit {
	var out thinkSplit
	switch s.state {
	case thinkUndecided:
		out.Content = s.pending
	case thinkInside:
		out.Reasoning, out.Unclosed = s.pending, true
	}
	s.pending, s.state = "", thinkPassthrough
	return out
}

// partialSuffixLen 返回 s 末尾可能是 tag 开头的最长长度，供跨 chunk 继续匹配。
func partialSuffixLen(s, tag string) int {
	for n := min(len(s), len(tag)-1); n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}
