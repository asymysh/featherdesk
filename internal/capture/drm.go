package capture

/*
#cgo pkg-config: libdrm
#include <xf86drm.h>
#include <xf86drmMode.h>
#include <fcntl.h>
#include <unistd.h>
#include <stdlib.h>
#include <string.h>

static int open_card(const char *path) {
	return open(path, O_RDWR | O_CLOEXEC);
}

static int close_card(int fd) {
	return close(fd);
}
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"
)

func OpenDRMCard() (*DRMCard, error) {
	for i := range 16 {
		path := fmt.Sprintf("/dev/dri/card%d", i)
		if _, err := os.Stat(path); err != nil {
			continue
		}

		cpath := C.CString(path)
		fd := C.open_card(cpath)
		C.free(unsafe.Pointer(cpath))
		if fd < 0 {
			continue
		}

		if C.drmSetClientCap(fd, C.DRM_CLIENT_CAP_UNIVERSAL_PLANES, 1) != 0 {
			C.close_card(fd)
			continue
		}

		card := &DRMCard{FD: int(fd), Path: path}
		if err := card.findPrimaryPlane(); err != nil {
			C.close_card(fd)
			continue
		}
		return card, nil
	}
	return nil, ErrNoCard{}
}

func (c *DRMCard) findPrimaryPlane() error {
	res := C.drmModeGetPlaneResources(C.int(c.FD))
	if res == nil {
		return ErrNoPrimaryPlane{}
	}
	defer C.drmModeFreePlaneResources(res)

	planes := unsafe.Slice(res.planes, int(res.count_planes))
	for _, pid := range planes {
		plane := C.drmModeGetPlane(C.int(c.FD), pid)
		if plane == nil {
			continue
		}

		planeType := getPlaneType(c.FD, pid)
		if planeType == PlaneTypePrimary && plane.fb_id != 0 {
			c.getCRTCDimensions(uint32(plane.crtc_id))
			C.drmModeFreePlane(plane)
			return nil
		}
		C.drmModeFreePlane(plane)
	}
	return ErrNoPrimaryPlane{}
}

func getPlaneType(fd int, planeID C.uint32_t) uint32 {
	props := C.drmModeObjectGetProperties(C.int(fd), planeID, C.DRM_MODE_OBJECT_PLANE)
	if props == nil {
		return PlaneTypeOverlay
	}
	defer C.drmModeFreeObjectProperties(props)

	propIDs := unsafe.Slice(props.props, int(props.count_props))
	propValues := unsafe.Slice(props.prop_values, int(props.count_props))

	for i := range int(props.count_props) {
		prop := C.drmModeGetProperty(C.int(fd), propIDs[i])
		if prop == nil {
			continue
		}
		name := C.GoString(&prop.name[0])
		if name == "type" {
			val := uint32(propValues[i])
			C.drmModeFreeProperty(prop)
			return val
		}
		C.drmModeFreeProperty(prop)
	}
	return PlaneTypeOverlay
}

func (c *DRMCard) getCRTCDimensions(crtcID uint32) {
	crtc := C.drmModeGetCrtc(C.int(c.FD), C.uint32_t(crtcID))
	if crtc == nil {
		return
	}
	defer C.drmModeFreeCrtc(crtc)
	c.Width = uint32(crtc.width)
	c.Height = uint32(crtc.height)
	if crtc.mode_valid != 0 {
		c.RefreshHz = uint32(crtc.mode.vrefresh)
	}
}

func (c *DRMCard) GetDMABufFD(planeID uint32) (int, error) {
	plane := C.drmModeGetPlane(C.int(c.FD), C.uint32_t(planeID))
	if plane == nil {
		return -1, fmt.Errorf("capture: failed to get plane %d", planeID)
	}
	defer C.drmModeFreePlane(plane)

	fb := C.drmModeGetFB(C.int(c.FD), plane.fb_id)
	if fb == nil {
		return -1, fmt.Errorf("capture: failed to get fb %d", plane.fb_id)
	}
	defer C.drmModeFreeFB(fb)

	var dmaFD C.int
	ret := C.drmPrimeHandleToFD(C.int(c.FD), fb.handle, C.DRM_CLOEXEC|C.DRM_RDWR, &dmaFD)
	if ret != 0 {
		return -1, fmt.Errorf("capture: drmPrimeHandleToFD failed: %d", ret)
	}
	return int(dmaFD), nil
}

func (c *DRMCard) GetFBInfo(planeID uint32) (*FBInfo, error) {
	plane := C.drmModeGetPlane(C.int(c.FD), C.uint32_t(planeID))
	if plane == nil {
		return nil, fmt.Errorf("capture: failed to get plane %d", planeID)
	}
	fbID := plane.fb_id
	C.drmModeFreePlane(plane)

	fb2 := C.drmModeGetFB2(C.int(c.FD), fbID)
	if fb2 == nil {
		return nil, fmt.Errorf("capture: drmModeGetFB2 failed for fb %d", fbID)
	}
	defer C.drmModeFreeFB2(fb2)

	var dmaFD C.int
	ret := C.drmPrimeHandleToFD(C.int(c.FD), fb2.handles[0], C.DRM_CLOEXEC|C.DRM_RDWR, &dmaFD)
	if ret != 0 {
		return nil, fmt.Errorf("capture: drmPrimeHandleToFD failed (run as root or with CAP_SYS_ADMIN)")
	}

	return &FBInfo{
		DMAFD:    int(dmaFD),
		Width:    uint32(fb2.width),
		Height:   uint32(fb2.height),
		Stride:   uint32(fb2.pitches[0]),
		Format:   uint32(fb2.pixel_format),
		Modifier: uint64(fb2.modifier),
	}, nil
}

func (c *DRMCard) FindPrimaryPlaneID() (uint32, error) {
	res := C.drmModeGetPlaneResources(C.int(c.FD))
	if res == nil {
		return 0, ErrNoPrimaryPlane{}
	}
	defer C.drmModeFreePlaneResources(res)

	planes := unsafe.Slice(res.planes, int(res.count_planes))
	for _, pid := range planes {
		plane := C.drmModeGetPlane(C.int(c.FD), pid)
		if plane == nil {
			continue
		}
		planeType := getPlaneType(c.FD, pid)
		hasFB := plane.fb_id != 0
		C.drmModeFreePlane(plane)
		if planeType == PlaneTypePrimary && hasFB {
			return uint32(pid), nil
		}
	}
	return 0, ErrNoPrimaryPlane{}
}

func (c *DRMCard) Close() error {
	if c.FD >= 0 {
		C.close_card(C.int(c.FD))
		c.FD = -1
	}
	return nil
}
