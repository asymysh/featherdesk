//go:build integration

package capture

import (
	"testing"
)

func TestNewEGLState(t *testing.T) {
	card, err := OpenDRMCard()
	if err != nil {
		t.Fatalf("OpenDRMCard() error: %v", err)
	}
	defer card.Close()

	egl, err := NewEGLState(card.FD)
	if err != nil {
		t.Fatalf("NewEGLState() error: %v", err)
	}
	defer egl.Close()

	t.Log("EGL context created successfully")
}

func TestImportDMABufAndReadPixels(t *testing.T) {
	card, err := OpenDRMCard()
	if err != nil {
		t.Fatalf("OpenDRMCard() error: %v", err)
	}
	defer card.Close()

	egl, err := NewEGLState(card.FD)
	if err != nil {
		t.Fatalf("NewEGLState() error: %v", err)
	}
	defer egl.Close()

	pid, err := card.FindPrimaryPlaneID()
	if err != nil {
		t.Fatalf("FindPrimaryPlaneID() error: %v", err)
	}

	dmaFD, err := card.GetDMABufFD(pid)
	if err != nil {
		t.Fatalf("GetDMABufFD() error: %v", err)
	}

	stride := int(card.Width) * 4
	err = egl.ImportDMABuf(dmaFD, int(card.Width), int(card.Height), stride, 0x34325241) // DRM_FORMAT_XRGB8888
	if err != nil {
		t.Fatalf("ImportDMABuf() error: %v", err)
	}

	pixels := egl.ReadPixels()
	if len(pixels) == 0 {
		t.Fatal("ReadPixels returned empty buffer")
	}

	expectedSize := int(card.Width) * int(card.Height) * 4
	if len(pixels) != expectedSize {
		t.Errorf("pixel buffer size = %d, want %d", len(pixels), expectedSize)
	}

	nonZero := 0
	for i := 0; i < len(pixels) && i < 1000; i++ {
		if pixels[i] != 0 {
			nonZero++
		}
	}
	if nonZero == 0 {
		t.Error("pixel buffer is all zeros (expected screen content)")
	}
	t.Logf("Read %d bytes, first 1000 bytes have %d non-zero values", len(pixels), nonZero)
}
