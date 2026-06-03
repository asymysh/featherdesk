package encode

import "testing"

func TestConverterPlaneSize(t *testing.T) {
	width, height := 640, 480
	c := NewConverter(width, height)

	ySize := width * height
	uvSize := (width / 2) * (height / 2)

	if len(c.frame.Y) != ySize {
		t.Fatalf("Y plane: got %d, want %d", len(c.frame.Y), ySize)
	}
	if len(c.frame.U) != uvSize {
		t.Fatalf("U plane: got %d, want %d", len(c.frame.U), uvSize)
	}
	if len(c.frame.V) != uvSize {
		t.Fatalf("V plane: got %d, want %d", len(c.frame.V), uvSize)
	}
}

func TestConverterBGRAToI420(t *testing.T) {
	width, height := 4, 4
	c := NewConverter(width, height)

	// Fill with solid green in RGBA (R=0, G=255, B=0, A=255) - matches GL_RGBA output
	rgba := make([]byte, width*height*4)
	for i := 0; i < len(rgba); i += 4 {
		rgba[i+0] = 0   // R
		rgba[i+1] = 255 // G
		rgba[i+2] = 0   // B
		rgba[i+3] = 255 // A
	}

	frame := c.Convert(rgba)

	if frame.Width != width || frame.Height != height {
		t.Fatalf("dimensions: got %dx%d, want %dx%d", frame.Width, frame.Height, width, height)
	}

	// libyuv uses BT.601 limited range for pure green (R=0, G=255, B=0):
	// Y ~= 144, U ~= 55, V ~= 35
	checkPlane := func(name string, plane []byte, lo, hi byte) {
		t.Helper()
		for i, v := range plane {
			if v < lo || v > hi {
				t.Fatalf("%s[%d] = %d, want [%d, %d]", name, i, v, lo, hi)
			}
		}
	}
	checkPlane("Y", frame.Y, 134, 154)
	checkPlane("U", frame.U, 45, 65)
	checkPlane("V", frame.V, 25, 45)
}

func TestConverterReusesBuffer(t *testing.T) {
	c := NewConverter(16, 16)
	bgra := make([]byte, 16*16*4)

	f1 := c.Convert(bgra)
	f2 := c.Convert(bgra)

	if &f1.Y[0] != &f2.Y[0] {
		t.Fatal("expected Convert to reuse Y buffer")
	}
}

func BenchmarkConvert1080p(b *testing.B) {
	c := NewConverter(1920, 1080)
	bgra := make([]byte, 1920*1080*4)
	b.SetBytes(int64(len(bgra)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Convert(bgra)
	}
}
