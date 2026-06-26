# FeatherDesk (ViewPort RDS) - Central Architecture Specification

## Product Overview

**Name:** FeatherDesk (binary: `viewport-rds`)
**Type:** Low-latency remote desktop streaming server (Linux primary, Windows + macOS planned)
**Language:** Go 1.26+ with CGo
**Deployment:** Single binary with embedded web client + optional add-on encoder binaries
**Target:** Parsec/Sunshine-level latency on LAN

## Product Goals

- Motion-to-photon latency: <20ms on LAN at 1080p60
- Resolution: up to 2560x1440
- Framerate: 60fps (hardware), 30fps (software fallback)
- Bandwidth: 5-15 Mbps/viewer
- Memory: <50MB RSS
- Concurrent viewers: up to 25 (1 controller + 24 passive)

---

## Pluggable Architecture (Zero-by-Default)

FeatherDesk is a **fully pluggable, add-on based project**. The default binary
on every platform ships with **zero encoders and zero capture backends**. Every
backend — software, hardware, or capture — is an opt-in Go build-tagged add-on.

Users compose the binary they need by combining the capture add-on(s) and
encoder add-on(s) for their target deployment. The same source tree produces
binaries for wildly different environments (commercial BSD-only deployments,
home installs with GPL x264, NVIDIA-only servers, AMD workstations, headless
Windows VMs) without `#ifdef` spaghetti or runtime configuration overhead.

### Why zero-by-default

- **Smallest possible default binary** — no unwanted dependencies, no
  unused codecs in the wire format
- **Explicit licensing per binary variant** — the binary linked against
  GPL x264 is clearly distinct from the BSD-only OpenH264 binary
- **Deployment flexibility** — single source tree, many target variants
- **Simpler probing** — only compiled-in add-ons get probed at runtime

### Build matrix

Users compose via build tags. Examples:

| Deployment | Build command |
|------------|--------------|
| Commercial Windows, generic | `go build -tags "dxgi_dd,openh264,mf_hw" ./cmd/server` |
| Home Windows, NVIDIA | `go build -tags "dxgi_dd,x264,nvenc" ./cmd/server` |
| Commercial Linux, AMD | `go build -tags "kms_egl,openh264,libva,amf_rocm" ./cmd/server` |
| Apple Silicon Mac | `go build -tags "sck,vt_hw" ./cmd/server` |

See each platform's `encoders/README.md` and `capture/README.md` for
recommended combinations.

### Runtime probe and selection

When multiple add-ons are compiled in, the pipeline picks at runtime based
on:

1. `[capture] force_addon` / `[encode] force_addon` in TOML (forces a specific add-on)
2. Probe order (HEVC HW > H.264 HW > x264 SW > VT SW > OpenH264 SW)
3. Hardware presence (NVENC only fires if NVIDIA GPU present, etc.)
4. `ErrFallbackToSoftware` from HW encoder triggers SW fallback for the session

Per-add-on tuning lives in `[addon_module_<build_tag>]` TOML sections, not
in code. See [`./MODULE_CONFIG.md`](./MODULE_CONFIG.md).

---

## Module Map (Core Modules)

The system is decomposed into 8 plug-and-play modules. Each module has its own spec
sheet with complete interface contracts, internal architecture, and refactoring directives.

| # | Module | Spec File | Responsibility |
|---|--------|-----------|----------------|
| 1 | **Capture** | [`./MODULE_CAPTURE.md`](./MODULE_CAPTURE.md) | Cross-platform `Capturer` interface contract (concrete impls are add-ons per OS) |
| 2 | **Encode** | [`./MODULE_ENCODE.md`](./MODULE_ENCODE.md) | Software encoder interface contract (concrete impls are add-ons) |
| 3 | **Hardware Encode** | [`./MODULE_HARDWARE_ENCODE.md`](./MODULE_HARDWARE_ENCODE.md) | Hardware encoder interface contract (concrete impls are add-ons) |
| 4 | **Protocol** | [`./MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) | Wire protocol (framing, serialization, versioning) — transport-agnostic; see also [`./FUTURE_NATIVE_CLIENT.md`](./FUTURE_NATIVE_CLIENT.md) |
| 5 | **Server** | [`./MODULE_SERVER.md`](./MODULE_SERVER.md) | HTTPS+WebSocket transport, client management, TLS |
| 6 | **Client** | [`./MODULE_CLIENT.md`](./MODULE_CLIENT.md) | Browser-based viewer (WebCodecs) |
| 7 | **Pipeline** | [`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md) | Orchestrator: probe + select compiled-in add-ons, lifecycle, pacing, frame drops, wiring |
| 8 | **Config** | [`./MODULE_CONFIG.md`](./MODULE_CONFIG.md) | TOML config schema, parsing, validation, hot reload |

> **Encoder + capture implementations are not core modules.** Every encoder
> (OpenH264 CGo, x264 subprocess, VideoToolbox, libva, NVENC, AMF, QSV,
> MediaFoundation HW) and every capture backend (KMS+EGL, NvFBC, SCK, DXGI DD)
> is a build-tagged add-on under
> [`../ADD-ON-SPECS/{Platform}/{capture,encoders}/`](../ADD-ON-SPECS/).
> The default binary ships with zero encoders and zero capture backends — users
> compile in what they need. See the index below.

> **Removed from the module map:**
> - **Logger** — replaced by stdlib `log/slog`. No dedicated module spec needed.
>   Server/Pipeline take a `*slog.Logger` directly. Behavior (text vs JSON,
>   level, output) is set via the `[log]` config section.
> - **Audio** + **Input** — deferred until video capture+encode is stable
>   across all three OSes. Specs retained at `MODULE_AUDIO.md` / `MODULE_INPUT.md`
>   for reference but marked deferred at the top of each file.

