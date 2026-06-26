package encode

/*
#cgo CFLAGS: -I${SRCDIR}/../../../vendor/openh264/include
#cgo LDFLAGS: -L${SRCDIR}/../../../vendor/openh264 -lopenh264

#include <string.h>
#include <wels/codec_api.h>
#include <wels/codec_app_def.h>

static ISVCEncoder *g_enc = NULL;
static SFrameBSInfo g_bsInfo;
static SSourcePicture g_pic;

int oh264_init(int w, int h, int fps, int qp, int threads) {
    int rv = WelsCreateSVCEncoder(&g_enc);
    if (rv != 0 || !g_enc) return -1;

    SEncParamBase param;
    memset(&param, 0, sizeof(param));
    param.iUsageType = CAMERA_VIDEO_REAL_TIME;
    param.iPicWidth = w;
    param.iPicHeight = h;
    param.fMaxFrameRate = (float)fps;
    param.iTargetBitrate = 5000000;
    param.iRCMode = RC_OFF_MODE;

    rv = (*g_enc)->Initialize(g_enc, &param);
    if (rv != 0) return -2;

    // Set QP
    SEncParamExt paramExt;
    memset(&paramExt, 0, sizeof(paramExt));
    (*g_enc)->GetDefaultParams(g_enc, &paramExt);
    paramExt.iUsageType = CAMERA_VIDEO_REAL_TIME;
    paramExt.iPicWidth = w;
    paramExt.iPicHeight = h;
    paramExt.fMaxFrameRate = (float)fps;
    paramExt.iTargetBitrate = 5000000;
    paramExt.iRCMode = RC_OFF_MODE;
    paramExt.iNumRefFrame = 1;
    paramExt.iMultipleThreadIdc = threads;
    paramExt.sSpatialLayers[0].sSliceArgument.uiSliceMode = (threads > 1) ? 1 : 0; // SM_FIXEDSLCNUM_SLICE or SM_SINGLE_SLICE
    paramExt.sSpatialLayers[0].sSliceArgument.uiSliceNum = threads;
    paramExt.sSpatialLayers[0].iVideoWidth = w;
    paramExt.sSpatialLayers[0].iVideoHeight = h;
    paramExt.sSpatialLayers[0].fFrameRate = (float)fps;
    paramExt.sSpatialLayers[0].iSpatialBitrate = 5000000;

    // Actually reinit with ext params
    (*g_enc)->Uninitialize(g_enc);
    rv = (*g_enc)->InitializeExt(g_enc, &paramExt);
    if (rv != 0) return -3;

    // Set fixed QP via SOption
    int videoFormat = videoFormatI420;
    (*g_enc)->SetOption(g_enc, ENCODER_OPTION_DATAFORMAT, &videoFormat);

    memset(&g_pic, 0, sizeof(g_pic));
    g_pic.iPicWidth = w;
    g_pic.iPicHeight = h;
    g_pic.iColorFormat = videoFormatI420;
    g_pic.iStride[0] = w;
    g_pic.iStride[1] = w / 2;
    g_pic.iStride[2] = w / 2;

    return 0;
}

// Encode one I420 frame. Returns encoded size, or negative on error.
int oh264_encode(unsigned char *y, unsigned char *u, unsigned char *v,
                 int w, int h, unsigned char **outBuf, int *outSize) {
    g_pic.pData[0] = y;
    g_pic.pData[1] = u;
    g_pic.pData[2] = v;

    memset(&g_bsInfo, 0, sizeof(g_bsInfo));
    int rv = (*g_enc)->EncodeFrame(g_enc, &g_pic, &g_bsInfo);
    if (rv != 0) return -1;

    if (g_bsInfo.eFrameType == videoFrameTypeSkip) {
        *outSize = 0;
        return 0;
    }

    // Concatenate all NAL layers
    int totalSize = 0;
    for (int i = 0; i < g_bsInfo.iLayerNum; i++) {
        SLayerBSInfo *layer = &g_bsInfo.sLayerInfo[i];
        for (int j = 0; j < layer->iNalCount; j++) {
            totalSize += layer->pNalLengthInByte[j];
        }
    }

    *outBuf = g_bsInfo.sLayerInfo[0].pBsBuf;
    *outSize = totalSize;
    return 0;
}

void oh264_close(void) {
    if (g_enc) {
        (*g_enc)->Uninitialize(g_enc);
        WelsDestroySVCEncoder(g_enc);
        g_enc = NULL;
    }
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type OpenH264Encoder struct {
	width, height int
	threads       int
}

func NewOpenH264Encoder() *OpenH264Encoder {
	return &OpenH264Encoder{threads: 4}
}

func NewOpenH264EncoderThreads(threads int) *OpenH264Encoder {
	return &OpenH264Encoder{threads: threads}
}

func (e *OpenH264Encoder) Name() string { return fmt.Sprintf("openh264-%dt", e.threads) }
func (e *OpenH264Encoder) Codec() string { return "h264" }
func (e *OpenH264Encoder) Type() string { return "software" }

func (e *OpenH264Encoder) Init(width, height, fps int) error {
	e.width = width
	e.height = height
	rv := C.oh264_init(C.int(width), C.int(height), C.int(fps), C.int(26), C.int(e.threads))
	if rv != 0 {
		return fmt.Errorf("OpenH264 init failed: %d", rv)
	}
	return nil
}

func (e *OpenH264Encoder) Encode(y, u, v []byte, width, height int) ([]byte, error) {
	var outBuf *C.uchar
	var outSize C.int

	rv := C.oh264_encode(
		(*C.uchar)(unsafe.Pointer(&y[0])),
		(*C.uchar)(unsafe.Pointer(&u[0])),
		(*C.uchar)(unsafe.Pointer(&v[0])),
		C.int(width), C.int(height),
		&outBuf, &outSize,
	)
	if rv != 0 {
		return nil, fmt.Errorf("encode error: %d", rv)
	}
	if outSize == 0 {
		return nil, nil // skipped frame
	}

	// Copy NAL data to Go slice
	out := make([]byte, int(outSize))
	copy(out, (*[1 << 30]byte)(unsafe.Pointer(outBuf))[:outSize:outSize])
	return out, nil
}

func (e *OpenH264Encoder) Close() {
	C.oh264_close()
}
