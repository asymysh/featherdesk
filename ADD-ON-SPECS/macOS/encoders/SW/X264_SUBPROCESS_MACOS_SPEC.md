# Software Encoder Add-On: x264 (Subprocess, GPL-Isolated)

## Purpose

libx264 H.264 software encoder running as a **separate subprocess** to maintain
GPL isolation from the proprietary main binary. The fastest software H.264
encoder available — 2x faster than OpenH264 at equivalent quality.

The subprocess model: the main `viewport-rds` binary (proprietary) spawns a
small GPL-licensed encoder process. Communication via stdin/stdout pipe (raw
I420 frames in, H.264 NALs out). Only the encoder subprocess is GPL; the main
binary never links libx264.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| libx264 | GPL-2.0+ | Encoder library |
| ffmpeg (as subprocess) | GPL-2.0+ | Used as the subprocess wrapper; already links libx264 |
| Our Go bridge code | Proprietary | Spawns subprocess, pipes frames, reads NALs |

**GPL isolation:** The main `viewport-rds` binary is proprietary. The x264
subprocess is a separate program (ffmpeg) distributed under GPL. This is the
same pattern used by VLC, commercial streaming products, and any proprietary
app that shells out to ffmpeg.

**What we publish:** The Go bridge code that spawns the subprocess is trivial
and proprietary. ffmpeg is distributed separately under its own GPL license.
Users install ffmpeg independently or we bundle it as a separate binary.

---

## Performance (Benchmarked — Windows reference, macOS native pending)

Measured on AMD Ryzen 9 5900X (12C/24T), Windows 11, synthetic I420 frames.
The x264 subprocess mechanism is platform-agnostic — the same ffmpeg
subprocess code runs identically on macOS. macOS native benchmarks pending
(Apple Silicon may show different thread scaling due to ARM micro-architecture).

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
| Total subprocess overhead vs hypothetical CGo direct | ~1-2ms |

The subprocess model adds ~1-2ms over what a hypothetical direct CGo binding
would achieve. At 3.3ms total, this is still faster than OpenH264's 7.4ms
in-process.

---

## Architecture

```
viewport-rds (proprietary)                    ffmpeg (GPL)
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
  -c:v libx264 -preset ultrafast -tune zerolatency \
  -crf 26 -threads 12 \
  -f h264 pipe:1
```

Parameters controlled by the Go bridge via config:

| Parameter | Config key | Default |
|-----------|-----------|---------|
| Preset | `encode.x264_preset` | `ultrafast` |
| CRF | `encode.qp` | 26 |
| Threads | auto (runtime.NumCPU) | all cores |
| Tune | hardcoded | `zerolatency` (mandatory for streaming) |

---

## Build & Distribution

### Build tag

```bash
go build -tags x264 -o viewport-rds ./cmd/server
```

The `x264` build tag compiles in the Go bridge code that spawns the subprocess.
No C compilation needed — the bridge is pure Go (os/exec + io.Pipe).

### Runtime dependency

ffmpeg must be in PATH or at a known location. The bridge searches:
1. `VIEWPORT_FFMPEG_PATH` environment variable
2. `ffmpeg` in PATH
3. `./ffmpeg` next to the binary (inside the .app bundle's Resources)

### Distribution options

| Option | How |
|--------|-----|
| User installs ffmpeg | `brew install ffmpeg` |
| Bundle ffmpeg binary | Ship `ffmpeg` inside the .app bundle's Resources directory |
| Docker | `FROM golang:1.26 AS build` + `apt install ffmpeg` in runtime stage |

---

## File Structure

```
internal/encode/x264/
├── x264.go              // Go bridge: subprocess management, pipe I/O
├── x264_stub.go         // No-op stub (build tag: !x264)
├── nal_split.go         // H.264 NAL unit splitting from pipe stream
└── x264_test.go         // Integration test (requires ffmpeg)
```

---

## Codec Output

- Profile: Constrained Baseline (ultrafast preset)
- Entropy: CAVLC (ultrafast) or CABAC (superfast+)
- Slices: auto (x264 decides based on thread count)
- B-frames: 0 (zerolatency tune)
- IDR: on-demand via `ForceKeyframe()` → kill + restart ffmpeg, or `-x264opts keyint=N`
- NAL format: Annex B (start codes retained) — matches our wire protocol

---

## When to use this add-on

Use when:
- No GPU hardware encoder available (headless CPU-only server)
- Want the fastest possible SW encode (3.3ms vs OpenH264's 7.4ms)
- GPL in the deployment is acceptable (ffmpeg is already GPL)
- Multi-core CPU available (scales well to 12+ threads)

Use OpenH264 instead when:
- GPL is not acceptable in the deployment
- Single-core / low-thread environments (OpenH264 4-slice is more efficient below 4 threads)
- ffmpeg is not available or installable

---

## Comparison with other SW encoders

| Encoder | 1080p ms | License | Ship? |
|---------|---------|---------|-------|
| **x264 ultrafast 12T** | **3.3ms** | GPL | ✅ subprocess |
| OpenH264 4T | 7.4ms | BSD | ✅ CGo direct |
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
of the TOML config (see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is compiled into the binary; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

This add-on implements `stream.ConfigurableEncoder` (see [`../../../../specs/MODULE_STREAM_PARAMS.md`](../../../../specs/MODULE_STREAM_PARAMS.md)). The x264 subprocess bridge does **not** support hot reconfiguration -- ALL parameter changes restart the ffmpeg child process.

| Param change | Mechanism | Hot? |
|--------------|-----------|------|
| Any of `Width`/`Height`/`FPS`/`BitrateBps`/`QP`/`KeyframeInterval` | `UpdateStreamParams` returns `stream.ErrRequiresRestart` -- pipeline tears down + respawns ffmpeg with new `-s WxH -r FPS -crf QP -g KI` (CRF mode) or `-s WxH -r FPS -b:v B -g KI` (bitrate mode). `-crf` and `-b:v` are mutually exclusive. | no |
| `BitDepth=10` / `HDR=true` | rejected with `stream.ErrHDRUnsupported` -- H.264 HDR profile not in WebCodecs spec | n/a |

**Restart semantics:** the bridge forces an IDR on the first frame from the new ffmpeg instance so the client decoder picks up the new SPS/PPS cleanly.