---

## Platform & Add-On Spec Index

OS-specific platform specs and vendor-specific add-on encoder specs live under
[`../ADD-ON-SPECS/`](../ADD-ON-SPECS/). This index is the **single source of truth**
for where any platform or add-on document lives — never duplicate specs, always link here.

### Platform specs

| Platform | Spec | Capture add-on(s) | Encoder add-on(s) |
|----------|------|------------------|-------------------|
| **Cross-platform compat** | [`ADD-ON-SPECS/CENTRAL_PLATFORM_COMPAT.md`](../ADD-ON-SPECS/CENTRAL_PLATFORM_COMPAT.md) | — | — |
| **Linux** | [`ADD-ON-SPECS/Linux/LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/LINUX_SPEC.md) | `kms_egl`, `nvfbc` | `openh264`, `x264`, `libva`, `nvenc`, `amf` |
| **macOS** | [`ADD-ON-SPECS/macOS/MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/MACOS_SPEC.md) | `sck` | `openh264`, `x264`, `vt_sw`, `vt_hw` |
| **Windows** | [`ADD-ON-SPECS/Windows/WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/WINDOWS_SPEC.md) | `dxgi_dd` | `openh264`, `x264`, `mf_hw`, `nvenc`, `amf`, `qsv` |

> **Default binary on every platform contains zero capture backends and zero
> encoders.** Every backend is an opt-in build-tagged add-on. Users compose the
> binary they need by combining one or more capture add-ons with one or more
> encoder add-ons. See each platform's `capture/README.md` and `encoders/README.md`
> for recommended combinations.

### Linux capture add-on specs

| Add-on | Build tag | Spec | Hardware | Status |
|--------|-----------|------|---------|--------|
| KMS+EGL DMA-BUF | `kms_egl` | [`ADD-ON-SPECS/Linux/capture/KMS_EGL_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/capture/KMS_EGL_LINUX_SPEC.md) | Universal — every GPU, any display server | ✅ Working |
| NvFBC | `nvfbc` | [`ADD-ON-SPECS/Linux/capture/NVFBC_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/capture/NVFBC_LINUX_SPEC.md) | NVIDIA proprietary driver | 📋 Specced |

> **KMS+EGL is the recommended default capture add-on.** Works on X11, Wayland
> (all compositors), and headless — it operates below the display server, so
> display server choice is irrelevant. Requires root / `CAP_SYS_ADMIN`. No-root
> fallback paths (XShm, PipeWire portal, wlr-screencopy, X11grab) were considered
> and explicitly rejected — none beat KMS+EGL when root is available, and no-root
> deployment is not currently a target.
>
> **Intel / AMD do not need capture add-ons** — neither vendor has a proprietary
> capture API on Linux. KMS+EGL is the entire path.

See [`ADD-ON-SPECS/Linux/capture/README.md`](../ADD-ON-SPECS/Linux/capture/README.md)
for the full rationale.

### Linux encoder add-on specs

| Add-on | Path | License | Spec | Hardware | Status |
|--------|------|---------|------|---------|--------|
| OpenH264 CGo | SW | BSD-2 (Cisco) | [`Linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md) | Any CPU (x86_64, ARM64) | ✅ Working |
| x264 subprocess | SW | GPL-2 (isolated) | [`Linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md) | Any CPU; needs ffmpeg | ✅ Benchmarked |
| libva direct | HW | MIT | [`Linux/encoders/HW/LIBVA_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/HW/LIBVA_LINUX_SPEC.md) | Intel + AMD + NVIDIA (via wrapper) | 📋 Specced |
| NVENC direct | HW | NVIDIA SDK | [`Linux/encoders/HW/NVENC_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/HW/NVENC_LINUX_SPEC.md) | NVIDIA Kepler+ | 📋 Specced |
| AMF on ROCm | HW | Apache 2.0 | [`Linux/encoders/HW/AMF_ROCM_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/HW/AMF_ROCM_SPEC.md) | AMD GCN+ via ROCm | 📋 Specced |

> **SW encoder choice:** OpenH264 for commercial deployments (BSD).
> x264 for home / OSS — 2× faster on multi-core CPUs but GPL contamination
> on the ffmpeg subprocess.
>
> **Intel on Linux is not a separate HW add-on** — Intel Quick Sync is exposed
> exclusively through VA-API. The `libva` add-on covers Intel Sandy Bridge through Arc.

### macOS capture add-on specs

**No add-ons needed.** ScreenCaptureKit is the only capture API on macOS 26+. All
legacy alternatives (CGDisplayStream, CGWindowListCreateImage, etc.) were removed.

See [`ADD-ON-SPECS/macOS/capture/README.md`](../ADD-ON-SPECS/macOS/capture/README.md)
for the explanation. SCK is specced in `ADD-ON-SPECS/macOS/MACOS_SPEC.md`.

### macOS encoder add-on specs

| Add-on | Path | License | Spec | Hardware | Status |
|--------|------|---------|------|---------|--------|
| OpenH264 CGo | SW | BSD-2 (Cisco) | [`macOS/encoders/SW/OPENH264_CGO_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/encoders/SW/OPENH264_CGO_MACOS_SPEC.md) | Any CPU; cross-platform | 📋 Specced |
| x264 subprocess | SW | GPL-2 (isolated) | [`macOS/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md) | Any CPU; needs ffmpeg | 📋 Specced |
| VideoToolbox SW | SW | Apple system | [`macOS/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md) | Any Mac (macOS 12.3+) | 📋 Specced |
| VideoToolbox HW | HW | Apple system | [`macOS/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md) | All Macs 2011+ (HW H.264), Skylake+/Apple Silicon (HW HEVC), M2+ (HW AV1) | 📋 Specced |

