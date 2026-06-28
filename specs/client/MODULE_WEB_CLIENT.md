# Module Spec: Web Client (Browser Viewer)

## Overview

The Web Client module is the browser-based viewer and controller (v1). It connects to the server via **WebTransport (QUIC)** over HTTPS, decodes video using the WebCodecs API, plays audio via AudioWorklet, and sends input events back on a dedicated **reliable input stream** as `[u16 RecLen]`-prefixed binary records (see [`MODULE_INPUT.md`](../interaction/MODULE_INPUT.md)). Media (video, audio, cursor, gamepad rumble, ping) arrives as **unreliable datagrams** with application-level fragment reassembly; the first keyframe arrives on a reliable **bootstrap stream**; rare control messages (keyframe req, resize, set_*, pong, stats) flow as newline-JSON on the **reliable control stream**; clipboard rides its own **clipboard stream**. Every stream's first byte is a `StreamType` tag. See [`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md) for the channel model.

---

## Public Interface (JavaScript)

The client is a single-page application embedded in the server binary via `rust-embed`. It has no build step, no framework, no external dependencies (ES modules, served as-is).

**Files (post-refactor, R-CLI-10):**
| File | Purpose |
|------|---------|
| `index.html` | HTML shell: canvas, cursor overlay, status overlay, `<script type="module">` |
| `main.js` | Entry point, reads role, wires modules |
| `connection.js` | WebTransport connect/reconnect, StreamType-tagged control/input/clipboard stream setup, bootstrap + datagram reassembly, control dispatch |
| `protocol.js` | 22-byte media FrameHeader parse, 8-byte DatagramHeader parse, control/clipboard JSON helpers |
| `decoder.js` | VideoDecoder config (codec from handshake), keyframe detect |
| `renderer.js` | Canvas rendering |
| `cursor.js` | Client-side cursor overlay (CursorUpdate) |
| `audio.js` | AudioContext + Worklet, A/V sync |
| `input.js` | Binary input encode (DataView), HID-usage map, pointer-lock, InputAck latency |
| `clipboard.js` | clipboardchange / copy / paste interception; host-update apply |
| `files.js` | Drag-drop upload + Files panel for downloads — opens per-transfer QUIC streams on the main session |
| `gamepad.js` | rAF poll of getGamepads, diff-send 0x40, connect/disconnect 0x41/0x42, rumble apply |
| `stats.js` | FPS/bandwidth/latency display |

---

## Internal Architecture

### Connection Management (WebTransport)

