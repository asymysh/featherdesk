package encode

/*
#cgo pkg-config: x264
#cgo CFLAGS: -DX264_API_IMPORTS
#include <stdint.h>
#include <x264.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    x264_t *enc;
    x264_picture_t pic_in;
    x264_picture_t pic_out;
    x264_nal_t *nals;
    int nal_count;
} X264H;

static X264H* x264_enc_create(int w, int h, int fps, int qp) {
    X264H *hd = (X264H*)calloc(1, sizeof(X264H));

    x264_param_t param;
    x264_param_default_preset(&param, "ultrafast", "zerolatency");

    param.i_width = w;
    param.i_height = h;
    param.i_fps_num = fps;
    param.i_fps_den = 1;
    param.i_threads = 1;
    param.i_keyint_max = 0;  // IDR only on demand
    param.i_keyint_min = 0;
    param.b_repeat_headers = 1;
    param.b_annexb = 1;
    param.i_csp = X264_CSP_I420;
    param.i_log_level = X264_LOG_NONE;

    if (qp > 0) {
        param.rc.i_rc_method = X264_RC_CQP;
        param.rc.i_qp_constant = qp;
    } else {
        param.rc.i_rc_method = X264_RC_CRF;
        param.rc.f_rf_constant = 26.0;
    }

    x264_param_apply_profile(&param, "baseline");

    hd->enc = x264_encoder_open(&param);
    if (!hd->enc) { free(hd); return NULL; }

    x264_picture_init(&hd->pic_in);
    hd->pic_in.img.i_csp = X264_CSP_I420;
    hd->pic_in.img.i_plane = 3;
    hd->pic_in.img.i_stride[0] = w;
    hd->pic_in.img.i_stride[1] = w / 2;
    hd->pic_in.img.i_stride[2] = w / 2;

    return hd;
}

static int x264_enc_encode(X264H *hd, unsigned char *y, unsigned char *u, unsigned char *v,
                           int w, int h, int force_idr,
                           unsigned char **out, int *outLen) {
    hd->pic_in.img.plane[0] = (uint8_t*)y;
    hd->pic_in.img.plane[1] = (uint8_t*)u;
    hd->pic_in.img.plane[2] = (uint8_t*)v;
    hd->pic_in.i_pts++;

    if (force_idr) {
        hd->pic_in.i_type = X264_TYPE_IDR;
    } else {
        hd->pic_in.i_type = X264_TYPE_AUTO;
    }

    int frame_size = x264_encoder_encode(hd->enc, &hd->nals, &hd->nal_count,
                                         &hd->pic_in, &hd->pic_out);
    if (frame_size < 0) return -1;
    if (frame_size == 0) { *outLen = 0; return 0; }

    *out = (unsigned char*)hd->nals[0].p_payload;
    *outLen = frame_size;
    return 0;
}

static void x264_enc_close(X264H *hd) {
    if (!hd) return;
    if (hd->enc) x264_encoder_close(hd->enc);
    free(hd);
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

type X264Encoder struct {
	handle *C.X264H
	config EncoderConfig
	idr    atomic.Bool
}

func NewX264Encoder(cfg EncoderConfig) (*X264Encoder, error) {
	h := C.x264_enc_create(C.int(cfg.Width), C.int(cfg.Height), C.int(cfg.FPS), C.int(cfg.QP))
	if h == nil {
		return nil, errors.New("encode: x264_encoder_open failed")
	}
	return &X264Encoder{handle: h, config: cfg}, nil
}

func (e *X264Encoder) Encode(frame *I420Frame) ([][]byte, error) {
	if e.handle == nil {
		return nil, errors.New("encode: encoder closed")
	}

	forceIDR := 0
	if e.idr.CompareAndSwap(true, false) {
		forceIDR = 1
	}

	var pinner runtime.Pinner
	pinner.Pin(&frame.Y[0])
	pinner.Pin(&frame.U[0])
	pinner.Pin(&frame.V[0])
	defer pinner.Unpin()

	var out *C.uchar
	var outLen C.int

	rv := C.x264_enc_encode(e.handle,
		(*C.uchar)(unsafe.Pointer(&frame.Y[0])),
		(*C.uchar)(unsafe.Pointer(&frame.U[0])),
		(*C.uchar)(unsafe.Pointer(&frame.V[0])),
		C.int(frame.Width), C.int(frame.Height), C.int(forceIDR),
		&out, &outLen)
	if rv != 0 {
		return nil, fmt.Errorf("encode: x264_encoder_encode failed: %d", rv)
	}
	if outLen == 0 {
		return nil, nil
	}

	nal := C.GoBytes(unsafe.Pointer(out), outLen)
	return [][]byte{nal}, nil
}

func (e *X264Encoder) ForceKeyframe() {
	e.idr.Store(true)
}

func (e *X264Encoder) Close() error {
	if e.handle == nil {
		return nil
	}
	C.x264_enc_close(e.handle)
	e.handle = nil
	return nil
}
