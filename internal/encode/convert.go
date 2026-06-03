package encode

/*
#cgo LDFLAGS: -lyuv
#include <libyuv/convert.h>
#include <stdint.h>
*/
import "C"
import "unsafe"

// Converter handles RGBA-to-I420 color space conversion using libyuv.
// Buffers are pre-allocated at creation and reused across frames.
type Converter struct {
	width  int
	height int
	frame  I420Frame
}

// NewConverter pre-allocates I420 buffers for the given resolution.
func NewConverter(width, height int) *Converter {
	ySize := width * height
	uvSize := (width / 2) * (height / 2)
	return &Converter{
		width:  width,
		height: height,
		frame: I420Frame{
			Y:      make([]byte, ySize),
			U:      make([]byte, uvSize),
			V:      make([]byte, uvSize),
			Width:  width,
			Height: height,
		},
	}
}

// Convert transforms RGBA pixel data (GL_RGBA byte order) to I420.
// The returned frame's buffers are owned by the Converter and valid until the next Convert call.
func (c *Converter) Convert(rgba []byte) *I420Frame {
	w := C.int(c.width)
	h := C.int(c.height)
	srcStride := C.int(c.width * 4)
	strideY := C.int(c.width)
	strideUV := C.int(c.width / 2)

	C.ABGRToI420(
		(*C.uint8_t)(unsafe.Pointer(&rgba[0])),
		srcStride,
		(*C.uint8_t)(unsafe.Pointer(&c.frame.Y[0])),
		strideY,
		(*C.uint8_t)(unsafe.Pointer(&c.frame.U[0])),
		strideUV,
		(*C.uint8_t)(unsafe.Pointer(&c.frame.V[0])),
		strideUV,
		w,
		h,
	)
	return &c.frame
}
