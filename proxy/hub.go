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
	clients  map[*websocket.Conn]*hubClient
	lastSeen time.Time // 最后一次有客户端连接的时间
	closed   bool
	workerWG sync.WaitGroup
}

type hubClient struct {
	hub       *Hub
	conn      *websocket.Conn
	send      chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func NewHub() *Hub {
	return &Hub{
		clients:  make(map[*websocket.Conn]*hubClient),
		lastSeen: time.Now(),
	}
}

func (h *Hub) Register(conn *websocket.Conn) {
	client := &hubClient{hub: h, conn: conn, send: make(chan []byte, 128), done: make(chan struct{})}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		return
	}
	h.clients[conn] = client
	h.workerWG.Add(1)
	h.lastSeen = time.Now()
	h.mu.Unlock()
	log.Printf("[WS] 客户端连接，当前连接数: %d", h.count())
	go client.run()
}

func (h *Hub) Unregister(conn *websocket.Conn) {
	h.mu.Lock()
	client, existed := h.clients[conn]
	if existed {
		delete(h.clients, conn)
	}
	if len(h.clients) == 0 {
		h.lastSeen = time.Now()
	}
	h.mu.Unlock()
	if existed {
		client.close()
		log.Printf("[WS] 客户端断开，当前连接数: %d", h.count())
	}
}

func (h *Hub) Broadcast(data []byte) {
	h.mu.RLock()
	clients := make([]*hubClient, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	for _, client := range clients {
		select {
		case <-client.done:
		case client.send <- data:
		default:
			// 客户端消费速度持续落后时主动断开，避免影响日志 worker。
			h.Unregister(client.conn)
		}
	}
}

func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	clients := make([]*hubClient, 0, len(h.clients))
	for conn, client := range h.clients {
		delete(h.clients, conn)
		clients = append(clients, client)
	}
	h.lastSeen = time.Now()
	h.mu.Unlock()
	for _, client := range clients {
		client.close()
	}
	h.workerWG.Wait()
}

func (c *hubClient) run() {
	defer c.hub.workerWG.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		var messageType int
		var data []byte
		select {
		case data = <-c.send:
			messageType = websocket.TextMessage
		case <-ticker.C:
			messageType = websocket.PingMessage
		case <-c.done:
			return
		}
		_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := c.conn.WriteMessage(messageType, data); err != nil {
			c.hub.Unregister(c.conn)
			return
		}
	}
}

func (c *hubClient) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
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
