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

    // ── Keyframe behavior ───────────────────────────────────────
    // 0 = on-demand only (current default — client requests via JSON text)
    // >0 = periodic IDR every N frames (only useful for stateless clients)
    KeyframeInterval int

    // ── Adaptive signals (pipeline measures, feeds back) ────────
    // These are READ-ONLY from add-ons' perspective — set by pipeline
    // based on network telemetry. Add-ons use them only as hints.
    NetworkRTTMs       int     // Last-measured round-trip time
    PacketLossPct      float64 // Smoothed packet loss percentage
}
```

---

## Configurable Interfaces

Add-ons implement these in addition to their base contract to support runtime
parameter changes:

```go
package encode  // SW encoder add-ons

type ConfigurableEncoder interface {
    Encoder
    // UpdateStreamParams applies new parameters to the running encoder.
    // Returns ErrRequiresRestart if the requested change cannot be applied
    // mid-stream (the pipeline will tear down + recreate the encoder).
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

// Returned when an add-on cannot apply a parameter change mid-stream.
var ErrRequiresRestart = errors.New("parameter change requires add-on restart")
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
| `Width`, `Height` | `SetOption(SVC_ENCODE_PARAM_EXT)` — requires re-init if resolution changes mid-stream | Restart ffmpeg with new `-s WxH` | `nvEncReconfigureEncoder` (hot) | `SetProperty(AMF_VIDEO_ENCODER_FRAMESIZE)` (hot) | `IMFTransform::ProcessMessage(MESSAGE_NOTIFY_END_OF_STREAM)` + reinit | `VTCompressionSessionInvalidate` + recreate |
| `FPS` | `SetOption(FRAMERATE)` (hot) | Restart with new `-r` | `nvEncReconfigureEncoder` (hot) | `SetProperty(FRAMERATE)` (hot) | `MF_MT_FRAME_RATE` (requires reinit) | `kVTCompressionPropertyKey_ExpectedFrameRate` (hot) |
| `BitrateBps` | `SetOption(BITRATE)` (hot) | Restart with new `-b:v` | `nvEncReconfigureEncoder` (hot) | `SetProperty(TARGET_BITRATE)` (hot) | `CODECAPI_AVEncCommonMeanBitRate` (hot via property store) | `kVTCompressionPropertyKey_AverageBitRate` (hot) |
| `QP` | `SetOption(SVC_ENCODE_PARAM)` (hot) | Restart with new `-crf` | `nvEncReconfigureEncoder` (hot) | `SetProperty(QP_I/QP_P)` (hot) | `CODECAPI_AVEncCommonQuality` (hot) | `kVTCompressionPropertyKey_Quality` (hot) |
| `BitDepth=10` + `HDR` | ❌ Unsupported (8-bit only) — pipeline switches encoder | `-pix_fmt yuv420p10le -profile:v high10` (restart, but **AVC HDR is not in WebCodecs scope** — pipeline switches to HEVC) | `NV_ENC_PIC_PARAMS_HEVC` Main10 profile (requires HEVC codec selection) | `SetProperty(BIT_DEPTH=10, PROFILE=MAIN10)` (HEVC only) | HEVC Main10 MFT subtype (requires HEVC mf_hw add-on) | `kVTProfileLevel_HEVC_Main10_AutoLevel` (requires vt_hw HEVC support) |
| `KeyframeInterval` | `SetOption(SVC_ENCODE_PARAM)` | Restart with `-g` | `nvEncReconfigureEncoder` (hot) | `SetProperty(IDR_PERIOD)` (hot) | `CODECAPI_AVEncMPVGOPSize` | `kVTCompressionPropertyKey_MaxKeyFrameInterval` (hot) |
| `NetworkRTTMs`, `PacketLossPct` | Ignored (no rate-distortion hooks) | Ignored | Used by `nvEncSetIOCudaStreams` for low-latency RC | Used by `RATE_CONTROL_HQVBR_QVBR` quality boost | Ignored | Used by `kVTCompressionPropertyKey_AverageBitRate` headroom |

### Capturers

| `stream.Params` field | KMS+EGL | NvFBC | SCK (macOS) | DXGI DD |
|----------------------|---------|-------|-------------|---------|
| `Width`, `Height` | Output is native — pipeline scales via libyuv or GL blit | Output is native — pipeline scales | `SCStreamConfiguration.{width,height}` (requires `updateConfiguration:`) | Output is native — pipeline scales via D3D11 blit |
| `FPS` | Pipeline pacing (capture is event-driven) | Pipeline pacing | `SCStreamConfiguration.minimumFrameInterval` (hot) | `IDXGIOutputDuplication::AcquireNextFrame` timeout |
| `BitDepth=10` + `HDR` | Request `DRM_FORMAT_XRGB2101010` framebuffer (driver-dependent) | NvFBC supports HDR via `NVFBC_FRAME_GRAB_FLAGS_NOWAIT` + 10-bit pixel format | `SCStreamConfiguration.pixelFormat = kCVPixelFormatType_64RGBALeAccurate` (requires macOS 14+) | `DXGI_FORMAT_R10G10B10A2_UNORM` (requires HDR enabled in Display Settings) |
| `ColorSpace` | Reported per surface metadata; pipeline annotates encoder | Reported per surface | Set automatically based on display | `DXGI_OUTDUPL_DESC.ColorSpace` |
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
4. Configure capture add-on:
   - UpdateStreamParams() with BitDepth=10, HDR=true, ColorSpace="bt2020"
   - Capture re-initializes with 10-bit pixel format
5. Configure encoder add-on:
   - UpdateStreamParams() — encoder switches to HEVC Main10 profile
   - SEI HDR10 metadata block prepended to first NAL of every IDR
6. Issue Config handshake to client:
   - codec = "hvc1.2.4.L93.B0" (HEVC Main10 Profile 3.1)
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
   ├─ JSON text: {"type":"resize",       │                              │
   │  "width":1920, "height":1080}       │                              │
   ├─────────────────────────────────────>                              │
   │                                     │ validate against display     │
   │                                     │ (clamp to native dims)       │
   │                                     ├─────────────────────────────>│
   │                                     │                              │ params.Width = 1920
   │                                     │                              │ params.Height = 1080
   │                                     │                              │ ApplyStreamParams()
   │                                     │                              │   → capture.UpdateStreamParams
   │                                     │                              │   → encoder.UpdateStreamParams
   │                                     │                              │     (or restart if required)
   │                                     │                              │ ForceKeyframe (new dims invalidate
   │                                     │                              │  reference frames)
   │                                     │                              │
   │                                     │ Send fresh Config frame      │
   │                                     │ with new width/height        │
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
- **Hysteresis.** Server ignores changes <10% from current resolution to
  prevent oscillation during continuous resize drags.
- **Re-keyframe required.** New resolution invalidates the GOP; the
  pipeline forces an IDR on the first frame after the change.

---

## Bandwidth Adaptation (Pipeline-Owned)

The pipeline monitors network telemetry (RTT, packet loss from WebSocket
Pong roundtrip + client stats messages) and feeds adaptive signals into
`stream.Params`:

```
[telemetry loop, runs every 500ms]
   ↓
measure RTT, PacketLossPct
   ↓
update params.NetworkRTTMs, params.PacketLossPct
   ↓
[adaptation policy]
   if PacketLossPct > 5% for 3 consecutive windows:
       new_bitrate = max(current * 0.7, min_bitrate)
   else if PacketLossPct < 1% for 5 consecutive windows AND RTT stable:
       new_bitrate = min(current * 1.1, max_bitrate)
   ↓
if new_bitrate != current:
   params.BitrateBps = new_bitrate
   encoder.UpdateStreamParams(params)
   (skip Config handshake — bitrate change is transparent to client)
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
4. Protocol additions — `{"type":"resize"}` JSON text message handler
5. HDR pipeline — encoder switching logic when `Params.HDR` flips
