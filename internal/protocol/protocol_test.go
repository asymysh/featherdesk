package protocol

import (
	"bytes"
	"testing"
)

func TestFrameTypeConstants(t *testing.T) {
	if FrameTypeVideoH264 != 1 {
		t.Errorf("FrameTypeVideoH264 = %d, want 1", FrameTypeVideoH264)
	}
	if FrameTypePing != 2 {
		t.Errorf("FrameTypePing = %d, want 2", FrameTypePing)
	}
	if FrameTypePong != 3 {
		t.Errorf("FrameTypePong = %d, want 3", FrameTypePong)
	}
}

func TestHeaderSize(t *testing.T) {
	if HeaderSize != 17 {
		t.Errorf("HeaderSize = %d, want 17", HeaderSize)
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		header FrameHeader
	}{
		{
			name: "video frame",
			header: FrameHeader{
				Type:        FrameTypeVideoH264,
				Timestamp:   1717480000123,
				Width:       1920,
				Height:      1080,
				PayloadSize: 65536,
			},
		},
		{
			name: "ping frame",
			header: FrameHeader{
				Type:        FrameTypePing,
				Timestamp:   9999999999999,
				Width:       0,
				Height:      0,
				PayloadSize: 0,
			},
		},
		{
			name: "pong frame",
			header: FrameHeader{
				Type:        FrameTypePong,
				Timestamp:   1,
				Width:       2560,
				Height:      1440,
				PayloadSize: 4294967295,
			},
		},
		{
			name: "zero values",
			header: FrameHeader{
				Type:        0,
				Timestamp:   0,
				Width:       0,
				Height:      0,
				PayloadSize: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, HeaderSize)
			MarshalHeader(tt.header, buf)

			got, err := UnmarshalHeader(buf)
			if err != nil {
				t.Fatalf("UnmarshalHeader() error = %v", err)
			}

			if got != tt.header {
				t.Errorf("round-trip failed:\n  got  %+v\n  want %+v", got, tt.header)
			}
		})
	}
}

func TestUnmarshalHeaderShortBuffer(t *testing.T) {
	for i := range HeaderSize {
		buf := make([]byte, i)
		_, err := UnmarshalHeader(buf)
		if err == nil {
			t.Errorf("UnmarshalHeader(len=%d) should error, got nil", i)
		}
	}
}

func TestMarshalHeaderReusesBuffer(t *testing.T) {
	buf := make([]byte, HeaderSize)
	h1 := FrameHeader{Type: FrameTypeVideoH264, Timestamp: 100, Width: 1920, Height: 1080, PayloadSize: 500}
	h2 := FrameHeader{Type: FrameTypePing, Timestamp: 200, Width: 0, Height: 0, PayloadSize: 0}

	MarshalHeader(h1, buf)
	MarshalHeader(h2, buf)

	got, err := UnmarshalHeader(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got != h2 {
		t.Errorf("buffer reuse failed: got %+v, want %+v", got, h2)
	}
}

func TestMarshalHeaderDeterministic(t *testing.T) {
	h := FrameHeader{Type: FrameTypeVideoH264, Timestamp: 12345, Width: 640, Height: 480, PayloadSize: 1024}
	buf1 := make([]byte, HeaderSize)
	buf2 := make([]byte, HeaderSize)

	MarshalHeader(h, buf1)
	MarshalHeader(h, buf2)

	if !bytes.Equal(buf1, buf2) {
		t.Error("MarshalHeader is not deterministic")
	}
}

func BenchmarkMarshalHeader(b *testing.B) {
	h := FrameHeader{Type: FrameTypeVideoH264, Timestamp: 1717480000123, Width: 1920, Height: 1080, PayloadSize: 65536}
	buf := make([]byte, HeaderSize)
	b.ResetTimer()
	for range b.N {
		MarshalHeader(h, buf)
	}
}

func BenchmarkUnmarshalHeader(b *testing.B) {
	h := FrameHeader{Type: FrameTypeVideoH264, Timestamp: 1717480000123, Width: 1920, Height: 1080, PayloadSize: 65536}
	buf := make([]byte, HeaderSize)
	MarshalHeader(h, buf)
	b.ResetTimer()
	for range b.N {
		UnmarshalHeader(buf)
	}
}
