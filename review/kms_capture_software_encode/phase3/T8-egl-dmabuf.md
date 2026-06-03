# Review: T8 - Implement EGL context and DMA-BUF import (cgo)

## Changes Made
- Created GBM device from DRM card fd
- Initialized EGL display via EGL_PLATFORM_GBM_KHR
- Created surfaceless EGL context (OpenGL ES 2.0)
- Import DMA-BUF as EGLImage via EGL_LINUX_DMA_BUF_EXT with format+stride
- Bind EGLImage to GL texture via glEGLImageTargetTexture2DOES
- Read pixels via FBO attachment + glReadPixels into pre-allocated buffer
- Proper cleanup chain: texture, image, context, display, GBM

## Files Created
- `internal/capture/egl.go` (EGL/GBM/GL cgo bindings)
- `internal/capture/egl_integration_test.go` (hardware tests)

## Test Results
- Unit tests: PASS (4 tests)
- Integration tests (sudo): PASS
  - EGL context created in 170ms
  - Read 14,745,600 bytes (2560x1440 RGBA) in 50ms
  - All pixel data non-zero (real screen content)

## Concerns / Trade-offs
- Using GLES2 + FBO readback (glReadPixels) rather than glGetTextureSubImage which requires GL 4.5. This works on Intel HD 630 with Mesa.
- Stride is assumed to be width*4; for non-standard pitch buffers we'd need FB2 info (deferred).

## Verdict
PASS
