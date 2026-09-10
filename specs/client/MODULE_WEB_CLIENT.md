# Module Spec: Web Client (Browser Viewer)

## Overview

The Web Client module is the browser-based viewer and controller (v1). The page itself is
fetched over ordinary **TCP + TLS** — a browser's first navigation to an origin is always
TCP, and the response carries `Alt-Svc: h3=":<port>"; ma=86400`, which moves subsequent
same-origin fetches (`/cert-hashes`, `/auth`) onto HTTP/3. The session is then opened on a
**carrier**: **HTTP/3 + WebTransport (QUIC)** where the browser has it and UDP is not
blocked, otherwise a **WebSocket fallback carrier** over TCP. Both carriers deliver the
identical protocol — same stream tags, same per-lane framing, same control vocabulary,
same close codes — so everything below this paragraph is carrier-blind (see
[`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md) "Carrier selection").

The client decodes video using the WebCodecs API, plays audio via AudioWorklet, and sends
input events back on a dedicated **reliable input stream** as `[u16 RecLen]`-prefixed
binary records (see [`MODULE_INPUT.md`](../interaction/MODULE_INPUT.md)). Media (video,
audio, cursor position, gamepad rumble, ping) arrives as **unreliable datagrams** with
application-level fragment reassembly; the first keyframe arrives on a reliable
**bootstrap stream**, cursor shapes on a reliable **cursor stream**; rare control messages
(keyframe req, resize, set_*, pong, stats) flow as newline-JSON on the **reliable control
stream**; clipboard rides its own **clipboard stream**. Every stream's first byte is a
`StreamType` tag. See [`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md) for the channel
model.

---

## Public Interface (JavaScript)

The client is a single-page application embedded in the server binary via `rust-embed`. It has no build step, no framework, no external dependencies (ES modules, served as-is).

**Files (post-refactor, R-CLI-10):**
| File | Purpose |
|------|---------|
| `index.html` | HTML shell: canvas, cursor overlay, status overlay (connection state — see R-CLI-13), `<script type="module">` |
| `main.js` | Entry point, reads role, wires modules |
| `connection.js` | Carrier selection (WebTransport, WebSocket fallback), connect/reconnect, StreamType-tagged lanes, bootstrap + datagram reassembly, control dispatch |
| `protocol.js` | 22-byte media FrameHeader parse, 8-byte DatagramHeader parse, control newline-JSON helpers, clipboard `[u32 Len][JSON]` helpers (two distinct framings — never share a reader) |
| `decoder.js` | Decode-capability probe, VideoDecoder config (codec from handshake), keyframe detect, needs-IDR gate, VideoDecoder error recovery |
| `renderer.js` | Canvas rendering, canvas colour space (sRGB / Display-P3), HDR tone-mapped presentation |
| `cursor.js` | Client-side cursor overlay: CURSOR_UPDATE datagrams + cursor-stream shapes |
| `audio.js` | AudioContext + Worklet, A/V sync, audio-sequence gap detection + concealment |
| `input.js` | Binary input encode (DataView), HID-usage map, pointer-lock, InputAck latency, focus-loss release (R-CLI-12) |
| `clipboard.js` | clipboardchange / copy / paste interception; host-update apply |
| `files.js` | Drag-drop upload + Files panel for downloads — opens per-transfer `0x03` lanes on the session, bounded by `config.fileStreamBudget` |
| `gamepad.js` | rAF poll of getGamepads, diff-send 0x40, connect/disconnect 0x41/0x42, rumble apply |
| `stats.js` | Outbound telemetry to server (R-CLI-06); on-screen FPS/latency/quality HUD (R-CLI-13) |

---

## Internal Architecture

### Connection Management (carrier selection)

```javascript
async function connect() {
    // 0. Hard capability gate. Version sniffing is never used — a build either
    //    has the APIs or it does not.
    if (!("VideoDecoder" in window)) {
        renderUnsupportedNotice(
            "This browser is missing WebCodecs. FeatherDesk needs Chrome 107+, " +
            "Edge 98+, Firefox 130+, or Safari 16.4+.");
        return;
    }

    const role = getRole();  // "control" | "view" | "player" — REQUESTED, from the
                             // URL hash (see Role Selection). The server arbitrates.

    // Session token: obtained ONLY from POST /auth and held in a module-scoped
    // variable for the life of the page. Never read from location.hash or
    // location.search, never written to localStorage/sessionStorage/cookies — a
    // long-lived bearer in the URL lands in history, Referer and profile sync
    // (see R-CLI-11 and MODULE_AUTH "Security Considerations"). On every
    // {"type":"config"} message the client REPLACES it with the message's
    // session_token, because resume rotates the token on every reconnect.
    const sessionToken = await obtainSessionToken();  // see "Credential entry"

    // 1. Certificate trust + pin. In self-signed mode the browser opens
    //    WebTransport ONLY if we pass the server cert's SHA-256(DER) hash(es) — a
    //    TLS click-through does NOT satisfy WebTransport. Read the current+previous
    //    hashes from /cert-hashes (also inlined as <meta> in index.html for the
    //    first connect). CA-trusted mode returns [] / no spki_sha256, and we omit
    //    the option and skip pinning. Always re-fetched here so a cert rotation
    //    self-heals on reconnect. See MODULE_SERVER "Browser certificate trust".
    const { hashes, spki_sha256 } = await (await fetch("/cert-hashes")).json();
    if (spki_sha256 && !checkPin(location.origin, spki_sha256)) return;  // hard stop
    const opts = hashes.length ? {
        serverCertificateHashes: hashes.map(b64 =>
            ({ algorithm: "sha-256", value: base64ToArrayBuffer(b64) })),
    } : {};

    // 2. Open the CARRIER — WebTransport if it is available and opens within the
    //    deadline, otherwise the /ws WebSocket fallback (MODULE_TRANSPORT
    //    "Carrier selection"). `serverCertificateHashes` applies to the
    //    WebTransport attempt only; the fallback is ordinary TLS over TCP, which
    //    is why it works on browsers whose serverCertificateHashes support is
    //    incomplete. Then open the CONTROL stream; its FIRST byte is the
    //    StreamType tag 0x00. (Role is NOT in the URL — it is in the auth msg.)
    const wt = await openCarrier(opts);
    const ctl = await wt.createBidirectionalStream();
    const ctlW = ctl.writable.getWriter();
    const ctlR = ctl.readable.getReader();
    await ctlW.write(new Uint8Array([0x00]));   // StreamControl tag
    await ctlW.write(jsonEncode({
        type: "auth",
        token: sessionToken,
        role,
        resume: haveCachedToken,     // true on a reconnect that still holds the
                                     // session_token from a previous `config`
        takeover: takeoverRequested, // true only when the user pressed
                                     // "Take control" on the role banner
        decode: await probeDecode(), // see "Decode capability probe"
    }));

    // 3. Read auth response, then the config line. The server's role is
    //    authoritative — it may have downgraded us (controller slot taken, or
    //    co-op disabled), and acting on our own URL hash instead would open a
    //    stream the server treats as a protocol error.
    const authResp = await readJSON(ctlR);
    if (authResp.type !== "auth_ok") throw new Error(authResp.reason || "auth failed");
    const effRole = authResp.role;            // "control" | "view" | "player"
    if (effRole !== role) {
        showRoleNotice(role, effRole, authResp.downgrade_reason,
                       authResp.takeover_allowed);
    }
    gamepadSlot = authResp.gamepad_slot;      // null for viewers

    // 4. Open the INPUT stream, gated on the EFFECTIVE role (controller for full
    //    input, or co-op player for gamepad-only); first byte tag 0x01.
    let inp = null;
    if (effRole === "control" || effRole === "player") {
        inp = await wt.createBidirectionalStream();
        await inp.writable.getWriter().write(new Uint8Array([0x01])); // StreamInput
    }

    // 5. Spin up reader loops
    readDatagrams(wt);              // video + audio + cursor + ping + rumble
    readUniStreams(wt);             // bootstrap (0x10) → seed decoder; cursor (0x11)
    readControl(ctlR);              // config + JSON control (NOT clipboard)
    if (inp) readInputAcks(inp);    // length-prefixed InputAck on input stream

    // Clipboard stream (tag 0x02) is opened EAGERLY — on the first `config`
    // message, not on first use. A host→client push needs somewhere to land
    // before the user has ever copied anything in the tab; opening lazily is
    // what made host_to_client mode deliver nothing at all.
    onConfig(cfg => {
        if (effRole === "control" && cfg.clipboard !== "disabled" && !clip) {
            openClipboardStream();  // writes tag 0x02, then reads [u32 Len][JSON]
        }
    });

    // 6. Reconnect on close — policy is per close code (see "Reconnect policy")
    wt.closed.then(info => scheduleReconnect(info.closeCode),
                   err  => scheduleReconnect(null));
}
```

- **Reconnect is per close code**, with exponential backoff and full jitter, and
  resumes with the cached `session_token` where the code permits it — the server
  reseeds the decoder via a fresh **bootstrap stream**, not a datagram replay. See
  "Reconnect policy" below.
- Both carriers require HTTPS + TLS 1.3 (QUIC mandates it; WebCodecs also
  requires a secure context — both conditions satisfied at once).
- **Self-signed cert trust** uses `serverCertificateHashes` on the WebTransport
  carrier (Chrome/Edge 107+, Firefox recent). **Safari's support is incomplete** —
  there the self-signed WebTransport attempt may fail, and the client falls
  through to the WebSocket carrier, which the one-time interstitial the user
  already clicked through for `GET /` covers. `spki_sha256` is pinned on first
  successful connection and a later change is a hard stop; the fingerprint is
  shown in the HUD (R-CLI-13). See MODULE_SERVER "Browser certificate trust".
- Role-based: `control` (input + video), `view` (video-only), or `player`
  (gamepad-only co-op, when `[gamepad] allow_coop`) — always the **effective**
  role from `auth_ok`, never the URL hash.
- Session tokens are obtained from `POST /auth` and carried in the **first
  control-stream message** — never the URL, a fragment, or an HTTP header
  (browsers cannot set the last of those on WebTransport in any case).

### Reconnect policy

`scheduleReconnect(code)` reads the application close code and applies this table.
"Backoff" means `delay = min(base × 2^n, cap)` for the `n`-th consecutive failure,
then a uniform random draw over `[0, delay]` (**full jitter** — a fleet evicted by
one event must not return in lockstep). `n` resets to 0 only after a session that
stayed connected for ≥ 30 s.

| Close code | Action | base / cap | UI |
|---|---|---|---|
| `0` `NORMAL`, transport-level loss, no code | reconnect, resuming with the cached session token | 1 s / 30 s | "Reconnecting…" after the first failure |
| `4429` `SERVER_FULL` | reconnect indefinitely, resuming | 5 s / 60 s | "Server is full — waiting for a slot", with the next attempt's countdown |
| `4410` `CONTROLLER_TAKEOVER` | reconnect **once, immediately**, requesting `role:"view"`; then normal policy | 0 s | "Another user took control — you are now viewing" |
| `4503` `SERVER_SHUTDOWN` | reconnect, resuming | 5 s / 60 s | "Server is restarting" |
| `4401` `AUTH_FAILED` | **discard the cached session token**, then make exactly **one** attempt through the full `/auth` flow. If that also closes 4401, stop and show the credential prompt. Never retry a rejected credential on a timer. | — | credential prompt |
| `4408` `AUTH_TIMEOUT` | reconnect once; a second 4408 stops with an error (the client failed to complete its own handshake — retrying will not help) | 1 s | "Connection handshake failed" |
| `4400` `PROTOCOL_ERROR` at **session** scope | reconnect once; a second 4400 stops and shows the code (this is a bug, and a retry loop hides it) | 2 s | "Protocol error — reload the page" |
| a stream reset with `4400` | **not** a session close — the affected feature is disabled for this session and the stream is not reopened; video continues | — | the affected feature's control is disabled |

A code that is not in this table is treated as the generic case (row 1). There is
no flat retry constant; `SERVER_FULL` in particular is deliberately distinct from
`AUTH_FAILED`, which a client cannot otherwise tell apart from a bad credential
and which is what drives a reconnect storm against a full server.

### Role Selection

The URL fragment carries the **requested** role and nothing else:
- `https://host:port/` → viewer mode (default)
- `https://host:port/#control` → controller mode
- `https://host:port/#player` → gamepad co-op player mode
- Viewer mode: disables keyboard/mouse capture, shows stream only
- Controller mode: captures input, requests pointer lock, sends events
- Player mode: sends **gamepad input only** (no kb/mouse), claims one virtual-pad
  slot. Honored only when the host has `[gamepad] allow_coop`; otherwise the
  server treats it as `view`. See [`MODULE_GAMEPAD.md`](../interaction/MODULE_GAMEPAD.md)
  "Co-op" and [`MODULE_AUTH.md`](../core/MODULE_AUTH.md).

**The hash is a *request*; `auth_ok.role` is what the client acts on.** The server
arbitrates — a second `control` connection becomes `view`, and a `player` becomes
`view` when `[gamepad] allow_coop` is off — and reports the arbitrated result in
`auth_ok` along with `requested_role` and a `downgrade_reason` of
`"controller_slot_taken"`, `"coop_disabled"`, `"unauthenticated"` or
`"takeover_not_permitted"`. Every subsequent decision — whether to open the input
stream (`0x01`), the clipboard stream (`0x02`) or file-transfer streams (`0x03`),
and whether to capture keyboard/mouse — is gated on the effective role. Acting on
the hash instead opens a stream the server treats as a protocol error, which
presents as an endless reconnect loop. The `downgrade_reason` is surfaced in the
HUD (R-CLI-13) and in `showRoleNotice()`'s banner — a non-blocking "Someone else is
controlling this host; you are viewing." — so the user is told why they are viewing
rather than left to guess. When `auth_ok.takeover_allowed` is true the banner also
carries a **Take control** button, which reconnects with `takeover: true`; that flag
is one-shot and is cleared once the auth line is written, so an automatic reconnect
never silently seizes the slot again.

### Credential entry

`obtainSessionToken()` is the only producer of a session token in the client:

1. If a token is already held in the module-scoped variable (a reconnect within the
   same page load), return it — reconnection resumes with the rotated token, never
   with a credential.
2. Otherwise `POST /auth` (schema in [`MODULE_AUTH.md`](../core/MODULE_AUTH.md) "The
   `/auth` endpoint"). If the server answers `503 auth_disabled` (`mode = "none"`),
   the client sends `{"type":"auth","token":""}` and proceeds.
3. On `401`, render the credential prompt appropriate to the mode the server
   reports and retry step 2 with what the user enters. The prompt is a normal form
   field with `autocomplete="off"`; the value is passed to `fetch` and never
   assigned to `location`, a link `href`, or a form `action`.

The URL fragment selects the **requested role only** (`#control` / `#player`;
absent = viewer) and is never a credential. If a fragment ever contains a `token=`
or `session=` parameter — a stale bookmark from an older build — the client MUST
strip it with `history.replaceState(null, "", location.pathname + location.search)`
**before** the first `fetch`, and MUST NOT use its value.

### Decode capability probe

Runs once, before `auth`. `decoder.js` exports `probeDecode()`; `connection.js`
awaits it and puts the result in the `auth` message, so the server never advertises
a codec no attached client can decode. Every string here is a fixed probe constant —
it is not the stream's codec, which is not known yet.

The five strings are exactly `codec_string(profile, 1920, 1080, 60)`
(MODULE_ABI "Codec-string computation") for `H264High`, `H264High422`,
`H264High444`, `HevcMain` and `HevcMain10` — the level the server actually
advertises at the default session geometry. A `true` bit therefore means precisely
"this client decodes that profile at 1920x1080 @ 60 fps" and is NOT a claim about a
larger geometry; a session resized above that is covered by
`{"type":"decode_unsupported"}`, the runtime backstop. The probe must never be
stated at a LOWER level than the advertised string: `avc1.640028` is Level 4.0,
which is 1080p30, and a client whose 4:2:2 or HEVC decode caps at Level 4.0 would
answer `true` and then throw `NotSupportedError` on `configure()` — the black
canvas this probe exists to prevent.

```javascript
const PROBES = {
  h264:     "avc1.64002A",        // High, Level 4.2
  h264_422: "avc1.7A002A",        // High 4:2:2, Level 4.2
  h264_444: "avc1.F4002A",        // High 4:4:4 Predictive, Level 4.2
  hevc:     "hvc1.1.6.L123.B0",   // Main, Level 4.1
  hevc10:   "hvc1.2.4.L123.B0",   // Main10, Level 4.1 — the HDR gate
};

export async function probeDecode() {
  const out = {};
  for (const [k, codec] of Object.entries(PROBES)) {
    try {
      const r = await VideoDecoder.isConfigSupported({
        codec, codedWidth: 1920, codedHeight: 1080, optimizeForLatency: true,
      });
      out[k] = !!r.supported;
    } catch { out[k] = false; }        // a throw is a "no", never a connect failure
  }
  return out;
}
```

A browser with no `VideoDecoder` at all fails the capability gate at `connect()`
step 0 and never reaches this code. The `decode` object is optional on the wire: a
message without it is read as `{h264:true, h264_422:false, h264_444:false,
hevc:false, hevc10:false}`, the conservative reading that keeps a native or
third-party client working. It is **not** a security input — a lying client only
harms itself.

### Handshake & Frame Dispatch

Each lane carries different message types — see
[`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md) "Channel Model". On the
WebSocket fallback carrier the same lanes arrive as tagged messages on one
connection, with identical per-lane framing, so the readers below are shared.

```
DATAGRAM (8-byte DatagramHeader + fragment payload)
    → reassemble by (Type, FrameID) per MODULE_PROTOCOL "Datagram reassembly rules";
      never decode a partial frame
    → switch Type:
        1  (VideoH264):    decodeVideo(seq, timestamp, reassembledPayload)
        7  (VideoHEVC):    decodeVideo(seq, timestamp, reassembledPayload)
        8  (AudioOpus):    playAudio(seq, timestamp, payload)            // single datagram
        4  (AudioPCM):     playAudio(seq, timestamp, reassembledPayload) // fragmented
        11 (CursorUpdate): cursor.update(payload)  // 14 bytes, single datagram,
                                                   // latest-wins by DatagramHeader.FrameID
        15 (GamepadRumble): gamepad.applyRumble(payload)  // 9 bytes, single datagram
        2  (Ping):         nonce = dv.getUint32(8, true);      // u32 LE, always < 2^53
                           sendControl({type:"pong", nonce});  // echoed verbatim

BOOTSTRAP STREAM (incoming UNI; first byte tag 0x10, then [u32 Len][FrameHeader‖IDR])
    → decode the IDR as a "key" chunk, set lastSeq AND bootstrapSeq from its
      FrameHeader.Sequence, clear needsIdr, then close. Seeds the decoder before
      any datagram is decoded.

CURSOR STREAM (incoming UNI; first byte tag 0x11, then [u32 Len][record])
    → Kind 0x01 → cursor.setShape(record)   // reliable; bitmap + draw size + hotspot
      Kind 0x02 → cursor.update(body)       // the join-time position, same 14 bytes
    → stays open for the session; read continuously so flow-control credit keeps
      being extended.

CONTROL STREAM (newline-delimited JSON, both directions — NO FrameHeader)
    → S → C lines: auth_ok, auth_failed, config, hdr_unavailable, codec_unavailable,
                   resize_suppressed, server_shutdown
        config:  configure decoder (ONLY if codec/width/height/chroma/hdr changed),
                 set cursorMode, open the clipboard stream, replace session_token
    → C → S lines: auth, keyframe, pong, stats, decode_unsupported, resize, set_*
                   (NOT clipboard, NOT input — those have their own streams)

CLIPBOARD STREAM (bidi; first byte tag 0x02, then [u32 Len][JSON] both ways)
    → S → C: clipboard.applyHostUpdate(json)
    → C → S: {"type":"clipboard", ...}

INPUT STREAM ([u16 RecLen]-prefixed records both directions)
    → S → C: 13-byte InputAck → input.recordAck(seq, serverTs)
    → C → S: [u16 RecLen]-prefixed binary input records (see MODULE_INPUT)

If the reassembly deadline expires for a video Type, mark the decoder needs-IDR and
request a keyframe through the throttle in "Video Decode", and bump a metric.
```

**Server-originated control handlers.**

- `hdr_unavailable` → clear the HDR toggle in the HUD and show "HDR not available
  on this host", naming the `reason` (`no_hevc_encoder`, `no_ten_bit_capture`,
  `attached_client_cannot_decode`, `degraded_to_software`). No decoder change: the
  stream was never reconfigured, so nothing on the video path is touched.
- `codec_unavailable` → show a persistent overlay naming the `codec`, and do
  **not** configure a decoder. Input, clipboard and file transfer keep working;
  this is a defined, visible outcome rather than a black canvas.
- `resize_suppressed` → keep the requested canvas size, letterbox at the
  `{width,height}` given (which are the **effective** stream dimensions), show a
  transient toast, and do not re-send `resize`.
- `server_shutdown` → move to the `disconnected` state with the reason "server shut
  down" and **cancel the auto-reconnect timer** — the host is going away, and
  reconnect-storming a shutting-down server is exactly the behaviour this notice
  exists to prevent.

**Decoder Configuration (driven by the config handshake — fixes the round-1 codec mismatch):**
```javascript
// On config message (a JSON line on the control stream, type == "config"):
const decCfg = {
    codec: cfg.codec,
    codedWidth: cfg.width,          // REQUIRED — without the dimensions the
    codedHeight: cfg.height,        // browser cannot honour the advertised level
    optimizeForLatency: true,       // Annex B in-band SPS/PPS -> no `description`
    hardwareAcceleration: cfg.hdr ? "prefer-hardware" : "no-preference",
};

// Decode gate. NEVER call configure() on a config that probed unsupported —
// configure() throws NotSupportedError and there is no recovery from it.
let probe;
try { probe = await VideoDecoder.isConfigSupported(decCfg); }
catch { probe = { supported: false }; }
if (!probe.supported) {
    sendControl({ type: "decode_unsupported", codec: cfg.codec,
                  chroma: cfg.chroma, hdr: cfg.hdr });
    showOverlay("This browser cannot decode the stream (" + cfg.codec + ").");
    return;                          // the server's ladder decides what happens next
}
hideOverlay();
decoder.configure(decCfg);
streamWidth = cfg.width; streamHeight = cfg.height; cursorMode = cfg.cursorMode;
```

`decode_unsupported` is the **runtime backstop** for a browser whose auth-time
`probeDecode()` was optimistic. The server answers it with one rung of its
downgrade ladder (chroma to 4:2:0, then `codec_unavailable`) — see
[`MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md) "Decode capability". A config
that changes `codec`, `width`, `height`, `chroma` or `hdr` reconfigures the decoder
and therefore also marks it needs-IDR (see "Video Decode").

The client NEVER hardcodes the codec. It comes from `config.codec` so the decoder always matches whatever the server chose to encode with — H.264 for every SDR session, HEVC Main10 only when the session is HDR.

The client also reads three session-scoped fields off the same message: `clipboard`
(whether — and in which direction — to open the `0x02` stream), `fileStreamBudget`
(how many file-transfer streams it may hold open at once), and `carrier`
(`"webtransport"` or `"websocket"`, shown in the HUD so a degraded session is
visible rather than mysterious).

### Video Decode

```javascript
// decoder.js
let needsIdr = true;            // true from connect until the bootstrap IDR lands
let lastKeyframeReqAt = 0;
let lastDecoderConfig = null;

// One throttle, matching the server's 500 ms coalescer: asking faster cannot
// produce IDRs faster, and it must not burn the session's keyframe tokens
// (MODULE_SERVER "Keyframe-Request Rate Limiting").
function requestKeyframe() {
    const now = performance.now();
    if (now - lastKeyframeReqAt < 500) return;
    lastKeyframeReqAt = now;
    sendControl({ type: "keyframe" });
}

function markNeedsIdr() { needsIdr = true; requestKeyframe(); }

function makeDecoder() {
    return new VideoDecoder({
        output: onDecodedFrame,
        error: (e) => {
            console.warn("VideoDecoder error", e);
            if (decoder.state !== "closed") decoder.close();
            decoder = makeDecoder();
            decoder.configure(lastDecoderConfig);
            markNeedsIdr();
        },
    });
}

decodeVideo(seq, timestamp, payload):
    → if (!isNewer(seq, bootstrapSeq)) return;      // dedup vs the bootstrap IDR
    → if (started && isNewer(seq, lastSeq) && ((seq - lastSeq) >>> 0) > 1) markNeedsIdr()
    → lastSeq = seq
    → isKey = detectKeyframe(payload)   // H.264: first VCL NAL type 5; HEVC: 16..21
    → if (needsIdr && !isKey) { droppedWhileNeedsIdr++; return; }   // NEVER decode a
      // P-frame with a broken reference chain — that is what produces the "garbage
      // forever" failure this gate exists to prevent.
    → if (isKey) needsIdr = false;
    → chunk = new EncodedVideoChunk({ type: isKey ? "key":"delta", timestamp, data: payload })
    → decoder.decode(chunk)
    → VideoDecoder output(frame): enqueue {frame, timestamp} in a tiny present-queue

presentLoop (rAF):
    → t = audioPlayoutTs (or, with no audio, the local video clock)
    → pick the queued frame whose timestamp is nearest t:
        video behind by > ~1 frame interval → drop frame(s) to catch up
        video ahead → hold (draw the current frame again)
    → drawImage(frame) on the context from makeContext() → frame.close()
    → close() every frame the pick skipped past; an unclosed VideoFrame pins a GPU
      buffer, so close() runs on EVERY drop path, not only the presented one
```

**`needsIdr` is set on, and only on:** connect (before the bootstrap IDR); a
reassembly-deadline expiry or a size-mismatched reassembly
([`MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md) → "Datagram reassembly rules");
a detected sequence gap; a `VideoDecoder` error; return from a backgrounded tab
(R-CLI-14); and a `config` message that changes `codec`, `width`, `height`, `chroma`
or `hdr` (the decoder is reconfigured, so its reference chain is gone). It is
cleared on, and only on, decoding a keyframe. While it is set the client keeps
presenting the last decoded frame; it never renders partial or undecodable output.

**Presentation is audio-clocked.** Decoded frames are NOT drawn immediately — they
wait in a short present-queue and are drawn to match the audio playout clock (see
"Audio Playback"). This is what keeps lip-sync locked. With audio disabled the
queue presents on the frame's own timestamp at the target FPS.

**Keyframe Detection:**
- H.264 (Annex B): scan NAL headers for type 5 (IDR). The keyframe access unit contains SPS+PPS+IDR.
- HEVC (HDR sessions): scan for NAL types 16–21 (BLA/IDR_W_RADL/IDR_N_LP/CRA_NUT). The keyframe access unit contains VPS+SPS+PPS+prefix-SEI+IDR.

### Rendering and colour space

```javascript
// renderer.js — the canvas context is created once, from the first `config`,
// and re-created only if cfg.hdr changes. `rec2100-*` are not shipped values.
function makeContext(canvas, cfg) {
    const wantP3 = cfg.hdr && canvasSupportsDisplayP3();
    return canvas.getContext("2d", {
        colorSpace: wantP3 ? "display-p3" : "srgb",
        alpha: false,
        desynchronized: true,     // lets the compositor skip a copy on the hot path
    });
}
// canvasSupportsDisplayP3(): create a 1x1 offscreen context with
// {colorSpace:"display-p3"} and read back ctx.getContextAttributes().colorSpace;
// browsers that ignore the hint report "srgb" and the client uses sRGB.
```

**HDR presentation is tone-mapped, and that is the honest description.** An HDR
session is decoded at full 10-bit BT.2020 PQ precision — the capture, encode and
transport path is genuinely HDR end to end — but the shipped
`CanvasRenderingContext2DSettings.colorSpace` enum is `"srgb" | "display-p3"` and
has no HDR value. `rec2100-pq` / `rec2100-hlg` come from the unshipped
CanvasHighDynamicRange draft and MUST NOT be requested. So the browser
colour-manages the decoded frames **down** into the canvas's SDR or Display-P3
output; there is no HDR display output path in the browser in v1. True HDR
presentation is the native client's
([`MODULE_NATIVE_CLIENT.md`](./MODULE_NATIVE_CLIENT.md) "True HDR presentation").
The canvas backing store is always the stream image
(`canvas.width/height = streamWidth/streamHeight`), displayed with
`object-fit: contain` — see "Cursor Overlay" for the content-box math that contract
requires.

**Fast-join rule:** the client sets `lastSeq` and `bootstrapSeq` from the **bootstrap-stream IDR** (read reliably before any datagram), discards any datagram not `is_newer` than `bootstrapSeq` (the same access unit also arrives on the datagram lane), and runs gap detection from the first live frame onward. The server guarantees the bootstrap IDR is fresh, so the first live frame is `bootstrapSeq + 1`.

### Audio Playback Pipeline (audio is the master clock)

The codec comes from `config.audioCodec`; the client never hardcodes it, and the
decoder is initialized from `config.audioDescription` — the base64 OpusHead of
RFC 7845 §5.1, which is what makes a multistream (5.1/7.1) Opus stream decodable at
all. It is empty for PCM.

```javascript
const audioCfg = {
  codec: cfg.audioCodec,               // "opus"
  sampleRate: cfg.audioSampleRate,     // 48000
  numberOfChannels: cfg.audioChannels, // 1..8
};
if (cfg.audioDescription) {
  audioCfg.description = base64ToBytes(cfg.audioDescription);
}
if (!(await AudioDecoder.isConfigSupported(audioCfg)).supported) {
  // fall back to the wasm libopus decoder, which takes the same OpusHead bytes
}
audioDecoder.configure(audioCfg);
```

```
Datagram media payload (Type == 8 AudioOpus, or Type == 4 AudioPCM)
    → read header.timestamp (CLOCK_MONOTONIC ns, capture time) + header.sequence
    → LOSS DETECTION, at the decode site (NOT in the AudioWorklet, which sees only
      post-decode Float32 and can never issue an FEC or PLC decode):
        gap = (header.sequence - lastAudioSeq) >>> 0        // modular u32
        if (lastAudioSeq !== null && gap > 1 && gap < 0x80000000) {
            conceal(gap - 1);            // gap-1 missing 20 ms packets
            audioGapsThisWindow += gap - 1;
        }
        lastAudioSeq = header.sequence
    → decode by audioCodec:
        "opus":     AudioDecoder.decode(EncodedAudioChunk{data})  → AudioData
                    (wasm libopus fallback where AudioDecoder lacks Opus)
        "pcm/s16le": S16LE → Float32 (÷32768) directly, no decoder
    → if config.audioChannels > AudioContext.destination.maxChannelCount:
        downmix from the VORBIS-order frame to stereo (coefficients below) using
        config.audioLayout, then clamp each sample to [-1.0, 1.0]
      else permute Vorbis order → Web Audio "speakers" order (table below)
    → post Float32 (interleaved) + capture-timestamp to the AudioWorklet
    → AUDIO IS NEVER HELD OR DROPPED FOR SYNC. A loss is concealed per the path
      table below and the clock is advanced across it. Audio plays gaplessly.

conceal(n):
    "opus" via wasm libopus — recover the single most recent lost packet from the
        CURRENT packet's in-band FEC: opus_decode(dec, pkt, len, out, 960, /*fec=*/1),
        then (n-1) null-packet PLC decodes: opus_decode(dec, NULL, 0, out, 960, 0).
    "opus" via WebCodecs AudioDecoder — the API exposes neither FEC nor null-packet
        PLC, so conceal with n × 20 ms of silence and count it.
    "pcm/s16le" — n × 20 ms of silence.
    In every case advance audioPlayoutTs by n × 20 ms, so the video presenter's
    audio-master clock does not jump backwards and drag the whole present-queue
    with it.

AudioWorkletProcessor:
    → small ring buffer (~40 ms target)
    → process() plays gaplessly; tracks audioPlayoutTs (capture-ts of the sample
      currently leaving the speakers) and exposes it to the main thread
    → audioPlayoutTs IS the presentation clock the VIDEO renderer slaves to
```

- **Channel order.** The wire order is the **Vorbis I order** — `L C R Ls Rs LFE`
  for 5.1 and `L C R Ls Rs Rls Rrs LFE` for 7.1 — because that is what Opus channel
  mapping family 1 defers to (RFC 7845 §5.1.1.2), and the PCM passthrough uses the
  same order so the deinterleave is codec-independent. It is **not** the
  WAV/SMPTE order (`L R C LFE …`). The Web Audio API's `"speakers"` interpretation
  defines 6 channels as `L R C LFE SL SR` and defines nothing above 6, so `audio.js`
  applies a fixed permutation before writing into the worklet ring buffer,
  `dst[i] = src[PERM[i]]`:

  | Layout | `PERM` (destination index → source index) | Destination order |
  |--------|-------------------------------------------|-------------------|
  | 5.1 | `[0, 2, 1, 5, 3, 4]` | `L R C LFE SL SR` (Web Audio "speakers") |
  | 7.1 | `[0, 2, 1, 7, 5, 6, 3, 4]` | `L R C LFE BL BR SL SR` (WAV 7.1; Web Audio treats >6 as "discrete") |

- **Downmix.** Most viewers are stereo. When the decoded channel count exceeds
  `AudioContext.destination.maxChannelCount`, the client downmixes from the
  **Vorbis-order** frame *before* the permutation:

  ```
  5.1 -> 2.0:  L' = L + 0.7071*C + 0.7071*Ls
               R' = R + 0.7071*C + 0.7071*Rs
  7.1 -> 2.0:  L' = L + 0.7071*C + 0.5*Ls + 0.5*Rls
               R' = R + 0.7071*C + 0.5*Rs + 0.5*Rrs
  ```

  **LFE is discarded**, per ITU-R BS.775-3 — folding it into a stereo mix at any
  gain produces bass build-up and clipping with no benefit on the small speakers a
  viewer is most likely using.
- **Concealment differs by path, and the browser path is not FEC.** The encoder
  always runs with in-band FEC on, but WebCodecs `AudioDecoder` exposes no
  lost-packet signal and no FEC or null-packet entry point, so it gets bounded,
  clock-preserving silence rather than Opus's own concealment. Only the wasm
  libopus path (and the v2 native client) can reach FEC + PLC. Do not describe
  browser playback as FEC-protected. See
  [`MODULE_AUDIO.md`](../media/MODULE_AUDIO.md) "Loss concealment, by path".
- **`audioGaps`** is reported in the 1 Hz `{"type":"stats"}` line alongside
  `dropped`, so audio loss and video loss are separable in the server's telemetry
  instead of both showing up as "the network is bad".
- **Audio-master sync.** The renderer presents the decoded **video** frame whose
  capture `Timestamp` is nearest `audioPlayoutTs` (video ahead → hold; video
  behind by > ~1 frame → drop to catch up). See "Video Decode" + protocol
  "A/V Synchronization". This replaces the old hold/drop-*audio* logic, which
  glitched audio — the worse choice perceptually.
- With no audio (disabled / no add-on), video presents on its own capture clock.
  Note the cost of the audio-master choice: the renderer slaves to a clock that is
  one jitter buffer behind capture, so enabling audio adds that whole buffer to
  video motion-to-photon (see "Performance Targets" and CENTRAL_SPEC
  "Motion-to-photon budget").
- **Initialization:** the `AudioContext` is created on the first user gesture
  (keydown/pointerdown) per the browser autoplay policy.

**Leaving audio-master.** The presentation clock switches back to the local video
clock on either of two triggers, whichever fires first:

1. A `config` whose `audio` is `false` after one whose `audio` was `true`. The
   client tears the worklet down (node disconnected *before* `audioContext.close()`,
   per R-CLI-14), clears `audioPlayoutTs`, and presents on the frame's own
   timestamp at the target fps.
2. **Watchdog:** `audioPlayoutTs` has not advanced for **200 ms** while video
   frames are still arriving. 200 ms is 5x the ~40 ms jitter buffer and 10x a
   20 ms frame, so it cannot fire on ordinary loss — concealment advances the clock
   across a genuine gap, and only a *stalled* clock trips it. The client logs it,
   switches to the local video clock, and keeps the worklet alive.

The switch back to audio-master requires BOTH a `config` with `audio: true` AND a
freshly decoded audio frame whose timestamp is ahead of the last presented video
frame; the client never slaves the renderer to a clock that is not moving.

### Cursor Overlay (cursorMode == "separate")

```
Datagram payload (Type == 11, CursorUpdate) — 14 bytes, never fragmented
    → drop it if DatagramHeader.FrameID is not newer than the last accepted one
    → parse [x:u16][y:u16][streamW:u16][streamH:u16][shapeId:u32][visible:u8][_:u8]
    → if !visible: hide the overlay and return
    → if shapeId is unknown: keep the shape currently on screen (never blank)
    → place the overlay per the mapping below

Cursor stream record (tag 0x11, [u32 Len][record])
    → reconcile Len FIRST, before reading any field past the fixed prefix:
      Kind 0x01 requires Len == 22 + PixelBytes, Kind 0x02 requires Len == 16
    → Kind 0x01: createImageBitmap(new ImageData(pixels, pixelW, pixelH))
      — pixels are straight RGBA, exactly what ImageData wants, so no conversion
      — cache by shapeId; keep drawW/drawH/hotspotX/hotspotY with it
    → Kind 0x02: the same 14-byte body as the datagram (the join-time position)
    → unknown Kind: skip the record using Len — not an error
```

**The canvas CSS contract.** The canvas backing store is the stream image
(`canvas.width/height = streamWidth/streamHeight`) and it is displayed with
`object-fit: contain`, so whenever the element's aspect ratio differs from the
stream's — which it does from the first connect, and again after every resize
that the server suppresses — the image is centred inside the element with bars on
two sides. The overlay must use the **rendered content box**, not the element box:

```javascript
// Shared by the cursor overlay and by absolute pointer mapping — one function,
// so the two can never disagree about where a pixel is.
function contentBox(canvas, streamW, streamH) {
    const r = canvas.getBoundingClientRect();
    const s = Math.min(r.width / streamW, r.height / streamH); // CSS px per stream px
    const w = streamW * s, h = streamH * s;
    return { left: r.left + (r.width - w) / 2, top: r.top + (r.height - h) / 2, s };
}

// Place the overlay. streamW/streamH come from the CursorUpdate itself, not from
// the latest config — an update in flight across a resolution change is still
// interpretable that way.
const b = contentBox(canvas, u.streamW, u.streamH);
overlay.style.left   = (b.left + (u.x - shape.hotspotX) * b.s) + "px";
overlay.style.top    = (b.top  + (u.y - shape.hotspotY) * b.s) + "px";
overlay.style.width  = (shape.drawW * b.s) + "px";
overlay.style.height = (shape.drawH * b.s) + "px";
```

- **A malformed cursor record is stream-scope, never session-scope.** The `Len`
  reconciliation above runs before any field past the fixed prefix is read, and it
  is the client-side half of the receiver rules in
  [`MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md) "Wire Format — cursor stream
  records": a `Len` that disagrees with `22 + PixelBytes` (or `16` on a Kind 0x02
  record), and every other validation failure listed there, cancels the cursor
  stream with
  `close::PROTOCOL_ERROR` (4400) and nothing else. The session survives with its
  video, audio, input, clipboard and file-transfer streams, and the overlay keeps
  the last shape it holds. Reading a field out of a record whose own size
  disagrees with its prefix is what makes `new ImageData(subarray, …)` throw and
  strands the reader mid-stream, which is the one thing the cursor lane must not
  do — it stops extending flow-control credit.
- When `cursorMode == "embedded"`, the cursor is already in the video; the client
  hides its overlay and ignores CursorUpdate and the cursor stream.
- Client-side cursor moves immediately on each update without waiting for a video
  frame → lower perceived input latency.
- A shape arrives once per session per distinct pointer shape (the server
  deduplicates by ShapeID), so the overlay bitmap is built at most a few dozen
  times for the life of the connection.

### Input Handling (binary)

Input is sent as binary records on the input stream using the compact record format
from [`MODULE_INPUT.md`](../interaction/MODULE_INPUT.md) — not JSON. The client encodes into a
reused `ArrayBuffer` via `DataView` (zero garbage on the hot path) and maps
`KeyboardEvent.code` → USB HID usage via a static table for layout neutrality.

```javascript
let inputSeq = 0;
const sentAt = new Map();              // seq → performance.now(), for latency
const SENT_AT_CAP = 4096;              // ≈4 s at the 1000 ev/s server rate limit
const buf = new ArrayBuffer(16), dv = new DataView(buf);  // record body; reused

function header(type) {                // 6-byte shared record header
    dv.setUint8(0, 1);                 // Version
    dv.setUint8(1, type);              // Type
    dv.setUint32(2, ++inputSeq, true); // Seq (LE)
    sentAt.set(inputSeq, performance.now());
    // Bounded: the server acks only records it actually injects, so records
    // dropped by the input rate limit or coalesced away as mousemoves are never
    // acked and their entries would otherwise live until teardown. A Map
    // iterates in insertion order, so the oldest keys are the first ones out.
    if (sentAt.size > SENT_AT_CAP) {
        let n = 1024;
        for (const k of sentAt.keys()) { sentAt.delete(k); if (--n === 0) break; }
    }
    return inputSeq;
}
// Frame as [u16 RecLen LE][record] so the server can delimit records on a byte
// stream (a stream has no intrinsic message boundaries). The framing is identical
// on both carriers. The fresh `out` buffer also makes `buf` safe to reuse
// immediately after write().
function send(len) {                                   // inpW = input-stream writer
    const out = new Uint8Array(2 + len);
    new DataView(out.buffer).setUint16(0, len, true);  // RecLen = record byte count
    out.set(new Uint8Array(buf, 0, len), 2);
    inpW.write(out).catch(() => {});
}

// R-CLI-12 (fixes a confirmed old-code bug — see "Held-Input Release on
// Focus Loss" below): every key/button the controller is currently holding
// down, so a synthetic "up" can be sent for each without waiting for the
// browser to ever fire a real keyup/pointerup (it won't, once focus is gone).
const heldKeys = new Set();     // HID usage codes currently down
const heldButtons = new Set();  // pointer button indices currently down
const heldTouches = new Set();  // TouchContact PointerIds currently down

if (effRole === "control") {   // the EFFECTIVE role, never the URL hash
    // Key: HID usage from code; Flags bit0 = down
    document.addEventListener('keydown', (e) => { e.preventDefault();
        const hid = hidFromCode(e.code); heldKeys.add(hid);
        header(0x10); dv.setUint16(6, hid, true); dv.setUint8(8, 1); send(9); });
    document.addEventListener('keyup', (e) => { e.preventDefault();
        const hid = hidFromCode(e.code); heldKeys.delete(hid);
        header(0x10); dv.setUint16(6, hid, true); dv.setUint8(8, 0); send(9); });

    // Mouse: absolute (or relative when pointer-locked). Touch and pen never
    // reach this handler — they take the TouchContact branch below.
    canvas.addEventListener('pointermove', (e) => {
        if (e.pointerType !== 'mouse') return;
        if (document.pointerLockElement === canvas) {           // relative (FPS)
            header(0x21); dv.setInt16(6, e.movementX, true); dv.setInt16(8, e.movementY, true); send(10);
        } else {
            // Absolute, mapped into the STREAM SPACE — encoded output pixels of the
            // selected display, upright, origin top-left (MODULE_STREAM_PARAMS
            // "Coordinate space"). contentBox() is the same helper the cursor
            // overlay uses, so the two mappings agree by construction: the canvas
            // is object-fit: contain, and element-box scaling would be off by the
            // letterbox offset on every non-matching aspect ratio.
            const b = contentBox(canvas, streamWidth, streamHeight);
            const x = Math.min(streamWidth  - 1, Math.max(0, Math.round((e.clientX - b.left) / b.s)));
            const y = Math.min(streamHeight - 1, Math.max(0, Math.round((e.clientY - b.top)  / b.s)));
            header(0x20); dv.setUint16(6, x, true); dv.setUint16(8, y, true); send(10);
        }
    });
    canvas.addEventListener('pointerdown', (e) => {
        if (e.pointerType !== 'mouse') return touchRecord(e, 0);
        heldButtons.add(e.button);
        header(0x22); dv.setUint8(6, e.button); dv.setUint8(7, 1); send(8); });
    canvas.addEventListener('pointerup',   (e) => {
        if (e.pointerType !== 'mouse') return touchRecord(e, 2);
        heldButtons.delete(e.button);
        header(0x22); dv.setUint8(6, e.button); dv.setUint8(7, 0); send(8); });
    canvas.addEventListener('wheel', (e) => { e.preventDefault();
        const unit = e.deltaMode;       // 0=pixel,1=line,2=page
        header(0x23); dv.setInt16(6, e.deltaX, true); dv.setInt16(8, e.deltaY, true); dv.setUint8(10, unit); send(11); });

    // Touch and pen: the 0x30 TouchContact record, NEVER mouse emulation. Without
    // this branch a finger tap arrives at the host as a mouse drag and the
    // `win_touch` add-on is never reached (MODULE_INPUT "TouchContact").
    // Phase: pointerdown=0, pointermove=1, pointerup=2, pointercancel=3.
    function touchRecord(e, phase) {
        const b = contentBox(canvas, streamWidth, streamHeight);
        const id = e.pointerId & 0xFFFF;         // low 16 bits, tracked per id
        if (phase === 0) heldTouches.add(id); else if (phase >= 2) heldTouches.delete(id);
        header(0x30);
        dv.setUint16(6, id, true); dv.setUint8(8, phase);
        const tx = Math.min(streamWidth  - 1, Math.max(0, Math.round((e.clientX - b.left) / b.s)));
        const ty = Math.min(streamHeight - 1, Math.max(0, Math.round((e.clientY - b.top)  / b.s)));
        dv.setUint16(9,  tx, true);
        dv.setUint16(11, ty, true);
        send(13);
    }
    canvas.addEventListener('pointermove',   (e) => { if (e.pointerType !== 'mouse') touchRecord(e, 1); });
    canvas.addEventListener('pointercancel', (e) => { if (e.pointerType !== 'mouse') touchRecord(e, 3); });

    // Focus-loss release: the browser only fires keyup/pointerup for events
    // it sees. Alt-tabbing away, or the tab going to a hidden/backgrounded
    // state, produces NEITHER — without this, whatever was held stays
    // pressed on the HOST indefinitely (confirmed bug in the pre-refactor
    // client; see "Held-Input Release on Focus Loss").
    function releaseAllHeld() {
        for (const hid of heldKeys) { header(0x10); dv.setUint16(6, hid, true); dv.setUint8(8, 0); send(9); }
        for (const btn of heldButtons) { header(0x22); dv.setUint8(6, btn); dv.setUint8(7, 0); send(8); }
        for (const id of heldTouches) {   // Phase 3 = cancel; coordinates are ignored
            header(0x30); dv.setUint16(6, id, true); dv.setUint8(8, 3);
            dv.setUint16(9, 0, true); dv.setUint16(11, 0, true); send(13);
        }
        heldKeys.clear(); heldButtons.clear(); heldTouches.clear();
    }
    window.addEventListener('blur', releaseAllHeld);
    document.addEventListener('visibilitychange', () => { if (document.hidden) releaseAllHeld(); });
}

// InputAck (binary type 14) → latency
function recordAck(seq /*, serverTs */) {
    const t0 = sentAt.get(seq);
    if (t0 !== undefined) { inputLatencyMs = performance.now() - t0; sentAt.delete(seq); }
}
```

The clamp is client-side and **mandatory**. The wire fields are `u16` and
`contentBox()` returns the letterboxed *content* box, so a pointer or touch event
in a bar yields a negative coordinate that `setUint16` wraps to ~65535; the
server's own clamp then pins it to the OPPOSITE edge. Clamping before encoding is
what makes the server clamp a backstop rather than the only check.

- **Input is binary**, written to the input stream, zero-alloc on the hot
  path. `hidFromCode()` is a static `KeyboardEvent.code` → HID-usage table.
- **All positions are in the stream space** — `[0, config.width-1] x
  [0, config.height-1]` in encoded output pixels, upright, origin top-left (see
  [`MODULE_STREAM_PARAMS.md`](../core/MODULE_STREAM_PARAMS.md) "Coordinate space").
  The client never sends points, logical pixels or normalized coordinates; each
  injector converts into its own OS space at its own boundary.
- Pointer Lock toggles absolute (0x20) ↔ relative (0x21); request
  `canvas.requestPointerLock({ unadjustedMovement: true })` on click (Chrome/Edge
  disable mouse acceleration; Safari ignores the option).
- Every record carries `Seq`; the server's `InputAck` (binary type 14) echoes it
  for latency measurement. **The ack is best-effort** — the server writes it from a
  dedicated task fed by a 64-deep drop-oldest queue, so an unacked record costs one
  missing measurement and nothing else. That is why `sentAt` is bounded at the
  insert site rather than relying on exact-seq deletion.
- **Control** messages (keyframe, resize, set_*, pong, stats, decode_unsupported)
  use newline-JSON on the **control stream**: `sendControl({type:"keyframe"})`.
  Clipboard does NOT — it has its own clipboard stream.

### Clipboard, File Transfer, Gamepad (client side)

- **Clipboard** (see [`MODULE_CLIPBOARD.md`](../interaction/MODULE_CLIPBOARD.md)): on Chrome/Edge,
  request `clipboard-read`/`clipboard-write` and use the `clipboardchange` event
  to push copies as `[u32 Len][JSON {"type":"clipboard",...}]` on the **clipboard
  stream** (tag 0x02, opened **eagerly** on the first `config` message whenever
  `clipboard != "disabled"`, not on first use — a host→client push needs somewhere
  to land before the user has copied anything); apply host→client clipboard
  messages read off the same stream silently. On Firefox/Safari (no `clipboardchange` event),
  intercept `copy`/`paste` events (gesture-bound). Text + sanitized HTML only.
- **File transfer** (see [`MODULE_FILETRANSFER.md`](../interaction/MODULE_FILETRANSFER.md)):
  `dragover`/`drop` on the canvas → open a new bidirectional stream (tag 0x03) on
  the session → stream `file.stream()` in 64 KiB chunks. The client opens at most
  `config.fileStreamBudget` streams concurrently, queues the rest locally, and races
  each open against a **10 s** deadline — QUIC does not reject an over-budget open,
  it flow-controls it, so the promise would otherwise never settle. On expiry the
  client abandons that open and surfaces `too_many_streams` against that file in the
  Files panel, retrying from its local queue when a slot frees. Show a drop overlay.
  A **Files** panel lists the host Outgoing folder for downloads
  (`showSaveFilePicker` on Chrome/Edge).
- **Gamepad** (see [`MODULE_GAMEPAD.md`](../interaction/MODULE_GAMEPAD.md)): poll
  `navigator.getGamepads()` on `requestAnimationFrame`, send binary
  `GamepadState` (type 0x40) only when the snapshot changes; emit `GamepadConnect`
  (0x41) / `GamepadDisconnect` (0x42) on the W3C events. Receive
  `FrameTypeGamepadRumble` (type 15) — whose `Index` is **this client's own local
  `navigator.getGamepads()` index**, because the server applies the inverse of the
  co-op slot remap before it builds the datagram, so a one-pad client driving global
  slot 3 is asked to rumble index 0 rather than an index it does not have — and
  forward to
  `gamepad.vibrationActuator.playEffect("dual-rumble", …)` (Chrome/Edge);
  fallback to `gamepad.hapticActuators[0].pulse(...)` on Firefox; silently drop
  on Safari (no haptic API). Honest scope: **casual gaming**; rAF polling caps
  effective latency at ~16-26 ms.

### Status Display

```
● Connected | 60 fps | 12.4 Mbps
○ Disconnected
```

Updates every 1 second with frame count and byte count deltas. The Mbps figure is
the client's own **measured** 1 s byte delta across every lane, not an advertised
target — it is the same number the HUD's bitrate field reads (R-CLI-13).

---

## Refactoring Directives

### R-CLI-01: Fix Duplicate init() Function
The file defines `init()` twice (line 22 and line 292). The second silently shadows the first. Merge into a single initialization function.

### R-CLI-02 + R-CLI-03: Codec from Config Handshake (RESOLVED)
The codec mismatch is fixed by the control-stream handshake: right after `auth_ok` the server sends a `{"type":"config",...}` JSON line FIRST, carrying the full WebCodecs `codec` string (e.g. `avc1.64002A` for H.264 High at 1080p60, or `hvc1.2.4.L123.B0` when the session is HDR — the level is computed from the active geometry, see MODULE_ABI "Codec-string computation"), plus `width/height/fps/chroma/hdr/audio*/cursorMode/clipboard/fileStreamBudget/carrier`. The client configures `VideoDecoder` from that — never hardcoded.

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
  audioGaps: audioGapsThisWindow, // concealed audio frames; see "Audio Playback Pipeline"
});
```

- **`dropped` is derived from datagram sequence gaps**, the same signal the
  decoder's gap detection already computes: for each live video frame accumulate
  `max(0, seq − lastSeq − 1)`, plus any frames the needs-IDR gate discarded and
  any `VideoDecoder` `dequeue`/error drops. There is no fast-join exception to
  skip — the bootstrap IDR is fresh, so the first live frame is `bootstrapSeq + 1`
  and gap detection runs from it. It is sent as a **per-window delta** (the server
  tracks the delta; see its `{"type":"stats"}` `dropped` handling).
- **`audioGaps`** is the count of concealed audio frames in the same window, so
  audio loss and video loss are separable in the server's telemetry.
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
- **This loop reacts to congestion; it does not estimate bandwidth.** Neither the
  client nor the server measures link capacity — the inputs are QUIC
  `smoothed_rtt` and `cwnd`, the server's own datagram-drop rate, and these 1 Hz
  deltas, and the controller ramps the bitrate against them (see
  [`MODULE_STREAM_PARAMS.md`](../core/MODULE_STREAM_PARAMS.md)
  "Congestion-Reactive Bitrate Control"). No spec should describe it as bandwidth
  estimation, and an active probe would need its own frame type and its own
  directive rather than being smuggled in here.

### R-CLI-07: Handle Stream Send Errors (RESOLVED)
`send()` now writes through the input-stream writer with an async
catch; failures are silently dropped (the next reconnect will re-establish the
stream). See "Input Handling (binary)".

### R-CLI-08: Touch Input (RESOLVED — native touch records)
Touch uses `PointerEvent` and the binary `TouchContact` record (type 0x30), NOT
mouse emulation. The client emits `0x30` — never `0x20`/`0x21`/`0x22` — for every
`PointerEvent` whose `pointerType` is `"touch"` or `"pen"`; only `"mouse"` produces
the mouse records. `PointerId` is the event's `pointerId` truncated to its low 16
bits and is tracked per id (not per `isPrimary`), so a two-finger gesture produces
two independent contact streams, and `Phase` follows the event name
(`pointerdown`=0, `pointermove`=1, `pointerup`=2, `pointercancel`=3). See the
`touchRecord()` code in "Input Handling (binary)". The host injects real multitouch where a `TouchInjector` add-on
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
├── connection.js    // Carrier selection (/wt, /ws) + lane + datagram management
├── protocol.js      // 22-byte header parse + binary input encode helpers
├── decoder.js       // VideoDecoder setup and frame dispatch
├── renderer.js      // Canvas rendering
├── cursor.js        // Cursor overlay: CURSOR_UPDATE datagrams + cursor-stream shapes
├── audio.js         // AudioContext + Worklet [audio deferred]
├── input.js         // Binary input, HID-usage map, pointer-lock
├── clipboard.js     // clipboardchange/copy/paste interception
├── files.js         // Drag-drop + Files panel (per-transfer 0x03 lanes)
├── gamepad.js       // Gamepad-API poll + rumble apply
└── stats.js         // Outbound telemetry (R-CLI-06) + on-screen HUD (R-CLI-13)
```
Use ES modules (`import`/`export`) since all target browsers support them.

