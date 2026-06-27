# Module Spec: Stream Parameters

## Overview

The Stream Parameters module defines the **central, cross-platform, codec-agnostic
streaming contract** that the pipeline owns and add-ons translate. It is the
single source of truth for resolution, frame rate, bitrate, quality, HDR, and
color space — values that change at runtime based on client requests, network
feedback, or admin actions.

**Why centralize:** Without this contract, each add-on would interpret
"resolution change" or "lower bitrate" differently, and bandwidth adaptation
would require N implementations. Centralizing the contract means the
**pipeline drives, add-ons translate**.

---

## The Contract

```go
package stream

// EncodedFrame is the pipeline-to-server handoff type. Shared by both
// SW and HW encoder paths so the server is path-agnostic.
type EncodedFrame struct {
    Data      []byte // Contiguous Annex B bitstream (start codes retained).
                     // NOT split per-NAL -- avoids decompose/recompose copy.
                     // The server prepends the 22-byte header and sends
                     // Data directly into the assembled access unit (then fragmented into datagrams).
    Width     uint16
    Height    uint16
    Timestamp uint64 // CLOCK_MONOTONIC ns, carried through from capture
    Keyframe  bool   // true if this access unit is a keyframe
    CodecType uint8  // protocol.FrameTypeVideoH264 or FrameTypeVideoHEVC
}

// Params is the cross-platform streaming contract.
// Owned by the pipeline; translated by each capture + encoder add-on.
type Params struct {
    // ── Resolution ──────────────────────────────────────────────
    // Width and Height in pixels. Both 0 = use display native resolution.
    // Changes here trigger re-init of capture (if resolution-fixed) and
    // re-config of encoder via UpdateStreamParams.
    Width, Height int

    // ── Frame rate ──────────────────────────────────────────────
    // Target capture+encode rate. Capture paces below this if compositor
    // produces fewer frames; encoder treats as ceiling for rate control.
    FPS int

    // ── Quality (mutually exclusive) ────────────────────────────
    // BitrateBps > 0 enables bandwidth-target mode (variable QP).
    // BitrateBps = 0 falls back to constant-QP mode using QP.
    BitrateBps int
    QP         int // 0..51 for H.264, codec-dependent range otherwise

    // ── Color / HDR ─────────────────────────────────────────────
    // BitDepth = 8 or 10. 10-bit requires HDR=true or explicit override.
    // HDR=true forces BitDepth=10, ColorSpace="bt2020", and switches
    // codec selection from H.264 → HEVC Main10 (no H.264 HDR profile).
    BitDepth   int
    HDR        bool
    ColorSpace string // "bt709" (SDR) | "bt2020" (HDR)

    // Chroma subsampling. "420" (default, universally decodable) | "422" | "444".
    // 4:2:2/4:4:4 sharpen text/fine detail (the remote-desktop use case) but need
    // BOTH an encoder that supports them AND a client that can decode them — so
    // they are CAPABILITY-NEGOTIATED with a transparent fall-back to "420" (see
    // "Chroma Subsampling" below). Reliable on the native client; best-effort in
    // the browser.
    ChromaSubsampling string

    // ── Keyframe behavior ───────────────────────────────────────
    // 0 = on-demand only (current default — client requests {"type":"keyframe"}
    //     on the control stream)
    // >0 = periodic IDR every N frames (only useful for stateless clients)
    KeyframeInterval int

    // ── Adaptive signals (pipeline measures, feeds back) ────────
    // These are READ-ONLY from add-ons' perspective — set by pipeline
    // based on network telemetry. Add-ons use them only as hints.
    NetworkRTTMs       int     // From QUIC SmoothedRTT + app ping/pong
    PacketLossPct      float64 // Smoothed; from server datagram-drop rate + client stats
}
```

---

## Configurable Interfaces

Add-ons implement these in addition to their base contract to support runtime
parameter changes:

