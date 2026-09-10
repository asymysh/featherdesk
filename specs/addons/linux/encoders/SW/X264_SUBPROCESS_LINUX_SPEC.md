# Software Encoder Add-On: x264 (Subprocess, GPL-Isolated)

## Purpose

libx264 H.264 software encoder running as a **separate subprocess** to maintain
GPL isolation from the proprietary main binary. The fastest software H.264
encoder available — 2x faster than OpenH264 at equivalent quality.

**Opt-in.** This add-on is never auto-selected; it is reached only by
`[encode] force_addon = "x264"`. Its only mechanism for an on-demand IDR is
killing and respawning the `ffmpeg` child — see
[`MODULE_ENCODE.md`](../../../../media/MODULE_ENCODE.md) "Software encoder order".
`openh264` is the software default.

The subprocess model: the main `featherdesk` binary (proprietary) spawns a
small GPL-licensed encoder process. Communication via stdin/stdout pipe (raw
I420 frames in, H.264 NALs out). Only the encoder subprocess is GPL; the main
binary never links libx264.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| libx264 | GPL-2.0+ | Encoder library |
| ffmpeg (as subprocess) | GPL-2.0+ | Used as the subprocess wrapper; already links libx264 |
| Our Rust bridge code | Proprietary | Spawns subprocess, pipes frames, reads NALs |

**GPL isolation:** The main `featherdesk` binary is proprietary. The x264
subprocess is a separate program (ffmpeg) distributed under GPL. This is the
same pattern used by VLC, commercial streaming products, and any proprietary
app that shells out to ffmpeg.

**What we publish:** The Rust bridge code that spawns the subprocess is trivial
and proprietary. ffmpeg is distributed separately under its own GPL license.
Users install ffmpeg independently or we bundle it as a separate binary.

---

## Performance (Benchmarked — Windows reference, Linux native pending)

Measured on AMD Ryzen 9 5900X (12C/24T), Windows 11, synthetic I420 frames.
The x264 subprocess mechanism is platform-agnostic — the same ffmpeg
subprocess code runs identically on Linux. Linux native benchmarks pending.

### 1920x1080

| Config | Threads | FPS | ms/frame | vs OpenH264 |
|--------|---------|-----|---------|-------------|
| **x264 ultrafast zerolatency** | 12 | **302** | **3.3ms** | **2.2x faster** |
| x264 ultrafast zerolatency | 8 | 294 | 3.4ms | 2.2x faster |
| x264 ultrafast zerolatency | 4 | 225 | 4.4ms | 1.7x faster |
| x264 superfast zerolatency | 4 | 150 | 6.7ms | 1.1x faster |
| x264 ultrafast zerolatency | 1 | 99 | 10.1ms | 0.7x (slower) |
| OpenH264 4-slice (reference) | 4 | 134 | 7.4ms | baseline |

### 2560x1440

| Config | Threads | FPS | ms/frame | vs OpenH264 |
|--------|---------|-----|---------|-------------|
| **x264 ultrafast zerolatency** | 12 | **181** | **5.5ms** | **2.5x faster** |
| x264 ultrafast zerolatency | 4 | 127 | 7.9ms | 1.7x faster |
| OpenH264 4-slice (reference) | 4 | 74 | 13.5ms | baseline |

### Pipe overhead

| Metric | Value |
|--------|-------|
| ffmpeg subprocess startup | ~50ms (one-time, amortized) |
| Per-frame pipe I/O (I420 1080p = 3.1MB) | <1ms |
| Total subprocess overhead vs hypothetical Rust FFI direct | ~1-2ms |

The subprocess model adds ~1-2ms over what a hypothetical direct Rust FFI binding
would achieve. At 3.3ms total, this is still faster than OpenH264's 7.4ms
in-process.

---

## Architecture

```
featherdesk (proprietary)                    ffmpeg (GPL)
┌────────────────────┐                   ┌─────────────────────┐
│                    │   stdin pipe       │                     │
│ x264 bridge module │ ── I420 frames ──>│ -c:v libx264        │
│                    │                   │ -preset ultrafast    │
│ Spawns ffmpeg once │   stdout pipe     │ -tune zerolatency   │
│ per session        │ <── H.264 NALs ──│ -crf 26             │
│                    │                   │ -threads N           │
└────────────────────┘                   └─────────────────────┘
        │                                         │
    MIT/Proprietary                            GPL-2.0+
    (our code)                              (separate binary)
```

The ffmpeg process is started once when the x264 encoder is selected and
persists for the entire streaming session. Frames are written to stdin as
raw I420, NALs are read from stdout in Annex B format.

---

## Subprocess ffmpeg command

