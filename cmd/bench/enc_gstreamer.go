package main

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

type GStreamerBench struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	mu     sync.Mutex
	buf    bytes.Buffer
	w, h   int
	done   chan struct{}
}

func NewGStreamerBench(name string, w, h int, props map[string]string) (*GStreamerBench, error) {
	encProps := ""
	for k, v := range props {
		encProps += fmt.Sprintf(" %s=%s", k, v)
	}

	pipeline := fmt.Sprintf(
		"fdsrc fd=0 ! rawvideoparse width=%d height=%d format=i420 framerate=30/1 ! "+
			"videoconvert ! video/x-raw,format=NV12 ! "+
			"vah264lpenc%s ! "+
			"video/x-h264,stream-format=byte-stream ! fdsink fd=1 sync=false",
		w, h, encProps)

	cmd := exec.Command("bash", "-c", "gst-launch-1.0 --quiet -e "+pipeline)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: stdin pipe: %v", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: stdout pipe: %v", name, err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: start failed: %v", name, err)
	}

	g := &GStreamerBench{
		name:   name,
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		w:      w,
		h:      h,
		done:   make(chan struct{}),
	}
	go g.readLoop()
	return g, nil
}

func (g *GStreamerBench) readLoop() {
	tmp := make([]byte, 2*1024*1024)
	for {
		n, err := g.stdout.Read(tmp)
		if n > 0 {
			g.mu.Lock()
			g.buf.Write(tmp[:n])
			g.mu.Unlock()
		}
		if err != nil {
			close(g.done)
			return
		}
	}
}

func (g *GStreamerBench) Name() string    { return g.name }
func (g *GStreamerBench) Library() string  { return "gstreamer-sub" }
func (g *GStreamerBench) Codec() string    { return "h264" }

func (g *GStreamerBench) Encode(y, u, v []byte, w, h int) ([]byte, error) {
	frameSize := w*h + (w/2)*(h/2)*2
	frame := make([]byte, 0, frameSize)
	frame = append(frame, y...)
	frame = append(frame, u...)
	frame = append(frame, v...)

	_, err := g.stdin.Write(frame)
	if err != nil {
		return nil, fmt.Errorf("gst write: %v", err)
	}

	// Wait for output
	for i := 0; i < 200; i++ {
		g.mu.Lock()
		n := g.buf.Len()
		g.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-g.done:
			return nil, fmt.Errorf("gst process exited")
		default:
		}
		time.Sleep(500 * time.Microsecond)
	}

	g.mu.Lock()
	data := make([]byte, g.buf.Len())
	copy(data, g.buf.Bytes())
	g.buf.Reset()
	g.mu.Unlock()

	if len(data) == 0 {
		return nil, nil
	}
	return data, nil
}

func (g *GStreamerBench) Close() {
	g.stdin.Close()
	g.cmd.Wait()
}