### R-CLI-11: Add Connection Token
Carry the session token in the **first JSON message on the control stream** —
`{"type":"auth","token":"<bearer>","role":"control|view|player","resume":false,"takeover":false,"decode":{…}}`
— NOT as a URL query parameter (which leaks to proxy logs / Referer / browser
history) **and never a URL fragment, which reaches the same sinks**. See
[`MODULE_AUTH.md`](../core/MODULE_AUTH.md) and "Credential entry".

The `session_token` from every `config` message is stored **in a module-level
variable in `connection.js` only** — never `localStorage`, never `sessionStorage`,
never the URL — because it is a bearer credential. The reconnect path reads it and
sets `resume: true`; a client that has none sends `resume: false` and falls back to
`POST /auth`. `takeoverRequested` is a one-shot flag, cleared after the auth line is
written, so a subsequent automatic reconnect does not silently seize the controller
slot again.

### R-CLI-12: Held-Input Release on Focus Loss (fixes a confirmed bug)

The pre-refactor client had no `blur`/`visibilitychange` handling: alt-tabbing
away from the tab (or the OS switching windows) fires neither `keyup` nor
`pointerup` for whatever was held at that moment, because the browser simply
stops delivering events to a backgrounded page — the session stays open, so
nothing tells the host those inputs were released. The result:
a key or mouse button can stay "pressed" on the host indefinitely. See the
`heldKeys`/`heldButtons`/`releaseAllHeld()` code above (Internal Architecture)
for the fix — every currently-held key, pointer button and touch contact gets a
synthetic release (a touch contact as a `Phase = 3` cancel) the moment
`window.blur` fires or `document.visibilityState` becomes `hidden`.
This is a client-only fix; no wire format or server change is needed since
`releaseAllHeld()` just sends ordinary up-events through the existing binary
input records.

