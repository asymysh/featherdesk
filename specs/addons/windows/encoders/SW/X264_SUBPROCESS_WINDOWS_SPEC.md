# Software Encoder Add-On: x264 (Subprocess, GPL-Isolated)

## Purpose

libx264 H.264 software encoder running as a **separate subprocess** to maintain
GPL isolation from the proprietary main binary. The fastest software H.264
encoder available — roughly 2x faster than OpenH264 at equivalent quality.

> **Opt-in.** This add-on is never auto-selected; it is reached only by
> `[encode] force_addon = "x264"`. The software default is `openh264`. Its only
> mechanism for an on-demand IDR is killing and respawning the `ffmpeg` child —
> see MODULE_ENCODE "Software encoder order" and "Crash Recovery (subprocess
> death)" below. It is the right choice for a deployment that has *measured*
> itself CPU-bound, and the wrong default for everyone else.

The subprocess model: the main `featherdesk` binary (proprietary) spawns a
small GPL-licensed encoder process. Communication via stdin/stdout pipe (raw
planar YUV frames in, H.264 NALs out). Only the encoder subprocess is GPL; the
main binary never links libx264.

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

## Performance (Benchmarked)

Measured on AMD Ryzen 9 5900X (12C/24T), Windows 11, synthetic I420 frames.

### 1920x1080

| Config | Threads | FPS | ms/frame | vs OpenH264 |
|--------|---------|-----|---------|-------------|
| **x264 ultrafast zerolatency** | 12 | **302** | **3.3ms** | **2.2x faster** |
| x264 ultrafast zerolatency | 8 | 294 | 3.4ms | 2.2x faster |
| x264 ultrafast zerolatency | 4 | 234 | 4.3ms | 1.9x faster |
| x264 superfast zerolatency | 4 | 150 | 6.7ms | 1.2x faster |
| x264 ultrafast zerolatency | 1 | 94 | 10.6ms | 0.7x (slower) |
| OpenH264 4-slice (reference) | 4 | 125 | 7.9ms | baseline |

### 2560x1440

| Config | Threads | FPS | ms/frame | vs OpenH264 |
|--------|---------|-----|---------|-------------|
| **x264 ultrafast zerolatency** | 12 | **181** | **5.5ms** | **2.4x faster** |
| x264 ultrafast zerolatency | 4 | 127 | 7.9ms | 1.7x faster |
| OpenH264 4-slice (reference) | 4 | 73 | 13.7ms | baseline |

### Pipe overhead

| Metric | Value |
|--------|-------|
| ffmpeg subprocess startup | ~50ms (one-time, amortized) |
| Per-frame pipe I/O (I420 1080p = 3.1MB) | <1ms |
| `force_keyframe()` (kill + respawn) | ~100 ms and a few dropped frames — **the only IDR mechanism this bridge has** |
| Total subprocess overhead vs hypothetical Rust FFI direct | ~1-2ms |

The subprocess model adds ~1-2ms over what a hypothetical direct Rust FFI binding
would achieve. At 3.3ms total on 12 threads, this is still faster than
OpenH264's 7.4ms on the same threads — which is the whole and only reason the
add-on exists.

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

The ffmpeg process is started once when the x264 encoder is forced and persists
for the entire streaming session. Frames are written to stdin as raw planar YUV
in the negotiated subsampling (`yuv420p` / `yuv422p` / `yuv444p`); NALs are read
from stdout in Annex B format and split into access units by the NAL splitter
below.

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
(NAL type 9) at every AU boundary. It is **mandatory**, not a tuning choice: it
is the only thing that lets the splitter tell an AU boundary from a NAL boundary
on an unframed byte stream (see "NAL splitter").

Parameters controlled by the Rust bridge:

