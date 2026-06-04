package main

/*
#cgo LDFLAGS: -lopenh264
#include <wels/codec_api.h>
#include <wels/codec_app_def.h>
#include <wels/codec_def.h>
#include <stdlib.h>
#include <string.h>

typedef struct { ISVCEncoder *enc; } OH264H;

static OH264H* oh264_create(int w, int h, int fps, int qp) {
    OH264H *hd = (OH264H*)malloc(sizeof(OH264H));
    hd->enc = NULL;
    if (WelsCreateSVCEncoder(&hd->enc) != 0 || !hd->enc) { free(hd); return NULL; }

    SEncParamExt p;
    (*hd->enc)->GetDefaultParams(hd->enc, &p);
    p.iUsageType = CAMERA_VIDEO_REAL_TIME;
    p.iPicWidth = w; p.iPicHeight = h;
    p.fMaxFrameRate = (float)fps;
    p.iTemporalLayerNum = 1; p.iSpatialLayerNum = 1;
    p.iNumRefFrame = 1; p.uiIntraPeriod = 30;
    p.iMultipleThreadIdc = 1;
    p.bEnableFrameSkip = false; p.bEnableDenoise = false;
    p.bEnableBackgroundDetection = false; p.bEnableAdaptiveQuant = false;
    p.bEnableSceneChangeDetect = false; p.iEntropyCodingModeFlag = 0;
    p.iRCMode = RC_OFF_MODE;
    p.sSpatialLayers[0].iVideoWidth = w; p.sSpatialLayers[0].iVideoHeight = h;
    p.sSpatialLayers[0].fFrameRate = (float)fps;
    p.sSpatialLayers[0].uiProfileIdc = 66;
    p.sSpatialLayers[0].sSliceArgument.uiSliceMode = SM_SINGLE_SLICE;
    p.sSpatialLayers[0].iDLayerQp = qp;

    if ((*hd->enc)->InitializeExt(hd->enc, &p) != 0) {
        WelsDestroySVCEncoder(hd->enc); free(hd); return NULL;
    }
    int fmt = videoFormatI420;
    (*hd->enc)->SetOption(hd->enc, ENCODER_OPTION_DATAFORMAT, &fmt);
    return hd;
}

static int oh264_enc(OH264H *hd, unsigned char *y, unsigned char *u, unsigned char *v,
                     int w, int h, unsigned char **out, int *outLen) {
    SSourcePicture pic; memset(&pic, 0, sizeof(pic));
    pic.iColorFormat = videoFormatI420;
    pic.iPicWidth = w; pic.iPicHeight = h;
    pic.iStride[0] = w; pic.iStride[1] = w/2; pic.iStride[2] = w/2;
    pic.pData[0] = y; pic.pData[1] = u; pic.pData[2] = v;
    SFrameBSInfo info; memset(&info, 0, sizeof(info));
    if ((*hd->enc)->EncodeFrame(hd->enc, &pic, &info) != 0) return -1;
    if (info.eFrameType == videoFrameTypeSkip) { *outLen = 0; return 0; }
    int total = 0;
    for (int i = 0; i < info.iLayerNum; i++)
        for (int j = 0; j < info.sLayerInfo[i].iNalCount; j++)
            total += info.sLayerInfo[i].pNalLengthInByte[j];
    *out = info.sLayerInfo[0].pBsBuf; *outLen = total;
    return 0;
}

static void oh264_free(OH264H *hd) {
    if (hd && hd->enc) { (*hd->enc)->Uninitialize(hd->enc); WelsDestroySVCEncoder(hd->enc); }
    free(hd);
}
*/
import "C"
import (
	"fmt"
	"runtime"
	"unsafe"
)

type OpenH264BenchEnc struct {
	h  *C.OH264H
	qp int
}

func NewOpenH264Bench(w, h, qp int) (*OpenH264BenchEnc, error) {
	hd := C.oh264_create(C.int(w), C.int(h), 30, C.int(qp))
	if hd == nil {
		return nil, fmt.Errorf("openh264 init failed")
	}
	return &OpenH264BenchEnc{h: hd, qp: qp}, nil
}

func (e *OpenH264BenchEnc) Name() string    { return fmt.Sprintf("openh264-qp%d", e.qp) }
func (e *OpenH264BenchEnc) Library() string  { return "openh264-cgo" }
func (e *OpenH264BenchEnc) Codec() string    { return "h264" }

func (e *OpenH264BenchEnc) Encode(y, u, v []byte, w, h int) ([]byte, error) {
	var pinner runtime.Pinner
	pinner.Pin(&y[0]); pinner.Pin(&u[0]); pinner.Pin(&v[0])
	defer pinner.Unpin()
	var out *C.uchar; var outLen C.int
	if C.oh264_enc(e.h, (*C.uchar)(unsafe.Pointer(&y[0])), (*C.uchar)(unsafe.Pointer(&u[0])),
		(*C.uchar)(unsafe.Pointer(&v[0])), C.int(w), C.int(h), &out, &outLen) != 0 {
		return nil, fmt.Errorf("encode failed")
	}
	if outLen == 0 { return nil, nil }
	return C.GoBytes(unsafe.Pointer(out), outLen), nil
}

func (e *OpenH264BenchEnc) Close() { C.oh264_free(e.h) }
