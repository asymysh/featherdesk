# Linux SW Encoder Add-On: OpenH264 CGo

## Purpose

Software H.264 Baseline encoder via Cisco's OpenH264 library, called from Go through
CGo. The cross-platform fallback encoder — the same Go file compiles on Linux, Windows,
and macOS without changes.

When the system has no GPU at all (containers, headless ARM, Graviton-class instances,
broken drivers, etc.), this is the encoder that runs.

---

## License & Royalties

| Component | License | Notes |
|-----------|---------|-------|
| OpenH264 library (Cisco) | **BSD-2-Clause** | Permissive, embeddable |
| MPEG-LA H.264 patent royalties | **Cisco pays** | Cisco operates the binary distribution and pays MPEG-LA on behalf of all users |
| Our CGo binding | MIT | We own this code |

This is the entire reason OpenH264 exists as a project — Cisco wants free H.264 in
WebRTC and pays the patent pool so downstream users don't have to. Zero royalty
concern for FeatherDesk shipping this.

---

## Hardware Compatibility

**Any CPU.** Pure software. No GPU required.

| Architecture | SIMD path | 1080p p50 |
|-------------|-----------|----------|
| x86_64 (Intel/AMD) | SSE4.2 / AVX2 | ~8ms |
| ARM64 (Apple Silicon native, Graviton, Snapdragon Linux) | NEON | ~10ms |
| ARM64 (Rosetta-translated) | — | Avoid; use VideoToolbox on macOS |

OpenH264 has tuned assembly for both x86_64 and ARM64 NEON. Verified working on
both architectures.

---

## Build & Distribution

### Shared library build

```bash
go build -buildmode=c-shared -o featherdesk-addon-openh264.so ./internal/encode/openh264
```

### Runtime dependencies

- `libopenh264.so.X` (system package: `libopenh264-dev` on Debian/Ubuntu, `openh264-devel` on RHEL/Fedora)
- pkg-config (build time only)

Most distros ship OpenH264 prebuilt. If absent, building from source is straightforward
(MIT-licensed NASM assembler required for SIMD optimisations).

### CGo configuration

```go
/*
#cgo pkg-config: openh264

#include <wels/codec_api.h>
#include <wels/codec_app_def.h>
#include <wels/codec_def.h>
#include <stdlib.h>
#include <string.h>
*/
import "C"
```

> **macOS 26 note (for reference, irrelevant on Linux):** when building on macOS,
> a different pointer-to-vtable indirection is needed than on Linux. The macOS
> spec covers this. On Linux the bindings are direct as shown above.

---

## CGo Implementation Sketch

```c
// Session setup
ISVCEncoder *enc = NULL;
WelsCreateSVCEncoder(&enc);

SEncParamExt p;
memset(&p, 0, sizeof(p));
enc->GetDefaultParams(enc, &p);
p.iUsageType         = CAMERA_VIDEO_REAL_TIME;
p.iPicWidth          = W;
p.iPicHeight         = H;
p.fMaxFrameRate      = fps;
p.iRCMode            = RC_OFF_MODE;        // fixed-QP (lowest latency)
p.iMultipleThreadIdc = 1;                  // single-threaded
p.iSpatialLayerNum   = 1;
p.sSpatialLayers[0].iVideoWidth = W;
p.sSpatialLayers[0].iVideoHeight = H;
enc->InitializeExt(enc, &p);

// Per-frame
SSourcePicture pic;
memset(&pic, 0, sizeof(pic));
pic.iColorFormat = videoFormatI420;
pic.iPicWidth = W; pic.iPicHeight = H;
pic.iStride[0] = W; pic.iStride[1] = W/2; pic.iStride[2] = W/2;
pic.pData[0] = yPlane; pic.pData[1] = uPlane; pic.pData[2] = vPlane;

SFrameBSInfo info;
memset(&info, 0, sizeof(info));
enc->EncodeFrame(enc, &pic, &info);

// info.sLayerInfo[i].pBsBuf contains the Annex B NAL units
```

