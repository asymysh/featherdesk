package capture

/*
#cgo pkg-config: libdrm
#include <xf86drm.h>
#include <xf86drmMode.h>
#include <unistd.h>

static uint32_t get_plane_fb_id(int fd, uint32_t plane_id) {
	drmModePlanePtr plane = drmModeGetPlane(fd, plane_id);
	if (!plane) return 0;
	uint32_t fb_id = plane->fb_id;
	drmModeFreePlane(plane);
	return fb_id;
}

static void close_fd(int fd) {
	close(fd);
}
*/
import "C"

import (
	"context"
	"fmt"
	"time"
)

type KMSCapturer struct {
	card      *DRMCard
	egl       *EGLState
	cursor    *CursorState
	planeID   uint32
	lastFBID  uint32
	lastDMAFD int
	ctx       context.Context
}

func NewKMSCapturer(ctx context.Context, fps int) (*KMSCapturer, error) {
	card, err := OpenDRMCard()
	if err != nil {
		return nil, err
	}

	egl, err := NewEGLState(card.FD)
	if err != nil {
		card.Close()
		return nil, err
	}

	pid, err := card.FindPrimaryPlaneID()
	if err != nil {
		egl.Close()
		card.Close()
		return nil, err
	}

	fbInfo, err := card.GetFBInfo(pid)
	if err != nil {
		egl.Close()
		card.Close()
		return nil, err
	}

	if err := egl.ImportDMABuf(fbInfo.DMAFD, int(fbInfo.Width), int(fbInfo.Height), int(fbInfo.Stride), fbInfo.Format); err != nil {
		C.close_fd(C.int(fbInfo.DMAFD))
		egl.Close()
		card.Close()
		return nil, err
	}

	currentFB := uint32(C.get_plane_fb_id(C.int(card.FD), C.uint32_t(pid)))

	cursor := NewCursorState(card)

	return &KMSCapturer{
		card:      card,
		egl:       egl,
		cursor:    cursor,
		planeID:   pid,
		lastFBID:  currentFB,
		lastDMAFD: fbInfo.DMAFD,
		ctx:       ctx,
	}, nil
}

func (c *KMSCapturer) NextFrame() (*Frame, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}

	currentFB := uint32(C.get_plane_fb_id(C.int(c.card.FD), C.uint32_t(c.planeID)))
	if currentFB == 0 {
		return nil, fmt.Errorf("capture: plane %d has no framebuffer", c.planeID)
	}

	if currentFB != c.lastFBID {
		c.lastFBID = currentFB
		fbInfo, err := c.card.GetFBInfo(c.planeID)
		if err != nil {
			return nil, err
		}
		if c.lastDMAFD >= 0 {
			C.close_fd(C.int(c.lastDMAFD))
		}
		c.lastDMAFD = fbInfo.DMAFD
		if err := c.egl.ImportDMABuf(fbInfo.DMAFD, int(fbInfo.Width), int(fbInfo.Height), int(fbInfo.Stride), fbInfo.Format); err != nil {
			return nil, err
		}
	}

	pixels := c.egl.ReadPixels()

	cursorFrame := c.cursor.Capture(c.card.FD)
	if cursorFrame != nil {
		BlendCursor(pixels, int(c.card.Width), int(c.card.Height), cursorFrame)
	}

	return &Frame{
		Data:      pixels,
		Width:     c.card.Width,
		Height:    c.card.Height,
		Timestamp: uint64(time.Now().UnixMilli()),
	}, nil
}

func (c *KMSCapturer) Close() error {
	if c.lastDMAFD >= 0 {
		C.close_fd(C.int(c.lastDMAFD))
		c.lastDMAFD = -1
	}
	c.cursor.Close()
	if c.egl != nil {
		c.egl.Close()
	}
	if c.card != nil {
		c.card.Close()
	}
	return nil
}
