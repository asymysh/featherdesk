package server

import (
	"context"
	"fmt"
	"time"

	"github.com/asymysh/featherdesk/internal/logger"
	"github.com/coder/websocket"
)

const (
	writeBufferSize = 16
	writeTimeout    = 5 * time.Second
	pingInterval    = 10 * time.Second
)

// Client represents a connected WebSocket viewer.
type Client struct {
	conn        *websocket.Conn
	send        chan []byte
	log         *logger.Logger
	onText      func([]byte)
	textLimiter *rateLimiter
	textDropped uint64
}

func newClient(conn *websocket.Conn, log *logger.Logger) *Client {
	c := &Client{
		conn: conn,
		send: make(chan []byte, writeBufferSize),
		log:  log,
		// Input messages: high-frequency mousemove tops out well below
		// this; anything beyond is abuse or a client bug.
		textLimiter: newRateLimiter(500, 1000),
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
	c.log.Info("server", "ReadLoop started")
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			c.log.Info("server", "ReadLoop exit: "+err.Error())
			return
		}
		c.log.Debug("server", fmt.Sprintf("ReadLoop msg type=%d len=%d", typ, len(data)))
		if typ == websocket.MessageText && c.onText != nil {
			if !c.textLimiter.allow() {
				c.textDropped++
				if c.textDropped == 1 || c.textDropped%1000 == 0 {
					c.log.Info("server", "input rate limit exceeded, dropping messages")
				}
				continue
			}
			c.onText(data)
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