```go
package stream

// Error sentinels — defined in the stream package to avoid import cycles
// between encode, hwencode, and capture packages.
var (
    // ErrRequiresRestart is returned by UpdateStreamParams when the requested
    // change cannot be applied mid-stream (pipeline tears down + recreates).
    ErrRequiresRestart = errors.New("stream: parameter change requires add-on restart")

    // ErrHDRUnsupported is returned by UpdateStreamParams when an encoder
    // cannot produce HDR output (8-bit only). The pipeline switches to an
    // HEVC-Main10-capable encoder IF one is compiled in; if NONE is available
    // (terminal case), the pipeline rejects the HDR request, sends the client
    // {"type":"hdr_unavailable"} on the control stream, and stays SDR.
    ErrHDRUnsupported = errors.New("stream: encoder does not support HDR/10-bit")

    // ErrChromaUnsupported is returned by an encoder that cannot produce the
    // requested 4:2:2/4:4:4 subsampling (e.g. OpenH264 is 4:2:0-only). The
    // pipeline falls back to "420". A CLIENT that cannot DECODE the advertised
    // chroma replies {"type":"chroma_unsupported"} and the server likewise
    // downgrades to "420" + new config + keyframe (see "Chroma Subsampling").
    ErrChromaUnsupported = errors.New("stream: encoder does not support requested chroma subsampling")

    // ErrFallbackToSoftware is returned by EncodeSurface (HW encoder) or
    // NextSurface (capturer) when the GPU path fails (surface import error,
    // driver constraint, GPU reset). Pipeline catches this once per session
    // and degrades permanently to SW path.
    ErrFallbackToSoftware = errors.New("stream: hardware path unavailable, fall back to software")
)

// Manager coordinates dynamic parameter changes across the pipeline.
// The server feeds client-driven changes (resize, set_bitrate, set_fps) and
// bandwidth-adaptation signals into the Manager, which clamps/applies hysteresis
// and computes the effective Params. It does NOT call UpdateStreamParams itself
// — it hands the effective Params to the pipeline's paramCh so the change is
// applied ON THE FRAME-LOOP GOROUTINE (M-6: Encode and UpdateStreamParams are
// never concurrent). See MODULE_PIPELINE "applyParams".
type Manager interface {
    // Apply clamps + records the requested params and enqueues the effective
    // result for the frame loop. Returns the effective params (which may differ
    // from requested due to clamping/hysteresis).
    Apply(requested Params) (effective Params, err error)

    // Current returns the active parameters.
    Current() Params
}
```

The `Configurable*` interfaces live in their respective packages but import
`stream.Params` and return `stream.ErrRequiresRestart` / `stream.ErrHDRUnsupported`:

```go
package encode  // SW encoder add-ons

type ConfigurableEncoder interface {
    Encoder
    UpdateStreamParams(p stream.Params) error
}

package hwencode  // HW encoder add-ons

type ConfigurableHardwareEncoder interface {
    HardwareEncoder
    UpdateStreamParams(p stream.Params) error
}

package capture  // Capture add-ons

type ConfigurableCapturer interface {
    Capturer
    UpdateStreamParams(p stream.Params) error
}
```

Add-ons that do **not** implement these interfaces are treated as immutable:
the pipeline tears them down and recreates with the new params whenever
parameters change.

---

## Per-Add-On Translation Table

Each add-on translates `stream.Params` to its native concepts:

### Encoders

