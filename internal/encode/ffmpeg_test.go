package encode

import (
	"io"
	"os/exec"
	"testing"

	"github.com/asymysh/featherdesk/internal/logger"
)

func ffmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func TestFFmpegEncoderSoftware(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not found")
	}

	log := logger.New(io.Discard, logger.ERROR)
	enc, err := NewFFmpegEncoder(EncoderConfig{
		Width:  320,
		Height: 240,
		FPS:    30,
		QP:     26,
	}, log, false)
	if err != nil {
		t.Fatalf("NewFFmpegEncoder: %v", err)
	}
	defer enc.Close()

	frame := &I420Frame{
		Y:      make([]byte, 320*240),
		U:      make([]byte, 160*120),
		V:      make([]byte, 160*120),
		Width:  320,
		Height: 240,
	}
	for i := range frame.Y {
		frame.Y[i] = byte(i % 256)
	}
	for i := range frame.U {
		frame.U[i] = 128
		frame.V[i] = 128
	}

	// Send several frames - ffmpeg needs a few before producing output
	var gotNALs bool
	for i := 0; i < 30; i++ {
		for j := range frame.Y {
			frame.Y[j] = byte((i*7 + j) % 256)
		}
		nals, err := enc.Encode(frame)
		if err != nil {
			t.Fatalf("Encode frame %d: %v", i, err)
		}
		if len(nals) > 0 {
			gotNALs = true
			for _, nal := range nals {
				if len(nal) < 4 {
					t.Fatalf("NAL too short: %d bytes", len(nal))
				}
				if nal[0] != 0 || nal[1] != 0 || nal[2] != 0 || nal[3] != 1 {
					t.Fatalf("NAL missing start code: %x", nal[:4])
				}
			}
		}
	}
	if !gotNALs {
		t.Fatal("expected at least one NAL output after 30 frames")
	}
}

func TestFFmpegEncoderVAAPI(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not found")
	}
	if !ProbeVAAPI() {
		t.Skip("VA-API not available")
	}

	log := logger.New(io.Discard, logger.ERROR)
	enc, err := NewFFmpegEncoder(EncoderConfig{
		Width:  320,
		Height: 240,
		FPS:    30,
		QP:     26,
	}, log, true)
	if err != nil {
		t.Fatalf("NewFFmpegEncoder (hw): %v", err)
	}
	defer enc.Close()

	frame := &I420Frame{
		Y:      make([]byte, 320*240),
		U:      make([]byte, 160*120),
		V:      make([]byte, 160*120),
		Width:  320,
		Height: 240,
	}
	for i := range frame.U {
		frame.U[i] = 128
		frame.V[i] = 128
	}

	var gotNALs bool
	for i := 0; i < 30; i++ {
		for j := range frame.Y {
			frame.Y[j] = byte((i*13 + j) % 256)
		}
		nals, err := enc.Encode(frame)
		if err != nil {
			t.Fatalf("Encode frame %d: %v", i, err)
		}
		if len(nals) > 0 {
			gotNALs = true
		}
	}
	if !gotNALs {
		t.Fatal("expected NAL output from VA-API encoder")
	}
}

func TestProbeVAAPI(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not found")
	}
	result := ProbeVAAPI()
	t.Logf("ProbeVAAPI: %v", result)
}
