package capture

/*
#cgo pkg-config: libdrm
#include <xf86drm.h>
#include <xf86drmMode.h>
#include <unistd.h>
#include <fcntl.h>

typedef struct {
	int valid;
	int x, y;
	uint32_t fb_id;
	uint32_t width, height;
	uint32_t stride;
	uint32_t format;
	uint64_t modifier;
	int dma_fd;
} cursor_info_t;

static cursor_info_t get_cursor_info(int card_fd, uint32_t cursor_plane_id) {
	cursor_info_t info = {0};
	drmModePlanePtr plane = drmModeGetPlane(card_fd, cursor_plane_id);
	if (!plane) return info;

	if (plane->fb_id == 0) {
		drmModeFreePlane(plane);
		return info;
	}

	info.x = (int)plane->crtc_x;
	info.y = (int)plane->crtc_y;
	info.fb_id = plane->fb_id;
	drmModeFreePlane(plane);

	drmModeFB2Ptr fb2 = drmModeGetFB2(card_fd, info.fb_id);
	if (!fb2) return info;

	info.width = fb2->width;
	info.height = fb2->height;
	info.stride = fb2->pitches[0];
	info.format = fb2->pixel_format;
	info.modifier = fb2->modifier;

	int dma_fd = -1;
	int ret = drmPrimeHandleToFD(card_fd, fb2->handles[0], DRM_CLOEXEC | DRM_RDWR, &dma_fd);
	drmModeFreeFB2(fb2);

	if (ret != 0) return info;

	info.dma_fd = dma_fd;
	info.valid = 1;
	return info;
}
*/
import "C"

import "unsafe"

type CursorState struct {
	planeID    uint32
	lastFBID   uint32
	lastDMAFD  int
	egl        *EGLState
	pixels     []byte
	width      int
	height     int
}

func NewCursorState(card *DRMCard) *CursorState {
	pid := findCursorPlane(card)
	if pid == 0 {
		return nil
	}

	egl, err := NewEGLState(card.FD)
	if err != nil {
		return nil
	}

	return &CursorState{
		planeID:   pid,
		lastDMAFD: -1,
		egl:       egl,
	}
}

func findCursorPlane(card *DRMCard) uint32 {
	res := C.drmModeGetPlaneResources(C.int(card.FD))
	if res == nil {
		return 0
	}
	defer C.drmModeFreePlaneResources(res)

	planes := unsafe.Slice(res.planes, int(res.count_planes))
	for _, pid := range planes {
		if getPlaneType(card.FD, pid) == PlaneTypeCursor {
			plane := C.drmModeGetPlane(C.int(card.FD), pid)
			if plane != nil {
				hasFB := plane.fb_id != 0
				C.drmModeFreePlane(plane)
				if hasFB {
					return uint32(pid)
				}
			}
		}
	}
	return 0
}

type CursorFrame struct {
	Pixels []byte
	X, Y   int
	W, H   int
}

func (cs *CursorState) Capture(cardFD int) *CursorFrame {
	if cs == nil {
		return nil
	}

	info := C.get_cursor_info(C.int(cardFD), C.uint32_t(cs.planeID))
	if info.valid == 0 {
		return nil
	}

	cs.egl.MakeCurrent()

	if uint32(info.fb_id) != cs.lastFBID {
		cs.lastFBID = uint32(info.fb_id)
		if cs.lastDMAFD >= 0 {
			C.close(C.int(cs.lastDMAFD))
		}
		cs.lastDMAFD = int(info.dma_fd)
		cs.width = int(info.width)
		cs.height = int(info.height)

		if err := cs.egl.ImportDMABuf(cs.lastDMAFD, cs.width, cs.height, int(info.stride), uint32(info.format), uint64(info.modifier)); err != nil {
			return nil
		}
	} else {
		C.close(C.int(info.dma_fd))
	}

	cs.pixels = cs.egl.ReadPixels()

	return &CursorFrame{
		Pixels: cs.pixels,
		X:      int(info.x),
		Y:      int(info.y),
		W:      cs.width,
		H:      cs.height,
	}
}

func (cs *CursorState) Close() {
	if cs == nil {
		return
	}
	if cs.lastDMAFD >= 0 {
		C.close(C.int(cs.lastDMAFD))
	}
	if cs.egl != nil {
		cs.egl.Close()
	}
}

// BlendCursor alpha-composites the cursor (RGBA) onto the frame buffer (RGBA).
func BlendCursor(frame []byte, frameW, frameH int, cursor *CursorFrame) {
	if cursor == nil {
		return
	}
	for cy := 0; cy < cursor.H; cy++ {
		fy := cursor.Y + cy
		if fy < 0 || fy >= frameH {
			continue
		}
		for cx := 0; cx < cursor.W; cx++ {
			fx := cursor.X + cx
			if fx < 0 || fx >= frameW {
				continue
			}

			srcOff := (cy*cursor.W + cx) * 4
			dstOff := (fy*frameW + fx) * 4

			sa := uint32(cursor.Pixels[srcOff+3])
			if sa == 0 {
				continue
			}
			if sa == 255 {
				frame[dstOff+0] = cursor.Pixels[srcOff+0]
				frame[dstOff+1] = cursor.Pixels[srcOff+1]
				frame[dstOff+2] = cursor.Pixels[srcOff+2]
				frame[dstOff+3] = 255
				continue
			}

			da := 255 - sa
			frame[dstOff+0] = byte((sa*uint32(cursor.Pixels[srcOff+0]) + da*uint32(frame[dstOff+0])) / 255)
			frame[dstOff+1] = byte((sa*uint32(cursor.Pixels[srcOff+1]) + da*uint32(frame[dstOff+1])) / 255)
			frame[dstOff+2] = byte((sa*uint32(cursor.Pixels[srcOff+2]) + da*uint32(frame[dstOff+2])) / 255)
			frame[dstOff+3] = 255
		}
	}
}
