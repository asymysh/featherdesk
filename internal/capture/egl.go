package capture

/*
#cgo pkg-config: libdrm egl gbm glesv2
#cgo LDFLAGS: -lEGL -lgbm -lGLESv2

#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#include <gbm.h>
#include <drm_fourcc.h>
#include <stdlib.h>
#include <string.h>

// EGL extension function pointers
static PFNEGLGETPLATFORMDISPLAYEXTPROC fn_eglGetPlatformDisplayEXT;
static PFNEGLCREATEIMAGEKHRPROC fn_eglCreateImageKHR;
static PFNEGLDESTROYIMAGEKHRPROC fn_eglDestroyImageKHR;
static PFNGLEGLIMAGETARGETTEXTURE2DOESPROC fn_glEGLImageTargetTexture2DOES;

static int load_egl_extensions() {
	fn_eglGetPlatformDisplayEXT = (PFNEGLGETPLATFORMDISPLAYEXTPROC)
		eglGetProcAddress("eglGetPlatformDisplayEXT");
	fn_eglCreateImageKHR = (PFNEGLCREATEIMAGEKHRPROC)
		eglGetProcAddress("eglCreateImageKHR");
	fn_eglDestroyImageKHR = (PFNEGLDESTROYIMAGEKHRPROC)
		eglGetProcAddress("eglDestroyImageKHR");
	fn_glEGLImageTargetTexture2DOES = (PFNGLEGLIMAGETARGETTEXTURE2DOESPROC)
		eglGetProcAddress("glEGLImageTargetTexture2DOES");

	if (!fn_eglGetPlatformDisplayEXT || !fn_eglCreateImageKHR ||
		!fn_eglDestroyImageKHR || !fn_glEGLImageTargetTexture2DOES) {
		return -1;
	}
	return 0;
}

static EGLDisplay create_egl_display(struct gbm_device *gbm) {
	if (load_egl_extensions() != 0) {
		return EGL_NO_DISPLAY;
	}
	EGLDisplay dpy = fn_eglGetPlatformDisplayEXT(
		EGL_PLATFORM_GBM_KHR, gbm, NULL);
	if (dpy == EGL_NO_DISPLAY) {
		return EGL_NO_DISPLAY;
	}
	EGLint major, minor;
	if (!eglInitialize(dpy, &major, &minor)) {
		return EGL_NO_DISPLAY;
	}
	return dpy;
}

static EGLContext create_egl_context(EGLDisplay dpy) {
	if (!eglBindAPI(EGL_OPENGL_ES_API)) {
		return EGL_NO_CONTEXT;
	}

	EGLint cfg_attribs[] = {
		EGL_SURFACE_TYPE, 0,
		EGL_RENDERABLE_TYPE, EGL_OPENGL_ES2_BIT,
		EGL_NONE
	};
	EGLConfig config;
	EGLint num_configs;
	if (!eglChooseConfig(dpy, cfg_attribs, &config, 1, &num_configs) || num_configs == 0) {
		return EGL_NO_CONTEXT;
	}

	EGLint ctx_attribs[] = {
		EGL_CONTEXT_CLIENT_VERSION, 2,
		EGL_NONE
	};
	return eglCreateContext(dpy, config, EGL_NO_CONTEXT, ctx_attribs);
}

static EGLImageKHR import_dmabuf(EGLDisplay dpy, int fd, int width, int height, int stride, uint32_t format) {
	EGLint attribs[] = {
		EGL_WIDTH, width,
		EGL_HEIGHT, height,
		EGL_LINUX_DRM_FOURCC_EXT, (EGLint)format,
		EGL_DMA_BUF_PLANE0_FD_EXT, fd,
		EGL_DMA_BUF_PLANE0_OFFSET_EXT, 0,
		EGL_DMA_BUF_PLANE0_PITCH_EXT, stride,
		EGL_NONE
	};
	return fn_eglCreateImageKHR(dpy, EGL_NO_CONTEXT, EGL_LINUX_DMA_BUF_EXT, NULL, attribs);
}

static void destroy_egl_image(EGLDisplay dpy, EGLImageKHR img) {
	if (img != EGL_NO_IMAGE_KHR) {
		fn_eglDestroyImageKHR(dpy, img);
	}
}

static GLuint create_texture_from_image(EGLImageKHR img) {
	GLuint tex;
	glGenTextures(1, &tex);
	glBindTexture(GL_TEXTURE_2D, tex);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
	fn_glEGLImageTargetTexture2DOES(GL_TEXTURE_2D, img);
	return tex;
}

static void read_texture_pixels(GLuint tex, int width, int height, void *buf) {
	GLuint fbo;
	glGenFramebuffers(1, &fbo);
	glBindFramebuffer(GL_FRAMEBUFFER, fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, tex, 0);
	glReadPixels(0, 0, width, height, GL_RGBA, GL_UNSIGNED_BYTE, buf);
	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	glDeleteFramebuffers(1, &fbo);
}

static void delete_texture(GLuint tex) {
	glDeleteTextures(1, &tex);
}

static int make_current_surfaceless(EGLDisplay dpy, EGLContext ctx) {
	return eglMakeCurrent(dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, ctx);
}

static void cleanup_egl(EGLDisplay dpy, EGLContext ctx) {
	eglMakeCurrent(dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT);
	if (ctx != EGL_NO_CONTEXT) {
		eglDestroyContext(dpy, ctx);
	}
	eglTerminate(dpy);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type EGLState struct {
	gbmDevice  *C.struct_gbm_device
	display    C.EGLDisplay
	context    C.EGLContext
	texture    C.GLuint
	image      C.EGLImageKHR
	pixelBuf   []byte
	width      int
	height     int
}

func NewEGLState(cardFD int) (*EGLState, error) {
	gbm := C.gbm_create_device(C.int(cardFD))
	if gbm == nil {
		return nil, fmt.Errorf("capture: failed to create GBM device")
	}

	dpy := C.create_egl_display(gbm)
	if dpy == C.EGLDisplay(C.EGL_NO_DISPLAY) {
		C.gbm_device_destroy(gbm)
		return nil, fmt.Errorf("capture: failed to create EGL display")
	}

	ctx := C.create_egl_context(dpy)
	if ctx == C.EGLContext(C.EGL_NO_CONTEXT) {
		C.eglTerminate(dpy)
		C.gbm_device_destroy(gbm)
		return nil, fmt.Errorf("capture: failed to create EGL context")
	}

	if C.make_current_surfaceless(dpy, ctx) == 0 {
		C.cleanup_egl(dpy, ctx)
		C.gbm_device_destroy(gbm)
		return nil, fmt.Errorf("capture: eglMakeCurrent failed")
	}

	return &EGLState{
		gbmDevice: gbm,
		display:   dpy,
		context:   ctx,
	}, nil
}

func (e *EGLState) ImportDMABuf(fd, width, height, stride int, format uint32) error {
	if e.image != nil {
		C.destroy_egl_image(e.display, e.image)
		e.image = nil
	}
	if e.texture != 0 {
		C.delete_texture(e.texture)
		e.texture = 0
	}

	img := C.import_dmabuf(e.display, C.int(fd), C.int(width), C.int(height), C.int(stride), C.uint32_t(format))
	if img == C.EGLImageKHR(C.EGL_NO_IMAGE_KHR) {
		return fmt.Errorf("capture: failed to import DMA-BUF as EGLImage")
	}

	tex := C.create_texture_from_image(img)
	if tex == 0 {
		C.destroy_egl_image(e.display, img)
		return fmt.Errorf("capture: failed to create texture from EGLImage")
	}

	e.image = img
	e.texture = tex
	e.width = width
	e.height = height

	bufSize := width * height * 4
	if len(e.pixelBuf) < bufSize {
		e.pixelBuf = make([]byte, bufSize)
	}

	return nil
}

func (e *EGLState) ReadPixels() []byte {
	C.read_texture_pixels(e.texture, C.int(e.width), C.int(e.height), unsafe.Pointer(&e.pixelBuf[0]))
	return e.pixelBuf[:e.width*e.height*4]
}

func (e *EGLState) Close() {
	if e.image != nil {
		C.destroy_egl_image(e.display, e.image)
	}
	if e.texture != 0 {
		C.delete_texture(e.texture)
	}
	C.cleanup_egl(e.display, e.context)
	if e.gbmDevice != nil {
		C.gbm_device_destroy(e.gbmDevice)
	}
}
