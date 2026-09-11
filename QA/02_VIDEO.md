# QA 02 — Video: capture, encode, cursor, resolution

Matching user stories: `PROJECT_ARTIFACTS/user_stories/02_video_streaming.md`.

Many of these are **look-at-it** checks. An automated test can assert that an
access unit decoded; only a human notices that the colours are wrong, the
picture is upside down, or the pointer is 40 px off.

## Capture and selection

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-VID-1 | With several capture add-ons loaded, no `force_addon` | The documented probe order picks the expected one; the log names which and why | `LINUX_SPEC` / `MODULE_PIPELINE` step 3d | ☐ |
| QA-VID-2 | `[capture] force_addon` naming an unavailable add-on, `mode = "forced"` | Startup fails cleanly saying so — never a silent fall-through to another backend | `MODULE_CONFIG` | ☐ |
| QA-VID-3 | Multi-display host, `output_index`/`display_id`/`output_name` selecting the **second** monitor | The second monitor is streamed. Changing the key requires a restart, as documented | `MODULE_CAPTURE` "Display selection" | ☐ |
| QA-VID-4 | Display **rotation** set to 90/180/270 on the host | Picture arrives **upright** in the browser, with correct aspect. The add-on reports rotation and never rotates itself; the converter or the encoder VPP applies it | `MODULE_CAPTURE` "Display rotation" | ☐ |
| QA-VID-5 | Padded readback (`stride > width*4`, common on DXGI) | No skew, no diagonal tearing. This is the classic symptom of a consumer ignoring stride | CENTRAL_SPEC Contract 1 | ☐ |
| QA-VID-6 | Colour check: display a known test image (colour bars, skin tones, a saturated red) | No green/magenta cast (BGRA↔RGBA swap), no washed-out or crushed levels (full↔limited range confusion). BT.709 limited range on both paths | CENTRAL_SPEC Contract 1 / `MODULE_ENCODE` | ☐ |
| QA-VID-7 | Monitor hotplug or mode change **on the selected display** mid-session | Resolution-change flow runs: new `config`, forced IDR, client reconfigures its decoder and rescales input. No crash, no permanent black | CENTRAL_SPEC "Resolution-Change Flow" | ☐ |
| QA-VID-8 | Unplug the selected display entirely | Capture-error ladder, not a crash: 3 consecutive errors → capturer restart; 10 → fatal; `Unrecoverable` → drop the add-on and walk the probe order | `MODULE_PIPELINE` "Error Recovery Strategy"; US-VID-14 | ☐ |

## Encode

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-VID-9 | Each HW encoder add-on on its matching GPU | Streams at the configured fps; `codec()` reports a **computed** WebCodecs string (e.g. `avc1.64002A` at 1080p60), never a hardcoded level | `MODULE_ABI` "Codec-string computation" | ☐ |
| QA-VID-10 | Software default (`openh264`) with no HW add-on loaded | Streams at 30 fps target; picture is correct | `MODULE_ENCODE` | ☐ |
| QA-VID-11 | `force_addon = "x264"` | Selected only when forced. It is **never** chosen by `mode = "auto"` | `MODULE_ENCODE` "Software encoder order" | ☐ |
| QA-VID-12 | `x264` add-on with **no ffmpeg** in PATH | Caught at `probe()` as unavailable with a reason — not a crash at first frame | `X264_SUBPROCESS_*_SPEC` | ☐ |
| QA-VID-13 | HW encoder that cannot import the surface (force the condition) | `StreamError::FallbackToSoftware`; the session degrades to the software path for the rest of its life and keeps streaming | CENTRAL_SPEC Contract 6 | ☐ |
| QA-VID-14 | Zero-copy path active (HW encoder + `SurfaceCapturer`) | Confirm via metrics/logs that no CPU readback is occurring. On the software path confirm the readback cost matches the recorded budget | CENTRAL_SPEC "Two Encoding Paths" | ☐ |
| QA-VID-15 | Watch a fast-moving scene for 10 minutes | No creeping latency, no unbounded queue growth, no drift between motion and picture | `MODULE_PIPELINE` frame-drop strategy | ☐ |
| QA-VID-16 | Load the host until encode exceeds the frame interval | Skip-before-capture behaviour; skip accumulation bounded (~200 ms); sustainable-rate control lowers advertised fps and **never below 5 fps** | `MODULE_PIPELINE` "Sustainable-rate control" | ☐ |

## Keyframes and joining

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-VID-17 | Second client joins a running session | Gets a **decodable** picture immediately from the bootstrap stream. No visible disturbance to the existing viewer — no black flash, no stutter | `MODULE_SERVER` "Keyframe Caching"; TD-26 | ☐ |
| QA-VID-18 | Join an **idle** host (static screen, stale cached IDR) | Receives a fresh IDR within `idle_keyframe_ms + join_idr_timeout`; first live delta frame references a picture the joiner actually has | `MODULE_SERVER` freshness rule | ☐ |
| QA-VID-19 | 10 clients join within one second | At most **one** forced IDR per `keyframe_min_interval_ms` in total. No keyframe storm | `MODULE_SERVER` "Keyframe-Request Rate Limiting" | ☐ |
| QA-VID-20 | A client spams `{"type":"keyframe"}` | Rate-limited by the token bucket; other clients unaffected; the session is not closed | `MODULE_SERVER` | ☐ |
| QA-VID-21 | Every join, watched at the host | The capturer is **never** restarted. Confirm by log and by the other viewers seeing no disturbance | TD-26 | ☐ |