```javascript
async function connect() {
    const role = isController ? "control" : "view";
    const sessionToken = getSessionToken(); // from URL hash or /auth POST

    // 1. Certificate trust. In self-signed mode the browser opens WebTransport
    //    ONLY if we pass the server cert's SHA-256(DER) hash(es) — a TLS
    //    click-through does NOT satisfy WebTransport. Read the current+previous
    //    hashes from /cert-hashes (also inlined as <meta> in index.html for the
    //    first connect). CA-trusted mode returns [] and we omit the option.
    //    Always re-fetched here so a cert rotation self-heals on reconnect.
    //    See MODULE_SERVER "Browser certificate trust".
    const { hashes } = await (await fetch("/cert-hashes")).json();
    const opts = hashes.length ? {
        serverCertificateHashes: hashes.map(b64 =>
            ({ algorithm: "sha-256", value: base64ToArrayBuffer(b64) })),
    } : {};

    // 2. Open WebTransport session (role is NOT in the URL — it's in the auth msg)
    const wt = new WebTransport(`https://${location.host}/wt`, opts);
    await wt.ready;

    // 2. Open the CONTROL stream; its FIRST byte is the StreamType tag 0x00.
    const ctl = await wt.createBidirectionalStream();
    const ctlW = ctl.writable.getWriter();
    const ctlR = ctl.readable.getReader();
    await ctlW.write(new Uint8Array([0x00]));   // StreamControl tag
    await ctlW.write(jsonEncode({
        type: "auth", token: sessionToken, role
    }));

    // 3. Read auth response (auth_ok or auth_failed), then the config line.
    const authResp = await readJSON(ctlR);
    if (authResp.type !== "auth_ok") throw new Error("auth failed");

    // 4. Open the INPUT stream (controller only); first byte tag 0x01.
    let inp = null;
    if (isController) {
        inp = await wt.createBidirectionalStream();
        await inp.writable.getWriter().write(new Uint8Array([0x01])); // StreamInput
    }

    // 5. Spin up reader loops
    readDatagrams(wt);              // video + audio + cursor + ping + rumble
    readUniStreams(wt);            // bootstrap stream (tag 0x10) → seed decoder
    readControl(ctlR);             // config + JSON control (NOT clipboard)
    if (inp) readInputAcks(inp);   // length-prefixed InputAck on input stream
    // Clipboard stream (tag 0x02) is opened lazily on first clipboard use.

    // 6. Reconnect on close (uses cached session_token for resume)
    wt.closed.then(() => setTimeout(connect, RECONNECT_DELAY)); // 2000 ms
}
```

- Auto-reconnects on session close with 2-second delay (uses the cached
  `session_token` to resume — the server reseeds the decoder via a fresh
  **bootstrap stream**, not a datagram replay).
- WebTransport requires HTTPS + TLS 1.3 (QUIC mandates it; WebCodecs also
  requires a secure context — both conditions satisfied at once).
- **Self-signed cert trust** uses `serverCertificateHashes` (Chrome/Edge 107+,
  Firefox recent). **Safari's support is incomplete** — on Safari the self-signed
  mode may fail to connect, and a CA-trusted cert (`server.tls.cert`/`key`) is
  required. See MODULE_SERVER "Browser certificate trust".
- Role-based: `control` for input + video, `view` for video-only.
- Token-based authentication via the **first control-stream message** (not the
  URL or HTTP headers — browsers can't set the latter on WebTransport).

### Role Selection

The client determines its role from the URL:
- `https://host:port/` → viewer mode (default)
- `https://host:port/#control` → controller mode
- Viewer mode: disables keyboard/mouse capture, shows stream only
- Controller mode: captures input, requests pointer lock, sends events

### Handshake & Frame Dispatch