### R-CLI-13: On-Screen Connection/Stats HUD

`stats.js` and the `index.html` "status overlay" were previously named in
this doc's file listing (see Public Interface above) but never actually
specced beyond `stats.js`'s outbound telemetry role (R-CLI-06) — there was no
spec for anything shown **to the user**, which is also why bandwidth/RTT
visibility was never built pre-refactor (see
`PROJECT_ARTIFACTS/summaries/multiclient_metrics_polish/phase2.md`). This
closes that gap:

- A toggleable HUD (default hidden; **F9** or a small on-canvas icon toggles
  it — pick one consistently, doesn't need to be configurable) overlays:
  - **FPS** — `framesPresentedThisWindow` from the same rolling window R-CLI-06
    already computes for outbound `stats` telemetry (no new measurement, just
    render the existing number instead of only sending it).
  - **Input latency** — `inputLatencyMs`, already computed in `recordAck()`
    (Internal Architecture above) but never consumed until now.
  - **Stream quality** — resolution, codec, chroma and carrier from the last
    `{"type":"config"}` message; **bitrate from the client's own 1 s byte-delta
    Mbps** (the counter the Status Display above already computes). `config`
    deliberately carries no bitrate field: an adaptive bitrate change sends no
    `config` message (see [`MODULE_STREAM_PARAMS.md`](../core/MODULE_STREAM_PARAMS.md)
    → "Adaptive bitrate"), so a config-sourced number would be stale within 100 ms
    of the first adaptation — and the measured number is what the user actually
    wants to see anyway.
  - **Connection state** — `connecting` / `connected` / `reconnecting` /
    `disconnected`, driven by the session's own state transitions and the resume
    flow (see MODULE_TRANSPORT.md "Connection Lifecycle"). When the state is
    `reconnecting` or `disconnected` it also shows the **close code** that produced
    it and the countdown to the next attempt, so a `4429` "server is full" wait is
    distinguishable from a `4401` credential rejection at a glance (see
    "Reconnect policy").
  - **Effective role** — the role from `auth_ok`, plus its `downgrade_reason` when
    the server granted something other than what the URL hash requested.
  - **Host fingerprint** — the pinned `spki_sha256`, in self-signed mode, so a user
    can compare it against the server's startup banner at any time (see
    MODULE_SERVER "Browser certificate trust"). Absent in CA-trusted mode, where
    the public PKI is the trust anchor and nothing is pinned.
