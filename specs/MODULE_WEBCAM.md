# Module Spec: Webcam Redirection

## Overview

Webcam redirection makes the **client's** physical camera appear as a local
camera on the **host**, so conferencing apps (Zoom, Teams, Meet) running on the
host can use it. This is the **reverse** of the normal video direction
(host→client screen): here video flows **client → host**.

FeatherDesk uses the **virtual-camera** approach (not USB redirection): the
browser captures the webcam, encodes H.264, streams it to the host, the host
decodes and writes frames into a per-OS **virtual camera device**. This is
browser-compatible, low-bandwidth (~2 Mbps), and the same model Citrix HDX uses.

> **USB-level UVC redirection is NOT supported** — it needs kernel drivers on
> both ends, cannot work from a browser, and costs 40-660 Mbps. See
> [`CENTRAL_SPEC.md`](./CENTRAL_SPEC.md) "Not Supported".

The core module owns the **interface contract** and the **decode path**. The
per-OS virtual-camera sink is a build-tagged **add-on** (it requires an OS
driver/extension):

| Platform | Add-on | Build tag | Sink | Spec |
|----------|--------|-----------|------|------|
| Linux | v4l2loopback | `v4l2loopback` | `/dev/videoN` (v4l2loopback kernel module) | [`../ADD-ON-SPECS/Linux/webcam/V4L2LOOPBACK_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/webcam/V4L2LOOPBACK_LINUX_SPEC.md) |
| Windows | DirectShow VCam | `dshow_vcam` | DirectShow source filter (shared mem) | [`../ADD-ON-SPECS/Windows/webcam/DSHOW_VCAM_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/webcam/DSHOW_VCAM_WINDOWS_SPEC.md) |
| macOS | CoreMediaIO Ext | `cmio_ext` | CoreMediaIO Camera Extension (XPC) | [`../ADD-ON-SPECS/macOS/webcam/CMIO_EXT_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/webcam/CMIO_EXT_MACOS_SPEC.md) |

Webcam is **zero-by-default**: no virtual-camera add-on compiled in → the host
advertises no webcam capability and the client hides the webcam toggle.

---

## Pipeline

```
Browser client                                  Host
┌────────────────────────┐                      ┌──────────────────────────────┐
│ getUserMedia(720p30)    │                      │ protocol: FrameTypeWebcamH264 │
│   ↓                     │                      │   ↓                          │
│ MediaStreamTrackProcessor│  WS BINARY frame    │ H.264 decode (NV12)          │
│   ↓                     │  type 0x50          │   ↓                          │
│ VideoEncoder            │ ──────────────────► │ (color convert if sink needs) │
│   H.264 CBP, 2 Mbps,    │  (client → host)    │   ↓                          │
│   latencyMode realtime  │                      │ WebcamSink.WriteFrame(nv12)   │
│   ↓                     │                      │   ↓                          │
│ EncodedVideoChunk       │                      │ virtual camera device        │
│   → ws.send(header+data)│                      │   ↓                          │
└────────────────────────┘                      │ Zoom/Teams opens the camera  │
                                                 └──────────────────────────────┘
```

---

## Codec & Parameters

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| Codec | **H.264 Constrained Baseline** (`avc1.42E01F`, Level 3.1) | WebCodecs-supported, no B-frames (no reorder delay), host can decode |
| Resolution | 720p (1280×720) | Industry standard for video calls; apps re-encode anyway |
| Frame rate | 30 fps | Standard; 15 fps fallback on constrained links |
| Bitrate | 2 Mbps target (1-3 Mbps) | Sufficient for talking-head content |
| `latencyMode` | `realtime` | Prevents encoder frame buffering |
| Keyframe | every 2-5 s + on host request | Bounds recovery without MJPEG-like bandwidth |
| Pixel format to sink | **NV12** | Preferred by conferencing apps; minimal/no color convert |

Latency budget: ~30-60 ms LAN, ~50-100 ms WAN — within video-conferencing
tolerance (~150 ms), leaving room for the call's own encode/transit.

---

## Public Interface

```go
package webcam

// Sink is the contract every virtual-camera add-on implements. The core decode
// path feeds it decoded NV12 frames; the add-on presents them as a local camera.
type Sink interface {
    // Start creates/opens the virtual camera device at the given geometry and
    // frame rate. Called when the client begins webcam sharing.
    Start(cfg SinkConfig) error

    // WriteFrame pushes one decoded NV12 frame to the virtual camera.
    // Must be cheap (write to /dev/videoN, shared mem, or XPC); called at fps.
    WriteFrame(nv12 []byte, ptsNanos uint64) error

    // Stop tears down the virtual camera device (client stopped sharing).
    Stop() error

    Close() error
}

type SinkConfig struct {
    Width, Height int
    FPS           int
    Label         string // device name shown to host apps (e.g. "FeatherDesk Camera")
    Logger        *slog.Logger
}

// Receiver is the core decode path (not an add-on). It owns the H.264 decoder
// and drives the Sink. The pipeline wires Receiver to the active Sink add-on.
type Receiver interface {
    // HandleFrame decodes one FrameTypeWebcamH264 payload and writes the
    // resulting NV12 frame to the Sink. Forces decoder reset on a new keyframe
    // after loss.
    HandleFrame(payload []byte, ptsNanos uint64) error

    // OnClientStart/OnClientStop manage Sink lifecycle.
    OnClientStart(cfg SinkConfig) error
    OnClientStop() error

    Close() error
}
```

**Decoder.** The core H.264 decoder for webcam reuses the same software decode
capability already present for the bench/probe tooling (libavcodec via CGo or an
equivalent), producing NV12. HW decode is a later optimization; SW decode of a
720p30 2 Mbps stream is well under the per-frame budget.

---

## Wire Protocol

Webcam is client→host media on the **main** WebSocket, sent as **binary** frames.
It reuses the standard media `FrameHeader` (22 bytes) with a distinct type so the
server's binary router can tell webcam from input:

```
FrameTypeWebcamH264 = 0x50  (80)   // client → host only
```

`byte[1]` (Type) discriminates: `0x01-0x4F` → input dispatcher; `0x50` → webcam
receiver. See [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md). The 22-byte header
carries `Sequence` (webcam frame counter), `Timestamp` (capture PTS),
`Width`/`Height`, and `PayloadSize`; payload is the Annex B H.264 access unit.

A small JSON text control message starts/stops sharing (low-rate, human-triggered):

```json
{"type": "webcam_start", "width": 1280, "height": 720, "fps": 30}
{"type": "webcam_stop"}
```

---

## Browser-Side Behavior

```js
const stream = await navigator.mediaDevices.getUserMedia({
  video: { width: 1280, height: 720, frameRate: 30 }
});
const enc = new VideoEncoder({
  output: (chunk) => { /* prepend FrameHeader(type=0x50), ws.send */ },
  error: console.error
});
enc.configure({
  codec: "avc1.42E01F", width: 1280, height: 720,
  bitrate: 2_000_000, framerate: 30,
  latencyMode: "realtime", avc: { format: "annexb" }
});
const reader = new MediaStreamTrackProcessor({ track: stream.getVideoTracks()[0] })
                 .readable.getReader();
// loop: read VideoFrame → enc.encode(frame, {keyFrame: every 150 frames}) → frame.close()
```

- **Browser support:** `VideoEncoder` + `MediaStreamTrackProcessor` in Chrome/Edge
  94+. Firefox/Safari lack `MediaStreamTrackProcessor` — webcam redirection is
  Chrome/Edge-only in v1 (the toggle is hidden where unsupported).
- The client shows a clear **camera-sharing indicator** and a stop control;
  starting requires the standard `getUserMedia` permission prompt.

---

## Configuration

```toml
[webcam]
enabled   = false                  # opt-in
label     = "FeatherDesk Camera"   # device name shown to host apps
width     = 1280
height    = 720
fps       = 30
bitrate   = 2000000                # client encoder target (advisory; client may adapt)
```

Per-OS add-on tuning lives in `[addon_module_v4l2loopback]` /
`[addon_module_dshow_vcam]` / `[addon_module_cmio_ext]`.

---

## Security Considerations

- **Opt-in default.** `enabled = false`. No virtual camera is created until the
  user explicitly starts sharing AND a sink add-on is compiled in.
- **Client consent.** Capture requires the browser `getUserMedia` permission;
  the OS shows its camera-in-use indicator on the client.
- **Controller-only.** Only the controller may share a webcam; viewers cannot.
- **Lifecycle.** The virtual camera device exists only while sharing is active;
  `Stop` removes it so no stale virtual camera lingers for other host apps.
- **No host→client camera.** This is strictly client→host. The host's real
  cameras are never exposed to the client.
- **Bounded decode.** The webcam decoder enforces the negotiated geometry; frames
  that don't match are dropped (prevents a malformed-stream resource attack).

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | FrameHeader(type=0x50) encode/decode; webcam vs input routing | No |
| Unit | Sink lifecycle (start/stop idempotency) with a mock sink | No |
| Integration | Linux: frames appear on `/dev/videoN`, readable by `ffplay` | Yes (v4l2loopback) |
| Integration | Windows: virtual camera enumerated by a DirectShow test app | Yes |
| Integration | macOS: extension enumerated by an AVFoundation test app | Yes |
| E2E | Browser shares webcam → host app shows the feed; latency ≤ ~100 ms LAN | Yes |

---

## Status

📋 **Specced — not yet implemented.** Core decode/contract + per-OS sink add-ons.
Chrome/Edge client only in v1 (WebCodecs `VideoEncoder` + `MediaStreamTrackProcessor`).