| `stream.Params` field | OpenH264 | x264 (subprocess) | NVENC | AMF | MF HW | VT HW |
|----------------------|----------|-------------------|-------|-----|-------|-------|
| `Width`, `Height` | `SetOption(SVC_ENCODE_PARAM_EXT)` — requires re-init | Restart ffmpeg with new `-s WxH` | `nvEncReconfigureEncoder` (hot if within initial `maxEncodeWidth/Height`) | `Terminate` + `ReInit` (cold -- AMF does NOT support hot resolution change) | `IMFTransform` teardown + reinit | `VTCompressionSessionInvalidate` + recreate |
| `FPS` | `SetOption(FRAMERATE)` (hot) | Restart with new `-r` | `nvEncReconfigureEncoder` (hot) | `SetProperty(FRAMERATE)` (hot) | `MF_MT_FRAME_RATE` (requires reinit) | `kVTCompressionPropertyKey_ExpectedFrameRate` (hot) |
| `BitrateBps` | `SetOption(BITRATE)` (hot) | Restart with new `-b:v` | `nvEncReconfigureEncoder` (hot) | `SetProperty(TARGET_BITRATE)` (hot) | `CODECAPI_AVEncCommonMeanBitRate` (hot via property store) | `kVTCompressionPropertyKey_AverageBitRate` (hot) |
| `QP` | `SetOption(SVC_ENCODE_PARAM)` (hot) | Restart with new `-crf` | `nvEncReconfigureEncoder` (hot) | `SetProperty(QP_I/QP_P)` (hot) | `CODECAPI_AVEncCommonQuality` (hot) | `kVTCompressionPropertyKey_Quality` (hot) |
| `BitDepth=10` + `HDR` | ❌ Unsupported (8-bit only) — pipeline switches encoder | `-pix_fmt yuv420p10le -profile:v high10` (restart, but **AVC HDR is not in WebCodecs scope** — pipeline switches to HEVC) | `NV_ENC_PIC_PARAMS_HEVC` Main10 profile (requires HEVC codec selection) | `SetProperty(BIT_DEPTH=10, PROFILE=MAIN10)` (HEVC only) | HEVC Main10 MFT subtype (requires HEVC mf_hw add-on) | `kVTProfileLevel_HEVC_Main10_AutoLevel` (requires vt_hw HEVC support) |
| `KeyframeInterval` | `SetOption(SVC_ENCODE_PARAM)` | Restart with `-g` | `nvEncReconfigureEncoder` (hot) | `SetProperty(IDR_PERIOD)` (hot) | `CODECAPI_AVEncMPVGOPSize` | `kVTCompressionPropertyKey_MaxKeyFrameInterval` (hot) |
| `NetworkRTTMs`, `PacketLossPct` | Ignored (no rate-distortion hooks) | Ignored | Used by `nvEncSetIOCudaStreams` for low-latency RC | Used by `RATE_CONTROL_HQVBR_QVBR` quality boost | Ignored | Used by `kVTCompressionPropertyKey_AverageBitRate` headroom |

### Capturers

