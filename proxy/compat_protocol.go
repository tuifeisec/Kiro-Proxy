package proxy

import (
	"strings"

	"kiro-proxy/store"
)

// kiroCompatProtocol 返回 Kiro 主路径本次实际使用的上游协议，与转换器的选择保持一致。
func kiroCompatProtocol(pre *Preflight, model *store.Model, forceChat bool) compatProtocol {
	switch strings.ToLower(strings.TrimSpace(model.Provider)) {
	case "anthropic":
		return compatProtocolAnthropic
	case "gemini":
		return compatProtocolGemini
	case "codex":
		return compatProtocolCodex
	}
	if !forceChat && useResponsesAPI(pre, model) {
		return compatProtocolResponses
	}
	return compatProtocolChat
}

func rawResponsesCompatProtocol(model *store.Model) (compatProtocol, error) {
	if strings.EqualFold(strings.TrimSpace(model.Provider), "anthropic") {
		return compatProtocolAnthropic, nil
	}
	_, chat, err := rawResponsesUpstreamURL(model)
	if err != nil {
		return "", err
	}
	if chat {
		return compatProtocolChat, nil
	}
	return compatProtocolResponses, nil
}

// newKiroCompatSession 只为 Kiro 转换后的请求创建兼容会话；SDK 透传请求保持客户端原样，
// 返回 nil，会话的所有方法对 nil 均为空操作。
func (f *Forwarder) newKiroCompatSession(pre *Preflight, model *store.Model, upstreamURL string, logger *TrafficLogger, forceChat bool) *compatSession {
	if f == nil || pre == nil || pre.Protocol != "kiro" || pre.Conv == nil || model == nil {
		return nil
	}
	return newCompatSession(f.caps, model, kiroCompatProtocol(pre, model, forceChat), upstreamURL, logger, pre.usage)
}
