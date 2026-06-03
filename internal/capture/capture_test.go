package capture

import "testing"

func TestErrNoCardMessage(t *testing.T) {
	err := ErrNoCard{}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func TestErrNoPrimaryPlaneMessage(t *testing.T) {
	err := ErrNoPrimaryPlane{}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func TestErrCapabilityMessage(t *testing.T) {
	err := ErrCapability{Cap: 1, Err: nil}
	msg := err.Error()
	if msg == "" {
		t.Error("expected non-empty error message")
	}
}

func TestDRMCardCloseIdempotent(t *testing.T) {
	card := &DRMCard{FD: -1, Path: ""}
	err := card.Close()
	if err != nil {
		t.Errorf("Close on already-closed card should not error: %v", err)
	}
}
