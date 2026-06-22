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

## Pluggable Architecture (Default + Add-Ons)

FeatherDesk is a **pluggable, add-on based project**. The core binary ships with the
encoders/capturers needed to cover the vast majority of hardware. Vendor-specific or
emerging-tech paths are shipped as **optional add-on binaries** that the core probes for
at runtime.

### Ship-by-default (in the core binary)

| Platform | Default capture | Default HW encode | Default SW encode |
|----------|----------------|-------------------|-------------------|
| Linux | KMS/DRM+EGL → X11grab fallback | **VA-API** (Intel/AMD/NVIDIA via wrapper) | **OpenH264 CGo** |
| Windows | DXGI Desktop Duplication → WGC fallback | (TBD — see Windows spec) | OpenH264 CGo |
| macOS | ScreenCaptureKit | **VideoToolbox** (HW + SW) | VideoToolbox SW |

The default set ships **on every binary** of FeatherDesk. Single download, works
on every supported machine.

### Add-on binaries (optional, vendor-specific)

| Add-on | Platform | Reason for separate binary |
|--------|----------|---------------------------|
| NVENC direct | Linux + Windows | Unlocks NVIDIA-specific features (REF_FRAMES_INVALIDATION) unavailable via VA-API wrapper |
| AMF on ROCm | Linux | AMD-specific tuning beyond what Mesa VA-API exposes |
| Vulkan Video | Linux + Windows | Cross-vendor royalty-free path; emerging, not yet mature for production primary |
| AMF (Windows) | Windows | AMD primary HW path on Windows |
| Quick Sync (QSV) | Windows | Intel primary HW path on Windows (oneVPL) |

**How add-ons work:**
- Each add-on is a separate compiled binary or Go build-tagged variant
- Naming convention: `viewport-rds-{platform}-{vendor}` (e.g. `viewport-rds-linux-nvenc`)
- Same `Encoder` interface, same protocol, same client
- Pipeline probes available encoders at startup, picks best
- User can ship just the default binary OR the default + any subset of add-ons

**Priority order at runtime probe (Linux example):**
```
1. NVENC add-on present?       → use NVENC direct (NVIDIA only, best NVIDIA path)
2. AMF-ROCm add-on present?    → use AMF (AMD only, opt-in beyond Mesa VA-API)
3. Vulkan Video add-on present?→ use Vulkan Video (cross-vendor, when mature)
4. VA-API in default binary    → use VA-API (Intel/AMD always, NVIDIA via wrapper)
5. OpenH264 SW in default      → universal fallback
```

This pattern keeps the default binary minimal and dependency-light while letting power
users opt into vendor-specific performance gains.

---

## Module Map (Core Modules)

The system is decomposed into 11 plug-and-play modules. Each module has its own spec
sheet with complete interface contracts, internal architecture, and refactoring directives.

| # | Module | Spec File | Responsibility |
|---|--------|-----------|----------------|
| 1 | **Capture** | [`./MODULE_CAPTURE.md`](./MODULE_CAPTURE.md) | Screen frame acquisition (KMS/DRM/EGL/X11/PipeWire) |
| 2 | **Encode** | [`./MODULE_ENCODE.md`](./MODULE_ENCODE.md) | Software video encoding (OpenH264 default) |
| 3 | **Hardware Encode** | [`./MODULE_HARDWARE_ENCODE.md`](./MODULE_HARDWARE_ENCODE.md) | Zero-copy GPU encoding interface (DMA-BUF) |
| 4 | **Custom libva** | [`./MODULE_CUSTOM_LIBVA.md`](./MODULE_CUSTOM_LIBVA.md) | Direct VA-API CGo implementation (no ffmpeg) |
| 5 | **Input** | [`./MODULE_INPUT.md`](./MODULE_INPUT.md) | Remote input injection (uinput keyboard/mouse) |
| 6 | **Audio** | [`./MODULE_AUDIO.md`](./MODULE_AUDIO.md) | System audio capture (PipeWire) |
| 7 | **Protocol** | [`./MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) | Wire protocol (framing, serialization, versioning) |
| 8 | **Server** | [`./MODULE_SERVER.md`](./MODULE_SERVER.md) | WebSocket server, client management, TLS |
| 9 | **Logger** | [`./MODULE_LOGGER.md`](./MODULE_LOGGER.md) | Structured logging subsystem |
| 10 | **Client** | [`./MODULE_CLIENT.md`](./MODULE_CLIENT.md) | Browser-based viewer (WebCodecs + AudioWorklet) |
| 11 | **Pipeline** | [`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md) | Orchestrator: lifecycle, pacing, frame drops, wiring |

