package server

import (
	"context"
	"time"

	"github.com/aseem/viewport-rds/internal/logger"
	"github.com/coder/websocket"
)

const (
	writeBufferSize = 16
	writeTimeout    = 5 * time.Second
	pingInterval    = 10 * time.Second
)

// Client represents a connected WebSocket viewer.
type Client struct {
	conn *websocket.Conn
	send chan []byte
	log  *logger.Logger
}

func newClient(conn *websocket.Conn, log *logger.Logger) *Client {
	c := &Client{
		conn: conn,
		send: make(chan []byte, writeBufferSize),
		log:  log,
	}
	go c.writePump()
	return c
}

// Send queues a message for the client. Drops the frame if the buffer is full.
func (c *Client) Send(msg []byte) {
	select {
	case c.send <- msg:
	default:
		// Drop frame: client can't keep up
	}
}

// ReadLoop blocks reading messages from the client until disconnect.
func (c *Client) ReadLoop(ctx context.Context) {
	defer c.conn.CloseNow()
	for {
		_, _, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	defer c.conn.CloseNow()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.conn.Write(ctx, websocket.MessageBinary, msg)
			cancel()
			if err != nil {
				return
			}
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
