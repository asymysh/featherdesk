package main

/*
#cgo pkg-config: libavcodec libavutil
#include <libavcodec/avcodec.h>
#include <libavutil/frame.h>
#include <libavutil/imgutils.h>
#include <libavutil/opt.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    const AVCodec *codec;
    AVCodecContext *ctx;
    AVFrame *frame;
    AVPacket *pkt;
} LAH;

static LAH* la_create(int w, int h, int fps, const char *codec_name,
                      const char *k1, const char *v1,
                      const char *k2, const char *v2,
                      const char *k3, const char *v3,
                      int threads) {
    LAH *hd = (LAH*)calloc(1, sizeof(LAH));
    hd->codec = avcodec_find_encoder_by_name(codec_name);
    if (!hd->codec) { free(hd); return NULL; }

    hd->ctx = avcodec_alloc_context3(hd->codec);
    hd->ctx->width = w; hd->ctx->height = h;
    hd->ctx->time_base = (AVRational){1, fps};
    hd->ctx->framerate = (AVRational){fps, 1};
    hd->ctx->gop_size = 30; hd->ctx->max_b_frames = 0;
    hd->ctx->thread_count = threads;
    hd->ctx->pix_fmt = AV_PIX_FMT_YUV420P;

    if (k1 && strlen(k1) > 0) av_opt_set(hd->ctx->priv_data, k1, v1, 0);
    if (k2 && strlen(k2) > 0) av_opt_set(hd->ctx->priv_data, k2, v2, 0);
    if (k3 && strlen(k3) > 0) av_opt_set(hd->ctx->priv_data, k3, v3, 0);

    if (avcodec_open2(hd->ctx, hd->codec, NULL) < 0) {
        avcodec_free_context(&hd->ctx); free(hd); return NULL;
    }

    hd->frame = av_frame_alloc();
    hd->frame->format = AV_PIX_FMT_YUV420P;
    hd->frame->width = w; hd->frame->height = h;
    av_frame_get_buffer(hd->frame, 0);
    hd->pkt = av_packet_alloc();
    return hd;
}

static int la_encode(LAH *hd, unsigned char *y, unsigned char *u, unsigned char *v,
                     int w, int h, unsigned char **out, int *outLen) {
    av_frame_make_writable(hd->frame);
    memcpy(hd->frame->data[0], y, w * h);
    memcpy(hd->frame->data[1], u, (w/2) * (h/2));
    memcpy(hd->frame->data[2], v, (w/2) * (h/2));
    hd->frame->pts++;

    if (avcodec_send_frame(hd->ctx, hd->frame) < 0) return -1;
    int ret = avcodec_receive_packet(hd->ctx, hd->pkt);
    if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) { *outLen = 0; return 0; }
    if (ret < 0) return -2;
    *out = hd->pkt->data; *outLen = hd->pkt->size;
    return 0;
}

static void la_unref(LAH *hd) { av_packet_unref(hd->pkt); }

static void la_free(LAH *hd) {
    if (!hd) return;
    if (hd->pkt) av_packet_free(&hd->pkt);
    if (hd->frame) av_frame_free(&hd->frame);
    if (hd->ctx) avcodec_free_context(&hd->ctx);
    free(hd);
}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

type LibavBenchEnc struct {
	h       *C.LAH
	encName string
	codec   string
}

func NewLibavBench(name string, w, h int, codecName string, opts map[string]string, threads int) (*LibavBenchEnc, error) {
	keys := [3]string{}
	vals := [3]string{}
	i := 0
	for k, v := range opts {
		if i >= 3 { break }
		keys[i] = k; vals[i] = v; i++
	}

	cCodec := C.CString(codecName)
	defer C.free(unsafe.Pointer(cCodec))
	cK := [3]*C.char{}; cV := [3]*C.char{}
	for j := 0; j < 3; j++ {
		cK[j] = C.CString(keys[j]); cV[j] = C.CString(vals[j])
		defer C.free(unsafe.Pointer(cK[j])); defer C.free(unsafe.Pointer(cV[j]))
	}

	hd := C.la_create(C.int(w), C.int(h), 30, cCodec, cK[0], cV[0], cK[1], cV[1], cK[2], cV[2], C.int(threads))
	if hd == nil {
		return nil, fmt.Errorf("%s init failed", name)
	}

	codec := "h264"
	switch codecName {
	case "libx265": codec = "h265"
	case "libvpx": codec = "vp8"
	case "libvpx-vp9": codec = "vp9"
	case "libsvtav1", "libaom-av1", "librav1e": codec = "av1"
	case "mpeg4": codec = "mpeg4"
	case "snow": codec = "snow"
	case "libtheora": codec = "theora"
	}

	return &LibavBenchEnc{h: hd, encName: name, codec: codec}, nil
}

func (e *LibavBenchEnc) Name() string    { return e.encName }
func (e *LibavBenchEnc) Library() string  { return "libavcodec-cgo" }
func (e *LibavBenchEnc) Codec() string    { return e.codec }

func (e *LibavBenchEnc) Encode(y, u, v []byte, w, h int) ([]byte, error) {
	var out *C.uchar; var outLen C.int
	rv := C.la_encode(e.h, (*C.uchar)(unsafe.Pointer(&y[0])),
		(*C.uchar)(unsafe.Pointer(&u[0])), (*C.uchar)(unsafe.Pointer(&v[0])),
		C.int(w), C.int(h), &out, &outLen)
	if rv != 0 { return nil, fmt.Errorf("libav encode failed: %d", rv) }
	if outLen == 0 { return nil, nil }
	result := C.GoBytes(unsafe.Pointer(out), outLen)
	C.la_unref(e.h)
	return result, nil
}

func (e *LibavBenchEnc) Close() { C.la_free(e.h) }