Force keyframe (IDR-on-demand) -- use the dedicated vtable method, NOT SetOption:
```c
// ForceIntraFrame(encoder, bIDR) -- forces the NEXT frame to be IDR (one-shot).
// Do NOT use ENCODER_OPTION_IDR_INTERVAL (that sets periodic IDR interval --
// setting it to 1 makes EVERY frame an IDR, destroying compression).
(*enc)->ForceIntraFrame(enc, true);
```

---

## Performance (Benchmarked)

Verified results from previous benchmark sessions:

| Platform | Resolution | FPS | p50 | p95 | CPU (1 core) |
|----------|-----------|-----|-----|-----|-------------|
| AMD Ryzen 9 5900X (Windows reference) | 1080p | ~226 | 4.3ms | 6.0ms | ~25% |
| AMD Ryzen 5 3600 (macOS Hackintosh) | 1080p | 280 | 3.6ms | 3.7ms | ~25% |
| Intel HD 630 (original Linux dev) | 1080p | ~125 | ~8ms | — | ~25% |

Within budget for 30fps remote control on every machine tested. Slower than libx264
ultrafast (~4ms vs 4.3ms) but eliminates GPL contamination and ffmpeg subprocess.

---

## Probe & Selection

```go
//go:build linux

func ProbeOpenH264() (*OpenH264Capabilities, error) {
    // 1. dlopen libopenh264.so (verify present)
    // 2. Create + destroy a test encoder (verify functional)
    // 3. Return version, max resolution
}
```

Pipeline probes (Linux, with this add-on loaded):
```
NVENC / AMF / libva HW add-ons available? → use HW
None available?                              → use OpenH264 CGo (this add-on)
This add-on not loaded either?               → fatal: no encoder
```

---

## File Structure

```
internal/encode/openh264/
├── openh264.go           // Encoder struct, NewOpenH264Encoder
├── openh264_cgo.go       // CGo binding (built into the add-on shared library)
├── probe.go              // ProbeOpenH264()
└── openh264_test.go      // Unit + benchmark tests
```

---

## When to use this add-on

Use this add-on when:
- Universal fallback needed across machines that may or may not have GPUs
- Container deployments (no GPU passthrough)
- ARM Linux (Graviton, Ampere) — Cisco's NEON build works well
- Cross-platform single SW codepath wanted (same encoder on Linux + Windows)

Skip when:
- Always have a GPU and a HW encoder add-on installed
- On macOS — prefer VideoToolbox SW (Apple-tuned for ARM, faster on Apple Silicon)

---

## Status

✅ **Working** — implemented today as the default SW encoder in featherdesk
(`internal/encode/openh264.go`). The refactor moves it to `internal/encode/openh264/`,
built as the `openh264` add-on shared library, but the encode code stays the same.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_openh264]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

This add-on implements `stream.ConfigurableEncoder` (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). All updates flow through `UpdateStreamParams(p stream.Params)`.

| Param change | OpenH264 API | Hot? |
|--------------|--------------|------|
| `FPS` | `ISVCEncoder::SetOption(ENCODER_OPTION_FRAME_RATE, &fps)` | yes |
| `BitrateBps` | `ISVCEncoder::SetOption(ENCODER_OPTION_BITRATE, &b)` | yes |
| `QP` | `ISVCEncoder::SetOption(ENCODER_OPTION_SVC_ENCODE_PARAM_EXT, &param)` | yes |
| `KeyframeInterval` | `param.uiIntraPeriod` via `ENCODER_OPTION_SVC_ENCODE_PARAM_EXT` | yes |
| `Width`, `Height` | requires teardown + `Initialize` (returns `stream.ErrRequiresRestart`) | no |
| `BitDepth=10` / `HDR=true` | rejected with `stream.ErrHDRUnsupported` (OpenH264 is 8-bit only -- pipeline switches to HEVC encoder) | n/a |