---

## Platform & Add-On Spec Index

OS-specific platform specs and vendor-specific add-on encoder specs live under
[`../ADD-ON-SPECS/`](../ADD-ON-SPECS/). This index is the **single source of truth**
for where any platform or add-on document lives — never duplicate specs, always link here.

### Platform specs

| Platform | Spec | Default capture | Default encode |
|----------|------|----------------|----------------|
| **Cross-platform compat** | [`ADD-ON-SPECS/CENTRAL_PLATFORM_COMPAT.md`](../ADD-ON-SPECS/CENTRAL_PLATFORM_COMPAT.md) | — | — |
| **Linux** | [`ADD-ON-SPECS/Linux/LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/LINUX_SPEC.md) | KMS+EGL / X11grab | VA-API → OpenH264 |
| **macOS** | [`ADD-ON-SPECS/macOS/MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/MACOS_SPEC.md) | ScreenCaptureKit | VideoToolbox |
| **Windows** | [`ADD-ON-SPECS/Windows/WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/WINDOWS_SPEC.md) | DXGI / WGC | (TBD) |

### Linux capture add-on specs

| Add-on | Spec | Hardware | Status |
|--------|------|---------|--------|
| NvFBC | [`ADD-ON-SPECS/Linux/capture/NVFBC_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/capture/NVFBC_LINUX_SPEC.md) | NVIDIA proprietary driver | 📋 Specced |

> **Default binary capture is KMS+EGL only.** KMS+EGL works on X11, Wayland (all
> compositors), and headless — it operates below the display server, so display server
> choice is irrelevant. Requires root / `CAP_SYS_ADMIN`. No-root fallback paths
> (XShm, PipeWire portal, wlr-screencopy, X11grab) were considered and explicitly
> rejected — none beat KMS+EGL when root is available, and no-root deployment is not
> currently a target.
>
> **Intel / AMD do not need capture add-ons** — neither vendor has a proprietary
> capture API on Linux. KMS+EGL is the entire path.

See [`ADD-ON-SPECS/Linux/capture/README.md`](../ADD-ON-SPECS/Linux/capture/README.md)
for the full rationale.

### Linux encoder add-on specs

