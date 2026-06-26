# Module Spec: Client (Browser Viewer)

## Overview

The Client module is the browser-based viewer and controller. It connects to the server via WebSocket (WSS), decodes video using the WebCodecs API, plays audio via AudioWorklet, and sends input events back to the server as **binary** WebSocket frames (compact 6-byte-header records — see [`MODULE_INPUT.md`](./MODULE_INPUT.md)). Rare control messages (keyframe req, resize, clipboard, webcam_start/stop, etc.) use **JSON text** frames; everything high-frequency is binary.

---

## Public Interface (JavaScript)

The client is a single-page application embedded in the server binary via `go:embed`. It has no build step, no framework, no external dependencies (ES modules, served as-is).

**Files (post-refactor, R-CLI-10):**
| File | Purpose |
|------|---------|
| `index.html` | HTML shell: canvas, cursor overlay, status overlay, `<script type="module">` |
| `main.js` | Entry point, reads role, wires modules |
| `connection.js` | WebSocket connect/reconnect, binary frame dispatch, JSON control send |
| `protocol.js` | 22-byte header parse, Config parse |
| `decoder.js` | VideoDecoder config (codec from handshake), keyframe detect |
| `renderer.js` | Canvas rendering |
| `cursor.js` | Client-side cursor overlay (CursorUpdate) |
| `audio.js` | AudioContext + Worklet, A/V sync |
| `input.js` | Binary input encode (DataView), HID-usage map, pointer-lock, InputAck latency |
| `clipboard.js` | clipboardchange / copy / paste interception; host-update apply |
| `files.js` | Drag-drop upload + Files panel for downloads (separate `/files` WS) |
| `webcam.js` | getUserMedia + WebCodecs VideoEncoder + binary 0x50 send |
| `gamepad.js` | rAF poll of getGamepads, diff-send 0x40, connect/disconnect 0x41/0x42, rumble apply |
| `stats.js` | FPS/bandwidth/latency display |

---

## Internal Architecture

### Connection Management

```javascript
function connect() {
    const role = isController ? "control" : "view";
    const token = getSessionToken(); // from URL hash or prompt
    const ws = new WebSocket(`wss://${location.host}/ws?role=${role}&token=${token}`);
    ws.binaryType = "arraybuffer";
    ws.onmessage = (e) => handleFrame(e.data);
    ws.onclose = () => setTimeout(connect, RECONNECT_DELAY); // 2000ms
}
```

- Auto-reconnects on disconnect with 2-second delay
- Uses `wss://` (secure WebSocket) for WebCodecs compatibility
- Role-based connection: `control` for input + video, `view` for video-only
- Token-based authentication (paired with R-SRV-01)

### Role Selection

The client determines its role from the URL:
- `https://host:port/` → viewer mode (default)
- `https://host:port/#control` → controller mode
- Viewer mode: disables keyboard/mouse capture, shows stream only
- Controller mode: captures input, requests pointer lock, sends events

### Handshake & Frame Dispatch

```
WebSocket binary frame
    → parse 22-byte header {version, type, seq, timestamp, w, h, payloadSize}
    → switch type:
        6  (Config):  JSON.parse(payload) → configure decoder (ONLY if codec/width/height changed), set cursorMode
        1  (VideoH264): decodeVideo(seq, timestamp, payload)
        7  (VideoHEVC): decodeVideo(seq, timestamp, payload)
        4  (AudioPCM):  playAudio(timestamp, payload)
        5  (reserved):  ignore (formerly VP8, rejected)
        11 (CursorUpdate): cursor.update(payload)
        12 (Clipboard): clipboard.applyHostUpdate(payload)
        14 (InputAck): input.recordAck(seq, serverTs)
        15 (GamepadRumble): gamepad.applyRumble(payload)
        2  (Ping):    send {"type":"pong","nonce":...} over text
```

**Decoder Configuration (driven by the Config handshake — fixes the round-1 codec mismatch):**
```javascript
// On Config frame:
decoder.configure({
    codec: cfg.codec,          // full WebCodecs string from server, e.g. "avc1.42E01F" (H.264) or "hvc1.*" (HW HEVC)
    optimizeForLatency: true,
    // Annex B in-band SPS/PPS → no `description` needed (avc Annex B mode)
});
streamWidth = cfg.width; streamHeight = cfg.height; cursorMode = cfg.cursorMode;
```

The client NEVER hardcodes the codec. It comes from `Config.codec` so the decoder always matches whatever the server chose to encode with (HW HEVC where available, H.264 otherwise).

### Video Decode