| Parameter | Source | Default |
|-----------|--------|---------|
| Preset | `[addon_module_x264] preset` (static, read once at startup) | `ultrafast` |
| CRF | `stream::Params.qp` (dynamic — delivered per `update_stream_params`, never read from config by this add-on) | 26 (from `[stream] qp`) |
| Threads | `[addon_module_x264] threads`; `0` = auto (`std::thread::available_parallelism()`, capped at 12) | `0` |
| ffmpeg binary | `[addon_module_x264] ffmpeg_path`; `""` = auto-discover | `""` |
| Tune | hardcoded | `zerolatency` (mandatory for streaming) |
| Profile | `[addon_module_x264] profile` — `"high"` \| `"high422"` \| `"high444"`; the chroma negotiation selects among them, this key is the ceiling | `high` |

---

## Build & Distribution

### Build (shared library)

```bash
cargo build --release -p featherdesk-addon-x264   # cdylib  featherdesk-addon-x264.dll
```

The `x264` add-on cdylib contains the Rust bridge code that spawns the
subprocess. The x264 bridge is pure Rust (`std::process::Command` + pipes); the
cdylib exports the abi_stable `FeatherDeskAddonOpen` entry point.

### Runtime dependency

ffmpeg must be in PATH or at a known location. The bridge searches:
1. `[addon_module_x264] ffmpeg_path`, when it is not `""`
2. `ffmpeg` in PATH
3. `./ffmpeg.exe` next to the binary (bundled deployment)

A configured `ffmpeg_path` that does not exist or does not execute fails
`construct()` with `AbiErr::BadConfig`, naming the key — it is never silently
skipped in favour of PATH.

### Distribution options

| Option | How |
|--------|-----|
| User installs ffmpeg | `winget install Gyan.FFmpeg` |
| Bundle ffmpeg binary | Ship `ffmpeg.exe` alongside `featherdesk.exe` in the installer |
| Docker | `FROM rust:1.83 AS build` + `apt install ffmpeg` in runtime stage |

---

## File Structure

```
addons/x264/
├── src/
│   ├── lib.rs           // Rust bridge: subprocess management, pipe I/O
│   └── nal_split.rs     // H.264 NAL unit splitting from pipe stream
└── tests.rs             // Integration test (requires ffmpeg)
```

No conditional-compilation stub file is needed — the add-on is its own cdylib
crate; an absent add-on is simply a `.dll` that isn't in the add-ons directory.

---

## Codec Output

