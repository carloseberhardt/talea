package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4096
)

// wsEvent is the JSON envelope sent to browser clients over WebSocket.
type wsEvent struct {
	Type       string  `json:"type"`
	Signal     *Signal `json:"signal,omitempty"`
	ThreadRoot string  `json:"threadRoot,omitempty"`
}

// Client is a single connected browser.
type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte
	logger *slog.Logger
}

// Hub maintains the set of active clients and broadcasts messages.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	logger     *slog.Logger
}

// NewHub creates an idle Hub; call Run() in a goroutine to start it.
func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		logger:     logger,
	}
}

// Run is the Hub's event loop. It must run in its own goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			h.logger.Debug("ws client connected", "total", len(h.clients))

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				h.logger.Debug("ws client disconnected", "total", len(h.clients))
			}

		case msg := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- msg:
				default:
					// Slow client — drop and disconnect.
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

// Broadcast enqueues a raw JSON message for all connected clients.
// Non-blocking: drops if the broadcast channel is full.
func (h *Hub) Broadcast(msg []byte) {
	select {
	case h.broadcast <- msg:
	default:
		h.logger.Warn("hub broadcast channel full, dropping message")
	}
}

// BroadcastSignal serialises a Signal into a wsEvent and broadcasts it.
func (h *Hub) BroadcastSignal(sig *Signal) {
	evt := wsEvent{
		Type:       "signal",
		Signal:     sig,
		ThreadRoot: sig.RootURI,
	}
	b, err := json.Marshal(evt)
	if err != nil {
		h.logger.Warn("failed to marshal ws event", "error", err)
		return
	}
	h.Broadcast(b)
}

// serveWS upgrades the HTTP connection to a WebSocket and registers the client.
func serveWS(hub *Hub, logger *slog.Logger, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Warn("ws upgrade failed", "error", err)
		return
	}
	client := &Client{
		hub:    hub,
		conn:   conn,
		send:   make(chan []byte, 256),
		logger: logger,
	}
	hub.register <- client
	go client.writePump()
	go client.readPump()
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		// We only read to detect disconnects; clients don't send data.
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}