```
decodeVideo(seq, timestamp, payload):
    → gap detection (skip on first frame / first post-IDR transition):
        if started && seq > lastSeq + 1: ws.send('{"type":"keyframe"}')   // request IDR
    → lastSeq = seq
    → isKey = detectKeyframe(payload)        // H.264: scan NAL header for type 5 (IDR)
    → chunk = new EncodedVideoChunk({ type: isKey ? "key":"delta", timestamp, data: payload })
    → decoder.decode(chunk)
    → output: drawImage(frame) → frame.close()
```

**Keyframe Detection:**
- H.264 (Annex B): scan NAL headers for type 5 (IDR). The keyframe access unit contains SPS+PPS+IDR.
- HEVC (when HW available): scan for NAL types 19–21 (IDR_W_RADL / IDR_N_LP / CRA_NUT).

**Fast-join rule:** the client sets `lastSeq` from the FIRST frame received (the cached IDR) and does NOT run gap detection on the transition to the first live frame (avoids a false "gap" → keyframe storm). Gap detection starts from the 2nd live frame.

### Audio Playback Pipeline

```
WebSocket binary frame (Type == 4, AudioPCM)
    → extract S16LE payload (3840 bytes) + header.timestamp (monotonic ns)
    → A/V sync against latest video timestamp:
        skew = lastVideoTs - audioTs
        if skew > +40ms: hold (audio ahead) ; if skew < -40ms: drop chunk (audio behind)
    → convert to Float32Array (divide by 32768)
    → post to AudioWorklet via MessagePort

AudioWorkletProcessor:
    → receives Float32 samples via port.onmessage
    → pushes to internal ring buffer
    → process() pulls from ring buffer into output channels
```

- A/V sync uses the shared monotonic timestamps in the headers (video and audio are on the same clock — see protocol). The 40 ms threshold is the perceptual boundary.
- **Initialization:** Triggered by first user gesture (keydown/pointerdown) due to browser autoplay policy.

### Cursor Overlay (cursorMode == "separate")

```
WebSocket binary frame (Type == 11, CursorUpdate)
    → parse [x:u16][y:u16][visible:u8][imageChanged:u8][w:u16][h:u16][rgba?]
    → position a CSS/canvas overlay at (x,y) scaled to the canvas rect
    → if imageChanged: update the overlay bitmap from the RGBA data
    → if !visible: hide overlay
```

- When `cursorMode == "embedded"`, the cursor is already in the video; the client hides its overlay and ignores CursorUpdate.
- Client-side cursor moves immediately on each update without waiting for a video frame → lower perceived input latency.

### Input Handling (binary)

Input is sent as **binary** WebSocket frames using the compact record format
from [`MODULE_INPUT.md`](./MODULE_INPUT.md) — not JSON. The client encodes into a
reused `ArrayBuffer` via `DataView` (zero garbage on the hot path) and maps
`KeyboardEvent.code` → USB HID usage via a static table for layout neutrality.

```javascript
let inputSeq = 0;
const sentAt = new Map();              // seq → performance.now(), for latency
const buf = new ArrayBuffer(16), dv = new DataView(buf);  // reused; largest record fits

function header(type) {                // 6-byte shared header
    dv.setUint8(0, 1);                 // Version
    dv.setUint8(1, type);              // Type
    dv.setUint32(2, ++inputSeq, true); // Seq (LE)
    sentAt.set(inputSeq, performance.now());
    return inputSeq;
}
function send(len) { if (ws.readyState === WebSocket.OPEN) ws.send(buf.slice(0, len)); }

if (isController) {
    // Key: HID usage from code; Flags bit0 = down
    document.addEventListener('keydown', (e) => { e.preventDefault();
        header(0x10); dv.setUint16(6, hidFromCode(e.code), true); dv.setUint8(8, 1); send(9); });
    document.addEventListener('keyup', (e) => { e.preventDefault();
        header(0x10); dv.setUint16(6, hidFromCode(e.code), true); dv.setUint8(8, 0); send(9); });

    // Mouse: absolute (or relative when pointer-locked)
    canvas.addEventListener('pointermove', (e) => {
        if (document.pointerLockElement === canvas) {           // relative (FPS)
            header(0x21); dv.setInt16(6, e.movementX, true); dv.setInt16(8, e.movementY, true); send(10);
        } else {                                                 // absolute, scaled to stream space
            const r = canvas.getBoundingClientRect();
            const x = Math.round((e.clientX - r.left) / r.width  * streamWidth);
            const y = Math.round((e.clientY - r.top)  / r.height * streamHeight);
            header(0x20); dv.setUint16(6, x, true); dv.setUint16(8, y, true); send(10);
        }
    });
    canvas.addEventListener('pointerdown', (e) => { header(0x22); dv.setUint8(6, e.button); dv.setUint8(7, 1); send(8); });
    canvas.addEventListener('pointerup',   (e) => { header(0x22); dv.setUint8(6, e.button); dv.setUint8(7, 0); send(8); });
    canvas.addEventListener('wheel', (e) => { e.preventDefault();
        const unit = e.deltaMode;       // 0=pixel,1=line,2=page
        header(0x23); dv.setInt16(6, e.deltaX, true); dv.setInt16(8, e.deltaY, true); dv.setUint8(10, unit); send(11); });
}

// InputAck (binary type 14) → latency
function recordAck(seq /*, serverTs */) {
    const t0 = sentAt.get(seq);
    if (t0 !== undefined) { inputLatencyMs = performance.now() - t0; sentAt.delete(seq); }
}
```

