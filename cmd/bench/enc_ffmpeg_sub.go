package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"time"
)

type FFmpegSubBench struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	nalCh chan []byte
	name  string
	codec string
}

func NewFFmpegSubBench(name string, w, h int, encArgs []string) (*FFmpegSubBench, error) {
	args := []string{
		"-hide_banner", "-nostats", "-loglevel", "error",
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-f", "rawvideo", "-pix_fmt", "yuv420p",
		"-video_size", fmt.Sprintf("%dx%d", w, h),
		"-framerate", "30", "-i", "pipe:0",
	}
	args = append(args, encArgs...)
	args = append(args, "-g", "30", "-bf", "0", "-f", "h264", "pipe:1")

	// Detect output format from codec
	outFmt := "h264"
	for i, a := range encArgs {
		if a == "-c:v" && i+1 < len(encArgs) {
			switch encArgs[i+1] {
			case "libvpx", "libvpx-vp9":
				args[len(args)-2] = "ivf"
			case "libx265":
				args[len(args)-2] = "hevc"
			case "libsvtav1", "libaom-av1", "librav1e":
				args[len(args)-2] = "ivf"
			case "mpeg4":
				args[len(args)-2] = "rawvideo"
				outFmt = "mpeg4"
			}
		}
	}

	codec := "h264"
	for i, a := range encArgs {
		if a == "-c:v" && i+1 < len(encArgs) {
			switch encArgs[i+1] {
			case "libx265": codec = "h265"
			case "libvpx": codec = "vp8"
			case "libvpx-vp9": codec = "vp9"
			case "libsvtav1", "libaom-av1", "librav1e": codec = "av1"
			case "mpeg4": codec = "mpeg4"
			}
		}
	}
	_ = outFmt

	cmd := exec.Command("ffmpeg", args...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}

	e := &FFmpegSubBench{cmd: cmd, stdin: stdin, nalCh: make(chan []byte, 8), name: name, codec: codec}
	go e.readLoop(stdout)
	time.Sleep(300 * time.Millisecond)
	return e, nil
}

func (e *FFmpegSubBench) readLoop(r io.Reader) {
	reader := bufio.NewReaderSize(r, 2*1024*1024)
	buf := make([]byte, 256*1024)
	var acc bytes.Buffer

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
			for acc.Len() > 64*1024 {
				chunk := make([]byte, 64*1024)
				acc.Read(chunk)
				select {
				case e.nalCh <- chunk:
				default:
				}
			}
		}
		if err != nil {
			if acc.Len() > 0 {
				rest := make([]byte, acc.Len())
				acc.Read(rest)
				select {
				case e.nalCh <- rest:
				default:
				}
			}
			return
		}
	}
}

func (e *FFmpegSubBench) Name() string    { return e.name }
func (e *FFmpegSubBench) Library() string  { return "ffmpeg-sub" }
func (e *FFmpegSubBench) Codec() string    { return e.codec }

func (e *FFmpegSubBench) Encode(y, u, v []byte, w, h int) ([]byte, error) {
	raw := make([]byte, 0, len(y)+len(u)+len(v))
	raw = append(raw, y...)
	raw = append(raw, u...)
	raw = append(raw, v...)

	if _, err := e.stdin.Write(raw); err != nil {
		return nil, err
	}

	var result []byte
	for {
		select {
		case nal := <-e.nalCh:
			result = append(result, nal...)
		default:
			goto done
		}
	}
done:
	if len(result) > 0 {
		return result, nil
	}
	return nil, nil
}

func (e *FFmpegSubBench) Close() {
	e.stdin.Close()
	time.Sleep(50 * time.Millisecond)
	e.cmd.Process.Kill()
	e.cmd.Wait()
}
