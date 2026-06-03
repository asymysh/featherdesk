//go:build integration

package capture

import (
	"testing"
)

func TestOpenDRMCard(t *testing.T) {
	card, err := OpenDRMCard()
	if err != nil {
		t.Fatalf("OpenDRMCard() error: %v", err)
	}
	defer card.Close()

	if card.FD < 0 {
		t.Error("expected valid fd")
	}
	if card.Width == 0 || card.Height == 0 {
		t.Errorf("expected non-zero dimensions, got %dx%d", card.Width, card.Height)
	}
	t.Logf("Card: %s, %dx%d @ %dHz", card.Path, card.Width, card.Height, card.RefreshHz)
}

func TestFindPrimaryPlaneID(t *testing.T) {
	card, err := OpenDRMCard()
	if err != nil {
		t.Fatalf("OpenDRMCard() error: %v", err)
	}
	defer card.Close()

	pid, err := card.FindPrimaryPlaneID()
	if err != nil {
		t.Fatalf("FindPrimaryPlaneID() error: %v", err)
	}
	if pid == 0 {
		t.Error("expected non-zero plane ID")
	}
	t.Logf("Primary plane ID: %d", pid)
}

func TestGetDMABufFD(t *testing.T) {
	card, err := OpenDRMCard()
	if err != nil {
		t.Fatalf("OpenDRMCard() error: %v", err)
	}
	defer card.Close()

	pid, err := card.FindPrimaryPlaneID()
	if err != nil {
		t.Fatalf("FindPrimaryPlaneID() error: %v", err)
	}

	dmaFD, err := card.GetDMABufFD(pid)
	if err != nil {
		t.Fatalf("GetDMABufFD() error: %v", err)
	}
	if dmaFD < 0 {
		t.Error("expected valid DMA-BUF fd")
	}
	t.Logf("DMA-BUF fd: %d", dmaFD)
}

func TestCardClose(t *testing.T) {
	card, err := OpenDRMCard()
	if err != nil {
		t.Fatalf("OpenDRMCard() error: %v", err)
	}

	err = card.Close()
	if err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if card.FD != -1 {
		t.Error("expected FD to be -1 after close")
	}
}
