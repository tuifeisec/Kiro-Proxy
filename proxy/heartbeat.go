// heartbeat.go
// 心跳接口仅用于 Web UI 状态感知/诊断。
// 进程生命周期由桌面壳窗口关闭或系统信号控制，不能再用 WS/heartbeat 超时推断窗口关闭，
// 否则浏览器/WebView 刷新、休眠、网络栈抖动等都会误杀代理服务。

package proxy

import (
	"net/http"
	"sync/atomic"
	"time"
)

var lastHeartbeat atomic.Int64

func init() {
	lastHeartbeat.Store(time.Now().Unix())
}

// SecondsSinceLastHeartbeat 返回距离上次 HTTP 心跳的秒数
func SecondsSinceLastHeartbeat() int64 {
	return time.Now().Unix() - lastHeartbeat.Load()
}

// WindowClosed 判断窗口是否已关闭
// 条件：当前无 WebSocket 连接，且距离最后一次心跳/断开超过 grace 秒
func (s *Server) WindowClosed(graceSeconds int64) bool {
	// 有活跃 WS 连接 → 窗口肯定开着
	if s.hub.ClientCount() > 0 {
		return false
	}
	// 无 WS 连接，看断开多久了 + HTTP 心跳多久没来
	wsSilent := s.hub.SecondsSinceLastClient()
	httpSilent := SecondsSinceLastHeartbeat()
	// 两个信号都超过 grace 才判定关闭
	return wsSilent >= graceSeconds && httpSilent >= graceSeconds
}

// handleHeartbeat 处理前端 HTTP 心跳请求
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	lastHeartbeat.Store(time.Now().Unix())
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}