| Add-on | Spec | Hardware | Status |
|--------|------|---------|--------|
| NVENC direct | [`ADD-ON-SPECS/Linux/encoders/NVENC_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/NVENC_LINUX_SPEC.md) | NVIDIA Kepler+ | 📋 Specced |
| AMF on ROCm | [`ADD-ON-SPECS/Linux/encoders/AMF_ROCM_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/AMF_ROCM_SPEC.md) | AMD GCN+ via ROCm | 📋 Specced |
| Vulkan Video | [`ADD-ON-SPECS/Linux/encoders/VULKAN_VIDEO_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/VULKAN_VIDEO_SPEC.md) | Any Vulkan 1.3+ GPU | 📋 Specced |

> **Intel on Linux is not a separate add-on** — Intel Quick Sync is exposed exclusively
> through VA-API on Linux. The default binary's VA-API path already covers Intel
> Sandy Bridge through Arc.

### macOS capture add-on specs

**No add-ons needed.** ScreenCaptureKit is the only capture API on macOS 26+. All
legacy alternatives (CGDisplayStream, CGWindowListCreateImage, etc.) were removed.

See [`ADD-ON-SPECS/macOS/capture/README.md`](../ADD-ON-SPECS/macOS/capture/README.md)
for the explanation. SCK is specced in `ADD-ON-SPECS/macOS/MACOS_SPEC.md`.

### macOS encoder add-on specs

**No add-ons needed.** VideoToolbox is a single unified API that covers Intel Quick
Sync, AMD VCE/GVA, Apple Media Engine (M1/M2+), and Apple's software encoder.

See [`ADD-ON-SPECS/macOS/encoders/README.md`](../ADD-ON-SPECS/macOS/encoders/README.md)
for the explanation. VideoToolbox is specced in `ADD-ON-SPECS/macOS/MACOS_SPEC.md`.

### Windows capture add-on specs

⏸️ **Pending architecture discussion** — see
[`ADD-ON-SPECS/Windows/capture/README.md`](../ADD-ON-SPECS/Windows/capture/README.md).

Likely single candidate: NVENC's NvFBC for Windows (Sunshine pattern). DDup is the
default for everything else.

### Windows encoder add-on specs

⏸️ **Pending architecture discussion.** Windows has the most fragmented vendor encoder
API landscape (NVENC, AMF, QSV, MediaFoundation) with no unified equivalent of VA-API
or VideoToolbox.

See [`ADD-ON-SPECS/Windows/encoders/README.md`](../ADD-ON-SPECS/Windows/encoders/README.md)
for the open questions and likely add-on candidates.

| Add-on | Spec | Hardware | Status |
|--------|------|---------|--------|
| NVENC Windows | TBD | NVIDIA | ⏸️ Pending discussion |
| AMF Windows | TBD | AMD | ⏸️ Pending discussion |
| QSV (oneVPL) | TBD | Intel | ⏸️ Pending discussion |
| MediaFoundation | TBD | Software / ARM | ⏸️ Pending discussion |

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
    → encoder.Encode() → NALs (CPU; OpenH264 or VP8)
    cursorMode = "embedded" (server-side blend) OR "separate"
```

**Decision (confirmed):** the legacy ffmpeg-`h264_vaapi` subprocess path (which still did a CPU round-trip via `glReadPixels`→libyuv→stdin→`hwupload`) is REMOVED. Hardware = zero-copy `hwencode` module only; software = in-process OpenH264/VP8. There is no third tier.

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
- First frame after `ForceKeyframe()` MUST be a keyframe (H.264: SPS+PPS+IDR; VP8: keyframe)
- Each `[]byte` element is exactly **one NAL unit, in Annex B form (with the `00 00 00 01` start code)** for H.264; for VP8 the slice has exactly one element (the whole frame).
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
| VideoVP8 | 5 | One VP8 frame |
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
    NALs      [][]byte // Annex B NAL units (H.264) or single VP8 frame
    Width     uint16
    Height    uint16
    Timestamp uint64   // CLOCK_MONOTONIC ns, carried through from capture
    Keyframe  bool     // true if this access unit is a keyframe (derived by encoder/pipeline)
}

type Server interface {
    // Broadcast assembles one per-frame message and fans it out.
    // codecType is FrameTypeVideoH264 or FrameTypeVideoVP8.
    // The server assigns the video Sequence and detects/uses Keyframe for IDR caching.
    Broadcast(codecType uint8, f EncodedFrame)
    BroadcastAudio(chunk AudioChunk)
    // ...
}
```

**Contract Rules:**
- The pipeline carries `Width/Height/Timestamp` from the capture step through encode to here (they are NOT recomputed).
- The server owns the per-type sequence counters; the pipeline never sets them.
- `Keyframe` lets the server cache the complete keyframe message (SPS+PPS+IDR) without re-parsing — though the server also verifies via NAL/VP8 inspection.

---

## Module Dependency Graph

```
              logger (leaf - no deps)
                │
    ┌───────────┼───────────────────────────────────────────┐
    │           │           │          │             │       │
    ▼           ▼           ▼          ▼             ▼       ▼
 capture     encode     hwencode    audio         server   input
    │           │           │                        │
    │           │           │                        │
    ▼           ▼           ▼                        ▼
 (system)   (system)    (system)                 protocol
 libdrm     libyuv      libva
 EGL        openh264    libva-drm
 GBM        libvpx      libdrm
             ffmpeg

              pipeline (imports ALL modules + wires them)
                │
    ┌───────────┼───────────┼───────────┼────────────┐
    ▼           ▼           ▼           ▼            ▼
 capture    encode/hw    server      audio        input
