package capture

/*
#cgo pkg-config: libdrm
#include <xf86drm.h>
#include <xf86drmMode.h>

static uint32_t get_plane_fb_id(int fd, uint32_t plane_id) {
	drmModePlanePtr plane = drmModeGetPlane(fd, plane_id);
	if (!plane) return 0;
	uint32_t fb_id = plane->fb_id;
	drmModeFreePlane(plane);
	return fb_id;
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
	planeID   uint32
	lastFBID  uint32
	targetFPS int
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

	dmaFD, err := card.GetDMABufFD(pid)
	if err != nil {
		egl.Close()
		card.Close()
		return nil, err
	}

	stride := int(card.Width) * 4
	if err := egl.ImportDMABuf(dmaFD, int(card.Width), int(card.Height), stride, 0x34325241); err != nil {
		egl.Close()
		card.Close()
		return nil, err
	}

	return &KMSCapturer{
		card:      card,
		egl:       egl,
		planeID:   pid,
		targetFPS: fps,
		ctx:       ctx,
	}, nil
}

func (c *KMSCapturer) NextFrame() (*Frame, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}

	start := time.Now()

	currentFB := uint32(C.get_plane_fb_id(C.int(c.card.FD), C.uint32_t(c.planeID)))
	if currentFB == 0 {
		return nil, fmt.Errorf("capture: plane %d has no framebuffer", c.planeID)
	}

	if currentFB != c.lastFBID {
		c.lastFBID = currentFB
		dmaFD, err := c.card.GetDMABufFD(c.planeID)
		if err != nil {
			return nil, err
		}
		stride := int(c.card.Width) * 4
		if err := c.egl.ImportDMABuf(dmaFD, int(c.card.Width), int(c.card.Height), stride, 0x34325241); err != nil {
			return nil, err
		}
	}

	pixels := c.egl.ReadPixels()

	frame := &Frame{
		Data:      pixels,
		Width:     c.card.Width,
		Height:    c.card.Height,
		Timestamp: uint64(time.Now().UnixMilli()),
	}

	elapsed := time.Since(start)
	budget := time.Second / time.Duration(c.targetFPS)
	if elapsed < budget {
		remaining := budget - elapsed
		timer := time.NewTimer(remaining)
		select {
		case <-timer.C:
		case <-c.ctx.Done():
			timer.Stop()
			return frame, c.ctx.Err()
		}
	}

	return frame, nil
}

func (c *KMSCapturer) Close() error {
	if c.egl != nil {
		c.egl.Close()
	}
	if c.card != nil {
		c.card.Close()
	}
	return nil
}