> **No vendor-specific HW add-ons on macOS** — Apple controls the entire graphics stack.
> VideoToolbox is the single API for Intel Quick Sync, AMD VCE, and Apple Media Engine.
>
> **For maximum HW performance on macOS, use VideoToolbox.** For cross-platform
> binary consistency with Linux/Windows, OpenH264 or x264 can be used as SW
> fallback (especially useful for the GPL/BSD licensing differentiation).

### Windows capture add-on specs

| Add-on | Build tag | Spec | Hardware | Status |
|--------|-----------|------|---------|--------|
| DXGI Desktop Duplication (with integrated IddCx headless install) | `dxgi_dd` | [`ADD-ON-SPECS/Windows/capture/DXGI_DD_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/capture/DXGI_DD_WINDOWS_SPEC.md) | Any GPU (WDDM 1.2+, Win 8+) | ✅ Benchmarked |

> **DXGI DD is the only Windows capture mechanism.** Benchmarking proved its
> raw acquisition overhead is sub-microsecond on every GPU, leaving no room
> for vendor-specific capture APIs (NvFBC, AMF Display Capture) to improve on.
> Output is `ID3D11Texture2D`, directly consumable by every Windows HW encoder
> (MF HW, NVENC, AMF, QSV) with zero-copy.
>
> For **headless deployments** (no physical display), the add-on bundles a
> pre-signed IddCx virtual display driver that auto-installs on first launch
> via `pnputil` (one-time UAC). Same approach as Sunshine/Moonlight.
>
> **No elevation required for standard use.** First-launch driver install
> needs one UAC prompt; subsequent runs need none.

See [`ADD-ON-SPECS/Windows/capture/README.md`](../ADD-ON-SPECS/Windows/capture/README.md)
for the full rationale, recommended combinations, and headless install flow.

### Windows encoder add-on specs

| Add-on | Path | License | Spec | Hardware | Status |
|--------|------|---------|------|---------|--------|
| OpenH264 CGo | SW | BSD-2 (Cisco) | [`Windows/encoders/SW/OPENH264_CGO_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/SW/OPENH264_CGO_WINDOWS_SPEC.md) | Any CPU | ✅ Benchmarked |
| x264 subprocess | SW | GPL-2 (isolated) | [`Windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md) | Any CPU; needs ffmpeg | ✅ Benchmarked |
| MediaFoundation HW | HW | Microsoft system | [`Windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md) | All vendors (cross-vendor via MFT routing) | ✅ Benchmarked |
| NVENC | HW | NVIDIA SDK | [`Windows/encoders/HW/NVENC_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/HW/NVENC_WINDOWS_SPEC.md) | NVIDIA Kepler+ | ✅ Benchmarked |
| AMF | HW | Apache 2.0 | [`Windows/encoders/HW/AMF_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/HW/AMF_WINDOWS_SPEC.md) | AMD GCN+ | ✅ Benchmarked |
| QSV (oneVPL) | HW | MIT | [`Windows/encoders/HW/QSV_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/HW/QSV_WINDOWS_SPEC.md) | Intel Sandy Bridge+ (covers Arc) | 📋 Specced |


> **MediaFoundation HW is the recommended cross-vendor default for Windows** —
> closest equivalent to VA-API on Linux. Ship `mf_hw` for one-binary-covers-everything;
> add vendor SDKs (NVENC/AMF/QSV) for peak performance and vendor-specific features.

### Where to register a new add-on

When adding a new vendor-specific encoder:
1. Write the spec at `ADD-ON-SPECS/{Platform}/encoders/{NAME}_SPEC.md`
2. Add a row to the relevant table in **this** section of CENTRAL_SPEC.md
3. Add a row to the compat matrix in `ADD-ON-SPECS/CENTRAL_PLATFORM_COMPAT.md`
4. Implement under `internal/hwencode/{name}/` with a Go build tag
5. Wire the runtime probe order in `MODULE_PIPELINE.md`

---

## System Architecture Diagram

```
                    ┌──────────────────────────────────────────────────────────────┐
                    │                   PIPELINE MODULE (Orchestrator)              │
                    │  Probes capabilities, selects backends, manages lifecycle    │
                    │  Frame pacing + drop decisions (floor: 5 FPS)               │
                    └───┬──────────┬──────────┬──────────┬───────────┬────────────┘
                        │          │          │          │           │
        ┌───────────────┘          │          │          │           └───────────┐
        ▼                          ▼          ▼          ▼                       ▼
┌─────────────────┐      ┌──────────────┐ ┌───────────┐ ┌───────────┐  ┌────────────┐
│  CAPTURE MODULE │      │   ENCODE     │ │   AUDIO   │ │  SERVER   │  │   INPUT    │
│                 │      │  (Software)  │ │   MODULE  │ │  MODULE   │  │   MODULE   │
│ NextFrame()     │─────▶│ Convert()    │ │           │ │           │  │            │
│ -> *Frame       │ RGBA │ Encode()     │ │ Chunks()  │ │ HTTPS+WSS │  │ uinput     │
│   (borrowed)    │      │ -> [][]byte  │ │ ->[]byte  │ │ Broadcast │  │ injection  │
│                 │      └──────┬───────┘ └─────┬─────┘ └─────┬─────┘  └────────────┘
│ NextDMABuf()    │──┐         │                │             │
│ -> *FBInfo      │  │  NALs   │                │ PCM         │
└─────────────────┘  │         │                │             │
                     │         ▼                ▼             │
                     │  ┌──────────────────────────────┐      │
                     │  │       PROTOCOL MODULE        │      │
                     │  │  v1 | 22-byte header         │◀─────┘
                     │  │  Seq + Timestamp + NAL-LP    │
                     │  └──────────────┬───────────────┘
                     │                 │
                     │                 ▼
                     │  ┌──────────────────────────────┐
                     │  │       CLIENT (Browser)        │
                     │  │  WebSocket → decode → canvas │
                     │  │  #control / #view modes      │
                     │  │  Client-side cursor render   │
                     │  └──────────────────────────────┘
                     │
                     │  ┌──────────────────────────────┐
                     └─▶│   HARDWARE ENCODE MODULE     │
                DMA-BUF │  VA-API zero-copy path       │
                  fd    │  DMA-BUF → VASurface →       │
                        │  encode → NALs (GPU-only)    │
                        └──────────────────────────────┘
```

### Two Encoding Paths (exactly two tiers — no ffmpeg-vaapi middle tier)

```
PATH B — Hardware (zero-copy, GPU-resident)  [preferred]:
    capturer.NextDMABuf() → FBInfo{fd, format, modifier, timestamp}
    → hwEncoder.EncodeDMABuf() → NALs (GPU→CPU: ~30KB compressed only)
    Use when: VA-API zero-copy available AND capturer implements DMABufCapturer
    cursorMode = "separate" (client-side cursor)

         │  on ErrFallbackToSoftware (DMA-BUF import unsupported, GPU reset, etc.)
         ▼
PATH A — Software (CPU round-trip)  [fallback / --software]:
    capturer.NextFrame() → RGBA []byte (GPU→CPU: ~24MB at 1440p)
    → converter.Convert() → I420 (CPU, SIMD libyuv)
    → encoder.Encode() → NALs (CPU; OpenH264 CGo or x264 subprocess — VP8/libavcodec/in-process-x264 rejected)
    cursorMode = "embedded" (server-side blend) OR "separate"
```

**Decision (confirmed):** the legacy ffmpeg-`h264_vaapi` subprocess path (which still did a CPU round-trip via `glReadPixels`→libyuv→stdin→`hwupload`) is REMOVED. Hardware = zero-copy `hwencode` module only; software = in-process OpenH264 (VP8/libvpx/libavcodec rejected). There is no third tier and no ffmpeg dependency anywhere.

---

## Module Interface Contracts

### Contract 1: Capture -> Encode

```go
// Capture produces raw RGBA frames
type Capturer interface {
    NextFrame() (*Frame, error)
    Close() error
}

type Frame struct {
    Data      []byte  // RGBA pixel buffer (width * height * 4)
    Width     uint32
    Height    uint32
    Timestamp uint64  // nanosecond timestamp
}
```

**Data Flow:** `capturer.NextFrame()` -> `converter.Convert(frame.Data)` -> `encoder.Encode(i420Frame)`

**Contract Rules:**
- `Frame.Data` is BORROWED — only valid until the next `NextFrame()` call. Caller must copy before calling again.
- Dimensions must remain stable across frames (no mid-stream resize without signaling)
- Timestamp must be monotonically increasing (sourced from `CLOCK_MONOTONIC`)
- `NextFrame()` may return `nil, nil` to indicate "no new frame available" (frame pacing / static screen optimization)

---

### Contract 2: Encode -> Server

```go
// Encode produces codec-specific packets
type Encoder interface {
    Encode(frame *I420Frame) ([][]byte, error)
    ForceKeyframe()
    Close() error
}
```

**Data Flow:** `encoder.Encode(frame)` returns `[][]byte` -> pipeline wraps as `EncodedFrame` -> `server.Broadcast(codecType, EncodedFrame)`

**Contract Rules:**
- `nil, nil` return means frame was skipped (no error, no output)
- First frame after `ForceKeyframe()` MUST be a keyframe (H.264: SPS+PPS+IDR)
- Each `[]byte` element is exactly **one NAL unit, in Annex B form (with the `00 00 00 01` start code)** for H.264.
- The server concatenates the elements verbatim into one per-frame message payload (no re-framing).
- Returned byte slices are OWNED by the caller (safe to hold across calls)

---

### Contract 3: Server -> Client (Wire Protocol)

See [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) for the authoritative definition. Summary:

```
[Header: 22 bytes][Payload: variable]

Header layout (little-endian):
  Byte 0:      Version (uint8, =1)
  Byte 1:      Type (uint8)
  Bytes 2-5:   Sequence (uint32, server-owned, per-type)
  Bytes 6-13:  Timestamp (uint64, CLOCK_MONOTONIC ns, stamped at capture)
  Bytes 14-15: Width (uint16)
  Bytes 16-17: Height (uint16)
  Bytes 18-21: PayloadSize (uint32)
```

**Frame Types (all server → client):**
| Type | Value | Payload |
|------|-------|---------|
| VideoH264 | 1 | One access unit: all NALs concatenated, Annex B (keyframe = SPS+PPS+IDR) |
| Ping | 2 | 8-byte nonce |
| AudioPCM | 4 | Raw S16LE PCM (Width=SampleRate, Height=Channels) |
| _(reserved)_ | 5 | Formerly VideoVP8 — VP8 codec rejected. Reserved; do not reuse without protocol version bump. |
| Config | 6 | JSON handshake (codec, dims, fps, audio, cursorMode) — sent first, and on change |
| CursorUpdate | 11 | Cursor position + optional image (client-side cursor) |
| InputAck | 14 | Echo of client input seq + server timestamp (RTT) |

One WebSocket binary message = one frame (one access unit). The server NEVER splits a frame's NALs across messages.

---

### Contract 4: Client -> Server (JSON text channel)

The client sends ONLY JSON text (never binary). Input events carry a per-connection `seq` echoed back via `InputAck`.

```json
{"type": "key", "seq": 1024, "event": "down|up", "code": "KeyA"}
{"type": "mousemove", "seq": 1025, "x": 500, "y": 300}
{"type": "mousedown", "seq": 1026, "button": 0}
{"type": "mouseup", "seq": 1027, "button": 1}
{"type": "wheel", "seq": 1028, "deltaY": -120}
{"type": "keyframe"}                  // request IDR (e.g., on detected gap)
{"type": "pong", "nonce": 12345}      // reply to server Ping
```

**Coordinate-space rule:** the `x`/`y` in `mousemove` are in the **stream coordinate space** advertised by the latest `Config` (its `width`/`height`). The server's uinput device MUST be configured to that exact range. Capture, encode, Config, and input dims must all agree (no hidden scaling).

---

### Contract 5: Audio -> Server

```go
// Audio delivers fixed-size PCM chunks, each stamped at capture time.
type AudioCapturer interface {
    Chunks() <-chan AudioChunk
    Close()
}

type AudioChunk struct {
    Data      []byte // 3840 bytes = 20ms of 48kHz stereo s16le
    Timestamp uint64 // CLOCK_MONOTONIC ns, sampled when the chunk was READ from pw-cat
}
```

**Contract Rules:**
- `Timestamp` MUST be sampled at capture time (in the audio read loop), NOT when the pipeline reads it from the channel. The channel may buffer up to ~640 ms; stamping late breaks A/V sync.
- `Timestamp` uses the SAME `CLOCK_MONOTONIC` epoch as video frames.
- `Data` is OWNED by the receiver (the channel hands off ownership; the capturer does not reuse it).

---

### Contract 6: Capture -> Hardware Encode (Zero-Copy Path)

```go
// Extended Capturer for hardware encode — exports DMA-BUF without CPU readback
type DMABufCapturer interface {
    Capturer

    // NextDMABuf returns framebuffer metadata with a DMA-BUF fd.
    // No pixels are read from GPU. Caller MUST close the returned fd.
    NextDMABuf() (*FBInfo, error)
}

type FBInfo struct {
    DMAFD     int    // File descriptor (caller owns, must close)
    Width     uint32
    Height    uint32
    Stride    uint32
    Format    uint32 // DRM fourcc (e.g., DRM_FORMAT_XRGB8888)
    Modifier  uint64 // Tiling/compression modifier
    Timestamp uint64 // CLOCK_MONOTONIC ns, stamped at capture (REQUIRED for A/V sync)
}

// Hardware encoder consumes DMA-BUF directly
type HardwareEncoder interface {
    Encoder

    // EncodeDMABuf encodes from GPU memory without CPU pixel copy.
    // fd is NOT consumed — caller retains ownership.
    EncodeDMABuf(params DMABufParams) ([][]byte, error)

    // SupportsFormat returns true if this format can be encoded
    // without a GPU-side conversion pass.
    SupportsFormat(format uint32, modifier uint64) bool
}
```

**Data Flow:** `capturer.NextDMABuf()` -> `hwEncoder.EncodeDMABuf(params)` -> pipeline wraps as `EncodedFrame` -> `server.Broadcast(codecType, EncodedFrame)`

**Contract Rules:**
- `FBInfo.DMAFD` is OWNED by caller — must be closed after `EncodeDMABuf` returns (it is synchronous; libva dups the fd internally on import)
- If hardware encode returns `ErrFallbackToSoftware`, pipeline degrades permanently to software path
- `SupportsFormat` is called once at startup to validate the pipeline is viable

---

### Contract 7: Pipeline -> Server (Encoded Frame)

```go
// The pipeline pairs encoder output with the frame's metadata before broadcasting.
type EncodedFrame struct {
    NALs      [][]byte // Annex B NAL units (H.264)
    Width     uint16
    Height    uint16
    Timestamp uint64   // CLOCK_MONOTONIC ns, carried through from capture
    Keyframe  bool     // true if this access unit is a keyframe (derived by encoder/pipeline)
}

type Server interface {
    // Broadcast assembles one per-frame message and fans it out.
    // codecType is FrameTypeVideoH264 (additional FrameType* values may be
    // added as new codecs are introduced — VP8 was rejected).
    // The server assigns the video Sequence and detects/uses Keyframe for IDR caching.
    Broadcast(codecType uint8, f EncodedFrame)
    BroadcastAudio(chunk AudioChunk)
    // ...
}
```

**Contract Rules:**
- The pipeline carries `Width/Height/Timestamp` from the capture step through encode to here (they are NOT recomputed).
- The server owns the per-type sequence counters; the pipeline never sets them.
- `Keyframe` lets the server cache the complete keyframe message (SPS+PPS+IDR) without re-parsing — though the server also verifies via NAL inspection.

---

## Module Dependency Graph

```
              log/slog (stdlib, no deps)
                │
    ┌───────────┼───────────────────────────────────┐
    │           │           │             │          │
    ▼           ▼           ▼             ▼          ▼
 capture     encode     hwencode      server      config
    │           │           │             │
    │           │           │             ▼
    ▼           ▼           ▼          protocol
 (per OS,   (per OS,    (per OS,
  add-on)    add-on)     add-on)
 libdrm     openh264    libva (Linux)
 EGL/GBM    libyuv      NVENC SDK
 (KMS+EGL)  (every SW   AMF SDK
 SCK macOS  add-on uses oneVPL (Win)
 NvFBC      libyuv for  MediaFoundation
            RGBA→I420)  VideoToolbox
                        VideoToolbox

              pipeline (imports core interfaces + probes compiled-in add-ons)
                │
    ┌───────────┼───────────┼───────────┐
    ▼           ▼           ▼           ▼
 capture    encode/hw    server      config
```

> Notes:
> - Each compiled-in add-on contributes its own native-library deps via CGo
>   (e.g. enabling `libva` build tag pulls in libva-dev at link time).
> - No `ffmpeg`, no `libavcodec`, no `libvpx` — all rejected.
> - No custom `logger` module — every module takes `*slog.Logger` directly.
> - Audio + Input not shown — deferred from the core dependency graph.

**Key Properties:**
- Each domain module is a leaf or near-leaf (depends only on stdlib + system libs via CGo)
- Modules NEVER import each other (zero import cycles)
- Only `pipeline` imports all modules — it's the sole wiring point
- `protocol` is shared between `server` and `client` (pure data, no logic deps)
- `hwencode` and `encode` are sibling modules, not parent-child (both define separate interfaces; add-ons implement one)

---

## Orchestrator Responsibilities (Pipeline Module)

The orchestrator is now a proper module (`MODULE_PIPELINE.md`) — not inline in main.go. It:

1. Loads config via `config.Load(--config path)` per [`./MODULE_CONFIG.md`](./MODULE_CONFIG.md) — the only CLI flag is `--config`
2. Probes compiled-in capture + encoder add-ons (no static enum; the runtime asks each compiled-in add-on whether its prerequisites are met)
3. Selects capture add-on per `[capture]` config (auto-probe order or forced)
4. Selects encode path per `[encode]` config:
   - **Hardware add-on** picked when it accepts the zero-copy surface handle produced by the selected capture add-on (DMA-BUF / IOSurface / D3D11 texture)
   - **Software add-on** picked when no compatible HW add-on is compiled in OR `force_addon` names a SW add-on
5. Creates and connects all modules
6. Manages lifecycle (signal handling, graceful shutdown, SIGHUP config reload)
7. Runs the frame pipeline loop with pacing and drop logic
8. Enforces 5 FPS minimum floor under all conditions
9. Exports rolling-window statistics via Prometheus on the metrics port (see `[metrics]` in MODULE_CONFIG)

---

## Cross-Cutting Concerns

### Error Handling Strategy
- Modules return errors; orchestrator decides recovery strategy
- Transient errors (capture hiccup, encode skip): log and continue
- Fatal errors (device lost, context cancelled): propagate for shutdown
- Never panic in hot path

### Buffer Ownership Model
- **Capture:** Returned `Frame.Data` is BORROWED — valid only until the next `NextFrame()` call. Caller must copy if retaining.
- **Convert:** Returned `*I420Frame` is BORROWED — valid only until the next `Convert()` call. Zero-alloc steady state.
- **Encode:** Returned `[][]byte` NALs are OWNED by caller — freshly allocated, safe to hold indefinitely.
- **Broadcast:** Server serializes header+payload into a single `[]byte` per frame, then copies into per-client write buffers.

**Rule:** Any function that returns borrowed data must document it in the interface comment. The caller must never store borrowed slices beyond the next call boundary.

### Concurrency Model
- Capture loop: single goroutine, `runtime.LockOSThread()` (X11/EGL requirement)
- Encode: synchronous call within capture goroutine (frame drops preferred over pipeline latency)
- Server broadcast: fan-out via per-client buffered channels
- Audio: separate goroutine with channel delivery
- Input: synchronous handler in server's read goroutine

### Frame Drop Strategy
- The system prioritizes realtime delivery over frame completeness
- If encode takes longer than the frame interval, the NEXT capture is skipped (not queued)
- Minimum floor: 5 FPS — the system will never drop below 5 FPS regardless of load
- Frame sequence numbers in the protocol allow clients to detect drops and request IDR if needed
- A dropped frame never enters the encode pipeline — it's discarded at capture

### Canonical Media Clock (A/V Sync)
- A single `CLOCK_MONOTONIC` epoch is established at process start.
- EVERY media timestamp on the wire — every video frame from every capture backend, and every audio chunk — is sampled from this clock in nanoseconds, AT CAPTURE TIME.
- The logger uses wall-clock (UTC) for human-readable lines; this is a SEPARATE clock and must never be used for media timestamps.
- Anti-pattern (current code, to be removed): `time.Now().UnixMilli()` for frame/audio timestamps — wrong clock domain and wrong unit.

### Cursor Model
- `Config.cursorMode` tells the client how the cursor is delivered:
  - `"embedded"` — cursor is alpha-blended into the video frame server-side (software path default).
  - `"separate"` — cursor is NOT in the video; the server sends `CursorUpdate` (position every frame, image only on change) and the client renders it as an overlay. Required for the zero-copy hardware path (frame never touches CPU); lowest latency.
- The client MUST handle both modes based on the handshake.

### Connection / Join Flow (no keyframe storm)
```
client connects → server sends Config → server sends cached IDR message (if present)
  → if NO cached IDR exists: server triggers ForceKeyframe on the active encoder
  → otherwise: NO forced keyframe (the cached IDR is self-contained: SPS+PPS+IDR)
  → client starts decoding from the IDR; sets lastSeq = first received frame's seq
  → live frames flow; gap detection starts from the 2nd live frame
```
- A join NEVER restarts the capturer (the old `capturer.Restart()` behavior is removed).
- Keyframe requests (from gap detection or join) are rate-limited by the server (e.g., max 1 forced IDR per 500 ms) to prevent storms when many clients join at once.

### Resolution-Change Flow
```
capturer detects resolution change (monitor hotplug / mode switch)
  → NextFrame/NextDMABuf returns new Width/Height (or a sentinel ErrResized)
  → pipeline: rebuild converter + encoder (new dims), call input.Resize(w,h)
  → server: send a fresh Config frame (new dims) + force a keyframe
  → client: reconfigure VideoDecoder, update input coordinate scaling
```
The pipeline owns this orchestration; no module drives it alone.

### Configuration
- Single TOML config file at a known OS-conventional path; only `--config <path>` CLI flag exists. Full schema in [`./MODULE_CONFIG.md`](./MODULE_CONFIG.md).
- Compile-time constants for protocol parameters (Version byte, header layout).
- Runtime capability probing for compiled-in add-on detection.
- Hot reload via `SIGHUP` (Linux/macOS) — most sections reload without restart; TLS / port / `force_addon` need a restart (marked in MODULE_CONFIG).

---

## Refactoring Principles

1. **Interface-First:** Every module exposes a Go interface. Implementations are private.
2. **Zero Import Cycles:** Modules never import each other (only the orchestrator imports all).
3. **Testable in Isolation:** Each module has unit tests that run without hardware.
4. **Hot-Swappable:** Changing a capture or encoder add-on is a config change (`[capture] force_addon`, `[encode] force_addon`) or a recompile with different build tags — never a code change in the pipeline.
5. **Error Propagation:** All errors flow up to the orchestrator with context (`fmt.Errorf("capture: %w", err)`).
6. **No Global State:** No package-level variables except constants. No init() functions.
7. **Explicit Lifecycle:** Every module has `New()` (create), optional `Start()` (begin work), and `Close()` (cleanup).
8. **Buffer Contracts:** Document whether returned slices are owned or borrowed.

---

## File Structure (Post-Refactor Target)

```
featherdesk/
├── cmd/
│   └── server/
│       ├── main.go              # config.Load() → pipeline.New() → pipeline.Start()
│       └── client/              # Embedded web client (//go:embed all:client)
│           ├── index.html
│           └── compositor.js    # Single bundle today; future modular split deferred
├── pkg/                         # Public interfaces (importable by future native clients)
│   ├── capture/
│   │   └── capture.go          # Capturer + zero-copy surface handle abstraction
│   │                           #   (DMA-BUF / IOSurface / D3D11 texture)
│   ├── encode/
│   │   └── encode.go           # Encoder interface + I420Frame + EncoderConfig
│   ├── hwencode/
│   │   └── hwencode.go         # HardwareEncoder interface + per-OS surface types
│   ├── protocol/
│   │   └── protocol.go         # Wire types + marshal/unmarshal (v1, 22-byte header)
│   └── config/
│       └── config.go           # TOML schema types + Load + Watch
├── internal/                    # Private implementations
│   ├── capture/
│   │   ├── kms/                # KMS+DRM+EGL Linux capture add-on (build tag: kms_egl)
│   │   ├── nvfbc/              # NvFBC Linux capture add-on (build tag: nvfbc)
│   │   ├── sck/                # ScreenCaptureKit macOS capture add-on (build tag: sck)
│   │   └── cursor/             # Cursor compositing (software) + cursor protocol (hardware)
│   ├── encode/
│   │   ├── openh264/           # OpenH264 SW encoder add-on (build tag: openh264)
│   │   ├── libva/              # libva Linux HW add-on (build tag: libva)
│   │   ├── nvenc/              # NVENC HW add-on (build tag: nvenc)
│   │   ├── amf/                # AMD AMF HW add-on (build tag: amf)
│   │   ├── qsv/                # Intel oneVPL HW add-on, Windows (build tag: qsv)
│   │   ├── vt/                 # VideoToolbox macOS SW+HW add-on (build tags: vt_sw, vt_hw)
│   │   ├── mf/                 # MediaFoundation Windows HW add-on (build tag: mf_hw)
│   │   └── convert/            # libyuv color conversion (used by every SW encoder add-on)
│   ├── server/
│   │   ├── server.go           # HTTPS/WSS server (TLS mandatory)
│   │   ├── client.go           # Per-client state
│   │   └── metrics.go          # Prometheus /metrics handler (separate port)
│   ├── config/
│   │   ├── load.go             # TOML parse + validate
│   │   └── watch.go            # SIGHUP / Service Control hot reload
│   └── pipeline/
│       ├── pipeline.go         # Pipeline struct, Start(), shutdown
│       ├── frameloop.go        # Main frame loop, pacing, drop logic
│       ├── probe.go            # Add-on probe + selection
│       └── stats.go            # Rolling-window statistics + Prometheus metric registration
│
│  (audio/ and input/ subdirs deferred — to be added when those modules are un-paused)
├── specs/                       # This spec directory
├── go.mod
├── go.sum
└── Makefile                     # Per-platform targets with build-tag composition
```

> `internal/logger/` is **gone** — replaced by stdlib `log/slog`. Every module
> that needs a logger takes `*slog.Logger` in its constructor.

---

## Known Technical Debt (Current Codebase)

> Many of the original TDs referenced files that are being **deleted entirely**
> as part of the architecture refactor (`x11grab.go`, `screencast.py`,
> `ffmpeg.go`, `vp8.go`, `vaapi.go`, `internal/logger/`). Those TDs are marked
> *obsolete* — the issue is resolved by deletion, not refactor.

| ID | Severity | Location | Issue | Resolution |
|----|----------|----------|-------|------------|
| TD-01 | High | `kms.go:99-111` | DMA-BUF fd leak on EGL import failure | Fold into `KMS_EGL_LINUX_SPEC.md` known-issues section; fix during kms add-on extraction |
| TD-02 | ~~High~~ obsolete | `x11grab.go:113` | Recursive retry without limit | `x11grab.go` being deleted (subprocess capture rejected) |
| TD-03 | High | `egl.go:22-25` | Static C globals prevent thread safety | Fold into `KMS_EGL_LINUX_SPEC.md` known-issues; fix during extraction |
| TD-04 | ~~High~~ obsolete | `ffmpeg.go:240` | ForceKeyframe stores flag but never signals ffmpeg | `ffmpeg.go` being deleted (subprocess encoders rejected) |
| TD-05 | Medium | `main.go:158` | Hardcoded 2560x1440 for input device | Deferred — Input module deferred per TECHSTACK |
| TD-06 | Medium | `compositor.js:22+292` | Duplicate init() function (dead code) | R-CLI-01 |
| TD-07 | ~~Medium~~ obsolete | `server.go:148+client.js` | Codec type mismatch (H264 constant for VP8 data) | VP8 rejected; mismatch source eliminated |
| TD-08 | Medium | `audio/capture.go` | Race condition on cmd/stdout fields | Deferred — Audio module deferred per TECHSTACK |
| TD-09 | ~~Medium~~ obsolete | `x11grab.go:165` | Hardcoded developer path `/home/aseem/...` | `x11grab.go` being deleted |
| TD-10 | Medium | `protocol.go` | No version/sequence in wire protocol | Fixed in new protocol spec (v1, 22-byte header) |
| TD-11 | Medium | `input/protocol.go:23-51` | All Inject errors silently discarded | Deferred — Input module deferred per TECHSTACK |
| TD-12 | Low | `server.go:286-306` | Custom itoa() reimplements strconv | R-SRV-03 |
| TD-13 | Low | `kms.go:39` | fps parameter accepted but unused | Fold into `KMS_EGL_LINUX_SPEC.md` (orchestrator handles pacing externally) |
| TD-14 | Low | `main.go:176` | Unbounded stats slice grows forever | R-PIP-02 (rolling window) + Prometheus export |

### New Issues Found During Design Review

| ID | Severity | Location | Issue | Resolution |
|----|----------|----------|-------|------------|
| TD-15 | High | Architecture | GPU→CPU→GPU round-trip on hardware encode path | MODULE_HARDWARE_ENCODE (zero-copy) |
| TD-16 | High | Protocol | No A/V sync mechanism | Protocol v1: shared CLOCK_MONOTONIC timestamps |
| TD-17 | Medium | Server | IDR cache missing SPS/PPS (undecodable by new clients) | R-SRV IDR cache update |
| TD-18 | Medium | Protocol | Multiple NALs per frame need grouping into one access unit | One message per frame, Annex B concatenation (TD-23); length-prefix rejected (TD-28) |
| TD-19 | Medium | Client | Always requests ?role=control (no viewer mode) | R-CLI role selection via URL hash |
| TD-20 | Medium | Architecture | No orchestrator spec (complex wiring logic undocumented) | MODULE_PIPELINE.md |
| TD-21 | Low | Server | No connection handshake (client guesses codec) | R-PRO-01: FrameTypeConfig on connect |
| TD-22 | Low | Pipeline | No frame drop strategy (unbounded latency under load) | Pipeline: 5 FPS floor + skip logic |

### Round-2 Review Findings (verified against source)

| ID | Severity | Location | Issue | Resolution |
|----|----------|----------|-------|------------|
| TD-23 | High | `server.go:149-178` | Broadcast sends ONE message PER NAL → multi-NAL H.264 yields partial access units; breaks WebCodecs | One message per frame, concatenate NALs (Annex B) |
| TD-24 | High | `server.go:164-168` | IDR cache stores only the IDR NAL; SPS/PPS (separate messages) lost → undecodable | Cache whole per-frame keyframe message (contains SPS+PPS+IDR) |
| TD-25 | High | `main.go:295` (video timestamping in main loop) | Video + Audio stamped at consumption with wall-ms; spec required monotonic-ns at capture → A/V sync impossible | Canonical CLOCK_MONOTONIC ns, stamped at capture by the capture add-on; AudioChunk carries timestamp (when audio is un-deferred) |
| TD-26 | High | `main.go:249-252` | New-client handler forces keyframe + `capturer.Restart()` (respawns capture) → storm for all viewers | Serve cached IDR; conditional keyframe; never restart capture; rate-limit |
| TD-27 | Med | `main.go:158` | Input device hardcoded 2560×1440 ≠ stream dims → cursor offset | Deferred — Input module deferred; when un-deferred: input dims = Config dims, pipeline derives from capture |
| TD-28 | Med | Protocol/round-1 | Length-prefix NAL framing added client AVCC complexity for no browser benefit | Reverted to Annex B per-frame concatenation |
| TD-29 | Med | Pipeline (round-1 spec) | Frame loop discarded W/H/timestamp; `continue` didn't skip capture; dead frameSeq | EncodedFrame struct; skip-before-capture; server owns sequence |
| TD-30 | Med | hwencode (round-1 spec) | Duplicate `config` field; non-existent `vaCreateSurfaceFromFD` | Renamed `vaConfig`; use `vaCreateSurfaces`+ExternalBuffers |
| TD-31 | Med | Client (round-1 spec) | Config described as JSON text vs protocol's binary frame 6; codec "h264" too short for WebCodecs | Config = binary frame 6, full codec string |
| TD-32 | Med | Protocol/Input | InputAck had nothing to echo (no input seq) | Input messages carry `seq`; server echoes in InputAck |
| TD-33 | Low | Protocol | KeyframeReq/Resize as binary types vs JSON-text client channel | Keyframe via JSON text; Resize via fresh Config |
| TD-34 | Low | Pipeline (round-1 spec) | `FramesCaptures` typo; unused `minInterval`; undefined Stats methods | Corrected in MODULE_PIPELINE |