```

**Key Properties:**
- Each domain module is a leaf or near-leaf (depends only on logger + system libs)
- Modules NEVER import each other (zero import cycles)
- Only `pipeline` imports all modules — it's the sole wiring point
- `protocol` is shared between `server` and `client` (pure data, no logic deps)
- `hwencode` and `encode` are sibling modules, not parent-child (both implement `encode.Encoder`)

---

## Orchestrator Responsibilities (Pipeline Module)

The orchestrator is now a proper module (`MODULE_PIPELINE.md`) — not inline in main.go. It:

1. Parses CLI flags (`--port`, `--fps`, `--hardware`, `--software`, `--no-audio`, `--verbose`, `--quiet`, `--bind`, `--log-file`)
2. Probes system capabilities (KMS root, VAAPI, uinput, PipeWire, ffmpeg)
3. Selects capture backend (KMS preferred, X11 fallback)
4. Selects encode path:
   - **Hardware:** DMABufCapturer + HardwareEncoder (zero-copy, GPU-resident)
   - **Software:** Capturer + Converter + Encoder (CPU round-trip)
5. Creates and connects all modules
6. Manages lifecycle (signal handling, graceful shutdown)
7. Runs the frame pipeline loop with pacing and drop logic
8. Enforces 5 FPS minimum floor under all conditions
9. Collects rolling-window statistics (fixed memory, O(1) per frame)

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
- CLI flags for user-facing options
- Compile-time constants for protocol parameters
- Runtime capability probing for hardware detection
- No config file (single-binary philosophy)

---

## Refactoring Principles

1. **Interface-First:** Every module exposes a Go interface. Implementations are private.
2. **Zero Import Cycles:** Modules never import each other (only the orchestrator imports all).
3. **Testable in Isolation:** Each module has unit tests that run without hardware.
4. **Hot-Swappable:** Changing a capture backend or encoder should be a config flag change, not a code change.
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
│       ├── main.go              # CLI parsing → pipeline.New() → pipeline.Start()
│       └── client/              # Embedded web client
│           ├── index.html
│           ├── main.js          # Entry point
│           ├── connection.js    # WebSocket management
│           ├── decoder.js       # VideoDecoder setup
│           ├── renderer.js      # Canvas rendering
│           ├── audio.js         # AudioContext + Worklet
│           ├── input.js         # Keyboard, mouse, wheel
│           └── stats.js         # FPS/bandwidth display
├── pkg/                         # Public interfaces (importable by plugins/native clients)
│   ├── capture/
│   │   └── capture.go          # Capturer, DMABufCapturer interfaces + Frame, FBInfo
│   ├── encode/
│   │   └── encode.go           # Encoder interface + I420Frame + EncoderConfig
│   ├── hwencode/
│   │   └── hwencode.go         # HardwareEncoder interface + DMABufParams
│   ├── audio/
│   │   └── audio.go            # AudioCapturer interface
│   ├── input/
│   │   └── input.go            # InputHandler interface
│   ├── protocol/
│   │   └── protocol.go         # Wire types + marshal/unmarshal (v1, 22-byte header)
│   └── logger/
│       └── logger.go           # Logger interface
├── internal/                    # Private implementations
│   ├── capture/
│   │   ├── kms/                # KMS+DRM+EGL implementation (Capturer + DMABufCapturer)
│   │   ├── x11/               # X11grab/screencast implementation (Capturer only)
│   │   └── cursor/            # Cursor compositing (software) + cursor protocol (hardware)
│   ├── encode/
│   │   ├── openh264/          # OpenH264 CGo encoder
│   │   ├── ffmpeg/            # FFmpeg subprocess encoder
│   │   ├── vp8/              # VP8 libavcodec encoder
│   │   └── convert/          # libyuv color conversion
│   ├── hwencode/
│   │   └── vaapi/            # VA-API zero-copy encoder (HardwareEncoder)
│   ├── audio/
│   │   └── pipewire/         # PipeWire pw-cat capture
│   ├── input/
│   │   └── uinput/           # Linux uinput injection
│   ├── server/
│   │   ├── server.go         # HTTP/WS server
│   │   └── client.go         # Per-client state
│   ├── logger/
│   │   └── logger.go         # Logging implementation (mutex-safe, structured)
│   └── pipeline/
│       ├── pipeline.go       # Pipeline struct, Start(), shutdown
│       ├── frameloop.go      # Main frame loop, pacing, drop logic
│       ├── probe.go          # System capability probing
│       └── stats.go          # Rolling-window statistics
├── specs/                     # This spec directory
├── go.mod
├── go.sum
└── Makefile
```