```bash
ffmpeg -hide_banner -loglevel error \
  -f rawvideo -pix_fmt yuv420p -s 1920x1080 -r 60 \
  -i pipe:0 \
  -c:v libx264 -preset ultrafast -tune zerolatency -profile:v high \
  -crf 26 -threads 12 \
  -bsf:v h264_metadata=aud=insert \
  -f h264 pipe:1
```

`-bsf:v h264_metadata=aud=insert` makes ffmpeg emit an Access Unit Delimiter
(NAL type 9) at every AU boundary. That delimiter is the only thing that makes an
unframed `-f h264 pipe:1` byte stream splittable into access units — see
"NAL splitter".

Parameters controlled by the Rust bridge:

| Parameter | Source | Default |
|-----------|--------|---------|
| Preset | `[addon_module_x264] preset` (static, read once at startup) | `ultrafast` |
| CRF | `stream::Params.qp` — never read from config by this add-on; it arrives with the initial params and again on every rebuild (see "Stream Params Translation") | 26 (from `[stream] qp`) |
| Threads | `[addon_module_x264] threads`; `0` = auto (`std::thread::available_parallelism()`, capped at 12) | `0` |
| ffmpeg binary | `[addon_module_x264] ffmpeg_path`; `""` = auto-discover | `""` |
| Tune | hardcoded | `zerolatency` (mandatory for streaming) |
| Profile | `[addon_module_x264] profile` — the chroma negotiation selects it; this is the ceiling | `high` |

---

## Build & Distribution

### Shared library build

```bash
cargo build --release -p featherdesk-addon-x264   # cdylib → featherdesk-addon-x264.so
```

The `x264` add-on cdylib contains the Rust bridge code that spawns the subprocess.
The x264 bridge is pure Rust (`std::process` + pipes); it exports the add-on's `abi_stable` root module as its entry point — no C SDK is linked into the library.

### Runtime dependency

ffmpeg must be in PATH or at a known location. The bridge searches:
1. `VIEWPORT_FFMPEG_PATH` environment variable
2. `ffmpeg` in PATH
3. `./ffmpeg` next to the binary (bundled deployment)

### Distribution options

| Option | How |
|--------|-----|
| User installs ffmpeg | `apt install ffmpeg` (Debian/Ubuntu) / `dnf install ffmpeg` (Fedora) |
| Bundle ffmpeg binary | Ship `ffmpeg` alongside `featherdesk` in the installer |
| Docker | `FROM rust:1.83 AS build` + `apt install ffmpeg` in runtime stage |

---

## File Structure

```
addons/encode/x264/
├── x264.rs              // Rust bridge: subprocess management, pipe I/O
├── nal_split.rs         // AUD-keyed access-unit splitting + timestamp FIFO
├── probe.rs             // the root module's probe() -> ProbeReport
└── tests.rs             // Integration test (requires ffmpeg)
```

---

## Codec Output

