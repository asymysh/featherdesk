package encode

/*
#cgo LDFLAGS: -lopenh264
#include <wels/codec_api.h>
#include <wels/codec_app_def.h>
#include <wels/codec_def.h>
#include <stdlib.h>
#include <string.h>

// Opaque handle wrapping ISVCEncoder* (which is itself a pointer typedef).
typedef struct { ISVCEncoder *enc; } EncHandle;

static EncHandle* enc_create() {
    EncHandle *h = (EncHandle*)malloc(sizeof(EncHandle));
    if (!h) return NULL;
    h->enc = NULL;
    int rv = WelsCreateSVCEncoder(&h->enc);
    if (rv != 0 || h->enc == NULL) {
        free(h);
        return NULL;
    }
    return h;
}

static void enc_destroy(EncHandle *h) {
    if (h && h->enc) {
        (*h->enc)->Uninitialize(h->enc);
        WelsDestroySVCEncoder(h->enc);
    }
    free(h);
}

static int enc_get_default(EncHandle *h, SEncParamExt *param) {
    return (*h->enc)->GetDefaultParams(h->enc, param);
}

static int enc_init(EncHandle *h, SEncParamExt *param) {
    return (*h->enc)->InitializeExt(h->enc, param);
}

static int enc_encode(EncHandle *h, SSourcePicture *pic, SFrameBSInfo *info) {
    return (*h->enc)->EncodeFrame(h->enc, pic, info);
}

static int enc_force_idr(EncHandle *h) {
    return (*h->enc)->ForceIntraFrame(h->enc, true);
}

static int enc_set_option(EncHandle *h, ENCODER_OPTION opt, void *val) {
    return (*h->enc)->SetOption(h->enc, opt, val);
}
*/
import "C"
import (
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"unsafe"
)

// H264Encoder wraps OpenH264 for software H.264 encoding.
type H264Encoder struct {
	handle *C.EncHandle
	config EncoderConfig
	idr    atomic.Bool
}

// NewH264Encoder creates and initializes an OpenH264 encoder.
func NewH264Encoder(cfg EncoderConfig) (*H264Encoder, error) {
	h := C.enc_create()
	if h == nil {
		return nil, errors.New("encode: WelsCreateSVCEncoder failed")
	}

	var param C.SEncParamExt
	C.enc_get_default(h, &param)

	param.iUsageType = C.CAMERA_VIDEO_REAL_TIME
	param.iPicWidth = C.int(cfg.Width)
	param.iPicHeight = C.int(cfg.Height)
	param.fMaxFrameRate = C.float(cfg.FPS)
	param.iTemporalLayerNum = 1
	param.iSpatialLayerNum = 1
	param.iNumRefFrame = 1
	param.uiIntraPeriod = 0 // IDR only on demand
	param.iMultipleThreadIdc = 1
	param.bEnableFrameSkip = C.bool(false)
	param.bEnableDenoise = C.bool(false)
	param.bEnableBackgroundDetection = C.bool(false)
	param.bEnableAdaptiveQuant = C.bool(false)
	param.bEnableSceneChangeDetect = C.bool(false)
	param.iEntropyCodingModeFlag = 0 // CAVLC for baseline

	// Spatial layer 0
	param.sSpatialLayers[0].iVideoWidth = C.int(cfg.Width)
	param.sSpatialLayers[0].iVideoHeight = C.int(cfg.Height)
	param.sSpatialLayers[0].fFrameRate = C.float(cfg.FPS)
	param.sSpatialLayers[0].uiProfileIdc = 66 // Baseline
	param.sSpatialLayers[0].uiLevelIdc = 0    // auto
	param.sSpatialLayers[0].sSliceArgument.uiSliceMode = C.SM_SINGLE_SLICE

	if cfg.QP > 0 {
		param.iRCMode = C.RC_OFF_MODE
		param.sSpatialLayers[0].iDLayerQp = C.int(cfg.QP)
	} else {
		param.iRCMode = C.RC_BITRATE_MODE
		bitrate := C.int(cfg.BitrateBps)
		if bitrate == 0 {
			bitrate = C.int(cfg.Width * cfg.Height * cfg.FPS / 10)
		}
		param.iTargetBitrate = bitrate
		param.iMaxBitrate = bitrate * 3 / 2
		param.sSpatialLayers[0].iSpatialBitrate = bitrate
		param.sSpatialLayers[0].iMaxSpatialBitrate = bitrate * 3 / 2
	}

	ret := C.enc_init(h, &param)
	if ret != 0 {
		C.enc_destroy(h)
		return nil, fmt.Errorf("encode: InitializeExt failed: %d", ret)
	}

	videoFmt := C.int(C.videoFormatI420)
	C.enc_set_option(h, C.ENCODER_OPTION_DATAFORMAT, unsafe.Pointer(&videoFmt))

	return &H264Encoder{handle: h, config: cfg}, nil
}