- This is a passive display of numbers the client already has, not a new
  active "bandwidth test" (no extra probe traffic, no new server endpoint) —
  deliberately the minimal fix that gives the user real-time connection
  visibility without introducing an unspecced new feature.

### R-CLI-14: Deterministic Teardown and Backgrounded-Tab Behavior

The client allocates OS-backed resources the GC does not promptly reclaim —
an `AudioContext` (a real audio device handle), an `AudioWorklet` node, a
`VideoDecoder`, in-flight `VideoFrame`s, and the carrier's own connection.
Nothing previously specified when they are released, which is how you get sound
continuing after the viewer navigates away, or a "this tab is using your
microphone/audio" indicator that never clears.

**Teardown — one idempotent `teardown()`, called from every exit path**
(`pagehide`, `beforeunload`, an explicit Disconnect, `server_shutdown`, and the
session's own closed promise). Use **`pagehide`, not `unload`** — `unload`
prevents the page from entering the browser's back/forward cache and does not
fire reliably on mobile. Order matters, and it is the reverse of setup:

1. `releaseAllHeld()` (R-CLI-12) **first**, while the session is still open —
   otherwise the host keeps the held keys. This is the one step that must
   happen before the transport goes away.
2. `videoDecoder.close()`; `close()` every `VideoFrame` still in the present
   queue. An unclosed `VideoFrame` pins a GPU buffer and will log a browser
   warning — which is why `close()` is called on **every** path a frame leaves by:
   presented, skipped by the present-loop's pick, evicted from the capped
   background queue, or dropped here at teardown.
3. Disconnect the worklet node, then `await audioContext.close()` — node first,
   or the worklet's `process()` can run against a closing context.
4. Close the carrier with an application close code (`wt.close()` on
   WebTransport, a close frame carrying the same `close::*` value on the WebSocket
   carrier — the mapping is 1:1), then clear the reassembly map, the cursor
   shape-bitmap cache and the `sentAt` latency map (all hold references, and the
   shape cache holds `ImageBitmap`s, that would otherwise outlive the session).

Every step is wrapped so one failure does not skip the rest, and `teardown()`
sets a flag so a second call is a no-op — `pagehide` and the session-closed
promise routinely both fire.

**Backgrounded tab — audio continues, video stops decoding.** This is
deliberate, not an oversight:

| Resource | Backgrounded (`visibilitychange` → hidden) | Why |
|----------|--------------------------------------------|-----|
| Held inputs | Released immediately (R-CLI-12) | A stuck key on the host is the worst outcome |
| Audio | **Keeps playing, gaplessly** | Audio is the master clock (MODULE_AUDIO); pausing it would break A/V sync on return, and a backgrounded remote session is a normal "listen while I work" case. Browsers throttle timers but not `AudioWorklet`. |
| Video decode | Stops presenting; `requestAnimationFrame` does not fire in a hidden tab | Continuing to decode frames nobody sees burns CPU and battery for nothing |
| Incoming video datagrams | Still reassembled, but the present queue is capped at **3** frames and drops oldest — **and `close()` is called on every evicted `VideoFrame`** | Keeps the reassembler's state coherent without growing unboundedly across a long background period. An unclosed frame pins a GPU buffer, so eviction without `close()` leaks one buffer per dropped frame for the whole background period |
| On return (`visible`) | `markNeedsIdr()` — the gate drops deltas until the fresh IDR arrives, and the keyframe request is throttled like any other | The decoder's reference chain is stale after dropping frames; ask for a fresh IDR rather than decoding garbage. No special-case gap suppression is needed: the gate, not a suppression rule, is what keeps the decoder clean. |

With audio disabled the same rules apply minus the audio row; video presents on
its own capture clock on return.

---

## Testing Strategy

| Level | What | Approach |
|-------|------|----------|
| Unit | Header parsing (22-byte protocol) | Jest/Node (buffer operations) |
| Unit | Config handshake parse → decoder config | Jest/Node |
| Unit | Gap detection from the first live frame; `is_newer` modular comparison across the u32 wrap | Jest/Node |
| Unit | needs-IDR gate: after a simulated sequence gap, delta chunks are counted and discarded and `decoder.decode()` is not called until a key chunk arrives | Jest/Node |
| Unit | Bootstrap dedup: datagram fragments whose `FrameID` is not `is_newer` than `bootstrapSeq` are discarded and never reassembled | Jest/Node |
| Unit | Keyframe-request throttle: ten `markNeedsIdr()` calls inside 500 ms produce exactly one `{"type":"keyframe"}` line | Jest/Node |
| Unit | Decode gate: an `isConfigSupported` rejection sends `decode_unsupported` and never calls `configure()`; `probeDecode()` maps a throwing probe to `false` | Jest/Node |
| Unit | Reconnect policy: each close code in the table produces its action, and the backoff draws uniformly over `[0, delay]` rather than a fixed interval | Jest/Node |
| Unit | Audio gap detection + concealment: dropping every 10th audio datagram advances `audioPlayoutTs` by exactly `frame_ms` per loss and counts each in `audioGaps` | Jest/Node |
| Unit | Channel permutation + downmix: a synthetic 5.1 frame in Vorbis order lands each tone in the right Web Audio destination channel, and the stereo downmix discards LFE | Jest/Node |
| Unit | `sentAt` stays at or below 4096 entries when 100 000 records are sent with no acks, and the surviving entries are the newest | Jest/Node |
| Unit | S16LE → Float32 conversion | Jest/Node |
| Unit | Coordinate scaling (client → stream space) via `contentBox`, including the letterboxed case; the pointer mapping and the cursor overlay agree pixel-for-pixel | Jest/Node |
| Unit | Touch routing: a `pointerType: "touch"` event emits a `0x30` TouchContact and never a `0x20`/`0x22`; two simultaneous contacts produce two independent `PointerId` streams | Jest/Node |
| Unit | CursorUpdate parse (14 bytes), reorder rejection by FrameID, shape-cache hit/miss, and overlay positioning at a 21:9 element showing a 16:9 stream — the letterboxed case, where element-box scaling is visibly wrong | Jest/Node |
| Unit | `releaseAllHeld()` sends an up-record for every entry in `heldKeys`/`heldButtons` and clears both sets | Jest/Node |
| Integration | `window.blur` while a key is held sends its release before any other input; a key pressed AFTER blur (impossible in a real browser, but the handler must not throw) is a no-op | Browser automation (Playwright) |
| Integration | Full connection + frame decode | Browser automation (Playwright) |
| Integration | Carrier fallback: with UDP blackholed, the 3 s WebTransport deadline fires and the client completes auth over `/ws` and decodes the bootstrap IDR — identical tags and framing on both carriers | Browser automation (Playwright) |
| Integration | Effective-role adoption: a second `control` client receives `auth_ok` with `role:"view"` and `downgrade_reason:"controller_slot_taken"`, does not open the input stream, and does not reconnect-loop | Browser automation (Playwright) |
| Integration | Teardown: `pagehide` releases held inputs first, closes every `VideoFrame` and the decoder, disconnects the worklet before closing the `AudioContext`, and is a no-op on a second call | Browser automation (Playwright) |
| Visual | Render quality, cursor alignment | Manual + screenshot comparison |
| Performance | Decode latency, frame drop rate | WebCodecs metrics API |

---

## Browser Compatibility

**Supported browsers:** Chrome 107+, Edge 98+, Firefox 130+, Safari 16.4+. The
floor is set by **WebCodecs**, which every carrier needs; **WebTransport** sets
the floor for the *preferred* carrier only, and browsers below it fall back to
the WebSocket carrier (see [`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md)
"Carrier selection"). Per engine: Chrome 107 / Firefox 130 / Safari 16.4 on the
fallback, Chrome 107 / Firefox 130 / Safari 26.4 on WebTransport.

| Feature | Required | Support |
|---|---|---|
| WebTransport | For the preferred carrier | Chrome 97+, Edge 98+, Firefox 114+, Safari 26.4+ (WebKit shipped it enabled-by-default in 26.4, March 2026; 18.x had it only behind an experimental flag). Below this the client uses the WebSocket carrier. |
| WebSocket | For the fallback carrier | All target browsers |
| WebCodecs VideoDecoder | Yes | Chrome 107+, Firefox 130+, Safari 16.4+ |
| WebCodecs AudioDecoder (Opus) | Audio only | Chrome 94+, Firefox 130+; **Safari falls back to a wasm libopus decoder**. (PCM audio needs neither.) |
| Pointer Lock | Optional | Chrome (all), Firefox (all), Safari 13.1 |
| Fullscreen API | Optional | Chrome (all), Firefox (all), Safari (all) |
| ES Modules | For refactored version | Chrome (all), Firefox (all), Safari (all) |

**Effective minimums: Chrome 107 / Firefox 130 / Safari 16.4.** iOS is WebKit-only,
so "use another browser" is not available there — the fallback carrier is what
makes iPhone/iPad below 26.4 work at all.

**Firefox is supported from 130+** (the first version shipping WebCodecs
`VideoDecoder` un-flagged). Firefox's low-latency WebCodecs path is younger than
Chromium's, so it is treated as a secondary target; earlier Firefox gets the
graceful unsupported-browser notice from `connect()` step 0. Firefox's WebCodecs
also cannot decode HEVC, which is why H.264 is the codec for every SDR session and
why the HDR admission gate refuses to enter HDR over the head of an attached client
that reported `decode.hevc10 == false`.

**The client gates on capabilities, never on a version string.** The numbers above
document what those capability checks resolve to today; the code tests for
`"VideoDecoder" in window` and `"WebTransport" in window`, so a browser that ships
either earlier or later needs no change here.

> AudioWorklet would be a future requirement when the deferred audio module is
> un-paused; until then, the client does not load any audio code path.

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Motion-to-photon, audio disabled | <20 ms (LAN, 1080p60) |
| Motion-to-photon, audio enabled | <65 ms (adds the ~40 ms audio jitter buffer) |
| Decode latency (1080p H.264) | <5ms |
| Render (drawImage) | <1ms |
| Input event → input-stream write | <1ms |
| Audio latency (buffer to speaker) | <50ms |
| Reconnect time | 1-3 seconds (first attempt; per-code backoff thereafter — see "Reconnect policy") |
| Memory (1080p decode) | <100MB |
