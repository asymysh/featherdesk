package main

/*
#cgo pkg-config: libva libva-drm
#include <stdint.h>
#include <va/va.h>
#include <va/va_drm.h>
#include <va/va_enc_h264.h>
#include <stdlib.h>
#include <string.h>
#include <fcntl.h>
#include <unistd.h>

typedef struct {
    int drm_fd;
    VADisplay display;
    VAConfigID config;
    VAContextID context;
    VASurfaceID src_surface;
    VASurfaceID ref_surface;
    VASurfaceID rec_surface;
    VABufferID coded_buf;
    int width;
    int height;
    int frame_num;
    int idr_period;
    int qp;
} LibVAEnc;

static LibVAEnc* libva_create(int w, int h, int qp, int idr_period) {
    LibVAEnc *e = (LibVAEnc*)calloc(1, sizeof(LibVAEnc));
    e->width = w;
    e->height = h;
    e->qp = qp;
    e->idr_period = idr_period;

    e->drm_fd = open("/dev/dri/renderD128", O_RDWR);
    if (e->drm_fd < 0) { free(e); return NULL; }

    e->display = vaGetDisplayDRM(e->drm_fd);
    if (!e->display) { close(e->drm_fd); free(e); return NULL; }

    int major, minor;
    if (vaInitialize(e->display, &major, &minor) != VA_STATUS_SUCCESS) {
        close(e->drm_fd); free(e); return NULL;
    }

    VAConfigAttrib attrib = { .type = VAConfigAttribRTFormat };
    vaGetConfigAttributes(e->display, VAProfileH264High, VAEntrypointEncSliceLP, &attrib, 1);

    if (vaCreateConfig(e->display, VAProfileH264High, VAEntrypointEncSliceLP,
                       &attrib, 1, &e->config) != VA_STATUS_SUCCESS) {
        vaTerminate(e->display); close(e->drm_fd); free(e); return NULL;
    }

    VASurfaceID surfaces[3];
    if (vaCreateSurfaces(e->display, VA_RT_FORMAT_YUV420, w, h, surfaces, 3, NULL, 0) != VA_STATUS_SUCCESS) {
        vaDestroyConfig(e->display, e->config);
        vaTerminate(e->display); close(e->drm_fd); free(e); return NULL;
    }
    e->src_surface = surfaces[0];
    e->ref_surface = surfaces[1];
    e->rec_surface = surfaces[2];

    if (vaCreateContext(e->display, e->config, w, h, VA_PROGRESSIVE,
                        surfaces, 3, &e->context) != VA_STATUS_SUCCESS) {
        vaDestroySurfaces(e->display, surfaces, 3);
        vaDestroyConfig(e->display, e->config);
        vaTerminate(e->display); close(e->drm_fd); free(e); return NULL;
    }

    unsigned int coded_size = w * h * 3 / 2;
    if (vaCreateBuffer(e->display, e->context, VAEncCodedBufferType,
                       coded_size, 1, NULL, &e->coded_buf) != VA_STATUS_SUCCESS) {
        vaDestroyContext(e->display, e->context);
        vaDestroySurfaces(e->display, surfaces, 3);
        vaDestroyConfig(e->display, e->config);
        vaTerminate(e->display); close(e->drm_fd); free(e); return NULL;
    }

    return e;
}

static int libva_upload_nv12(LibVAEnc *e, unsigned char *y, unsigned char *u, unsigned char *v) {
    VAImage image;
    if (vaDeriveImage(e->display, e->src_surface, &image) != VA_STATUS_SUCCESS) return -1;

    void *buf;
    if (vaMapBuffer(e->display, image.buf, &buf) != VA_STATUS_SUCCESS) {
        vaDestroyImage(e->display, image.image_id);
        return -2;
    }

    unsigned char *dst = (unsigned char*)buf;
    int w = e->width, h = e->height;

    // Y plane
    for (int row = 0; row < h; row++) {
        memcpy(dst + row * image.pitches[0], y + row * w, w);
    }

    // NV12 UV interleave
    unsigned char *uv_dst = dst + image.offsets[1];
    int uv_w = w / 2, uv_h = h / 2;
    for (int row = 0; row < uv_h; row++) {
        unsigned char *d = uv_dst + row * image.pitches[1];
        unsigned char *u_row = u + row * uv_w;
        unsigned char *v_row = v + row * uv_w;
        for (int col = 0; col < uv_w; col++) {
            d[col * 2] = u_row[col];
            d[col * 2 + 1] = v_row[col];
        }
    }

    vaUnmapBuffer(e->display, image.buf);
    vaDestroyImage(e->display, image.image_id);
    return 0;
}

static int libva_encode_frame(LibVAEnc *e, int force_idr, unsigned char **out, int *outLen) {
    int is_idr = force_idr || (e->frame_num == 0) ||
                 (e->idr_period > 0 && (e->frame_num % e->idr_period == 0));

    // Sequence parameter (on IDR)
    if (is_idr) {
        VAEncSequenceParameterBufferH264 seq = {0};
        seq.level_idc = 41;
        seq.intra_period = e->idr_period > 0 ? e->idr_period : 0;
        seq.ip_period = 1;
        seq.max_num_ref_frames = 1;
        seq.picture_width_in_mbs = (e->width + 15) / 16;
        seq.picture_height_in_mbs = (e->height + 15) / 16;
        seq.seq_fields.bits.frame_mbs_only_flag = 1;
        seq.seq_fields.bits.chroma_format_idc = 1;
        seq.seq_fields.bits.log2_max_frame_num_minus4 = 0;
        seq.seq_fields.bits.pic_order_cnt_type = 0;
        seq.seq_fields.bits.log2_max_pic_order_cnt_lsb_minus4 = 2;
        seq.time_scale = 60;
        seq.num_units_in_tick = 1;

        VABufferID seq_buf;
        vaCreateBuffer(e->display, e->context, VAEncSequenceParameterBufferType,
                       sizeof(seq), 1, &seq, &seq_buf);
        vaRenderPicture(e->display, e->context, &seq_buf, 1);
        vaDestroyBuffer(e->display, seq_buf);
    }

    // Picture parameter
    VAEncPictureParameterBufferH264 pic = {0};
    pic.CurrPic.picture_id = e->rec_surface;
    pic.CurrPic.TopFieldOrderCnt = e->frame_num * 2;
    pic.coded_buf = e->coded_buf;
    pic.pic_fields.bits.idr_pic_flag = is_idr ? 1 : 0;
    pic.pic_fields.bits.reference_pic_flag = 1;
    pic.pic_fields.bits.entropy_coding_mode_flag = 1; // CABAC
    pic.frame_num = is_idr ? 0 : e->frame_num;
    pic.pic_init_qp = e->qp;

    if (!is_idr) {
        pic.ReferenceFrames[0].picture_id = e->ref_surface;
        pic.ReferenceFrames[0].TopFieldOrderCnt = (e->frame_num - 1) * 2;
        pic.ReferenceFrames[0].flags = VA_PICTURE_H264_SHORT_TERM_REFERENCE;
    }
    for (int i = is_idr ? 0 : 1; i < 16; i++) {
        pic.ReferenceFrames[i].picture_id = VA_INVALID_ID;
        pic.ReferenceFrames[i].flags = VA_PICTURE_H264_INVALID;
    }

    VABufferID pic_buf;
    vaCreateBuffer(e->display, e->context, VAEncPictureParameterBufferType,
                   sizeof(pic), 1, &pic, &pic_buf);

    // Slice parameter
    VAEncSliceParameterBufferH264 slice = {0};
    slice.num_macroblocks = ((e->width + 15) / 16) * ((e->height + 15) / 16);
    slice.slice_type = is_idr ? 2 : 0; // I : P
    slice.slice_qp_delta = 0;
    slice.disable_deblocking_filter_idc = 0;
    if (!is_idr) {
        slice.num_ref_idx_l0_active_minus1 = 0;
        slice.RefPicList0[0].picture_id = e->ref_surface;
        slice.RefPicList0[0].TopFieldOrderCnt = (e->frame_num - 1) * 2;
        slice.RefPicList0[0].flags = VA_PICTURE_H264_SHORT_TERM_REFERENCE;
    }
    for (int i = is_idr ? 0 : 1; i < 32; i++) {
        slice.RefPicList0[i].picture_id = VA_INVALID_ID;
        slice.RefPicList0[i].flags = VA_PICTURE_H264_INVALID;
        slice.RefPicList1[i].picture_id = VA_INVALID_ID;
        slice.RefPicList1[i].flags = VA_PICTURE_H264_INVALID;
    }

    VABufferID slice_buf;
    vaCreateBuffer(e->display, e->context, VAEncSliceParameterBufferType,
                   sizeof(slice), 1, &slice, &slice_buf);

    // Render
    vaBeginPicture(e->display, e->context, e->src_surface);
    vaRenderPicture(e->display, e->context, &pic_buf, 1);
    vaRenderPicture(e->display, e->context, &slice_buf, 1);
    VAStatus st = vaEndPicture(e->display, e->context);
    if (st != VA_STATUS_SUCCESS) {
        vaDestroyBuffer(e->display, pic_buf);
        vaDestroyBuffer(e->display, slice_buf);
        return -1;
    }

    vaSyncSurface(e->display, e->src_surface);

    // Extract coded data
    VACodedBufferSegment *seg = NULL;
    if (vaMapBuffer(e->display, e->coded_buf, (void**)&seg) != VA_STATUS_SUCCESS) {
        vaDestroyBuffer(e->display, pic_buf);
        vaDestroyBuffer(e->display, slice_buf);
        return -2;
    }

    *outLen = 0;
    *out = NULL;
    if (seg && seg->size > 0) {
        *out = (unsigned char*)seg->buf;
        *outLen = seg->size;
    }

    // Swap ref/rec for next frame
    VASurfaceID tmp = e->ref_surface;
    e->ref_surface = e->rec_surface;
    e->rec_surface = tmp;

    e->frame_num++;

    vaDestroyBuffer(e->display, pic_buf);
    vaDestroyBuffer(e->display, slice_buf);
    return 0;
}

static void libva_unmap_coded(LibVAEnc *e) {
    vaUnmapBuffer(e->display, e->coded_buf);
}

static void libva_free(LibVAEnc *e) {
    if (!e) return;
    VASurfaceID surfs[3] = {e->src_surface, e->ref_surface, e->rec_surface};
    vaDestroyBuffer(e->display, e->coded_buf);
    vaDestroyContext(e->display, e->context);
    vaDestroySurfaces(e->display, surfs, 3);
    vaDestroyConfig(e->display, e->config);
    vaTerminate(e->display);
    close(e->drm_fd);
    free(e);
}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

type LibVABenchEnc struct {
	h       *C.LibVAEnc
	encName string
}

func NewLibVABench(name string, w, h, qp, idrPeriod int) (*LibVABenchEnc, error) {
	hd := C.libva_create(C.int(w), C.int(h), C.int(qp), C.int(idrPeriod))
	if hd == nil {
		return nil, fmt.Errorf("%s: libva init failed", name)
	}
	return &LibVABenchEnc{h: hd, encName: name}, nil
}

func (e *LibVABenchEnc) Name() string    { return e.encName }
func (e *LibVABenchEnc) Library() string  { return "libva-direct" }
func (e *LibVABenchEnc) Codec() string    { return "h264" }

func (e *LibVABenchEnc) Encode(y, u, v []byte, w, h int) ([]byte, error) {
	if C.libva_upload_nv12(e.h, (*C.uchar)(unsafe.Pointer(&y[0])),
		(*C.uchar)(unsafe.Pointer(&u[0])),
		(*C.uchar)(unsafe.Pointer(&v[0]))) != 0 {
		return nil, fmt.Errorf("libva upload failed")
	}

	var out *C.uchar
	var outLen C.int
	rv := C.libva_encode_frame(e.h, 0, &out, &outLen)
	if rv != 0 {
		return nil, fmt.Errorf("libva encode failed: %d", rv)
	}

	if outLen == 0 {
		C.libva_unmap_coded(e.h)
		return nil, nil
	}

	result := C.GoBytes(unsafe.Pointer(out), outLen)
	C.libva_unmap_coded(e.h)
	return result, nil
}

func (e *LibVABenchEnc) Close() { C.libva_free(e.h) }
