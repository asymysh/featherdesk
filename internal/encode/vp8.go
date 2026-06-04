package encode

/*
#cgo pkg-config: libavcodec libavutil
#include <stdint.h>
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
} VP8H;

static VP8H* vp8_create(int w, int h, int fps) {
    VP8H *hd = (VP8H*)calloc(1, sizeof(VP8H));
    hd->codec = avcodec_find_encoder_by_name("libvpx");
    if (!hd->codec) { free(hd); return NULL; }

    hd->ctx = avcodec_alloc_context3(hd->codec);
    hd->ctx->width = w;
    hd->ctx->height = h;
    hd->ctx->time_base = (AVRational){1, fps};
    hd->ctx->framerate = (AVRational){fps, 1};
    hd->ctx->gop_size = 30;
    hd->ctx->max_b_frames = 0;
    hd->ctx->thread_count = 1;
    hd->ctx->pix_fmt = AV_PIX_FMT_YUV420P;

    av_opt_set(hd->ctx->priv_data, "quality", "realtime", 0);
    av_opt_set(hd->ctx->priv_data, "cpu-used", "8", 0);
    av_opt_set_int(hd->ctx->priv_data, "crf", 26, 0);

    if (avcodec_open2(hd->ctx, hd->codec, NULL) < 0) {
        avcodec_free_context(&hd->ctx); free(hd); return NULL;
    }

    hd->frame = av_frame_alloc();
    hd->frame->format = AV_PIX_FMT_YUV420P;
    hd->frame->width = w;
    hd->frame->height = h;
    av_frame_get_buffer(hd->frame, 0);
    hd->pkt = av_packet_alloc();
    return hd;
}

static int vp8_encode(VP8H *hd, unsigned char *y, unsigned char *u, unsigned char *v,
                      int w, int h, int force_key,
                      unsigned char **out, int *outLen) {
    av_frame_make_writable(hd->frame);
    memcpy(hd->frame->data[0], y, w * h);
    memcpy(hd->frame->data[1], u, (w/2) * (h/2));
    memcpy(hd->frame->data[2], v, (w/2) * (h/2));
    hd->frame->pts++;

    if (force_key) {
        hd->frame->pict_type = AV_PICTURE_TYPE_I;
        hd->frame->flags |= AV_FRAME_FLAG_KEY;
    } else {
        hd->frame->pict_type = AV_PICTURE_TYPE_NONE;
        hd->frame->flags &= ~AV_FRAME_FLAG_KEY;
    }

    if (avcodec_send_frame(hd->ctx, hd->frame) < 0) return -1;
    int ret = avcodec_receive_packet(hd->ctx, hd->pkt);
    if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) { *outLen = 0; return 0; }
    if (ret < 0) return -2;
    *out = hd->pkt->data;
    *outLen = hd->pkt->size;
    return 0;
}

static void vp8_unref(VP8H *hd) { av_packet_unref(hd->pkt); }

static void vp8_free(VP8H *hd) {
    if (!hd) return;
    if (hd->pkt) av_packet_free(&hd->pkt);
    if (hd->frame) av_frame_free(&hd->frame);
    if (hd->ctx) avcodec_free_context(&hd->ctx);
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

type VP8Encoder struct {
	handle *C.VP8H
	config EncoderConfig
	idr    atomic.Bool
}

func NewVP8Encoder(cfg EncoderConfig) (*VP8Encoder, error) {
	h := C.vp8_create(C.int(cfg.Width), C.int(cfg.Height), C.int(cfg.FPS))
	if h == nil {
		return nil, errors.New("encode: VP8 encoder init failed (libvpx not available?)")
	}
	return &VP8Encoder{handle: h, config: cfg}, nil
}

func (e *VP8Encoder) Encode(frame *I420Frame) ([][]byte, error) {
	if e.handle == nil {
		return nil, errors.New("encode: encoder closed")
	}

	forceKey := 0
	if e.idr.CompareAndSwap(true, false) {
		forceKey = 1
	}

	var pinner runtime.Pinner
	pinner.Pin(&frame.Y[0])
	pinner.Pin(&frame.U[0])
	pinner.Pin(&frame.V[0])
	defer pinner.Unpin()

	var out *C.uchar
	var outLen C.int

	rv := C.vp8_encode(e.handle,
		(*C.uchar)(unsafe.Pointer(&frame.Y[0])),
		(*C.uchar)(unsafe.Pointer(&frame.U[0])),
		(*C.uchar)(unsafe.Pointer(&frame.V[0])),
		C.int(frame.Width), C.int(frame.Height), C.int(forceKey),
		&out, &outLen)
	if rv != 0 {
		return nil, fmt.Errorf("encode: VP8 encode failed: %d", rv)
	}
	if outLen == 0 {
		return nil, nil
	}

	result := C.GoBytes(unsafe.Pointer(out), outLen)
	C.vp8_unref(e.handle)
	return [][]byte{result}, nil
}

func (e *VP8Encoder) ForceKeyframe() {
	e.idr.Store(true)
}

func (e *VP8Encoder) Close() error {
	if e.handle == nil {
		return nil
	}
	C.vp8_free(e.handle)
	e.handle = nil
	return nil
}