## Cursor

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-VID-22 | `cursor_mode = "separate"` on an add-on that supports it | Pointer visible as a client overlay. It keeps moving smoothly **while video is static or skipping** — this is the whole point of the mode | CENTRAL_SPEC Contract 8 | ☐ |
| QA-VID-23 | Pointer alignment | A click at the visible pointer lands exactly there on the host. Check at each corner, and on a **scaled** stream (client window ≠ native) | `MODULE_INPUT` "Coordinate-space rule" | ☐ |
| QA-VID-24 | Change the cursor shape (text I-beam, resize, custom app cursor) | The new bitmap arrives once and renders correctly, with the right hotspot | `MODULE_CAPTURE` "Shape identity" | ☐ |
| QA-VID-25 | Pointer goes idle | Position re-sent at 250/500/750 ms then stops. The client's pointer does not freeze at a stale place | CENTRAL_SPEC Contract 8 | ☐ |
| QA-VID-26 | New client joins while the host pointer is idle | It still gets a pointer — seeded from the cached shape + position on its own cursor stream | CENTRAL_SPEC Contract 8 | ☐ |
| QA-VID-27 | Force a `next_cursor` failure | Pointer is **hidden** (not frozen), a warning fires once, video keeps running, and the mode never flaps per frame | `FrameLoop::on_cursor_error` | ☐ |
| QA-VID-28 | `cursor_mode = "embedded"` | Exactly **one** pointer visible — never a composited cursor *and* an overlay on top | `MODULE_CAPTURE` cursor decision | ☐ |
| QA-VID-29 | `cursor_mode = "separate"` with an add-on lacking `CURSOR` (e.g. `wl_screencopy`) | That add-on is **ineligible at selection** — not selected and then silently pointer-less | CENTRAL_SPEC Contract 8 | ☐ |

## Resolution, HDR, chroma

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-VID-30 | Resize the browser window slowly and continuously | Debounced; hysteresis prevents oscillation; the picture never stretches — aspect is preserved via letterbox/pillarbox, and `resize_suppressed` carries the dims **actually in force** | `MODULE_STREAM_PARAMS` "Dynamic Resolution Change Flow" | ☐ |
| QA-VID-31 | Request a resolution **above** the host's native | Clamped to native, and the client is told the real dims | `MODULE_STREAM_PARAMS` | ☐ |
| QA-VID-32 | HiDPI client (`devicePixelRatio` 2) | The stream is requested at true device pixels, so text is sharp rather than resampled | OQ-03 | ☐ |
| QA-VID-33 | `set_hdr` on a host with HDR available, Chromium client | HEVC Main10 negotiated; picture tone-maps sanely into the canvas — not blown out, not grey | `MODULE_STREAM_PARAMS` "HDR Pipeline" | ☐ |
| QA-VID-34 | `set_hdr` with a **Firefox** viewer attached | Refused with `{"type":"hdr_unavailable"}` — Firefox's WebCodecs cannot decode HEVC, and the gate prevents a black screen rather than producing one | `PLATFORM_COMPAT` HEVC caveat; US-VID-15 | ☐ |
| QA-VID-35 | `chroma = "444"` on an encoder that supports it, then on one that does not | Transparent fall-back to 4:2:0 in the unsupported case; text visibly sharper in the supported case | `MODULE_STREAM_PARAMS` "Chroma Subsampling" | ☐ |
| QA-VID-36 | Client that reports it cannot decode the advertised codec | Server never advertises a codec no attached client can decode; `decode_unsupported` is handled | `MODULE_PROTOCOL` "Decode capability" | ☐ |

## Loss and recovery

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-VID-37 | **N5**, 1 % induced loss, WebTransport | Occasional dropped frames; the picture recovers via gap detection + IDR request. No permanent corruption, no green blocks that persist | US-VID-13 | ☐ |
| QA-VID-38 | Same at 2 % | Degraded but usable; bitrate adapts down and recovers when loss clears | `MODULE_STREAM_PARAMS` | ☐ |
| QA-VID-39 | Same on the **WebSocket** carrier | Visibly worse (head-of-line blocking) but never corrupt. Confirms the documented trade is what actually happens | `MODULE_TRANSPORT` perf targets | ☐ |
| QA-VID-40 | Drop a single fragment of a multi-fragment access unit | Reassembly deadline fires, the partial frame is discarded (never delivered partially), and a keyframe is requested | `MODULE_PROTOCOL` "Datagram reassembly rules" | ☐ |
