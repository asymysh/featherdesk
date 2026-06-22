# Module Spec: Client (Browser Viewer)

## Overview

The Client module is the browser-based viewer and controller. It connects to the server via WebSocket (WSS), decodes video using the WebCodecs API, plays audio via AudioWorklet, and sends input events back to the server as JSON text frames.

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
| `input.js` | Keyboard/mouse/wheel capture, seq, InputAck latency |
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
        6  (Config):  JSON.parse(payload) → configure decoder, set stream dims, cursorMode
        1/5 (Video):  decodeVideo(seq, timestamp, payload)
        4  (AudioPCM): playAudio(timestamp, payload)
        11 (CursorUpdate): cursor.update(payload)
        14 (InputAck): input.recordAck(seq, serverTs)
        2  (Ping):    send {"type":"pong","nonce":...} over text
```

**Decoder Configuration (driven by the Config handshake — fixes the round-1 codec mismatch):**
```javascript
// On Config frame:
decoder.configure({
    codec: cfg.codec,          // full WebCodecs string from server, e.g. "avc1.42E01E" or "vp8"
    optimizeForLatency: true,
    // Annex B in-band SPS/PPS → no `description` needed (avc Annex B mode)
});
streamWidth = cfg.width; streamHeight = cfg.height; cursorMode = cfg.cursorMode;
```

The client NEVER hardcodes the codec. It comes from `Config.codec`, so H.264 and VP8 are both supported and always match the server.

### Video Decode

```
decodeVideo(seq, timestamp, payload):
    → gap detection (skip on first frame / first post-IDR transition):
        if started && seq > lastSeq + 1: ws.send('{"type":"keyframe"}')   // request IDR
    → lastSeq = seq
    → isKey = detectKeyframe(payload)        // H.264: scan for NAL type 5; VP8: keyframe bit
    → chunk = new EncodedVideoChunk({ type: isKey ? "key":"delta", timestamp, data: payload })
    → decoder.decode(chunk)
    → output: drawImage(frame) → frame.close()
```

**Keyframe Detection (codec-aware):**
- H.264 (Annex B): scan NAL headers for type 5 (IDR). The keyframe access unit contains SPS+PPS+IDR.
- VP8: `payload[0] & 0x01 === 0` means keyframe.

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

### Input Handling

```javascript
let inputSeq = 0;
const sentAt = new Map();              // seq → performance.now(), for latency

function sendInput(msg) {
    if (ws.readyState !== WebSocket.OPEN) return;   // R-CLI-07
    msg.seq = ++inputSeq;
    sentAt.set(msg.seq, performance.now());
    ws.send(JSON.stringify(msg));
}

// Only the controller captures input (viewer mode does nothing)
if (isController) {
    document.addEventListener('keydown', (e) => { e.preventDefault();
        sendInput({ type: "key", event: "down", code: e.code }); });
    document.addEventListener('keyup', (e) => { e.preventDefault();
        sendInput({ type: "key", event: "up", code: e.code }); });

    // Mouse movement: absolute, scaled to the stream space from Config (streamWidth/Height)
    canvas.addEventListener('pointermove', (e) => {
        const rect = canvas.getBoundingClientRect();
        const x = Math.round((e.clientX - rect.left) / rect.width  * streamWidth);
        const y = Math.round((e.clientY - rect.top)  / rect.height * streamHeight);
        sendInput({ type: "mousemove", x, y });
    });
    canvas.addEventListener('pointerdown', (e) => sendInput({ type: "mousedown", button: e.button }));
    canvas.addEventListener('pointerup',   (e) => sendInput({ type: "mouseup",   button: e.button }));
    canvas.addEventListener('wheel', (e) => { e.preventDefault();
        sendInput({ type: "wheel", deltaY: e.deltaY }); });
}

// InputAck → latency
function recordAck(seq /*, serverTs */) {
    const t0 = sentAt.get(seq);
    if (t0 !== undefined) { inputLatencyMs = performance.now() - t0; sentAt.delete(seq); }
}
```

- **Input is JSON text** (never binary), separate from the media stream.
- Every event carries a monotonic `seq`; the server's `InputAck` (binary type 14) echoes it so the client measures input round-trip latency.
- `sendInput` checks `ws.readyState` before sending (fixes R-CLI-07).
- Keyframe requests on gap use the same text channel: `ws.send('{"type":"keyframe"}')`.

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
The codec mismatch is fixed by the protocol handshake: the server sends a binary `FrameTypeConfig` (type 6) frame FIRST, whose JSON payload carries the full WebCodecs `codec` string (e.g., `avc1.42E01E` or `vp8`), plus `width/height/fps/audio*/cursorMode`. The client configures `VideoDecoder` from that — never hardcoded.

> Note: Config is a **binary** frame (type 6) with a JSON payload, NOT a JSON text control message. (Round-1 specs incorrectly described it as a text message — corrected here and in MODULE_PROTOCOL.)

### R-CLI-04: Add Pointer Lock
Request `canvas.requestPointerLock()` on click for FPS-game-style mouse capture. Send relative mouse deltas when locked.

### R-CLI-05: Add Fullscreen Toggle
Implement F11 or double-click for fullscreen mode: `document.documentElement.requestFullscreen()`.

### R-CLI-06: Add Adaptive Quality Feedback
Measure decode latency and frame drop rate. Send periodic stats back to server to enable adaptive bitrate/resolution.

### R-CLI-07: Handle WebSocket Send Errors (RESOLVED)
`sendInput()` now checks `ws.readyState === OPEN` before sending and drops otherwise (see Input Handling).

### R-CLI-08: Add Touch Input Support
Map touch events to mouse events for tablet/mobile access:
- `touchstart` → `mousedown` (button 0)
- `touchmove` → `mousemove`
- `touchend` → `mouseup`

### R-CLI-09: Add Clipboard Sync (Future)
Bidirectional clipboard: `navigator.clipboard.read/write` on client, xclip/wl-copy on server.

### R-CLI-10: Modularize JavaScript
Split `compositor.js` into modules:
```
client/
├── index.html
├── main.js          // Entry point, init
├── connection.js    // WebSocket management
├── decoder.js       // VideoDecoder setup and frame dispatch
├── renderer.js      // Canvas rendering
├── audio.js         // AudioContext + Worklet
├── input.js         // Keyboard, mouse, wheel handlers
└── stats.js         // FPS/bandwidth display
```
Use ES modules (`import`/`export`) since all target browsers support them.

### R-CLI-11: Add Connection Token
Support `?token=<auth_token>` query parameter for authentication (paired with R-SRV-01).

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

| Feature | Required | Support |
|---------|----------|---------|
| WebSocket (binary) | Yes | All modern browsers |
| WebCodecs VideoDecoder | Yes | Chrome 94+, Edge 94+, Firefox 130+ |
| AudioWorklet | Yes | Chrome 66+, Firefox 76+, Safari 14.1+ |
| Pointer Lock | Optional | Chrome 37+, Firefox 50+ |
| Fullscreen API | Optional | All modern browsers |
| ES Modules | For refactored version | All modern browsers |

**Minimum:** Chrome/Edge 94+ (WebCodecs requirement)

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