- **Input is binary** (`ws.binaryType = 'arraybuffer'`), zero-alloc on the hot
  path. `hidFromCode()` is a static `KeyboardEvent.code` → HID-usage table.
- Pointer Lock toggles absolute (0x20) ↔ relative (0x21); request
  `canvas.requestPointerLock({ unadjustedMovement: true })` on click (Chrome/Edge
  disable mouse acceleration; Safari ignores the option).
- Every record carries `Seq`; the server's `InputAck` (binary type 14) echoes it
  for latency measurement.
- **Control** messages (keyframe, resize, set_*, clipboard, webcam_*) still use
  JSON **text** frames: `ws.send('{"type":"keyframe"}')`.

### Clipboard, File Transfer, Webcam, Gamepad (client side)

- **Clipboard** (see [`MODULE_CLIPBOARD.md`](./MODULE_CLIPBOARD.md)): on Chrome/Edge,
  request `clipboard-read`/`clipboard-write` and use the `clipboardchange` event
  to push copies (JSON text `{"type":"clipboard",...}`); write host clipboard
  pushes (binary type 12) silently. On Firefox/Safari, intercept `copy`/`paste`
  events (gesture-bound). Text + sanitized HTML only.
- **File transfer** (see [`MODULE_FILETRANSFER.md`](./MODULE_FILETRANSFER.md)):
  `dragover`/`drop` on the canvas → open the dedicated `/files` WebSocket → stream
  `file.stream()` in 64 KiB chunks. Show a drop overlay. A **Files** panel lists
  the host Outgoing folder for downloads (`showSaveFilePicker` on Chrome/Edge).
- **Webcam** (see [`MODULE_WEBCAM.md`](./MODULE_WEBCAM.md), Chrome/Edge only):
  `getUserMedia(720p30)` → WebCodecs `VideoEncoder` (H.264 CBP, realtime) →
  binary frames (type 0x50) on the main socket. A camera-sharing indicator + stop
  control are shown.
- **Gamepad** (see [`MODULE_GAMEPAD.md`](./MODULE_GAMEPAD.md)): poll
  `navigator.getGamepads()` on `requestAnimationFrame`, send binary
  `GamepadState` (type 0x40) only when the snapshot changes; emit `GamepadConnect`
  (0x41) / `GamepadDisconnect` (0x42) on the W3C events. Receive
  `FrameTypeGamepadRumble` (type 15) and forward to
  `gamepad.vibrationActuator.playEffect("dual-rumble", …)` (Chrome/Edge);
  fallback to `gamepad.hapticActuators[0].pulse(...)` on Firefox; silently drop
  on Safari (no haptic API). Honest scope: **casual gaming**; rAF polling caps
  effective latency at ~16-26 ms.

### Status Display

```
● Connected | 60 fps | 12.4 Mbps
○ Disconnected
```

Updates every 1 second with frame count and byte count deltas.

---

## Refactoring Directives

### R-CLI-01: Fix Duplicate init() Function
The file defines `init()` twice (line 22 and line 292). The second silently shadows the first. Merge into a single initialization function.

### R-CLI-02 + R-CLI-03: Codec from Config Handshake (RESOLVED)
The codec mismatch is fixed by the protocol handshake: the server sends a binary `FrameTypeConfig` (type 6) frame FIRST, whose JSON payload carries the full WebCodecs `codec` string (e.g., `avc1.42E01F` for H.264 or `hvc1.*` when HW HEVC is in use), plus `width/height/fps/audio*/cursorMode`. The client configures `VideoDecoder` from that — never hardcoded.

> Note: Config is a **binary** frame (type 6) with a JSON payload, NOT a JSON text control message. (Round-1 specs incorrectly described it as a text message — corrected here and in MODULE_PROTOCOL.)

### R-CLI-04: Add Pointer Lock
Request `canvas.requestPointerLock()` on click for FPS-game-style mouse capture. Send relative mouse deltas when locked.

### R-CLI-05: Add Fullscreen Toggle
Implement F11 or double-click for fullscreen mode: `document.documentElement.requestFullscreen()`.