---

## Known Technical Debt (Current Codebase)

| ID | Severity | Location | Issue | Resolution |
|----|----------|----------|-------|------------|
| TD-01 | High | `kms.go:99-111` | DMA-BUF fd leak on EGL import failure | R-CAP-02 |
| TD-02 | High | `x11grab.go:113` | Recursive retry without limit (stack overflow risk) | R-CAP-05 |
| TD-03 | High | `egl.go:22-25` | Static C globals prevent thread safety | R-CAP-04 |
| TD-04 | High | `ffmpeg.go:240` | ForceKeyframe stores flag but never signals ffmpeg | R-ENC-01 |
| TD-05 | Medium | `main.go:158` | Hardcoded 2560x1440 for input device | R-INP-07 |
| TD-06 | Medium | `compositor.js:22+292` | Duplicate init() function (dead code) | R-CLI-01 |
| TD-07 | Medium | `server.go:148+client.js` | Codec type mismatch (H264 constant for VP8 data) | R-SRV-02 + R-PRO-01 (handshake) |
| TD-08 | Medium | `audio/capture.go` | Race condition on cmd/stdout fields | R-AUD-01 |
| TD-09 | Medium | `x11grab.go:165` | Hardcoded developer path `/home/aseem/...` | R-CAP-06 |
| TD-10 | Medium | `protocol.go` | No version/sequence in wire protocol | Fixed in new protocol spec (v1, 22-byte header) |
| TD-11 | Medium | `input/protocol.go:23-51` | All Inject errors silently discarded | R-INP-01 |
| TD-12 | Low | `server.go:286-306` | Custom itoa() reimplements strconv | R-SRV-03 |
| TD-13 | Low | `kms.go:39` | fps parameter accepted but unused | R-CAP-03 (pacing now specified) |
| TD-14 | Low | `main.go:176` | Unbounded stats slice grows forever | R-PIP-02 (rolling window) |

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
| TD-25 | High | `x11grab.go:120` + `main.go:295` | Video=wall-ms, Audio=wall-ms stamped at consumption; spec claimed monotonic-ns → A/V sync impossible | Canonical CLOCK_MONOTONIC ns, stamped at capture; AudioChunk carries timestamp |
| TD-26 | High | `main.go:249-252` | New-client handler forces keyframe + `capturer.Restart()` (respawns capture) → storm for all viewers | Serve cached IDR; conditional keyframe; never restart capture; rate-limit |
| TD-27 | Med | `main.go:158` | Input device hardcoded 2560×1440 ≠ stream dims → cursor offset | Input dims = Config dims; pipeline derives from capture |
| TD-28 | Med | Protocol/round-1 | Length-prefix NAL framing added client AVCC complexity for no browser benefit | Reverted to Annex B per-frame concatenation |
| TD-29 | Med | Pipeline (round-1 spec) | Frame loop discarded W/H/timestamp; `continue` didn't skip capture; dead frameSeq | EncodedFrame struct; skip-before-capture; server owns sequence |
| TD-30 | Med | hwencode (round-1 spec) | Duplicate `config` field; non-existent `vaCreateSurfaceFromFD` | Renamed `vaConfig`; use `vaCreateSurfaces`+ExternalBuffers |
| TD-31 | Med | Client (round-1 spec) | Config described as JSON text vs protocol's binary frame 6; codec "h264" too short for WebCodecs | Config = binary frame 6, full codec string |
| TD-32 | Med | Protocol/Input | InputAck had nothing to echo (no input seq) | Input messages carry `seq`; server echoes in InputAck |
| TD-33 | Low | Protocol | KeyframeReq/Resize as binary types vs JSON-text client channel | Keyframe via JSON text; Resize via fresh Config |
| TD-34 | Low | Pipeline (round-1 spec) | `FramesCaptures` typo; unused `minInterval`; undefined Stats methods | Corrected in MODULE_PIPELINE |
