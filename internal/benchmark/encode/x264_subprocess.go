package encode

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"time"
)

// X264SubprocessEncoder spawns ffmpeg as a subprocess for x264 encoding.
// GPL-isolated: only ffmpeg binary is GPL, our code stays proprietary.
type X264SubprocessEncoder struct {
	preset  string
	threads int
	crf     int
	width   int
	height  int
}

func NewX264Subprocess(preset string, threads, crf int) *X264SubprocessEncoder {
	return &X264SubprocessEncoder{preset: preset, threads: threads, crf: crf}
}

func (e *X264SubprocessEncoder) Name() string {
	return fmt.Sprintf("x264-sub-%s-%dt", e.preset, e.threads)
}
func (e *X264SubprocessEncoder) Codec() string { return "h264" }
func (e *X264SubprocessEncoder) Type() string  { return "software-subprocess" }

func (e *X264SubprocessEncoder) Init(width, height, fps int) error {
	e.width = width
	e.height = height
	// Verify ffmpeg exists
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found in PATH")
	}
	return nil
}

func (e *X264SubprocessEncoder) Encode(y, u, v []byte, width, height int) ([]byte, error) {
	frame := make([]byte, 0, len(y)+len(u)+len(v))
	frame = append(frame, y...)
	frame = append(frame, u...)
	frame = append(frame, v...)

	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo",
		"-pix_fmt", "yuv420p",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-i", "pipe:0",
		"-frames:v", "1",
		"-c:v", "libx264",
		"-preset", e.preset,
		"-tune", "zerolatency",
		"-crf", strconv.Itoa(e.crf),
		"-threads", strconv.Itoa(e.threads),
		"-f", "h264",
		"pipe:1",
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdin = bytes.NewReader(frame)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v", err)
	}

	return stdout.Bytes(), nil
}

func (e *X264SubprocessEncoder) Close() {}

// X264PersistentEncoder keeps a single ffmpeg process alive for multiple frames.
// Much lower per-frame overhead than spawning ffmpeg per frame.
type X264PersistentEncoder struct {
	preset  string
	threads int
	crf     int
	width   int
	height  int
	fps     int
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
}

func NewX264Persistent(preset string, threads, crf int) *X264PersistentEncoder {
	return &X264PersistentEncoder{preset: preset, threads: threads, crf: crf}
}

func (e *X264PersistentEncoder) Name() string {
	return fmt.Sprintf("x264-persist-%s-%dt", e.preset, e.threads)
}
func (e *X264PersistentEncoder) Codec() string { return "h264" }
func (e *X264PersistentEncoder) Type() string  { return "software-subprocess" }

func (e *X264PersistentEncoder) Init(width, height, fps int) error {
	e.width = width
	e.height = height
	e.fps = fps

	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo",
		"-pix_fmt", "yuv420p",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", strconv.Itoa(fps),
		"-i", "pipe:0",
		"-c:v", "libx264",
		"-preset", e.preset,
		"-tune", "zerolatency",
		"-crf", strconv.Itoa(e.crf),
		"-threads", strconv.Itoa(e.threads),
		"-f", "h264",
		"pipe:1",
	}

	e.cmd = exec.Command("ffmpeg", args...)
	var err error
	e.stdin, err = e.cmd.StdinPipe()
	if err != nil {
		return err
	}
	e.stdout, err = e.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	return e.cmd.Start()
}

func (e *X264PersistentEncoder) Encode(y, u, v []byte, width, height int) ([]byte, error) {
	// Write frame
	if _, err := e.stdin.Write(y); err != nil {
		return nil, err
	}
	if _, err := e.stdin.Write(u); err != nil {
		return nil, err
	}
	if _, err := e.stdin.Write(v); err != nil {
		return nil, err
	}

	// Read encoded output with timeout
	// x264 with zerolatency should output immediately
	buf := make([]byte, 1024*1024) // 1MB max NAL
	done := make(chan int, 1)
	var readErr error

	go func() {
		// Give ffmpeg a moment to encode
		time.Sleep(time.Millisecond)
		n, err := e.stdout.Read(buf)
		readErr = err
		done <- n
	}()

	select {
	case n := <-done:
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		return buf[:n], nil
	case <-time.After(100 * time.Millisecond):
		return nil, fmt.Errorf("encode timeout")
	}
}

func (e *X264PersistentEncoder) Close() {
	if e.stdin != nil {
		e.stdin.Close()
	}
	if e.cmd != nil {
		e.cmd.Wait()
	}
}