- Profile: High (`-profile:v high`); `ultrafast` disables CABAC, which High permits
- Entropy: CAVLC (ultrafast) or CABAC (superfast+)
- Slices: auto (x264 decides based on thread count)
- B-frames: 0 (zerolatency tune)
- IDR: on-demand via `force_keyframe()` → kill + restart ffmpeg (the only
  mechanism this bridge has; ~100 ms and a few dropped frames — see "Crash
  Recovery"), or `-x264opts keyint=N`. There is no cheaper path, which is why
  this add-on is opt-in
- NAL format: Annex B (start codes retained) — matches our wire protocol, with an
  Access Unit Delimiter at every AU boundary (see "NAL splitter")

---

## Probe & Selection

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Root module surface"):

```rust
// crate: featherdesk-addon-x264   (the add-on's cdylib)

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;
```

`probe` resolves the ffmpeg binary (`[addon_module_x264] ffmpeg_path`, then
`PATH`, then next to the host binary) and runs `ffmpeg -hide_banner -encoders` to
confirm `libx264` is built in. It reports:

```rust
ROk(ProbeReport {
    available: true, reason: RString::new(),
    codecs: RVec::from(vec![CodecId::H264]),
    caps: AddonCaps(0),          // NOT ENC_CONFIGURABLE: every parameter change
                                 //   respawns the child (see "Stream Params
                                 //   Translation"), so nothing changes hot
    displays: RVec::new(),
})
```

**Availability is not an error.** A missing or non-executable `ffmpeg`, or one
built without `libx264`, is `ROk(ProbeReport { available: false, reason: "ffmpeg
not found on PATH or built without libx264" })`, never an `RErr` — and it is
caught here rather than at the first frame, so a broken install is a startup
message and not a crash loop.

**Set every capability bit this add-on actually serves.** `AddonCaps(0)` is
correct and complete here: `ENC_CONFIGURABLE` would claim a hot parameter change
this bridge cannot perform.

**Only claim what this call can prove.** A bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons"); the constructed object's
`caps()` is authoritative and may be a strict subset of this one.

Being available is not being selected: this add-on is never in the auto order and
runs only under `[encode] force_addon = "x264"`.

---

## When to use this add-on

Force this add-on when:
- The deployment is CPU-bound and has **measured** that x264 beats OpenH264 on it
  (3.3ms vs OpenH264's 7.4ms at 1080p on 12 threads)
- No GPU hardware encoder is available (headless CPU-only server)
- GPL in the deployment is acceptable (ffmpeg is already GPL)
- Multi-core CPU available (scales well to 12+ threads)
- The session tolerates a process restart per on-demand keyframe — every client
  join and every gap recovery costs one

Stay on the OpenH264 default when:
- GPL is not acceptable in the deployment
- Single-core / low-thread environments (OpenH264 4-slice is more efficient below 4 threads)
- ffmpeg is not available or installable
- Clients join and leave often, or the link is lossy — each keyframe request is a
  respawn here and a flag on the next frame there

---

## Comparison with other SW encoders

| Encoder | 1080p ms | License | Ship? |
|---------|---------|---------|-------|
| **x264 ultrafast 12T** | **3.3ms** | GPL | ✅ subprocess, opt-in |
| OpenH264 4T | 7.4ms | BSD | ✅ Rust FFI direct, the default |
| VP9 speed8 4T | 9.0ms | BSD | ⚠️ marginal |
| x265 ultrafast 4T | 9.8ms | GPL | ❌ slower than OpenH264 |
| SVT-AV1 p12 12T | 13.2ms | BSD | ❌ too slow |

---

## Status

✅ **Benchmarked.** Subprocess batch encoding measured at 3.3ms P50 @ 1080p
(12 threads) on Ryzen 9 5900X. Persistent subprocess bridge (single ffmpeg
process per session) to be implemented. Opt-in only — never chosen by
`[encode] mode = "auto"`. Repo split (GPL subprocess → separate repo) deferred.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_x264]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

This add-on does **not** implement `encode::ConfigurableEncoder`, and its `ProbeReport` leaves `AddonCaps::ENC_CONFIGURABLE` clear (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)): the encoder is a child process behind an unframed pipe with no in-band control channel, so ALL parameter changes restart the ffmpeg child.

| Param change | Mechanism | Hot? |
|--------------|-----------|------|
| Any of `Width`/`Height`/`FPS`/`BitrateBps`/`QP`/`KeyframeInterval` | The pipeline sees the cleared `ENC_CONFIGURABLE` bit and rebuilds: it tears the bridge down and respawns ffmpeg with new `-s WxH -r FPS -crf QP -g KI` (CRF mode) or `-s WxH -r FPS -b:v B -g KI` (bitrate mode). `-crf` and `-b:v` are mutually exclusive. | no (rebuild) |
| `BitDepth=10` / `HDR=true` | Refused at construction with `StreamError::HdrUnsupported` -- there is no H.264 HDR profile in the WebCodecs spec, so the session stays SDR or moves to a HEVC-capable HW add-on | n/a |

**Restart semantics:** the bridge forces an IDR on the first frame from the new
ffmpeg instance so the client decoder picks up the new SPS/PPS cleanly — which is
why `apply_params` does NOT additionally call `force_keyframe()` after a rebuild
(MODULE_PIPELINE `apply_params`): doing so would respawn ffmpeg a second time for
one parameter change.

---

## Crash Recovery (subprocess death)

A *deliberate* restart (param change, forced IDR) is the section above. This
section covers the ffmpeg child dying on its own — killed by the OOM killer,
segfaulting, or never starting because the binary is missing or the codec was
built out. Fixes **TD-39**.

This add-on implements **Level 1** of the normative ladder in
[`MODULE_PIPELINE.md`](../../../../core/MODULE_PIPELINE.md) "Add-On Crash
Recovery" — read that first; it is the contract, this is the binding:

| Concern | This add-on's binding |
|---------|-----------------------|
| Detecting death | A dedicated reaper task owns `Child::wait()`. Death is also inferred from `EPIPE`/`BrokenPipe` on the stdin write or clean EOF on the stdout NAL reader — whichever fires first wins; the other is a no-op. |
| Backoff | 100/200/400/800/1600 ms ±20 % jitter, counter decays after 60 s of a healthy child. |
| During the gap | `encode()` returns `StreamError::Backend("x264: subprocess restarting")`. Frames are **dropped, never buffered** — a queue here would defeat the whole zero-latency design and re-add the accumulated-latency problem the frame-drop strategy exists to prevent. |
| Give-up | 6th death inside the window → every subsequent call returns `StreamError::Unrecoverable("x264: ffmpeg died 6x in 60s: <last stderr line>")` — carried across the ABI in `AbiError.detail` ([`MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "AbiErr registry") — and the add-on stops spawning. The pipeline then falls through to the SW auto order (`openh264`, and `vt_sw` on macOS). |
| Post-restart correctness | The new child is fed a **fresh IDR**, and the NAL splitter's partial-Annex-B accumulator is **cleared** before the first byte of the new stdout stream — otherwise a half-read NAL from the dead process would be concatenated onto the new SPS and hand the client a corrupt access unit. The timestamp FIFO is cleared with it, so a post-restart access unit is never stamped with a pre-restart capture time. |
| Diagnostics | ffmpeg's stderr (`-loglevel error`) is drained continuously into a small ring buffer (last 8 lines) and logged with each restart, so "died 6x" carries the actual reason rather than just an exit code. A missing/unexecutable `ffmpeg` binary is caught at **`probe()`** time, not at first frame — that is a negative `ProbeReport`, not a crash loop. |
| Hung child (alive, not draining stdin) | Neither a pipe error nor an exit fires, so the two detectors above cannot see it. The bridge therefore never issues a plain blocking write: **stdin is non-blocking and each frame is written under a 250 ms deadline** (`poll(2)`, never `write(2)` on a blocking fd), and each access unit is read under a **500 ms** deadline. Either deadline expiring means the child is hung and counts as ONE death on the ladder above. |
| Kill escalation | `SIGTERM`, then **2 s**, then `SIGKILL`. The escalation timer runs on the reaper task, never on the frame-loop thread. |
| Reaping | The dedicated reaper task owns `Child::wait()`. A respawn does not begin until the previous `wait()` has returned, so no zombie can accumulate across a restart storm, and the `Child` value is dropped only after `wait()` returns. |
| Pipe close order (teardown or restart) | **stdin first**, then drain stdout to EOF with a 500 ms budget, then close stdout and stderr, then `wait()`, then escalate if `wait()` has not returned within 2 s. Closing stdout first makes ffmpeg die on `EPIPE` mid-flush and loses the last access unit. |
| Deliberate restart ≠ crash | A restart the HOST asked for — a `RequiresRestart` parameter change, or `force_keyframe()` — is **not** counted by the circuit breaker. The bridge sets `expecting_exit = true` before the kill; the reaper sees the flag, records the exit as deliberate, and leaves the crash ladder untouched. Only an exit the reaper did not expect, an `EPIPE`/EOF outside a deliberate restart, or a hang deadline advances it. Without this, six client keyframe requests in a minute — an ordinary loss burst on the default `keyframe_interval = 0` — would poison the encoder. |
| Restart-for-IDR is the ONLY IDR mechanism | This add-on cannot force an IDR cheaply: it has no in-band control channel to a running `ffmpeg`, so `force_keyframe()` is a kill + respawn costing one backoff step (~100 ms) and a few dropped frames. `openh264` (the software default) sets a flag on the next `encode` call. This is the load-bearing reason x264 is opt-in and `openh264` is the default. |

**Never:** a flat sleep-and-respawn loop, an unbounded retry count, a per-frame
log line on a dead child, or a blocking write to a dead pipe.

---

## NAL splitter

`-f h264 pipe:1` is an unframed byte stream: a start code marks a NAL boundary,
never an access-unit boundary, so a large IDR read across three pipe reads would
otherwise be handed back as a bare SPS+PPS. The splitter therefore keys on the
**Access Unit Delimiter** the child is configured to emit:

- The accumulator emits one `EncodedUnit` only when it has seen the **next** AUD
  (NAL type 9), or on the child's EOF — never on a start code alone.
- `keyframe` is derived from the presence of a NAL type 5 (IDR slice) inside the
  completed access unit, never from a counter or a request flag.
- The bridge keeps a **FIFO of submitted `timestamp_ns`** and attaches the head
  one to each completed AU. The child holds one or two frames, so without the
  FIFO the pipeline would stamp each access unit with a later frame's capture
  time — by the pipe depth — which is the TD-25 timestamp error reintroduced on
  the software path.
- While the child is holding a frame and no AU is complete, `encode()` returns
  `Ok(None)`. That is the pipelined-encoder meaning of `Ok(None)`
  ([`MODULE_ENCODE.md`](../../../../media/MODULE_ENCODE.md)): never an error and
  never a dropped frame — that frame's access unit is delivered by a later call.
