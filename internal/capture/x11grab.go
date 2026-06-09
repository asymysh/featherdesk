package capture

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

type X11Capturer struct {
	cmd         *exec.Cmd
	reader      *bufio.Reader
	frameSize   int
	width       uint32
	height      uint32
	buf         []byte
	ctx         context.Context
	cancel      context.CancelFunc
	fps         int
	nodeID      uint32
	dbusConn    *dbus.Conn
	sessionPath dbus.ObjectPath
}

func NewX11Capturer(ctx context.Context, fps int) (*X11Capturer, error) {
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}

	width, height, err := detectX11Size(display)
	if err != nil {
		return nil, err
	}

	childCtx, cancel := context.WithCancel(ctx)
	frameSize := int(width) * int(height) * 4

	c := &X11Capturer{
		frameSize: frameSize,
		width:     uint32(width),
		height:    uint32(height),
		buf:       make([]byte, frameSize),
		ctx:       childCtx,
		cancel:    cancel,
		fps:       fps,
	}

	if err := c.startProcess(); err != nil {
		cancel()
		return nil, err
	}

	return c, nil
}

func (c *X11Capturer) startProcess() error {
	nodeID, conn, sessionPath, err := getMutterScreencastNode()
	if err == nil {
		c.nodeID = nodeID
		c.dbusConn = conn
		c.sessionPath = sessionPath
		fmt.Fprintf(os.Stderr, "PipeWire node: %d\n", nodeID)
		return c.startGStreamer(nodeID)
	}

	fmt.Fprintf(os.Stderr, "screencast unavailable (%v), falling back to x11grab\n", err)
	return c.startX11Grab()
}

func (c *X11Capturer) startGStreamer(nodeID uint32) error {
	args := []string{
		"-q",
		"pipewiresrc", fmt.Sprintf("path=%d", nodeID), "do-timestamp=true",
		"keepalive-time=100", "resend-last=true", "!",
		"queue", "max-size-buffers=1", "max-size-time=0", "max-size-bytes=0", "leaky=downstream", "!",
		"videoconvert", "!",
		"video/x-raw,format=RGBA", "!",
		"fdsink", "fd=1", "sync=false",
	}
	cmd := exec.CommandContext(c.ctx, "gst-launch-1.0", args...)
	return c.attachCmd(cmd)
}

func (c *X11Capturer) startX11Grab() error {
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}
	args := []string{
		"-f", "x11grab",
		"-video_size", fmt.Sprintf("%dx%d", c.width, c.height),
		"-framerate", fmt.Sprintf("%d", c.fps),
		"-draw_mouse", "1",
		"-i", display,
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-an",
		"-",
	}
	cmd := exec.CommandContext(c.ctx, "ffmpeg", args...)
	return c.attachCmd(cmd)
}

func (c *X11Capturer) attachCmd(cmd *exec.Cmd) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("capture: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("capture: start process: %w", err)
	}

	c.cmd = cmd
	c.reader = bufio.NewReaderSize(stdout, c.frameSize*2)
	return nil
}

func (c *X11Capturer) NextFrame() (*Frame, error) {
	_, err := io.ReadFull(c.reader, c.buf)
	if err != nil {
		if c.ctx.Err() != nil {
			return nil, fmt.Errorf("capture: read frame: %w", err)
		}
		c.cmd.Wait()
		time.Sleep(500 * time.Millisecond)
		if c.ctx.Err() != nil {
			return nil, fmt.Errorf("capture: context cancelled")
		}
		if restartErr := c.startProcess(); restartErr != nil {
			return nil, fmt.Errorf("capture: respawn failed: %w", restartErr)
		}
		return c.NextFrame()
	}

	return &Frame{
		Data:      c.buf,
		Width:     c.width,
		Height:    c.height,
		Timestamp: uint64(time.Now().UnixMilli()),
	}, nil
}

func (c *X11Capturer) Size() (width, height int) {
	return int(c.width), int(c.height)
}

func (c *X11Capturer) Close() error {
	c.cancel()
	if c.cmd != nil {
		c.cmd.Wait()
	}
	if c.dbusConn != nil {
		stopMutterSession(c.dbusConn, c.sessionPath)
		c.dbusConn = nil
	}
	return nil
}

func (c *X11Capturer) Restart() {
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
}

func detectX11Size(display string) (int, int, error) {
	out, err := exec.Command("xdpyinfo", "-display", display).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("capture: xdpyinfo failed: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "dimensions:") {
			var w, h int
			_, err := fmt.Sscanf(line, "dimensions: %dx%d", &w, &h)
			if err == nil && w > 0 && h > 0 {
				return w, h, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("capture: could not detect display dimensions")
}
