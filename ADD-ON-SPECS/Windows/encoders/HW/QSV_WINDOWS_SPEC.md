# Windows HW Encoder Add-On: Intel Quick Sync (oneVPL)

## Purpose

Direct Intel hardware encoding on Windows via the modern **oneVPL** (Intel Video
Processing Library) SDK. Covers integrated Intel graphics (Sandy Bridge through Arrow
Lake) and discrete Intel Arc GPUs through a single API.

Bypasses MediaFoundation to unlock QSV-specific tuning:
- Low-power mode (`low_power = 1`) — runs on Intel's dedicated low-power encode silicon
- `vcm = 1` (video conferencing mode for H.264) — sub-3ms latency
- Direct rate-control tuning beyond MF defaults
- AV1 encode on Arc with full QSV control

**Intel Arc is NOT a separate add-on** — same oneVPL API, better hardware (adds AV1
encode, more concurrent sessions). This one add-on covers both Intel iGPUs and Arc.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| oneVPL (libvpl) | **MIT** ✅ | Genuinely permissive |
| Intel Media SDK (legacy) | MIT (now archived) | Predecessor of oneVPL; we use the newer SDK |
| Our CGo binding | MIT | We own this code |

No royalties. SDK and headers can be vendored and redistributed without restriction.

---

## Hardware Compatibility

| Intel GPU | H.264 | HEVC 8-bit | HEVC 10-bit | AV1 |
|-----------|-------|-----------|-------------|-----|
| Sandy Bridge (2nd gen, 2011) | ✅ | ❌ | ❌ | ❌ |
| Ivy Bridge–Haswell (3rd–4th gen) | ✅ | ❌ | ❌ | ❌ |
| Broadwell (5th gen, 2014) | ✅ | ❌ | ❌ | ❌ |
| Skylake–Coffee Lake (6th–9th gen) | ✅ | ✅ | ✅ Kaby Lake+ | ❌ |
| Ice Lake (10th gen) | ✅ | ✅ | ✅ | ❌ |
| Tiger Lake (11th gen) | ✅ | ✅ | ✅ | ❌ |
| Alder Lake–Raptor Lake (12th–13th gen) | ✅ | ✅ | ✅ | ❌ |
| **Arc A-series (DG2)** | ✅ | ✅ | ✅ | ✅ |
| **Battlemage / Lunar Lake+** | ✅ | ✅ | ✅ | ✅ |

Runtime probe via `MFXLoad` + `MFXEnumImplementations` returns the available
encoder + supported codecs.

---

## Build & Distribution

```bash
go build -tags qsv -o viewport-rds-windows-qsv.exe ./cmd/server
```

CGo config:

```go
/*
#cgo CFLAGS: -I${SRCDIR}/vpl/include
#cgo LDFLAGS: -lvpl -ld3d11 -ldxgi

#include <vpl/mfx.h>
#include <vpl/mfxstructures.h>
#include <vpl/mfxvideo.h>
#include <d3d11.h>
*/
import "C"
```

`vpl.lib` ships with the Intel graphics driver. SDK headers vendored under
`internal/encode/qsv/vpl/`.

---

## CGo Implementation Sketch

```c
// 1. Initialise oneVPL with D3D11 device
mfxLoader loader = MFXLoad();
mfxConfig cfg = MFXCreateConfig(loader);
// Filter for hardware impl with H.264 encoder + D3D11 surfaces
// ... configure mfxConfig filters

mfxSession session = NULL;
MFXCreateSession(loader, 0, &session);

mfxHDL hdl = (mfxHDL)d3d11Device;
MFXVideoCORE_SetHandle(session, MFX_HANDLE_D3D11_DEVICE, hdl);

// 2. Configure encoder for low latency
mfxVideoParam encParams = { 0 };
encParams.mfx.CodecId            = MFX_CODEC_AVC;
encParams.mfx.RateControlMethod  = MFX_RATECONTROL_CBR;
encParams.mfx.TargetKbps         = bitrate / 1000;
encParams.mfx.FrameInfo.Width    = W;
encParams.mfx.FrameInfo.Height   = H;
encParams.mfx.FrameInfo.FourCC   = MFX_FOURCC_NV12;
encParams.mfx.FrameInfo.FrameRateExtN = fps;
encParams.mfx.FrameInfo.FrameRateExtD = 1;
encParams.mfx.GopRefDist         = 1;            // no B-frames
encParams.AsyncDepth             = 1;            // critical for low latency
encParams.IOPattern              = MFX_IOPATTERN_IN_VIDEO_MEMORY;
encParams.mfx.LowPower           = MFX_CODINGOPTION_ON;

// H.264-specific: video conferencing mode
mfxExtCodingOption2 co2 = { { MFX_EXTBUFF_CODING_OPTION2, sizeof(co2) } };
co2.LookAheadDepth = 0;
co2.MaxFrameSize   = 0;
mfxExtBuffer *extBufs[] = { &co2.Header };
encParams.NumExtParam = 1;
encParams.ExtParam    = extBufs;

MFXVideoENCODE_Init(session, &encParams);

// 3. Per frame: wrap D3D11 texture as mfxFrameSurface1
mfxFrameSurface1 surface = { 0 };
surface.Data.MemId = (mfxMemId)d3d11Texture;   // shared via D3D11 device

mfxBitstream bs = { 0 };
mfxSyncPoint sync;
MFXVideoENCODE_EncodeFrameAsync(session, NULL, &surface, &bs, &sync);
MFXVideoCORE_SyncOperation(session, sync, INFINITE);
// bs.Data = encoded H.264 NALs
```

---

## Performance Targets

| Intel GPU | 1080p p50 | 1440p p50 | CPU at 60fps |
|-----------|----------|----------|-------------|
| UHD 630 (Coffee Lake, low_power) | ~5ms | ~8ms | <2% |
| Iris Xe (Tiger Lake) | ~3ms | ~5ms | <2% |
| Arc A380 | ~3ms | ~4ms | <1% |
| Arc A770 | ~2ms | ~3ms | <1% (multi-stream capable) |

Low-power mode runs the encode on Intel's dedicated low-power encode silicon (LPE),
keeping the main GPU available for other tasks. This is one of Intel's biggest
advantages over NVIDIA / AMD for thin clients.

---

## File Structure

```
internal/encode/qsv/
├── qsv.go
├── qsv_cgo.go      // build tag: qsv  (Windows-only — Linux Intel uses libva)
├── qsv_stub.go     // build tag: !qsv
├── d3d11_interop.go
├── probe.go
├── vpl/            // oneVPL SDK headers (MIT)
└── qsv_test.go
```

> Note: there is no Linux QSV add-on because Intel Quick Sync on Linux is exposed
> through VA-API, fully covered by the LIBVA Linux add-on. On Windows, QSV needs
> its own SDK path (oneVPL) — hence this Windows-only add-on.

---

## When to use this add-on

Use this add-on when:
- Intel-only deployment (especially Arc)
- Need low-power encoding for thin clients / always-on devices
- Need AV1 encode on Arc with full tuning control

Skip when:
- Heterogeneous fleet (use MF HW for cross-vendor in one binary)
- NVIDIA or AMD deployment

---

## Status

📋 Specced — not yet implemented.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_qsv]` section
of the TOML config (see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is compiled into the binary`;` unknown
keys in this section will cause startup to fail.

