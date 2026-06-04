package encode

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aseem/viewport-rds/internal/logger"
)

type FFmpegEncoder struct {
	cfg    EncoderConfig
	log    *logger.Logger
	hwAccel bool

	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	nalCh      chan [][]byte
	errCh      chan error
	cancel     context.CancelFunc
	ctx        context.Context
	idr        atomic.Bool
	running    atomic.Bool
	firstFrame atomic.Bool
}

func NewFFmpegEncoder(cfg EncoderConfig, log *logger.Logger, hwAccel bool) (*FFmpegEncoder, error) {
	ctx, cancel := context.WithCancel(context.Background())
	e := &FFmpegEncoder{
		cfg:     cfg,
		log:     log,
		hwAccel: hwAccel,
		nalCh:   make(chan [][]byte, 4),
		errCh:   make(chan error, 1),
		ctx:     ctx,
		cancel:  cancel,
	}
	e.firstFrame.Store(true)
	if err := e.start(); err != nil {
		cancel()
		return nil, err
	}
	return e, nil
}

func (e *FFmpegEncoder) start() error {
	args := e.buildArgs()
	e.cmd = exec.CommandContext(e.ctx, "ffmpeg", args...)
	e.cmd.Stderr = &logWriter{log: e.log, module: "ffmpeg"}

	var err error
	e.stdin, err = e.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("encode: ffmpeg stdin pipe: %w", err)
	}

	stdout, err := e.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("encode: ffmpeg stdout pipe: %w", err)
	}

	if err := e.cmd.Start(); err != nil {
		return fmt.Errorf("encode: ffmpeg start: %w", err)
	}

	e.running.Store(true)
	go e.readNALs(stdout)
	return nil
}

func (e *FFmpegEncoder) buildArgs() []string {
	w := fmt.Sprintf("%d", e.cfg.Width)
	h := fmt.Sprintf("%d", e.cfg.Height)
	fps := fmt.Sprintf("%d", e.cfg.FPS)

	args := []string{
		"-hide_banner", "-nostats",
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-f", "rawvideo",
		"-pix_fmt", "yuv420p",
		"-video_size", w + "x" + h,
		"-framerate", fps,
		"-i", "pipe:0",
	}

	if e.hwAccel {
		args = append(args,
			"-vaapi_device", "/dev/dri/renderD128",
			"-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi",
			"-low_power", "1",
			"-rc_mode", "CQP",
			"-qp", "26",
		)
	} else {
		args = append(args,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-crf", "26",
			"-profile:v", "baseline",
		)
	}

	args = append(args,
		"-g", "30",
		"-bf", "0",
		"-f", "h264",
		"pipe:1",
	)
	return args
}

func (e *FFmpegEncoder) readNALs(r io.Reader) {
	defer e.running.Store(false)
	reader := bufio.NewReaderSize(r, 1024*1024)
	var buf bytes.Buffer
	startCode := []byte{0, 0, 0, 1}
	scanBuf := make([]byte, 32*1024)

	for {
		n, err := reader.Read(scanBuf)
		if n > 0 {
			buf.Write(scanBuf[:n])
			e.extractNALs(&buf, startCode)
		}
		if err != nil {
			if buf.Len() > 4 {
				data := buf.Bytes()
				nal := make([]byte, len(data))
				copy(nal, data)
				select {
				case e.nalCh <- [][]byte{nal}:
				default:
				}
			}
			return
		}
	}
}

func (e *FFmpegEncoder) extractNALs(buf *bytes.Buffer, startCode []byte) {
	for {
		data := buf.Bytes()
		if len(data) < 8 {
			return
		}

		// Find second start code (marks end of first NAL)
		idx := bytes.Index(data[4:], startCode)
		if idx < 0 {
			return
		}
		idx += 4

		nal := make([]byte, idx)
		copy(nal, data[:idx])
		buf.Next(idx)

		select {
		case e.nalCh <- [][]byte{nal}:
		default:
		}
	}
}

func (e *FFmpegEncoder) Encode(frame *I420Frame) ([][]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.running.Load() {
		if err := e.restart(); err != nil {
			return nil, err
		}
	}

	frameSize := len(frame.Y) + len(frame.U) + len(frame.V)
	raw := make([]byte, 0, frameSize)
	raw = append(raw, frame.Y...)
	raw = append(raw, frame.U...)
	raw = append(raw, frame.V...)

	if _, err := e.stdin.Write(raw); err != nil {
		e.log.Error("ffmpeg", "write error: "+err.Error())
		e.kill()
		return nil, err
	}

	timeout := 50 * time.Millisecond
	if e.firstFrame.CompareAndSwap(true, false) {
		timeout = 500 * time.Millisecond
	}

	select {
	case nals := <-e.nalCh:
		return nals, nil
	case <-time.After(timeout):
		return nil, nil
	case <-e.ctx.Done():
		return nil, e.ctx.Err()
	}
}

func (e *FFmpegEncoder) ForceKeyframe() {
	e.idr.Store(true)
}

func (e *FFmpegEncoder) Close() error {
	e.cancel()
	e.kill()
	return nil
}

func (e *FFmpegEncoder) restart() error {
	e.kill()
	time.Sleep(50 * time.Millisecond)
	return e.start()
}

func (e *FFmpegEncoder) kill() {
	if e.stdin != nil {
		e.stdin.Close()
	}
	if e.cmd != nil && e.cmd.Process != nil {
		e.cmd.Process.Signal(os.Kill)
		e.cmd.Wait()
	}
	// drain channel
	for {
		select {
		case <-e.nalCh:
		default:
			return
		}
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
