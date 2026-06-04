package encode

/*
#cgo pkg-config: libavcodec libavutil
#include <stdint.h>
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
    int force_idr;
} VAAPIENC;

static VAAPIENC* vaapi_enc_create(int w, int h, int fps, int qp, const char *device) {
    VAAPIENC *hd = (VAAPIENC*)calloc(1, sizeof(VAAPIENC));

    int ret = av_hwdevice_ctx_create(&hd->hw_device_ctx, AV_HWDEVICE_TYPE_VAAPI,
                                     device, NULL, 0);
    if (ret < 0) { free(hd); return NULL; }

    hd->codec = avcodec_find_encoder_by_name("h264_vaapi");
    if (!hd->codec) { av_buffer_unref(&hd->hw_device_ctx); free(hd); return NULL; }

    hd->ctx = avcodec_alloc_context3(hd->codec);
    hd->ctx->width = w;
    hd->ctx->height = h;
    hd->ctx->time_base = (AVRational){1, fps};
    hd->ctx->framerate = (AVRational){fps, 1};
    hd->ctx->gop_size = 0;
    hd->ctx->max_b_frames = 0;
    hd->ctx->pix_fmt = AV_PIX_FMT_VAAPI;
    hd->ctx->thread_count = 1;

    av_opt_set(hd->ctx->priv_data, "rc_mode", "CQP", 0);
    av_opt_set_int(hd->ctx->priv_data, "qp", qp, 0);
    av_opt_set_int(hd->ctx->priv_data, "low_power", 1, 0);
    av_opt_set(hd->ctx->priv_data, "profile", "high", 0);

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

    hd->sw_frame = av_frame_alloc();
    hd->sw_frame->format = AV_PIX_FMT_NV12;
    hd->sw_frame->width = w;
    hd->sw_frame->height = h;
    av_frame_get_buffer(hd->sw_frame, 0);

    hd->hw_frame = av_frame_alloc();
    hd->pkt = av_packet_alloc();
    return hd;
}

static int vaapi_enc_encode(VAAPIENC *hd, unsigned char *y, unsigned char *u, unsigned char *v,
                            int w, int h, unsigned char **out, int *outLen) {
    av_frame_make_writable(hd->sw_frame);

    memcpy(hd->sw_frame->data[0], y, w * h);

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

    av_frame_unref(hd->hw_frame);
    if (av_hwframe_get_buffer(hd->hw_frames_ctx, hd->hw_frame, 0) < 0) return -1;
    if (av_hwframe_transfer_data(hd->hw_frame, hd->sw_frame, 0) < 0) return -1;

    hd->hw_frame->pts = hd->sw_frame->pts++;

    if (hd->force_idr) {
        hd->hw_frame->pict_type = AV_PICTURE_TYPE_I;
        hd->hw_frame->flags |= AV_FRAME_FLAG_KEY;
        hd->force_idr = 0;
    } else {
        hd->hw_frame->pict_type = AV_PICTURE_TYPE_NONE;
        hd->hw_frame->flags &= ~AV_FRAME_FLAG_KEY;
    }

    int ret = avcodec_send_frame(hd->ctx, hd->hw_frame);
    if (ret < 0) return -2;

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

static void vaapi_enc_unref(VAAPIENC *hd) { av_packet_unref(hd->pkt); }

static void vaapi_enc_force_idr(VAAPIENC *hd) { hd->force_idr = 1; }

static void vaapi_enc_free(VAAPIENC *hd) {
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
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

type VAAPIEncoder struct {
	handle *C.VAAPIENC
	config EncoderConfig
}

func NewVAAPIEncoder(cfg EncoderConfig) (*VAAPIEncoder, error) {
	device := C.CString("/dev/dri/renderD128")
	defer C.free(unsafe.Pointer(device))

	h := C.vaapi_enc_create(C.int(cfg.Width), C.int(cfg.Height), C.int(cfg.FPS), C.int(cfg.QP), device)
	if h == nil {
		return nil, errors.New("encode: VA-API encoder init failed")
	}
	return &VAAPIEncoder{handle: h, config: cfg}, nil
}

func (e *VAAPIEncoder) Encode(frame *I420Frame) ([][]byte, error) {
	if e.handle == nil {
		return nil, errors.New("encode: encoder closed")
	}

	var pinner runtime.Pinner
	pinner.Pin(&frame.Y[0])
	pinner.Pin(&frame.U[0])
	pinner.Pin(&frame.V[0])
	defer pinner.Unpin()

	var out *C.uchar
	var outLen C.int

	rv := C.vaapi_enc_encode(e.handle,
		(*C.uchar)(unsafe.Pointer(&frame.Y[0])),
		(*C.uchar)(unsafe.Pointer(&frame.U[0])),
		(*C.uchar)(unsafe.Pointer(&frame.V[0])),
		C.int(frame.Width), C.int(frame.Height),
		&out, &outLen)
	if rv != 0 {
		return nil, fmt.Errorf("encode: VA-API encode failed: %d", rv)
	}
	if outLen == 0 {
		return nil, nil
	}

	result := C.GoBytes(unsafe.Pointer(out), outLen)
	C.vaapi_enc_unref(e.handle)
	return [][]byte{result}, nil
}

func (e *VAAPIEncoder) ForceKeyframe() {
	if e.handle != nil {
		C.vaapi_enc_force_idr(e.handle)
	}
}

func (e *VAAPIEncoder) Close() error {
	if e.handle == nil {
		return nil
	}
	C.vaapi_enc_free(e.handle)
	e.handle = nil
	return nil
}
