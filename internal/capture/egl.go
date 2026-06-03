package capture

/*
#cgo pkg-config: libdrm egl gbm
#cgo LDFLAGS: -lEGL -lgbm -lGL

#include <EGL/egl.h>
#include <EGL/eglext.h>
#define GL_GLEXT_PROTOTYPES
#include <GL/gl.h>
#include <GL/glext.h>
#include <gbm.h>
#include <drm_fourcc.h>
#include <stdlib.h>
#include <string.h>

static PFNEGLGETPLATFORMDISPLAYEXTPROC fn_eglGetPlatformDisplayEXT;
static PFNEGLCREATEIMAGEKHRPROC fn_eglCreateImageKHR;
static PFNEGLDESTROYIMAGEKHRPROC fn_eglDestroyImageKHR;
static PFNGLEGLIMAGETARGETTEXTURE2DOESPROC fn_glEGLImageTargetTexture2DOES;

static GLuint read_fbo_src = 0;
static GLuint read_fbo_dst = 0;
static GLuint read_rbo = 0;
static int read_rbo_w = 0, read_rbo_h = 0;

static int ensure_blit_fbo(int width, int height) {
	if (read_fbo_dst != 0 && read_rbo_w == width && read_rbo_h == height) {
		return 0;
	}
	if (read_fbo_src) glDeleteFramebuffers(1, &read_fbo_src);
	if (read_fbo_dst) glDeleteFramebuffers(1, &read_fbo_dst);
	if (read_rbo) glDeleteRenderbuffers(1, &read_rbo);

	glGenFramebuffers(1, &read_fbo_src);
	glGenFramebuffers(1, &read_fbo_dst);
	glGenRenderbuffers(1, &read_rbo);

	glBindRenderbuffer(GL_RENDERBUFFER, read_rbo);
	glRenderbufferStorage(GL_RENDERBUFFER, GL_RGBA8, width, height);

	glBindFramebuffer(GL_FRAMEBUFFER, read_fbo_dst);
	glFramebufferRenderbuffer(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_RENDERBUFFER, read_rbo);
	GLenum status = glCheckFramebufferStatus(GL_FRAMEBUFFER);
	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	if (status != GL_FRAMEBUFFER_COMPLETE) return -1;

	read_rbo_w = width;
	read_rbo_h = height;
	return 0;
}

static int read_texture_pixels(GLuint tex, int width, int height, void *buf) {
	if (ensure_blit_fbo(width, height) != 0) return -99;

	glBindFramebuffer(GL_READ_FRAMEBUFFER, read_fbo_src);
	glFramebufferTexture2D(GL_READ_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, tex, 0);

	glBindFramebuffer(GL_DRAW_FRAMEBUFFER, read_fbo_dst);
	glBlitFramebuffer(0, 0, width, height, 0, 0, width, height, GL_COLOR_BUFFER_BIT, GL_NEAREST);

	glBindFramebuffer(GL_READ_FRAMEBUFFER, read_fbo_dst);
	glReadPixels(0, 0, width, height, GL_RGBA, GL_UNSIGNED_BYTE, buf);
	GLenum err = glGetError();

	glBindFramebuffer(GL_READ_FRAMEBUFFER, 0);
	glBindFramebuffer(GL_DRAW_FRAMEBUFFER, 0);

	if (err != GL_NO_ERROR) return -(int)err;
	return 0;
}

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
	if (!eglBindAPI(EGL_OPENGL_API)) {
		return EGL_NO_CONTEXT;
	}

	EGLint cfg_attribs[] = {
		EGL_SURFACE_TYPE, 0,
		EGL_RENDERABLE_TYPE, EGL_OPENGL_BIT,
		EGL_NONE
	};
	EGLConfig config;
	EGLint num_configs;
	if (!eglChooseConfig(dpy, cfg_attribs, &config, 1, &num_configs) || num_configs == 0) {
		return EGL_NO_CONTEXT;
	}

	EGLint ctx_attribs[] = {
		EGL_NONE
	};
	return eglCreateContext(dpy, config, EGL_NO_CONTEXT, ctx_attribs);
}

static EGLImageKHR import_dmabuf(EGLDisplay dpy, int fd, int width, int height, int stride, uint32_t format, uint64_t modifier) {
	EGLint attribs[17];
	int i = 0;
	attribs[i++] = EGL_WIDTH; attribs[i++] = width;
	attribs[i++] = EGL_HEIGHT; attribs[i++] = height;
	attribs[i++] = EGL_LINUX_DRM_FOURCC_EXT; attribs[i++] = (EGLint)format;
	attribs[i++] = EGL_DMA_BUF_PLANE0_FD_EXT; attribs[i++] = fd;
	attribs[i++] = EGL_DMA_BUF_PLANE0_OFFSET_EXT; attribs[i++] = 0;
	attribs[i++] = EGL_DMA_BUF_PLANE0_PITCH_EXT; attribs[i++] = stride;
	if (modifier != 0 && modifier != 0x00ffffffffffffffULL) {
		attribs[i++] = EGL_DMA_BUF_PLANE0_MODIFIER_LO_EXT; attribs[i++] = (EGLint)(modifier & 0xFFFFFFFF);
		attribs[i++] = EGL_DMA_BUF_PLANE0_MODIFIER_HI_EXT; attribs[i++] = (EGLint)(modifier >> 32);
	}
	attribs[i++] = EGL_NONE;
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
	GLenum err = glGetError();
	if (err != GL_NO_ERROR) {
		glDeleteTextures(1, &tex);
		return 0;
	}
	return tex;
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

func (e *EGLState) ImportDMABuf(fd, width, height, stride int, format uint32, modifier uint64) error {
	if e.image != nil {
		C.destroy_egl_image(e.display, e.image)
		e.image = nil
	}
	if e.texture != 0 {
		C.delete_texture(e.texture)
		e.texture = 0
	}

	img := C.import_dmabuf(e.display, C.int(fd), C.int(width), C.int(height), C.int(stride), C.uint32_t(format), C.uint64_t(modifier))
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

func (e *EGLState) MakeCurrent() {
	C.make_current_surfaceless(e.display, e.context)
}

func (e *EGLState) ReadPixels() ([]byte, error) {
	ret := C.read_texture_pixels(e.texture, C.int(e.width), C.int(e.height), unsafe.Pointer(&e.pixelBuf[0]))
	if ret > 0 {
		return nil, fmt.Errorf("capture: framebuffer incomplete (status=0x%X)", ret)
	}
	if ret < 0 {
		return nil, fmt.Errorf("capture: glReadPixels error (GL error=0x%X)", -ret)
	}
	return e.pixelBuf[:e.width*e.height*4], nil
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
