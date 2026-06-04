package main

/*
#cgo pkg-config: libavcodec libavutil
#include <libavcodec/avcodec.h>
#include <libavutil/frame.h>
#include <libavutil/imgutils.h>
#include <libavutil/opt.h>
#include <libavutil/hwcontext.h>
#include <libavutil/hwcontext_vaapi.h>
#include <libavutil/pixfmt.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    const AVCodec *codec;
    AVCodecContext *ctx;
    AVFrame *sw_frame;
    AVFrame *hw_frame;
    AVPacket *pkt;
    AVBufferRef *hw_device_ctx;
    AVBufferRef *hw_frames_ctx;
} VAAPIH;

static VAAPIH* vaapi_create(int w, int h, int fps, const char *device,
                            const char *profile, int qp, int threads) {
    VAAPIH *hd = (VAAPIH*)calloc(1, sizeof(VAAPIH));

    // Create hw device
    int ret = av_hwdevice_ctx_create(&hd->hw_device_ctx, AV_HWDEVICE_TYPE_VAAPI,
                                     device, NULL, 0);
    if (ret < 0) { free(hd); return NULL; }

    // Find encoder
    hd->codec = avcodec_find_encoder_by_name("h264_vaapi");
    if (!hd->codec) { av_buffer_unref(&hd->hw_device_ctx); free(hd); return NULL; }

    hd->ctx = avcodec_alloc_context3(hd->codec);
    hd->ctx->width = w;
    hd->ctx->height = h;
    hd->ctx->time_base = (AVRational){1, fps};
    hd->ctx->framerate = (AVRational){fps, 1};
    hd->ctx->gop_size = 30;
    hd->ctx->max_b_frames = 0;
    hd->ctx->pix_fmt = AV_PIX_FMT_VAAPI;
    hd->ctx->thread_count = threads;

    if (qp > 0) {
        av_opt_set_int(hd->ctx->priv_data, "qp", qp, 0);
        hd->ctx->global_quality = qp;
    }
    if (profile && strlen(profile) > 0) {
        av_opt_set(hd->ctx->priv_data, "profile", profile, 0);
    }
    av_opt_set(hd->ctx->priv_data, "rc_mode", "CQP", 0);
    av_opt_set_int(hd->ctx->priv_data, "idr_interval", 30, 0);
    av_opt_set_int(hd->ctx->priv_data, "low_power", 1, 0);

    // Create hw frames context
    hd->hw_frames_ctx = av_hwframe_ctx_alloc(hd->hw_device_ctx);
    AVHWFramesContext *frames_ctx = (AVHWFramesContext*)hd->hw_frames_ctx->data;
    frames_ctx->format = AV_PIX_FMT_VAAPI;
    frames_ctx->sw_format = AV_PIX_FMT_NV12;
    frames_ctx->width = w;
    frames_ctx->height = h;
    frames_ctx->initial_pool_size = 8;

    ret = av_hwframe_ctx_init(hd->hw_frames_ctx);
    if (ret < 0) {
        avcodec_free_context(&hd->ctx);
        av_buffer_unref(&hd->hw_device_ctx);
        av_buffer_unref(&hd->hw_frames_ctx);
        free(hd);
        return NULL;
    }
    hd->ctx->hw_frames_ctx = av_buffer_ref(hd->hw_frames_ctx);

    ret = avcodec_open2(hd->ctx, hd->codec, NULL);
    if (ret < 0) {
        avcodec_free_context(&hd->ctx);
        av_buffer_unref(&hd->hw_device_ctx);
        av_buffer_unref(&hd->hw_frames_ctx);
        free(hd);
        return NULL;
    }

    // Allocate sw frame (NV12)
    hd->sw_frame = av_frame_alloc();
    hd->sw_frame->format = AV_PIX_FMT_NV12;
    hd->sw_frame->width = w;
    hd->sw_frame->height = h;
    av_frame_get_buffer(hd->sw_frame, 0);

    // Allocate hw frame
    hd->hw_frame = av_frame_alloc();
    hd->hw_frame->format = AV_PIX_FMT_VAAPI;
    hd->hw_frame->width = w;
    hd->hw_frame->height = h;
    av_hwframe_get_buffer(hd->hw_frames_ctx, hd->hw_frame, 0);

    hd->pkt = av_packet_alloc();
    return hd;
}

static int vaapi_encode(VAAPIH *hd, unsigned char *y, unsigned char *u, unsigned char *v,
                        int w, int h, unsigned char **out, int *outLen) {
    av_frame_make_writable(hd->sw_frame);

    // Convert I420 to NV12 (Y plane copy, interleave UV)
    memcpy(hd->sw_frame->data[0], y, w * h);

    // Interleave U and V into NV12 UV plane
    int uv_w = w / 2;
    int uv_h = h / 2;
    unsigned char *dst_uv = hd->sw_frame->data[1];
    int dst_stride = hd->sw_frame->linesize[1];
    for (int row = 0; row < uv_h; row++) {
        unsigned char *dst_row = dst_uv + row * dst_stride;
        unsigned char *u_row = u + row * uv_w;
        unsigned char *v_row = v + row * uv_w;
        for (int col = 0; col < uv_w; col++) {
            dst_row[col * 2] = u_row[col];
            dst_row[col * 2 + 1] = v_row[col];
        }
    }

    // Upload to HW surface
    av_frame_unref(hd->hw_frame);
    if (av_hwframe_get_buffer(hd->hw_frames_ctx, hd->hw_frame, 0) < 0) return -1;
    if (av_hwframe_transfer_data(hd->hw_frame, hd->sw_frame, 0) < 0) return -1;

    hd->hw_frame->pts = hd->sw_frame->pts++;

    int ret = avcodec_send_frame(hd->ctx, hd->hw_frame);
    if (ret < 0) return -2;

    // Try to receive - may need multiple attempts for async pipeline
    ret = avcodec_receive_packet(hd->ctx, hd->pkt);
    if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) {
        *outLen = 0;
        return 0;
    }
    if (ret < 0) return -3;
    *out = hd->pkt->data;
    *outLen = hd->pkt->size;
    return 0;
}

static void vaapi_unref(VAAPIH *hd) { av_packet_unref(hd->pkt); }

static void vaapi_free(VAAPIH *hd) {
    if (!hd) return;
    if (hd->pkt) av_packet_free(&hd->pkt);
    if (hd->hw_frame) av_frame_free(&hd->hw_frame);
    if (hd->sw_frame) av_frame_free(&hd->sw_frame);
    if (hd->ctx) avcodec_free_context(&hd->ctx);
    if (hd->hw_frames_ctx) av_buffer_unref(&hd->hw_frames_ctx);
    if (hd->hw_device_ctx) av_buffer_unref(&hd->hw_device_ctx);
    free(hd);
}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

type VAAPIBenchEnc struct {
	h       *C.VAAPIH
	encName string
}

func NewVAAPIBench(name string, w, h int, device, profile string, qp, threads int) (*VAAPIBenchEnc, error) {
	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))
	cProfile := C.CString(profile)
	defer C.free(unsafe.Pointer(cProfile))

	hd := C.vaapi_create(C.int(w), C.int(h), 30, cDevice, cProfile, C.int(qp), C.int(threads))
	if hd == nil {
		return nil, fmt.Errorf("%s: vaapi init failed (check /dev/dri/renderD128)", name)
	}
	return &VAAPIBenchEnc{h: hd, encName: name}, nil
}

func (e *VAAPIBenchEnc) Name() string    { return e.encName }
func (e *VAAPIBenchEnc) Library() string  { return "libavcodec-vaapi" }
func (e *VAAPIBenchEnc) Codec() string    { return "h264" }

func (e *VAAPIBenchEnc) Encode(y, u, v []byte, w, h int) ([]byte, error) {
	var out *C.uchar
	var outLen C.int
	rv := C.vaapi_encode(e.h, (*C.uchar)(unsafe.Pointer(&y[0])),
		(*C.uchar)(unsafe.Pointer(&u[0])), (*C.uchar)(unsafe.Pointer(&v[0])),
		C.int(w), C.int(h), &out, &outLen)
	if rv != 0 {
		return nil, fmt.Errorf("vaapi encode failed: %d", rv)
	}
	if outLen == 0 {
		return nil, nil
	}
	result := C.GoBytes(unsafe.Pointer(out), outLen)
	C.vaapi_unref(e.h)
	return result, nil
}

func (e *VAAPIBenchEnc) Close() { C.vaapi_free(e.h) }
