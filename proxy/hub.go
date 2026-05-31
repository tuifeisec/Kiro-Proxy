package proxy

import (
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Hub 管理所有 WebSocket 连接，负责广播消息
type Hub struct {
	mu       sync.RWMutex
	clients  map[*websocket.Conn]struct{}
	writeMu  sync.Mutex // 串行化 WriteMessage，避免并发写同一连接 panic
	lastSeen time.Time  // 最后一次有客户端连接的时间
}

func NewHub() *Hub {
	return &Hub{
		clients:  make(map[*websocket.Conn]struct{}),
		lastSeen: time.Now(),
	}
}

func (h *Hub) Register(conn *websocket.Conn) {
	h.mu.Lock()
	h.clients[conn] = struct{}{}
	h.lastSeen = time.Now()
	h.mu.Unlock()
	log.Printf("[WS] 客户端连接，当前连接数: %d", h.count())
}

func (h *Hub) Unregister(conn *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, conn)
	if len(h.clients) == 0 {
		h.lastSeen = time.Now()
	}
	h.mu.Unlock()
	log.Printf("[WS] 客户端断开，当前连接数: %d", h.count())
}

func (h *Hub) Broadcast(data []byte) {
	// 先拿连接快照，避免长时间持锁
	h.mu.RLock()
	conns := make([]*websocket.Conn, 0, len(h.clients))
	for conn := range h.clients {
		conns = append(conns, conn)
	}
	h.mu.RUnlock()

	if len(conns) == 0 {
		return
	}

	// 串行化写，gorilla websocket 不允许并发写同一连接
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	for _, conn := range conns {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			// 写失败的连接移除
			h.Unregister(conn)
			conn.Close()
		}
	}
}

// ClientCount 返回当前连接数
func (h *Hub) ClientCount() int {
	return h.count()
}

// SecondsSinceLastClient 返回距离最后一个客户端断开的秒数
// 如果还有客户端连接，返回 0
func (h *Hub) SecondsSinceLastClient() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.clients) > 0 {
		return 0
	}
	return int64(time.Since(h.lastSeen).Seconds())
}

func (h *Hub) count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
