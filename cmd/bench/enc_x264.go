package main

/*
#cgo pkg-config: x264
#include <stdint.h>
#include <x264.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    x264_t *enc;
    x264_picture_t pic_in, pic_out;
    int w, h;
} X264H;

static X264H* x264_create(int w, int h, int fps, const char *preset, const char *tune, int threads) {
    X264H *hd = (X264H*)calloc(1, sizeof(X264H));
    x264_param_t p;
    x264_param_default_preset(&p, preset, tune);
    p.i_width = w; p.i_height = h;
    p.i_fps_num = fps; p.i_fps_den = 1;
    p.i_csp = X264_CSP_I420;
    p.i_threads = threads; p.i_keyint_max = 30;
    p.i_bframe = 0; p.b_repeat_headers = 1;
    p.rc.i_rc_method = X264_RC_CQP;
    p.rc.i_qp_constant = 26; p.i_log_level = X264_LOG_NONE;
    x264_param_apply_profile(&p, "baseline");
    hd->enc = x264_encoder_open(&p);
    if (!hd->enc) { free(hd); return NULL; }
    x264_picture_alloc(&hd->pic_in, X264_CSP_I420, w, h);
    hd->w = w; hd->h = h;
    return hd;
}

static int x264_enc(X264H *hd, unsigned char *y, unsigned char *u, unsigned char *v,
                    unsigned char **out, int *outLen) {
    memcpy(hd->pic_in.img.plane[0], y, hd->w * hd->h);
    memcpy(hd->pic_in.img.plane[1], u, (hd->w/2) * (hd->h/2));
    memcpy(hd->pic_in.img.plane[2], v, (hd->w/2) * (hd->h/2));
    x264_nal_t *nals; int i_nal;
    int sz = x264_encoder_encode(hd->enc, &nals, &i_nal, &hd->pic_in, &hd->pic_out);
    if (sz < 0) return -1;
    if (sz == 0) { *outLen = 0; return 0; }
    *out = nals[0].p_payload; *outLen = sz;
    hd->pic_in.i_pts++;
    return 0;
}

static void x264_free_h(X264H *hd) {
    if (hd) { if (hd->enc) x264_encoder_close(hd->enc); x264_picture_clean(&hd->pic_in); free(hd); }
}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

type X264BenchEnc struct {
	h                    *C.X264H
	preset, tune         string
	threads              int
}

func NewX264Bench(w, h int, preset, tune string, threads int) (*X264BenchEnc, error) {
	cp := C.CString(preset); ct := C.CString(tune)
	defer C.free(unsafe.Pointer(cp)); defer C.free(unsafe.Pointer(ct))
	hd := C.x264_create(C.int(w), C.int(h), 30, cp, ct, C.int(threads))
	if hd == nil {
		return nil, fmt.Errorf("x264 init failed")
	}
	return &X264BenchEnc{h: hd, preset: preset, tune: tune, threads: threads}, nil
}

func (e *X264BenchEnc) Name() string {
	return fmt.Sprintf("x264-%s-%s-%dt", e.preset, e.tune[:2], e.threads)
}
func (e *X264BenchEnc) Library() string { return "x264-cgo" }
func (e *X264BenchEnc) Codec() string   { return "h264" }

func (e *X264BenchEnc) Encode(y, u, v []byte, w, h int) ([]byte, error) {
	var out *C.uchar; var outLen C.int
	if C.x264_enc(e.h, (*C.uchar)(unsafe.Pointer(&y[0])), (*C.uchar)(unsafe.Pointer(&u[0])),
		(*C.uchar)(unsafe.Pointer(&v[0])), &out, &outLen) != 0 {
		return nil, fmt.Errorf("x264 encode failed")
	}
	if outLen == 0 { return nil, nil }
	return C.GoBytes(unsafe.Pointer(out), outLen), nil
}

func (e *X264BenchEnc) Close() { C.x264_free_h(e.h) }
