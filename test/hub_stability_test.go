package test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"kiro-proxy/proxy"
)

func TestHubConcurrentBroadcastUnregisterAndClose(t *testing.T) {
	hub := proxy.NewHub()
	registered := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		hub.Register(conn)
		registered <- conn
	}))
	defer httpServer.Close()

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	serverConn := <-registered

	hub.Broadcast([]byte("ready"))
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_, message, err := client.ReadMessage()
	if err != nil || string(message) != "ready" {
		t.Fatalf("initial broadcast failed: message=%q err=%v", message, err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				hub.Broadcast([]byte("message"))
			}
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		hub.Unregister(serverConn)
	}()
	go func() {
		defer wg.Done()
		hub.Close()
	}()
	wg.Wait()
	if hub.ClientCount() != 0 {
		t.Fatalf("hub still has %d clients after close", hub.ClientCount())
	}
}