Three channels carry different message types — see
[`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md) "Channel Model".

```
DATAGRAM (8-byte DatagramHeader + fragment payload)
    → reassemble by (Type, FrameID), drop after fragment_reassembly_ms
    → switch Type:
        1  (VideoH264):    decodeVideo(seq, timestamp, reassembledPayload)
        7  (VideoHEVC):    decodeVideo(seq, timestamp, reassembledPayload)
        8  (AudioOpus):    playAudio(timestamp, payload)            // single datagram
        4  (AudioPCM):     playAudio(timestamp, reassembledPayload) // fragmented
        11 (CursorUpdate): cursor.update(reassembledPayload)  // latest-wins
        15 (GamepadRumble): gamepad.applyRumble(reassembledPayload)
        2  (Ping):         send JSON {"type":"pong","nonce":...} on the control stream

BOOTSTRAP STREAM (incoming UNI; first byte tag 0x10, then [u32 Len][FrameHeader‖IDR])
    → decode the IDR as a "key" chunk, set lastSeq from its FrameHeader.Sequence,
      then close. Seeds the decoder before any datagram is decoded.

CONTROL STREAM (newline-delimited JSON, both directions — NO FrameHeader)
    → S → C lines: auth_ok, auth_failed, config, hdr_unavailable, server_shutdown
        config:  configure decoder (ONLY if codec/width/height changed), set cursorMode
    → C → S lines: auth, keyframe, pong, stats, resize, set_* (NOT clipboard)

CLIPBOARD STREAM (bidi; first byte tag 0x02, then [u32 Len][JSON] both ways)
    → S → C: clipboard.applyHostUpdate(json)
    → C → S: {"type":"clipboard", ...}

INPUT STREAM ([u16 RecLen]-prefixed records both directions)
    → S → C: 13-byte InputAck → input.recordAck(seq, serverTs)
    → C → S: [u16 RecLen]-prefixed binary input records (see MODULE_INPUT)

If reassembly deadline expires for a video Type, send JSON
{"type":"keyframe"} on the control stream and bump a metric.
```

**Decoder Configuration (driven by the config handshake — fixes the round-1 codec mismatch):**
```javascript
// On config message (a JSON line on the control stream, type == "config"):
const decCfg = { codec: cfg.codec, optimizeForLatency: true };
//                                 ↑ Annex B in-band SPS/PPS → no `description` needed

// Chroma gate: 4:2:2/4:4:4 codec strings (avc1.7A…/F4…, hvc1 RExt) aren't decodable
// in every browser. Probe first; if unsupported, ask the server to downgrade.
if (!(await VideoDecoder.isConfigSupported(decCfg)).supported) {
    if (cfg.chroma !== "420") { sendControl({type:"chroma_unsupported"}); return; }
    // (server re-sends config with the 4:2:0 codec string + a keyframe)
}
decoder.configure(decCfg);
streamWidth = cfg.width; streamHeight = cfg.height; cursorMode = cfg.cursorMode;
```

The client NEVER hardcodes the codec. It comes from `Config.codec` so the decoder always matches whatever the server chose to encode with (HW HEVC where available, H.264 otherwise).

### Video Decode

```
decodeVideo(seq, timestamp, payload):
    → gap detection (skip on first frame / first post-IDR transition):
        if started && seq > lastSeq + 1: sendControl({type:"keyframe"})   // request IDR
    → lastSeq = seq
    → isKey = detectKeyframe(payload)        // H.264: scan NAL header for type 5 (IDR)
    → chunk = new EncodedVideoChunk({ type: isKey ? "key":"delta", timestamp, data: payload })
    → decoder.decode(chunk)
    → VideoDecoder output(frame): enqueue {frame, timestamp} in a tiny present-queue

presentLoop (rAF):
    → t = audioPlayoutTs (or, with no audio, the local video clock)
    → pick the queued frame whose timestamp is nearest t:
        video behind by > ~1 frame interval → drop frame(s) to catch up
        video ahead → hold (draw the current frame again)
    → drawImage(frame) → frame.close()
```

**Presentation is audio-clocked.** Decoded frames are NOT drawn immediately — they
wait in a short present-queue and are drawn to match the audio playout clock (see
"Audio Playback"). This is what keeps lip-sync locked. With audio disabled the
queue presents on the frame's own timestamp at the target FPS.

**Keyframe Detection:**
- H.264 (Annex B): scan NAL headers for type 5 (IDR). The keyframe access unit contains SPS+PPS+IDR.
- HEVC (when HW available): scan for NAL types 19–21 (IDR_W_RADL / IDR_N_LP / CRA_NUT).

**Fast-join rule:** the client sets `lastSeq` from the **bootstrap-stream IDR** (read reliably before any datagram) and does NOT run gap detection on the transition to the first live datagram frame (avoids a false "gap" → keyframe storm). Gap detection starts from the 2nd live datagram frame.

### Audio Playback Pipeline (audio is the master clock)

The codec comes from `config.audioCodec`; the client never hardcodes it.

```
Datagram media payload (Type == 8 AudioOpus, or Type == 4 AudioPCM)
    → read header.timestamp (CLOCK_MONOTONIC ns, capture time) + audio seq
    → decode by audioCodec:
        "opus":     AudioDecoder.decode(EncodedAudioChunk{data})  → AudioData
                    (wasm libopus fallback where AudioDecoder lacks Opus)
        "pcm/s16le": S16LE → Float32 (÷32768) directly, no decoder
    → if config.audioChannels > AudioContext.destination.maxChannelCount:
        downmix (5.1/7.1 → stereo, ITU-R coefficients) using config.audioLayout
    → post Float32 (interleaved per audioLayout) + capture-timestamp to the AudioWorklet
    → AUDIO IS NEVER HELD OR DROPPED FOR SYNC. A lost packet is concealed by
      Opus FEC/PLC (or a 20 ms silence for PCM). Audio plays gaplessly.

AudioWorkletProcessor:
    → small ring buffer (~40 ms target)
    → process() plays gaplessly; tracks audioPlayoutTs (capture-ts of the sample
      currently leaving the speakers) and exposes it to the main thread
    → audioPlayoutTs IS the presentation clock the VIDEO renderer slaves to
```

- **Audio-master sync.** The renderer presents the decoded **video** frame whose
  capture `Timestamp` is nearest `audioPlayoutTs` (video ahead → hold; video
  behind by > ~1 frame → drop to catch up). See "Video Decode" + protocol
  "A/V Synchronization". This replaces the old hold/drop-*audio* logic, which
  glitched audio — the worse choice perceptually.
- With no audio (disabled / no add-on), video presents on its own capture clock.
- **Initialization:** the `AudioContext` is created on the first user gesture
  (keydown/pointerdown) per the browser autoplay policy.

### Cursor Overlay (cursorMode == "separate")

```
Datagram payload (Type == 11, CursorUpdate, after reassembly)
    → parse [x:u16][y:u16][visible:u8][imageChanged:u8][w:u16][h:u16][rgba?]
    → position a CSS/canvas overlay at (x,y) scaled to the canvas rect
    → if imageChanged: update the overlay bitmap from the RGBA data
    → if !visible: hide overlay
```

- When `cursorMode == "embedded"`, the cursor is already in the video; the client hides its overlay and ignores CursorUpdate.
- Client-side cursor moves immediately on each update without waiting for a video frame → lower perceived input latency.

### Input Handling (binary)

Input is sent as binary records on the WebTransport input stream using the compact record format
from [`MODULE_INPUT.md`](../interaction/MODULE_INPUT.md) — not JSON. The client encodes into a
reused `ArrayBuffer` via `DataView` (zero garbage on the hot path) and maps
`KeyboardEvent.code` → USB HID usage via a static table for layout neutrality.

```javascript
let inputSeq = 0;
const sentAt = new Map();              // seq → performance.now(), for latency
const buf = new ArrayBuffer(16), dv = new DataView(buf);  // record body; reused

function header(type) {                // 6-byte shared record header
    dv.setUint8(0, 1);                 // Version
    dv.setUint8(1, type);              // Type
    dv.setUint32(2, ++inputSeq, true); // Seq (LE)
    sentAt.set(inputSeq, performance.now());
    return inputSeq;
}
// Frame as [u16 RecLen LE][record] so the server can delimit records on the
// QUIC byte stream (a stream has no intrinsic message boundaries). The fresh
// `out` buffer also makes `buf` safe to reuse immediately after write().
function send(len) {                                   // inpW = input-stream writer
    const out = new Uint8Array(2 + len);
    new DataView(out.buffer).setUint16(0, len, true);  // RecLen = record byte count
    out.set(new Uint8Array(buf, 0, len), 2);
    inpW.write(out).catch(() => {});
}

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

- **Input is binary**, written to the WebTransport input stream, zero-alloc on the hot
  path. `hidFromCode()` is a static `KeyboardEvent.code` → HID-usage table.
- Pointer Lock toggles absolute (0x20) ↔ relative (0x21); request
  `canvas.requestPointerLock({ unadjustedMovement: true })` on click (Chrome/Edge
  disable mouse acceleration; Safari ignores the option).
- Every record carries `Seq`; the server's `InputAck` (binary type 14) echoes it
  for latency measurement.
- **Control** messages (keyframe, resize, set_*, pong, stats) use newline-JSON
  on the **control stream**: `sendControl({type:"keyframe"})`. Clipboard does
  NOT — it has its own clipboard stream.

### Clipboard, File Transfer, Gamepad (client side)

- **Clipboard** (see [`MODULE_CLIPBOARD.md`](../interaction/MODULE_CLIPBOARD.md)): on Chrome/Edge,
  request `clipboard-read`/`clipboard-write` and use the `clipboardchange` event
  to push copies as `[u32 Len][JSON {"type":"clipboard",...}]` on the **clipboard
  stream** (tag 0x02, opened lazily); apply host→client clipboard messages read
  off the same stream silently. On Firefox/Safari (no `clipboardchange` event),
  intercept `copy`/`paste` events (gesture-bound). Text + sanitized HTML only.
- **File transfer** (see [`MODULE_FILETRANSFER.md`](../interaction/MODULE_FILETRANSFER.md)):
  `dragover`/`drop` on the canvas → open a new bidirectional stream on the WebTransport session → stream
  `file.stream()` in 64 KiB chunks. Show a drop overlay. A **Files** panel lists
  the host Outgoing folder for downloads (`showSaveFilePicker` on Chrome/Edge).
- **Gamepad** (see [`MODULE_GAMEPAD.md`](../interaction/MODULE_GAMEPAD.md)): poll
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
The codec mismatch is fixed by the control-stream handshake: right after `auth_ok` the server sends a `{"type":"config",...}` JSON line FIRST, carrying the full WebCodecs `codec` string (e.g., `avc1.42E01F` for H.264 or `hvc1.*` when HW HEVC is in use), plus `width/height/fps/audio*/cursorMode`. The client configures `VideoDecoder` from that — never hardcoded.

> Note: `config` is a **newline-JSON message on the control stream**, the same channel as `auth_ok`/`keyframe`/`resize` — NOT a binary `FrameHeader` frame. (The QUIC-transport switch removed the binary type-6 Config frame; type 6 is retired. See MODULE_PROTOCOL "Frame Types".)

### R-CLI-04: Add Pointer Lock
Request `canvas.requestPointerLock()` on click for FPS-game-style mouse capture. Send relative mouse deltas when locked.

### R-CLI-05: Add Fullscreen Toggle
Implement F11 or double-click for fullscreen mode: `document.documentElement.requestFullscreen()`.

### R-CLI-06: Add Adaptive Quality Feedback (RESOLVED)

The client is the **slow-path** signal source for the server's adaptive loop
(the server's own datagram-drop rate + QUIC RTT are the fast path — see
[`MODULE_SERVER.md`](../core/MODULE_SERVER.md) "adaptive" and
[`MODULE_STREAM_PARAMS.md`](../core/MODULE_STREAM_PARAMS.md)). `stats.js` keeps a
rolling 1-second window and emits one newline-JSON line on the **control stream**:

```js
// emitted once per second (or skipped if nothing decoded since the last tick)
sendControl({
  type: "stats",
  decodeMs: p50DecodeLatency,   // median (decoder output ts − decode() call ts) over the window
  dropped: droppedThisWindow,   // see below; reset each window (delta, not cumulative)
  fps: framesPresentedThisWindow,
});
```

- **`dropped` is derived from datagram sequence gaps**, the same signal the
  decoder's gap detection already computes: for each live video frame accumulate
  `max(0, seq − lastSeq − 1)` (skipping the fast-join transition per the
  "Fast-join rule"), plus any `VideoDecoder` `dequeue`/error drops. It is sent as
  a **per-window delta** (the server tracks the delta; see its `{"type":"stats"}`
  `dropped` handling).
- **`decodeMs`** is the median over the window of `(frame output timestamp −
  decode() submit timestamp)`; a rising `decodeMs` means the client CPU/GPU can't
  keep up (signals the server to back off fps/bitrate even when the network is
  fine).
- **Cadence:** 1 Hz. This is intentionally coarse — it is a *slow*-path hint that
  augments the server's 100 ms fast path, not a per-frame ACK. Lines are
  best-effort on the reliable control stream; a dropped/late line just means the
  server leans on its own fast-path signals that tick.
- **No client-driven resolution change.** The client only *reports*; the server's
  `stream::Manager` decides bitrate/fps and (v2) resolution. Resolution-level
  adaptation is deferred to v2 per `MODULE_STREAM_PARAMS.md`.

### R-CLI-07: Handle Stream Send Errors (RESOLVED)
`send()` now writes through the WebTransport input-stream writer with an async
catch; failures are silently dropped (the next reconnect will re-establish the
stream). See "Input Handling (binary)".

### R-CLI-08: Touch Input (RESOLVED — native touch records)
Touch uses `PointerEvent` and the binary `TouchContact` record (type 0x30), NOT
mouse emulation. The host injects real multitouch where a `TouchInjector` add-on
exists (Windows `win_touch`); where none exists the host drops touch records.
Pen is downgraded to touch (pressure preserved on Windows). See
[`MODULE_INPUT.md`](../interaction/MODULE_INPUT.md).

### R-CLI-09: Clipboard Sync (RESOLVED — see MODULE_CLIPBOARD)
Bidirectional text + sanitized HTML clipboard, opt-in, direction-controlled.
Client uses `clipboardchange` (Chrome/Edge) or `copy`/`paste` interception
(Firefox/Safari). See [`MODULE_CLIPBOARD.md`](../interaction/MODULE_CLIPBOARD.md).

### R-CLI-10: Modularize JavaScript
Split `compositor.js` into modules:
```
client/
├── index.html
├── main.js          // Entry point, init
├── connection.js    // WebTransport (/wt) + stream + datagram management
├── protocol.js      // 22-byte header parse + binary input encode helpers
├── decoder.js       // VideoDecoder setup and frame dispatch
├── renderer.js      // Canvas rendering
├── cursor.js        // Client-side cursor overlay
├── audio.js         // AudioContext + Worklet [audio deferred]
├── input.js         // Binary input, HID-usage map, pointer-lock
├── clipboard.js     // clipboardchange/copy/paste interception
├── files.js         // Drag-drop + Files panel (QUIC streams on the main session)
├── gamepad.js       // Gamepad-API poll + rumble apply
└── stats.js         // FPS/bandwidth display
```
Use ES modules (`import`/`export`) since all target browsers support them.

### R-CLI-11: Add Connection Token
Carry the session token in the **first JSON message on the WebTransport
control stream** — `{"type":"auth","token":"<bearer>","role":"control|view"}`
— NOT as a URL query parameter (which leaks to proxy logs / Referer / browser
history). See [`MODULE_AUTH.md`](../core/MODULE_AUTH.md).

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

**Supported browsers:** Chrome 107+, Edge 98+, Firefox 130+, Safari 18.2+. The floor is the **intersection** of WebTransport support (Chrome 97 / Edge 98 / Firefox 114 / Safari 18.2) and WebCodecs support (Chrome 107 / Firefox 130 / Safari 16.4+). Per engine the higher of the two wins: Chrome 107 on Chromium, Firefox 130 on Gecko, Safari 18.2 on WebKit.

**Firefox is supported from 130+** (the first version shipping WebCodecs
`VideoDecoder` un-flagged alongside its existing WebTransport support). Firefox's
low-latency WebCodecs path is younger than Chromium's, so it is treated as a
secondary target; earlier Firefox (114–129, which has WebTransport but no
WebCodecs) gets a graceful unsupported-browser notice.

| Feature | Required | Minimum supported version |
|---------|----------|--------------------------|
| WebTransport | Yes | Chrome 97+, Edge 98+, Firefox 114+, Safari 18.2+ |
| WebCodecs VideoDecoder | Yes | Chrome 107+, Firefox 130+, Safari 18.2+ |
| WebCodecs AudioDecoder (Opus) | Audio only | Chrome 94+, Firefox 130+; **Safari falls back to a wasm libopus decoder**. (PCM audio needs neither.) |
| Pointer Lock | Optional | Chrome (all), Firefox (all), Safari 13.1 |
| Fullscreen API | Optional | Chrome (all), Firefox (all), Safari (all) |
| ES Modules | For refactored version | Chrome (all), Firefox (all), Safari (all) |

**Effective minimums: Chrome 107 / Firefox 130 / Safari 18.2.** On Chromium,
earlier versions have a less mature WebCodecs implementation that does not honor
`optimizeForLatency` end-to-end; on Gecko, WebCodecs is absent before 130.

> AudioWorklet would be a future requirement when the deferred audio module is
> un-paused; until then, the client does not load any audio code path.

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Decode latency (1080p H.264) | <5ms |
| Render (drawImage) | <1ms |
| Input event → input-stream write | <1ms |
| Audio latency (buffer to speaker) | <50ms |
| Reconnect time | 2-3 seconds |
| Memory (1080p decode) | <100MB |
