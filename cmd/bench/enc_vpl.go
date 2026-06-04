package main

/*
#cgo pkg-config: vpl
#cgo LDFLAGS: -ldl
#include <stdint.h>
#include <vpl/mfxvideo.h>
#include <vpl/mfxdispatcher.h>
#include <vpl/mfxstructures.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    mfxLoader loader;
    mfxSession session;
    mfxVideoParam param;
    mfxFrameSurface1 *surface;
    mfxBitstream bs;
    int width;
    int height;
} VPLENC;

static VPLENC* vpl_create(int w, int h, int fps, int qp, int target_usage) {
    VPLENC *e = (VPLENC*)calloc(1, sizeof(VPLENC));
    e->width = w;
    e->height = h;

    e->loader = MFXLoad();
    if (!e->loader) { free(e); return NULL; }

    mfxConfig cfg = MFXCreateConfig(e->loader);
    mfxVariant val;

    // Request H.264 encoder
    val.Type = MFX_VARIANT_TYPE_U32;
    val.Data.U32 = MFX_CODEC_AVC;
    MFXSetConfigFilterProperty(cfg, (const mfxU8*)"mfxImplDescription.mfxEncoderDescription.encoder.CodecID", val);

    // Request hardware impl
    val.Data.U32 = MFX_IMPL_TYPE_HARDWARE;
    MFXSetConfigFilterProperty(cfg, (const mfxU8*)"mfxImplDescription.Impl", val);

    mfxStatus st = MFXCreateSession(e->loader, 0, &e->session);
    if (st != MFX_ERR_NONE) {
        MFXUnload(e->loader);
        free(e);
        return NULL;
    }

    memset(&e->param, 0, sizeof(mfxVideoParam));
    e->param.mfx.CodecId = MFX_CODEC_AVC;
    e->param.mfx.TargetUsage = target_usage;
    e->param.mfx.RateControlMethod = MFX_RATECONTROL_CQP;
    e->param.mfx.QPI = qp;
    e->param.mfx.QPP = qp;
    e->param.mfx.QPB = qp;
    e->param.mfx.FrameInfo.FourCC = MFX_FOURCC_NV12;
    e->param.mfx.FrameInfo.ChromaFormat = MFX_CHROMAFORMAT_YUV420;
    e->param.mfx.FrameInfo.Width = (w + 15) & ~15;
    e->param.mfx.FrameInfo.Height = (h + 15) & ~15;
    e->param.mfx.FrameInfo.CropW = w;
    e->param.mfx.FrameInfo.CropH = h;
    e->param.mfx.FrameInfo.FrameRateExtN = fps;
    e->param.mfx.FrameInfo.FrameRateExtD = 1;
    e->param.mfx.GopPicSize = 0;
    e->param.mfx.GopRefDist = 1;
    e->param.mfx.NumRefFrame = 1;
    e->param.mfx.CodecProfile = MFX_PROFILE_AVC_HIGH;
    e->param.IOPattern = MFX_IOPATTERN_IN_SYSTEM_MEMORY;

    st = MFXVideoENCODE_Init(e->session, &e->param);
    if (st != MFX_ERR_NONE && st != MFX_WRN_PARTIAL_ACCELERATION) {
        MFXClose(e->session);
        MFXUnload(e->loader);
        free(e);
        return NULL;
    }

    // Allocate surface
    int aligned_w = (w + 15) & ~15;
    int aligned_h = (h + 15) & ~15;
    e->surface = (mfxFrameSurface1*)calloc(1, sizeof(mfxFrameSurface1));
    e->surface->Info = e->param.mfx.FrameInfo;
    int buf_size = aligned_w * aligned_h * 3 / 2;
    e->surface->Data.Y = (mfxU8*)calloc(1, buf_size);
    e->surface->Data.UV = e->surface->Data.Y + aligned_w * aligned_h;
    e->surface->Data.Pitch = aligned_w;

    // Allocate bitstream
    e->bs.MaxLength = w * h * 3 / 2;
    e->bs.Data = (mfxU8*)malloc(e->bs.MaxLength);

    return e;
}

static int vpl_encode(VPLENC *e, unsigned char *y, unsigned char *u, unsigned char *v,
                      int w, int h, unsigned char **out, int *outLen) {
    int aligned_w = (w + 15) & ~15;

    // Copy Y
    for (int row = 0; row < h; row++) {
        memcpy(e->surface->Data.Y + row * aligned_w, y + row * w, w);
    }

    // Interleave UV to NV12
    int uv_w = w / 2, uv_h = h / 2;
    for (int row = 0; row < uv_h; row++) {
        mfxU8 *dst = e->surface->Data.UV + row * aligned_w;
        unsigned char *u_row = u + row * uv_w;
        unsigned char *v_row = v + row * uv_w;
        for (int col = 0; col < uv_w; col++) {
            dst[col * 2] = u_row[col];
            dst[col * 2 + 1] = v_row[col];
        }
    }

    e->bs.DataOffset = 0;
    e->bs.DataLength = 0;

    mfxSyncPoint syncp;
    mfxStatus st = MFXVideoENCODE_EncodeFrameAsync(e->session, NULL, e->surface, &e->bs, &syncp);
    if (st == MFX_ERR_MORE_DATA) {
        *outLen = 0;
        return 0;
    }
    if (st != MFX_ERR_NONE && st != MFX_WRN_DEVICE_BUSY) {
        return -1;
    }

    if (syncp) {
        st = MFXVideoCORE_SyncOperation(e->session, syncp, 5000);
        if (st != MFX_ERR_NONE) return -2;
    }

    if (e->bs.DataLength > 0) {
        *out = e->bs.Data + e->bs.DataOffset;
        *outLen = e->bs.DataLength;
    } else {
        *outLen = 0;
    }
    return 0;
}

static void vpl_free(VPLENC *e) {
    if (!e) return;
    MFXVideoENCODE_Close(e->session);
    MFXClose(e->session);
    MFXUnload(e->loader);
    if (e->surface) {
        free(e->surface->Data.Y);
        free(e->surface);
    }
    free(e->bs.Data);
    free(e);
}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

type VPLBenchEnc struct {
	h       *C.VPLENC
	encName string
}

func NewVPLBench(name string, w, h, qp, targetUsage int) (*VPLBenchEnc, error) {
	hd := C.vpl_create(C.int(w), C.int(h), 30, C.int(qp), C.int(targetUsage))
	if hd == nil {
		return nil, fmt.Errorf("%s: VPL init failed (MFXCreateSession or ENCODE_Init)", name)
	}
	return &VPLBenchEnc{h: hd, encName: name}, nil
}

func (e *VPLBenchEnc) Name() string    { return e.encName }
func (e *VPLBenchEnc) Library() string  { return "intel-vpl" }
func (e *VPLBenchEnc) Codec() string    { return "h264" }

func (e *VPLBenchEnc) Encode(y, u, v []byte, w, h int) ([]byte, error) {
	var out *C.uchar
	var outLen C.int
	rv := C.vpl_encode(e.h, (*C.uchar)(unsafe.Pointer(&y[0])),
		(*C.uchar)(unsafe.Pointer(&u[0])),
		(*C.uchar)(unsafe.Pointer(&v[0])),
		C.int(w), C.int(h), &out, &outLen)
	if rv != 0 {
		return nil, fmt.Errorf("vpl encode failed: %d", rv)
	}
	if outLen == 0 {
		return nil, nil
	}
	return C.GoBytes(unsafe.Pointer(out), outLen), nil
}

func (e *VPLBenchEnc) Close() { C.vpl_free(e.h) }
