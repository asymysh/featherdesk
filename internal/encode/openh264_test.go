package encode

import "testing"

func TestH264EncoderCreateClose(t *testing.T) {
	enc, err := NewH264Encoder(EncoderConfig{
		Width:  640,
		Height: 480,
		FPS:    30,
		QP:     26,
	})
	if err != nil {
		t.Fatalf("NewH264Encoder: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestH264EncoderProducesNALs(t *testing.T) {
	enc, err := NewH264Encoder(EncoderConfig{
		Width:  320,
		Height: 240,
		FPS:    30,
		QP:     26,
	})
	if err != nil {
		t.Fatalf("NewH264Encoder: %v", err)
	}
	defer enc.Close()

	frame := &I420Frame{
		Y:      make([]byte, 320*240),
		U:      make([]byte, 160*120),
		V:      make([]byte, 160*120),
		Width:  320,
		Height: 240,
	}
	// Fill Y plane with gradient
	for i := range frame.Y {
		frame.Y[i] = byte(i % 256)
	}
	// Fill UV with mid-gray
	for i := range frame.U {
		frame.U[i] = 128
		frame.V[i] = 128
	}

	nals, err := enc.Encode(frame)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(nals) == 0 {
		t.Fatal("expected NAL units from first frame")
	}

	// First frame should be IDR: check for NAL start code and IDR type
	hasIDR := false
	for _, nal := range nals {
		if len(nal) < 5 {
			continue
		}
		// Start code: 00 00 00 01 or 00 00 01
		nalType := byte(0)
		if nal[0] == 0 && nal[1] == 0 && nal[2] == 0 && nal[3] == 1 {
			nalType = nal[4] & 0x1F
		} else if nal[0] == 0 && nal[1] == 0 && nal[2] == 1 {
			nalType = nal[3] & 0x1F
		}
		if nalType == 5 { // IDR slice
			hasIDR = true
		}
	}
	if !hasIDR {
		t.Fatal("first frame should contain IDR NAL unit")
	}
}

func TestH264EncoderForceKeyframe(t *testing.T) {
	enc, err := NewH264Encoder(EncoderConfig{
		Width:  320,
		Height: 240,
		FPS:    30,
		QP:     26,
	})
	if err != nil {
		t.Fatalf("NewH264Encoder: %v", err)
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

	// Encode a few P-frames
	for i := 0; i < 5; i++ {
		for j := range frame.Y {
			frame.Y[j] = byte((i*7 + j) % 256)
		}
		_, err := enc.Encode(frame)
		if err != nil {
			t.Fatalf("Encode frame %d: %v", i, err)
		}
	}

	// Force IDR
	enc.ForceKeyframe()
	nals, err := enc.Encode(frame)
	if err != nil {
		t.Fatalf("Encode after ForceKeyframe: %v", err)
	}

	hasIDR := false
	for _, nal := range nals {
		if len(nal) < 5 {
			continue
		}
		nalType := byte(0)
		if nal[0] == 0 && nal[1] == 0 && nal[2] == 0 && nal[3] == 1 {
			nalType = nal[4] & 0x1F
		} else if nal[0] == 0 && nal[1] == 0 && nal[2] == 1 {
			nalType = nal[3] & 0x1F
		}
		if nalType == 5 {
			hasIDR = true
		}
	}
	if !hasIDR {
		t.Fatal("ForceKeyframe should produce IDR NAL")
	}
}

func TestH264EncoderSequence(t *testing.T) {
	enc, err := NewH264Encoder(EncoderConfig{
		Width:  320,
		Height: 240,
		FPS:    30,
		QP:     26,
	})
	if err != nil {
		t.Fatalf("NewH264Encoder: %v", err)
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

	// Encode 30 frames, verify no errors
	for i := 0; i < 30; i++ {
		for j := range frame.Y {
			frame.Y[j] = byte((i*13 + j) % 256)
		}
		nals, err := enc.Encode(frame)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		// First frame must produce output, subsequent may skip but shouldn't error
		if i == 0 && len(nals) == 0 {
			t.Fatal("first frame produced no NALs")
		}
	}
}

func BenchmarkEncode320x240(b *testing.B) {
	enc, err := NewH264Encoder(EncoderConfig{
		Width:  320,
		Height: 240,
		FPS:    30,
		QP:     26,
	})
	if err != nil {
		b.Fatalf("NewH264Encoder: %v", err)
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

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range frame.Y {
			frame.Y[j] = byte((i + j) % 256)
		}
		enc.Encode(frame)
	}
}
