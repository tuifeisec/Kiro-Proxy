package proxy

import (
	"log"
	"strings"
)

// reasoningDiagnosticLog 写入进程日志（runner 同时落盘）；测试可替换以断言。
var reasoningDiagnosticLog = log.Printf

// reasoningSkipCounts 统计一次请求中无法交给目标上游的 reasoning item，只记录
// 数量，不记录推理正文或密文。
type reasoningSkipCounts struct {
	Foreign    int // 其他上游产生、目标上游无法验证的推理
	Unreadable int // 带本项目前缀但内容已损坏
	Orphaned   int // 后面没有可附着的 assistant 轮
}

// reasoningSkip 是单个 reasoning item 的转换结论。
type reasoningSkip int

const (
	reasoningKept reasoningSkip = iota
	reasoningSkipForeign
	reasoningSkipUnreadable
)

func (c *reasoningSkipCounts) add(skip reasoningSkip) {
	switch skip {
	case reasoningSkipForeign:
		c.Foreign++
	case reasoningSkipUnreadable:
		c.Unreadable++
	}
}

func (c reasoningSkipCounts) log(target, modelID string) {
	if c.Foreign+c.Unreadable+c.Orphaned == 0 {
		return
	}
	reasoningDiagnosticLog("[responses-reasoning] 跳过无法发往 %s 的 reasoning item: model=%s foreign=%d unreadable=%d orphaned=%d",
		target, strings.TrimSpace(modelID), c.Foreign, c.Unreadable, c.Orphaned)
}
