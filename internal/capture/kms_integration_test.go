//go:build integration

package capture

import (
	"context"
	"testing"
	"time"
)

func TestKMSCapturerNextFrame(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cap, err := NewKMSCapturer(ctx, 30)
	if err != nil {
		t.Fatalf("NewKMSCapturer() error: %v", err)
	}
	defer cap.Close()

	frame, err := cap.NextFrame()
	if err != nil {
		t.Fatalf("NextFrame() error: %v", err)
	}

	if frame.Width == 0 || frame.Height == 0 {
		t.Errorf("frame dimensions = %dx%d, expected non-zero", frame.Width, frame.Height)
	}
	expectedSize := int(frame.Width) * int(frame.Height) * 4
	if len(frame.Data) != expectedSize {
		t.Errorf("frame data size = %d, want %d", len(frame.Data), expectedSize)
	}
	if frame.Timestamp == 0 {
		t.Error("frame timestamp is zero")
	}
	t.Logf("Frame: %dx%d, %d bytes, ts=%d", frame.Width, frame.Height, len(frame.Data), frame.Timestamp)
}

func TestKMSCapturerFramePacing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fps := 30
	cap, err := NewKMSCapturer(ctx, fps)
	if err != nil {
		t.Fatalf("NewKMSCapturer() error: %v", err)
	}
	defer cap.Close()

	start := time.Now()
	frames := 5
	for range frames {
		_, err := cap.NextFrame()
		if err != nil {
			t.Fatalf("NextFrame() error: %v", err)
		}
	}
	elapsed := time.Since(start)

	expectedMin := time.Duration(frames-1) * time.Second / time.Duration(fps)
	if elapsed < expectedMin {
		t.Errorf("captured %d frames in %v, expected at least %v", frames, elapsed, expectedMin)
	}
	t.Logf("Captured %d frames in %v (target: %d fps)", frames, elapsed, fps)
}

func TestKMSCapturerCancelContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cap, err := NewKMSCapturer(ctx, 30)
	if err != nil {
		t.Fatalf("NewKMSCapturer() error: %v", err)
	}
	defer cap.Close()

	cancel()
	_, err = cap.NextFrame()
	if err == nil {
		t.Error("expected error after context cancel")
	}
}
