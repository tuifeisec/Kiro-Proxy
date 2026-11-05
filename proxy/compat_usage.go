package proxy

import "strings"

// appendCompatDispatchReason 把兼容层的决定追加到 dispatch reason，保留已有的路由/分组选择原因，
// 使请求详情中能看到本次实际发送的形态是否被改写及原因。
func appendCompatDispatchReason(usage *requestUsageRecorder, note string) {
	note = strings.TrimSpace(note)
	if usage == nil || note == "" {
		return
	}
	usage.itemMu.Lock()
	if usage.item.DispatchReason == "" {
		usage.item.DispatchReason = note
	} else {
		usage.item.DispatchReason += ";" + note
	}
	usage.itemMu.Unlock()
}

// setUsageWireReasoningEffort 只更新实际下发档位；requested 与 source 仍是档位决策的结果，
// 兼容改写不改变用户请求了什么、档位来自哪一层。
func setUsageWireReasoningEffort(usage *requestUsageRecorder, wire string) {
	if usage == nil {
		return
	}
	usage.itemMu.Lock()
	usage.item.ReasoningEffort = wire
	usage.itemMu.Unlock()
}