// Encode converts an I420 frame to H.264 NAL units. Returns nil on skip frames.
func (e *H264Encoder) Encode(frame *I420Frame) ([][]byte, error) {
	if e.handle == nil {
		return nil, errors.New("encode: encoder closed")
	}

	if e.idr.CompareAndSwap(true, false) {
		C.enc_force_idr(e.handle)
	}

	var pinner runtime.Pinner
	pinner.Pin(&frame.Y[0])
	pinner.Pin(&frame.U[0])
	pinner.Pin(&frame.V[0])
	defer pinner.Unpin()

	var pic C.SSourcePicture
	C.memset(unsafe.Pointer(&pic), 0, C.sizeof_SSourcePicture)
	pic.iColorFormat = C.videoFormatI420
	pic.iPicWidth = C.int(frame.Width)
	pic.iPicHeight = C.int(frame.Height)
	pic.iStride[0] = C.int(frame.Width)
	pic.iStride[1] = C.int(frame.Width / 2)
	pic.iStride[2] = C.int(frame.Width / 2)
	pic.pData[0] = (*C.uchar)(unsafe.Pointer(&frame.Y[0]))
	pic.pData[1] = (*C.uchar)(unsafe.Pointer(&frame.U[0]))
	pic.pData[2] = (*C.uchar)(unsafe.Pointer(&frame.V[0]))

	var info C.SFrameBSInfo
	C.memset(unsafe.Pointer(&info), 0, C.sizeof_SFrameBSInfo)

	ret := C.enc_encode(e.handle, &pic, &info)
	if ret != 0 {
		return nil, fmt.Errorf("encode: EncodeFrame failed: %d", ret)
	}

	if info.eFrameType == C.videoFrameTypeSkip {
		return nil, nil
	}

	return extractNALs(&info), nil
}

// ForceKeyframe requests the next Encode call to produce an IDR frame.
func (e *H264Encoder) ForceKeyframe() {
	e.idr.Store(true)
}

// Close releases encoder resources.
func (e *H264Encoder) Close() error {
	if e.handle == nil {
		return nil
	}
	C.enc_destroy(e.handle)
	e.handle = nil
	return nil
}

func extractNALs(info *C.SFrameBSInfo) [][]byte {
	var nals [][]byte
	for i := 0; i < int(info.iLayerNum); i++ {
		layer := info.sLayerInfo[i]
		buf := layer.pBsBuf
		for j := 0; j < int(layer.iNalCount); j++ {
			nalLen := int(*(*C.int)(unsafe.Pointer(uintptr(unsafe.Pointer(layer.pNalLengthInByte)) + uintptr(j)*unsafe.Sizeof(C.int(0)))))
			nal := C.GoBytes(unsafe.Pointer(buf), C.int(nalLen))
			nals = append(nals, nal)
			buf = (*C.uchar)(unsafe.Pointer(uintptr(unsafe.Pointer(buf)) + uintptr(nalLen)))
		}
	}
	return nals
}
