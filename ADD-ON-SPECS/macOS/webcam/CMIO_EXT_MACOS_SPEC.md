# macOS Webcam Add-On: CoreMediaIO Camera Extension

## Purpose

The `cmio_ext` add-on is the macOS virtual-camera sink for the core Webcam
module (see [`../../../../specs/MODULE_WEBCAM.md`](../../../../specs/MODULE_WEBCAM.md)).
It implements `webcam.Sink`: the core decode path produces NV12 frames and calls
`WriteFrame` at fps; this add-on forwards those frames to a CoreMediaIO Camera
Extension that publishes a "FeatherDesk Camera" visible to FaceTime, Chrome,
Zoom, and every other camera app.

On macOS 12.3+ the supported mechanism is a **CoreMediaIO Camera Extension**
built on SystemExtensions/DriverKit. It runs in a sandboxed user-space process
managed by the system (not a kernel driver), and the system routes its stream to
all camera clients.

> Pre-12.3 **DAL plugins are deprecated** and are NOT used. This add-on targets
> the Camera Extension exclusively.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| CoreMediaIO / SystemExtensions (system) | Apple system | Linked, not redistributed |
| Our Go IPC side | MIT | Talks to the extension over XPC / unix socket |
| Camera Extension bundle (Swift/ObjC) | MIT (separate signed component) | Must be signed + notarized; vendored prebuilt |

Apple requires the extension itself to be Swift/ObjC implementing the CMIO
classes - it cannot be pure Go. The Go side only drives IPC and activation.

---

## How It Works

The extension is a separate signed bundle. FeatherDesk decodes H.264 to NV12 and
hands raw frames to the extension over IPC; the extension wraps them as
`CMSampleBuffer`s and serves the stream.

```
FeatherDesk (Go)                       Camera Extension (Swift, system-managed)
  H.264 decode -> NV12                    CMIOExtensionProvider
     |                                       CMIOExtensionDevice
     v                                       CMIOExtensionStream
  XPC / unix-socket send ------------->   recv NV12 + geometry
  {w, h, fps, pts, nv12}                  CVPixelBuffer (420YpCbCr8BiPlanar
     |                                       VideoRange) -> CMSampleBuffer
     |                                     stream.send(sampleBuffer)
     v                                       |
                                          FaceTime / Chrome / Zoom
```

The extension implements `CMIOExtensionProvider`, `CMIOExtensionDevice`, and
`CMIOExtensionStream`. NV12 maps to
`kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange`, so frames become
`CVPixelBuffer`s with no color convert, then `CMSampleBuffer`s carrying the pts.

### IPC channel

The Go side connects to the extension over XPC (or a unix domain socket bridge
when avoiding CGo). On `Start` it sends the negotiated geometry/fps; each
`WriteFrame` sends one NV12 buffer with its pts. The socket bridge keeps the Go
side CGo-free; the XPC variant needs a small CGo shim.

### Activation

The bundle is installed as a system extension via `OSSystemExtensionRequest`.
The user approves it once in System Settings -> General -> Login Items &
Extensions. Until approved, no virtual camera exists.

---

## Build & Distribution

```bash
go build -tags cmio_ext -o featherdesk-macos ./cmd/server
```

Distribution requires an Apple Developer account with the
`com.apple.developer.system-extension.install` entitlement. The extension bundle
must be **signed + notarized** and shipped inside the app bundle; the Go side
activates it with `OSSystemExtensionRequest` and the user approves once. The
socket-bridge IPC keeps the Go side CGo-free; the XPC path adds a small CGo shim.

---

## Constructor & Probe

```go
// internal/webcam/cmio_ext/cmio_ext_darwin.go  (build tag: cmio_ext)

// Probe returns true if the Camera Extension is installed and activated.
func Probe() bool

// New constructs the sink from its addon config. The IPC channel opens in Start.
func New(cfg Config) (webcam.Sink, error)
```

`Probe` checks activation by querying `OSSystemExtensionManager` (or looking for
`extension_id` among the activated system extensions). Not activated -> add-on
not selected, with a hint to approve it in System Settings.

`Start(cfg webcam.SinkConfig)` opens the IPC channel at `ipc_path` and sends the
geometry/fps. `Stop` closes the channel (the extension idles its stream).
`Close` releases the connection. `WriteFrame` sends one NV12 frame + pts.

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| extension not installed/activated | `Probe` false -> not selected; log "approve in System Settings" |
| activation pending (user has not approved) | `Start` error with approval instructions |
| IPC connect fails (`ipc_path`) | `Start` error |
| IPC send fails mid-stream | drop frame + log; if the peer is gone, `Stop` and surface error |
| frame geometry != negotiated | drop frame (extension also validates) |

Activation/approval failures are surfaced to the operator with explicit
System-Settings steps; transient IPC drops are rate-limited in the log.

---

## File Structure

```
internal/webcam/cmio_ext/
  cmio_ext_darwin.go      // build tag: cmio_ext (Sink impl)
  ipc_darwin.go           // XPC / unix-socket client, frame framing
  activate_darwin.go      // OSSystemExtensionRequest activation + status
  stub.go                 // build tag: !cmio_ext (no-op, never registers)
  cmio_ext_test.go        // IPC framing unit tests
  // NOTE: the Swift Camera Extension is a separate signed/vendored bundle:
  //   third_party/FeatherDeskCameraExtension/  (CMIOExtension* sources + bundle)
```

---

## Configuration

Reads `[addon_module_cmio_ext]` (see
[`../../../../specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

```toml
[addon_module_cmio_ext]
ipc_path     = "/tmp/featherdesk-cam.sock"          # XPC service name or unix socket path
extension_id = "ai.featherdesk.camera.extension"    # bundle id of the Camera Extension
```

If absent, defaults apply. Strictly validated only when this add-on is compiled
in; unknown keys fail startup.

---

## Status

Specced - not yet built. Implementation order: probe activation
(OSSystemExtensionManager) -> activate bundle + user-approval flow -> open IPC +
send geometry (Start/Stop) -> `WriteFrame` NV12 framing over IPC -> match the
Swift extension's CMSampleBuffer path -> FaceTime/Chrome integration test.