- Profile: High (`-profile:v high`); `ultrafast` disables CABAC, which High
  permits. Reported as `VideoProfile::H264High`, so `codec()` returns
  `avc1.6400LL` with `LL` computed from the active geometry per
  [`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Codec-string
  computation". 4:2:2 and 4:4:4 sessions use `-pix_fmt yuv422p` / `yuv444p` with
  `-profile:v high422` / `high444` and report `H264High422` / `H264High444`.
- Chroma: accepts `Subsampling::I420`, `I422` and `I444` — whichever the bridge
  spawned ffmpeg for. A `YuvFrame` whose `subsampling` differs from the running
  child's `-pix_fmt` returns `stream::StreamError::ChromaUnsupported` rather than
  writing mis-sized planes into the pipe.
- Colour: `-color_primaries bt709 -color_trc bt709 -colorspace bt709
  -color_range tv`, so the SPS VUI carries `colour_primaries = 1`,
  `transfer_characteristics = 1`, `matrix_coefficients = 1`,
  `video_full_range_flag = 0` (see MODULE_ENCODE "Colour signalling"). Not a
  knob; a stream without the VUI is non-conforming.
- Entropy: CAVLC (ultrafast) or CABAC (superfast+)
- Slices: auto (x264 decides based on thread count)
- B-frames: 0 (zerolatency tune)
- IDR: on-demand via `force_keyframe()` → kill + restart ffmpeg (the only
  mechanism this bridge has; ~100 ms and a few dropped frames — see "Crash
  Recovery"), or `-x264opts keyint=N`. There is no cheaper path, which is why
  this add-on is opt-in.
- NAL format: Annex B (start codes retained) — matches our wire protocol
- Access units: delimited by the AUD the child is configured to emit — see
  "NAL splitter"

### NAL splitter

`-f h264 pipe:1` is an unframed byte stream, so the bridge accumulates and emits
one `EncodedUnit` per **access unit**, never per NAL:

- The accumulator emits when it reads the **next** AUD (NAL type 9), or on the
  child's EOF. A start code alone is not a boundary — it cannot distinguish a NAL
  boundary from an AU boundary, which is how a large IDR read across three pipe
  reads becomes a bare SPS+PPS with an invented `keyframe` flag.
- `keyframe` is derived from the presence of a NAL type 5 in the completed AU.
- The bridge keeps a **FIFO of submitted `timestamp_ns`** and attaches the head
  one to each completed AU. The child holds one or two frames, so the AU handed
  back from `encode(frame_N)` is generally an earlier frame's; without the FIFO
  the pipeline would stamp it with frame N's capture time, off by the pipe depth.
- `encode()` returns `Ok(None)` while no complete access unit has emerged. That
  is not an error and not a dropped frame — this encoder is pipelined, and the
  frame's access unit is delivered by a later call.

---

## When to use this add-on

Force it with `[encode] force_addon = "x264"` when **all** of these hold:
- No GPU hardware encoder is available (headless CPU-only server)
- The host has been **measured** CPU-bound on the software path (3.3ms vs
  OpenH264's 7.4ms at 1080p/12T)
- GPL in the deployment is acceptable (ffmpeg is already GPL)
- Multi-core CPU available (scales well to 12+ threads)
- Forced keyframes are rare — few client joins, a low-loss link. Each one is a
  process respawn.

Leave OpenH264 as the default when:
- Any of the above is untrue, or has not been measured
- GPL is not acceptable in the deployment
- Single-core / low-thread environments (OpenH264 4-slice is more efficient below 4 threads)
- ffmpeg is not available or installable
- Clients join and leave often, or the link is lossy — each forced IDR costs a
  respawn here and one flag in OpenH264

---

## Comparison with other SW encoders

| Encoder | 1080p ms | License | Ship? |
|---------|---------|---------|-------|
| **x264 ultrafast 12T** | **3.3ms** | GPL | ✅ subprocess |
| OpenH264 4T | 7.9ms | BSD | ✅ Rust FFI direct (the SW default) |
| VP9 speed8 4T | 9.0ms | BSD | ⚠️ marginal |
| x265 ultrafast 4T | 9.8ms | GPL | ❌ slower than OpenH264 |
| SVT-AV1 p12 12T | 13.2ms | BSD | ❌ too slow |

---

## Status

✅ **Benchmarked.** Subprocess batch encoding measured at 3.3ms P50 @ 1080p
(12 threads) on Ryzen 9 5900X. Persistent subprocess bridge (single ffmpeg
process per session) to be implemented. Repo split (GPL subprocess → separate
repo) deferred.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_x264]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

This add-on does **not** serve `encode::ConfigurableEncoder`, and `probe()` reports `codecs: [CodecId::H264]` with **`AddonCaps::ENC_CONFIGURABLE` clear** (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). There is no in-band control channel to a running `ffmpeg`, so there is no parameter this bridge can change without a rebuild, and claiming the bit would be a capability lie. The host therefore takes the non-optional path for every parameter change: tear the encoder down and rebuild it. For this add-on a rebuild is a respawn of the child with new flags.

`probe()` follows the one signature every add-on uses ([`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Root module surface"). Availability is not an error: no ffmpeg binary is `ROk(ProbeReport { available: false, reason: "ffmpeg not found in [addon_module_x264] ffmpeg_path, PATH, or beside the executable" })`, never an `RErr`. Set every capability bit the add-on actually serves, and only claim what the probe can prove — a bit claimed here and refused later is a capability lie.

| Param change | Mechanism | Hot? |
|--------------|-----------|------|
| Any of `Width`/`Height`/`FPS`/`BitrateBps`/`QP`/`KeyframeInterval` | Rebuild: the pipeline tears the encoder down and reconstructs it, and the bridge respawns ffmpeg with new `-s WxH -r FPS -crf QP -g KI` (CRF mode) or `-s WxH -r FPS -b:v B -g KI` (bitrate mode). `-crf` and `-b:v` are mutually exclusive. The bridge sets `expecting_exit` first, so the respawn is not counted by the crash ladder. | no |
| `ChromaSubsampling` | Rebuild, with `-pix_fmt yuv420p\|yuv422p\|yuv444p` and the matching `-profile:v high\|high422\|high444`, capped by `[addon_module_x264] profile` | no |
| `BitDepth=10` / `HDR=true` | rejected with `stream::StreamError::HdrUnsupported` -- there is no H.264 HDR profile in the WebCodecs set, and this bridge emits H.264 only. An HDR session runs on a HW encoder's HEVC Main10; `degrade_to_software` leaves HDR before it ever builds this path | n/a |

**Restart semantics:** the bridge forces an IDR on the first frame from the new
ffmpeg instance so the client decoder picks up the new SPS/PPS cleanly — which is
why `apply_params` does NOT additionally call `force_keyframe()` after a rebuild
(MODULE_PIPELINE `apply_params`): doing so would respawn ffmpeg a second time for
one parameter change.

---

## Crash Recovery (subprocess death)

The child is a separate process, so the bridge owns its liveness. Three detectors
and one circuit breaker; every row below counts as at most one death, and six
deaths inside 60 s return `stream::StreamError::Unrecoverable` — whose `detail`
carries the last ffmpeg stderr line across the ABI in `AbiError.detail`, so the
host can log *why* rather than "the add-on gave up". The pipeline then poisons
this add-on for the session and falls through to the auto SW order (`openh264`).

| Situation | Behavior |
|-----------|----------|
| Child exits | The reaper task observes the exit status; the bridge respawns with exponential backoff (100 ms, doubling, capped at 2 s). Counts as one death. |
| Broken pipe (`EPIPE` on stdin, EOF on stdout) | Same ladder as an exit. Counts as one death. |
| Hung child (alive, not draining stdin) | Neither a pipe error nor an exit fires, so the two detectors above cannot see it. The bridge therefore never issues a plain blocking write: **stdin is non-blocking and each frame is written under a 250 ms deadline** (`WriteFile` with an event on Windows, never a blocking write), and each access unit is read under a **500 ms** deadline. Either deadline expiring means the child is hung and counts as ONE death on the ladder above. |
| Kill escalation | `TerminateProcess` immediately — there is no console to signal on Windows, so there is no graceful stage. The escalation timer runs on the reaper task, never on the frame-loop thread. |
| Reaping | The dedicated reaper task owns `Child::wait()`. A respawn does not begin until the previous `wait()` has returned, so no zombie handle can accumulate across a restart storm, and the `Child` value is dropped only after `wait()` returns. |
| Pipe close order (teardown or restart) | **stdin first**, then drain stdout to EOF with a 500 ms budget, then close stdout and stderr, then `wait()`, then `TerminateProcess` if `wait()` has not returned within 2 s. Closing stdout first makes ffmpeg die mid-flush and loses the last access unit. |
| Deliberate restart ≠ crash | A restart the HOST asked for — a `RequiresRestart` parameter change, or `force_keyframe()` — is **not** counted by the circuit breaker. The bridge sets `expecting_exit = true` before the kill; the reaper sees the flag, records the exit as deliberate, and leaves the crash ladder untouched. Only an exit the reaper did not expect, an `EPIPE`/EOF outside a deliberate restart, or a hang deadline advances it. Without this, six client keyframe requests in a minute — an ordinary loss burst on the default `keyframe_interval = 0` — would poison the encoder. |
| Restart-for-IDR is the ONLY IDR mechanism | This add-on cannot force an IDR cheaply: it has no in-band control channel to a running `ffmpeg`, so `force_keyframe()` is a kill + respawn costing one backoff step (~100 ms) and a few dropped frames. `openh264` (the software default) sets a flag on the next `encode` call. This is the load-bearing reason x264 is opt-in and `openh264` is the default. |
| Across every restart | The NAL splitter's partial-Annex-B accumulator is **cleared**, and the timestamp FIFO is cleared with it, so a post-restart access unit is never stamped with a pre-restart capture time. |

The bridge keeps the child's last 16 stderr lines in a ring buffer; the most
recent one is what travels in `AbiError.detail`.
