package proxy

import "regexp"

const compatSamplingOmitted = "omit"

var compatSamplingFields = []string{"temperature", "top_p", "top_k"}

// 推理模型拒绝采样参数：Claude 的 "`temperature` is deprecated for this model."（magpie anthropic.go
// samplingDeprecated 记录），OpenAI 推理模型的 "Unsupported value: 'temperature' does not support 0.7 with this model."，
// 以及校验器的 "temperature: Extra inputs are not permitted"。这类模型通常三个采样参数都不接受，
// 一次去掉三者，避免逐个参数各吃一次 400；去掉后模型按默认采样。
var samplingRefused = regexp.MustCompile("(?i)\\b(?:temperature|top_p|top_k)['\"`]?\\W{0,4}(?:is (?:deprecated|not supported|unsupported)\\b[^\\n]{0,60}|does not support\\b[^\\n]{0,60}|extra inputs are not permitted)|unsupported (?:parameter|value)\\W{0,4}(?:temperature|top_p|top_k)\\b")

var samplingRule = compatRule{
	kind:      capabilitySampling,
	protocols: []compatProtocol{compatProtocolChat, compatProtocolResponses, compatProtocolAnthropic},
	recognize: func(_ compatTarget, refusal compatRefusal, body []byte) (string, string, bool) {
		if !compatHasSampling(body) {
			return "", "", false
		}
		evidence, ok := compatMatch(samplingRefused, refusal.text)
		if !ok {
			return "", "", false
		}
		return compatSamplingOmitted, evidence, true
	},
	apply: func(_ compatTarget, body []byte, _ string) ([]byte, bool, error) {
		return compatWithoutSampling(body)
	},
}

func compatHasSampling(body []byte) bool {
	root, err := decodeCompatRoot(body)
	if err != nil {
		return false
	}
	for _, field := range compatSamplingFields {
		if _, ok := root[field]; ok {
			return true
		}
	}
	return false
}

func compatWithoutSampling(body []byte) ([]byte, bool, error) {
	root, err := decodeCompatRoot(body)
	if err != nil {
		return nil, false, err
	}
	changed := false
	for _, field := range compatSamplingFields {
		if _, ok := root[field]; ok {
			delete(root, field)
			changed = true
		}
	}
	if !changed {
		return body, false, nil
	}
	return encodeCompatRoot(root)
}
