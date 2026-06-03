package capture

import "fmt"

type DRMCard struct {
	FD       int
	Path     string
	Width    uint32
	Height   uint32
	RefreshHz uint32
}

type DRMPlane struct {
	ID   uint32
	FBID uint32
	Type uint32
}

type Frame struct {
	Data      []byte
	Width     uint32
	Height    uint32
	Timestamp uint64
}

type Capturer interface {
	NextFrame() (*Frame, error)
	Close() error
}

const (
	PlaneTypePrimary = 1
	PlaneTypeCursor  = 2
	PlaneTypeOverlay = 0
)

type ErrNoCard struct{}

func (e ErrNoCard) Error() string { return "capture: no usable DRM card found" }

type ErrNoPrimaryPlane struct{}

func (e ErrNoPrimaryPlane) Error() string { return "capture: no primary plane with active framebuffer" }

type ErrCapability struct {
	Cap int
	Err error
}

func (e ErrCapability) Error() string {
	return fmt.Sprintf("capture: failed to set DRM capability %d: %v", e.Cap, e.Err)
}