### R-CLI-06: Add Adaptive Quality Feedback
Measure decode latency and frame drop rate. Send periodic stats back to server to enable adaptive bitrate/resolution.

### R-CLI-07: Handle WebSocket Send Errors (RESOLVED)
`sendInput()` now checks `ws.readyState === OPEN` before sending and drops otherwise (see Input Handling).

### R-CLI-08: Touch Input (RESOLVED — native touch records)
Touch uses `PointerEvent` and the binary `TouchContact` record (type 0x30), NOT
mouse emulation. The host injects real multitouch where a `TouchInjector` add-on
exists (Windows `win_touch`); where none exists the host drops touch records.
Pen is downgraded to touch (pressure preserved on Windows). See
[`MODULE_INPUT.md`](./MODULE_INPUT.md).

### R-CLI-09: Clipboard Sync (RESOLVED — see MODULE_CLIPBOARD)
Bidirectional text + sanitized HTML clipboard, opt-in, direction-controlled.
Client uses `clipboardchange` (Chrome/Edge) or `copy`/`paste` interception
(Firefox/Safari). See [`MODULE_CLIPBOARD.md`](./MODULE_CLIPBOARD.md).

### R-CLI-10: Modularize JavaScript
Split `compositor.js` into modules:
```
client/
├── index.html
├── main.js          // Entry point, init
├── connection.js    // WebSocket management (main /ws)
├── protocol.js      // 22-byte header parse + binary input encode helpers
├── decoder.js       // VideoDecoder setup and frame dispatch
├── renderer.js      // Canvas rendering
├── cursor.js        // Client-side cursor overlay
├── audio.js         // AudioContext + Worklet [audio deferred]
├── input.js         // Binary input, HID-usage map, pointer-lock
├── clipboard.js     // clipboardchange/copy/paste interception
├── files.js         // Drag-drop + Files panel (separate /files WS)
├── webcam.js        // getUserMedia + WebCodecs VideoEncoder
├── gamepad.js       // Gamepad-API poll + rumble apply
└── stats.js         // FPS/bandwidth display
```
Use ES modules (`import`/`export`) since all target browsers support them.

### R-CLI-11: Add Connection Token
Carry the session token via the WebSocket `Sec-WebSocket-Protocol` subprotocol (`new WebSocket(url, ["bearer." + sessionToken])`), NOT as a URL query parameter (which leaks to proxy logs / Referer / browser history). See [`MODULE_AUTH.md`](./MODULE_AUTH.md).

---

## Testing Strategy

| Level | What | Approach |
|-------|------|----------|
| Unit | Header parsing (22-byte protocol) | Jest/Node (buffer operations) |
| Unit | Config handshake parse → decoder config | Jest/Node |
| Unit | Gap detection + fast-join suppression | Jest/Node |
| Unit | S16LE → Float32 conversion | Jest/Node |
| Unit | Coordinate scaling (client → stream space) | Jest/Node |
| Unit | CursorUpdate parse + overlay positioning | Jest/Node |
| Integration | Full connection + frame decode | Browser automation (Playwright) |
| Visual | Render quality, cursor alignment | Manual + screenshot comparison |
| Performance | Decode latency, frame drop rate | WebCodecs metrics API |

---

## Browser Compatibility

**Supported browsers:** Chrome 107+, Edge (Chromium-based), Safari 16.4+ (partial WebCodecs; full support Safari 26+).

**Firefox is not supported.** WebCodecs support in Firefox lags meaningfully
in feature parity (`optimizeForLatency`, hardware decode path) and the
project explicitly does not test against it. Users on Firefox will see a
graceful fail with an unsupported-browser notice.

| Feature | Required | Minimum supported version |
|---------|----------|--------------------------|
| WebSocket (binary) | Yes | All supported browsers |
| WebCodecs VideoDecoder | Yes | Chrome 107+, Safari 16.4+ (partial), Safari 26+ (full) |
| Pointer Lock | Optional | Chrome (all), Safari 13.1 |
| Fullscreen API | Optional | Chrome (all), Safari (all) |
| ES Modules | For refactored version | Chrome (all), Safari (all) |

**Minimum: Chrome 107.** Earlier versions have a less mature WebCodecs
implementation that does not honor `optimizeForLatency` end-to-end.

> AudioWorklet would be a future requirement when the deferred audio module is
> un-paused; until then, the client does not load any audio code path.

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Decode latency (1080p H.264) | <5ms |
| Render (drawImage) | <1ms |
| Input event → WebSocket send | <1ms |
| Audio latency (buffer to speaker) | <50ms |
| Reconnect time | 2-3 seconds |
| Memory (1080p decode) | <100MB |
