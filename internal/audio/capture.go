package audio

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/aseem/viewport-rds/internal/logger"
)

const (
	SampleRate = 48000
	Channels   = 2
	BitDepth   = 16
	FrameSize  = 960 // samples per chunk (20ms at 48kHz)
	ChunkBytes = FrameSize * Channels * (BitDepth / 8) // 3840 bytes
)

type Capturer struct {
	log    *logger.Logger
	cmd    *exec.Cmd
	stdout io.ReadCloser
	ctx    context.Context
	cancel context.CancelFunc
	ch     chan []byte
}

func NewCapturer(parentCtx context.Context, log *logger.Logger) (*Capturer, error) {
	if _, err := exec.LookPath("pw-cat"); err != nil {
		return nil, fmt.Errorf("audio: pw-cat not found: %w", err)
	}

	ctx, cancel := context.WithCancel(parentCtx)
	c := &Capturer{
		log:    log,
		ctx:    ctx,
		cancel: cancel,
		ch:     make(chan []byte, 32),
	}

	if err := c.start(); err != nil {
		cancel()
		return nil, err
	}

	go c.readLoop()
	return c, nil
}

func (c *Capturer) start() error {
	// Capture the monitor of the default sink (desktop audio)
	c.cmd = exec.CommandContext(c.ctx, "pw-cat",
		"--record",
		"--rate", "48000",
		"--channels", "2",
		"--format", "s16",
		"--latency", "20ms",
		"--media-category", "Monitor",
		"-")

	c.cmd.Stderr = &logWriter{log: c.log, module: "pw-cat"}

	var err error
	c.stdout, err = c.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("audio: stdout pipe: %w", err)
	}

	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("audio: start pw-cat: %w", err)
	}
	return nil
}

func (c *Capturer) readLoop() {
	buf := make([]byte, ChunkBytes)
	for {
		if c.ctx.Err() != nil {
			return
		}

		n, err := io.ReadFull(c.stdout, buf)
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			c.log.Error("audio", "read: "+err.Error())
			c.reconnect()
			continue
		}

		chunk := make([]byte, n)
		copy(chunk, buf[:n])

		select {
		case c.ch <- chunk:
		default:
			// drop oldest, push new
			select {
			case <-c.ch:
			default:
			}
			c.ch <- chunk
		}
	}
}

func (c *Capturer) reconnect() {
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Signal(os.Kill)
		c.cmd.Wait()
	}
	time.Sleep(2 * time.Second)
	if c.ctx.Err() != nil {
		return
	}
	if err := c.start(); err != nil {
		c.log.Error("audio", "reconnect: "+err.Error())
	}
}

func (c *Capturer) Chunks() <-chan []byte {
	return c.ch
}

func (c *Capturer) Close() {
	c.cancel()
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Signal(os.Kill)
		c.cmd.Wait()
	}
}

type logWriter struct {
	log    *logger.Logger
	module string
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.log.Debug(w.module, string(p))
	return len(p), nil
}