| `stream.Params` field | KMS+EGL | NvFBC | SCK (macOS) | DXGI DD |
|----------------------|---------|-------|-------------|---------|
| `Width`, `Height` | Native capture; the **encoder** scales (SW: libyuv `I420Scale`; HW: in-encoder) | Native capture; encoder scales | Native capture; encoder scales (SCK *can* also scale via `SCStreamConfiguration.{width,height}`, but default is encoder-scale to keep the invariant) | Native capture; encoder scales |
| `FPS` | Pipeline pacing (capture is event-driven) | Pipeline pacing | `SCStreamConfiguration.minimumFrameInterval` (hot) | `IDXGIOutputDuplication::AcquireNextFrame` timeout |
| `BitDepth=10` + `HDR` | Request `DRM_FORMAT_XRGB2101010` framebuffer (driver-dependent) | NvFBC supports HDR via `NVFBC_FRAME_GRAB_FLAGS_NOWAIT` + 10-bit pixel format | `SCStreamConfiguration.pixelFormat = kCVPixelFormatType_64RGBALeAccurate` (requires macOS 14+) | `DXGI_FORMAT_R10G10B10A2_UNORM` (requires HDR enabled in Display Settings) |
| `ColorSpace` | Reported per surface metadata; pipeline annotates encoder | Reported per surface | Set automatically based on display | `IDXGIOutput6::GetDesc1()` → `DXGI_OUTPUT_DESC1.ColorSpace` |
| `NetworkRTTMs`, `PacketLossPct` | Ignored (capture isn't bandwidth-sensitive) | Ignored | Ignored | Ignored |

---

## HDR Pipeline (Full Flow)

When `Params.HDR = true`, the pipeline enforces an HDR-capable chain end-to-end.
HDR has multiple touchpoints and **only HEVC Main10 is HDR-viable** (no H.264 HDR
profile is in the WebCodecs spec).

### Selection cascade

```
1. Pipeline receives Params{HDR: true}
2. Validate selected capture add-on supports 10-bit:
   - KMS+EGL: requires DRM_FORMAT_XRGB2101010 support on the active card
   - DXGI DD: requires HDR enabled in Windows Display Settings
   - SCK:     requires macOS 14+ and an HDR-capable display
   - NvFBC:   requires Capture SDK 8.0+ with bWithHDR flag
3. Switch encoder selection:
   - Reject H.264-only encoders (OpenH264, x264 standard build)
   - Require HEVC Main10 capable: NVENC, AMF, MF HW HEVC, VT HW HEVC
   - TERMINAL CASE: if NO HEVC-Main10 encoder is compiled in, the HDR request
     is rejected — the server sends {"type":"hdr_unavailable"} on the control
     stream and the session STAYS SDR (H.264, bt709). The pipeline does not
     half-switch capture to 10-bit. This is the only graceful failure mode.
4. Configure capture add-on:
   - UpdateStreamParams() with BitDepth=10, HDR=true, ColorSpace="bt2020"
   - Capture re-initializes with 10-bit pixel format
5. Configure encoder add-on:
   - UpdateStreamParams() — encoder switches to HEVC Main10 profile
   - The ENCODER ADD-ON owns SEI insertion: it emits the HDR10 mastering-display
     + content-light-level SEI NALs inside each keyframe access unit
     (VPS + SPS + PPS + prefix-SEI + IDR). The pipeline/server never synthesize
     SEI — they only carry the bytes the encoder produced.
6. Issue config handshake to client:
   - codec = "hvc1.2.4.L93.B0" (HEVC Main10, Level 3.1)
   - Add hdr_metadata block with display mastering + content light level
7. Client configures VideoDecoder:
   - {codec: "hvc1.2.4.L93.B0", hardwareAcceleration: "prefer-hardware"}
   - Canvas attaches with colorSpace: "rec2100-hlg" or "rec2100-pq"
```

### HDR is a one-way trip per session

Once an encoder is initialized for HDR (10-bit Main10), it cannot revert to
SDR (8-bit) without full teardown. The pipeline treats `HDR` changes as
restart-requiring on every add-on.

### What's deferred for HDR

- **Tonemapping** (HDR capture → SDR encode for clients without HDR): not in
  scope; HDR mode is all-or-nothing per session.
- **Dolby Vision / HDR10+** (dynamic metadata): not in scope; HDR10 static
  metadata only.
- **AV1 HDR** (HEVC alternative): not in scope until AV1 HW encoders are
  ubiquitous (currently RDNA3+/RTX 40+/M2+ only).

---

## Chroma Subsampling (4:2:0 / 4:2:2 / 4:4:4)

`Params.ChromaSubsampling` selects the chroma format. **4:2:0** is the universal
default; **4:2:2/4:4:4** sharpen text and fine UI detail — the remote-desktop win
(this is Parsec's headline feature) — but are gated by both encoder and decoder
support, so they are **capability-negotiated with a transparent fall-back to
4:2:0**. They are **reliable on the native client (v2)** and **best-effort in the
browser**.

### Encoder support (advertised via probe; see MODULE_ENCODE / MODULE_HARDWARE_ENCODE)

| Encoder | 4:2:0 | 4:2:2 | 4:4:4 |
|---------|:----:|:----:|:----:|
| OpenH264 | ✅ | ❌ | ❌ (4:2:0-only — picking 444 here falls back) |
| x264 (subprocess) | ✅ | ✅ | ✅ |
| NVENC | ✅ | 🔶 (HEVC RExt) | ✅ (H.264 + HEVC on Turing+) |
| AMF / QSV / VideoToolbox | ✅ | 🔶 vendor-dependent | 🔶 vendor-dependent |

### Codec string carries the chroma (so the client can probe it)

The chroma is encoded in the **profile** of the WebCodecs codec string the
`config` message advertises — the client passes it to `VideoDecoder` and probes it:
- H.264 4:2:0 = High (`avc1.64…`); **4:2:2** = High 4:2:2 (`avc1.7A…`); **4:4:4** =
  High 4:4:4 Predictive (`avc1.F4…`).
- HEVC 4:2:0 = Main; **4:2:2/4:4:4** = Range Extensions profiles (`hvc1.4…` RExt).

### Negotiation + fall-back (two gates, both end at 4:2:0)

```
1. Pipeline wants ChromaSubsampling = "444".
2. ENCODER gate: the active encoder advertises its max chroma. If it can't do 444,
   it returns stream.ErrChromaUnsupported → pipeline downgrades to the encoder's
   best (e.g. 420 for OpenH264; 422 if that's the ceiling).
3. CLIENT gate: the server advertises the resulting codec string in `config`. The
   client runs VideoDecoder.isConfigSupported({codec}); if it can't decode it, the
   client sends {"type":"chroma_unsupported"} on the control stream.
4. The server downgrades to "420", re-sends `config` (4:2:0 codec string), and
   forces a keyframe. 4:2:0 is guaranteed decodable everywhere — this gate always
   terminates.
```

This mirrors the HDR terminal-case pattern. A change to `ChromaSubsampling` is
**restart-requiring** on the encoder (new profile), like a resolution change.

### What's not in scope

- **Per-client chroma** (one viewer 4:4:4, another 4:2:0): the stream is one
  broadcast, so chroma is session-wide — if any negotiated client can't decode
  4:4:4, the whole session falls to 4:2:0 (or the operator pins 4:2:0).

---

## Dynamic Resolution Change Flow

The client (browser window resize) drives resolution changes. The pipeline owns
the policy; add-ons translate.

### Sequence

```
[client]                              [server]                       [pipeline]
   │                                     │                              │
   │ window.onresize fires               │                              │
   │ debounce 250ms                      │                              │
   │                                     │                              │
   ├─ control-stream JSON: {"type":"resize",                            │
   │  "width":1920, "height":1080}       │                              │
   ├─────────────────────────────────────>                              │
   │                                     │ validate against display     │
   │                                     │ (clamp to native dims)       │
   │                                     ├──── paramCh ────────────────>│
   │                                     │                              │ effective Params{W:1920,H:1080}
   │                                     │                              │ applyParams() on the FRAME LOOP:
   │                                     │                              │   → capture.UpdateStreamParams
   │                                     │                              │   → encoder.UpdateStreamParams
   │                                     │                              │     (or restart if required)
   │                                     │                              │ ForceKeyframe (new dims invalidate
   │                                     │                              │  reference frames)
   │                                     │                              │
   │                                     │ Send fresh config message    │
   │                                     │ (JSON line) new width/height │
   │ <───────────────────────────────────┤                              │
   │                                     │                              │
   │ Reconfigure VideoDecoder            │                              │
   │ Resize canvas                       │                              │
   │ Resume decoding (new keyframe       │                              │
   │  arrives next)                      │                              │
```

### Constraints

- **Server clamps** to display native resolution. Client cannot request
  4K when display is 1080p.
- **One reconfig at a time.** Concurrent resize requests are coalesced; the
  pipeline applies only the latest.
- **Hysteresis.** Server applies a resize if ANY dimension changes by >5%
  OR if the aspect ratio changes by >2%. This prevents oscillation during
  continuous resize drags while avoiding the stretching artifact that a
  10% per-dimension threshold would cause on aspect ratio changes.
  When a resize IS suppressed, the server sends a control message
  `{"type":"resize_suppressed","width":W,"height":H}` so the client can
  maintain correct aspect ratio (letterbox/pillarbox) instead of stretching.
- **Re-keyframe required.** New resolution invalidates the GOP; the
  pipeline forces an IDR on the first frame after the change.

---

## Bandwidth Adaptation (Pipeline-Owned)

The pipeline monitors network telemetry (QUIC `SmoothedRTT` + app ping/pong for
RTT; the server's own datagram-drop rate + client stats messages for loss) and
feeds adaptive signals into `stream.Params`:

```
[telemetry loop, runs every 100ms]
   ↓
measure RTT (QUIC SmoothedRTT + app ping/pong),
        PacketLossPct (server frameOut-drop rate + client stats)
   ↓
update params.NetworkRTTMs, params.PacketLossPct
   ↓
[adaptation policy — two-tier response]
   FAST path (server-side): the datagram out-queue (frameOut) is dropping frames
       on overflow — the server, not the client, observes this immediately
       (SendDatagram is fire-and-forget and reports nothing). On a sustained
       drop spike: immediate 0.5× bitrate reduction (single measurement).
   SLOW path (client feedback):
       if PacketLossPct > 5% for 2 consecutive 100ms windows (200ms):
           new_bitrate = max(current * 0.7, min_bitrate)
       else if PacketLossPct < 1% for 10 consecutive windows (1s) AND RTT stable:
           new_bitrate = min(current * 1.1, max_bitrate)
   ↓
if new_bitrate != current:
   effective.BitrateBps = new_bitrate
   send effective Params to pipeline.paramCh   // applied on the frame loop (M-6)
   (no config message — a bitrate change is transparent to the client decoder)
```

### Bounds and policy knobs

Lives in `[stream.adaptive]` config section:

```toml
[stream.adaptive]
enabled              = true
min_bitrate_bps      = 1_000_000   # 1 Mbps floor
max_bitrate_bps      = 25_000_000  # 25 Mbps ceiling
loss_threshold_pct   = 5.0         # trigger downscale
recovery_threshold_pct = 1.0       # allow upscale
adjustment_factor    = 0.7         # multiply on downscale
recovery_factor      = 1.1         # multiply on upscale
```

### Resolution-level adaptation (deferred)

For severe network degradation (>15% loss for 30s), reducing resolution is
more effective than reducing bitrate. **Deferred to v2** — initial release
adapts only bitrate.

---

## Capability Probing

Add-ons advertise which `stream.Params` fields they can change without restart:

```go
type StreamParamsCapability struct {
    HotChangeable map[string]bool // e.g. {"BitrateBps": true, "Width": false}
    MinValues     map[string]any
    MaxValues     map[string]any
}

// Optional add-on method
type StreamParamsCapable interface {
    StreamParamsCapability() StreamParamsCapability
}
```

The pipeline uses this at startup to decide:
- Which fields trigger `UpdateStreamParams()` vs full add-on restart
- Validation bounds for incoming client requests (clamp to add-on's MinValues / MaxValues)

Add-ons that don't implement `StreamParamsCapable` are treated as fully
immutable: every parameter change requires restart.

---

## What Stays in `[addon_module_*]` TOML Sections

`stream.Params` fields **leave** `[encode]` and `[capture]` sections. The
add-on-specific TOML sections keep only **static, build-time tuning** that
doesn't fit the dynamic Params model:

| Add-on | Static keys retained in `[addon_module_*]` |
|--------|--------------------------------------------|
| openh264 | `threads`, `slice_mode`, `profile` (baseline/main/high) |
| x264 | `ffmpeg_path`, `preset` (ultrafast..medium), `threads`, `tune` |
| nvenc | `gpu` index, `preset` (p1..p7), `tune` (ull/ll/hq), `multipass` |
| amf | `usage` (lowlatency/transcoding), `quality` (speed/balanced/quality) |
| mf_hw | `adapter_index`, `rate_control_mode` enum |
| qsv | `adapter_index`, `target_usage` (1..7) |
| vt_hw | `realtime` bool, `profile` enum, `allow_frame_reordering` |
| dxgi_dd | `output_index`, `auto_install_vdd`, `virtual_display_*` |
| sck | `display_id`, `show_cursor` |
| kms_egl | `drm_card`, `cursor_plane` |

The TOML `[stream]` section provides **initial defaults**; runtime
`stream.Params` may diverge based on client requests and adaptive policy.

---

## Status

📋 **Specced.** Implementation requires:

1. `pkg/stream/params.go` — `stream.Params` struct + `ConfigurableEncoder` /
   `ConfigurableHardwareEncoder` / `ConfigurableCapturer` interfaces
2. Pipeline updates — telemetry loop, adaptation policy, resize message handler
3. Per-add-on `UpdateStreamParams` implementations
4. Protocol additions — `{"type":"resize"}` control-stream JSON message handler
5. HDR pipeline — encoder switching logic when `Params.HDR` flips
