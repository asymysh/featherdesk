# Branch: `feature-benchmark`

## Purpose

This branch is the home for **FeatherDesk Benchmark** — a standalone application that probes a machine's hardware capabilities, runs timed encode/capture tests across every available codec and encoder, and produces a machine-readable recommendation file that the main FeatherDesk server can consume to auto-select the best configuration.

The benchmark is designed to run **once on first install** (or manually on demand). It may take 30–60 minutes on slower hardware. That runtime is acceptable because better profiling = better default configuration = faster streaming for the life of the deployment.

---

## Why This Exists

FeatherDesk must support a wide codec matrix:

| Codec | Software | Intel (VA-API / QSV) | AMD (AMF / VA-API) | NVIDIA (NVENC) |
|-------|----------|----------------------|-------------------|----------------|
| H.264 | OpenH264, libx264 | ✅ h264_vaapi / h264_qsv | h264_amf / h264_vaapi | h264_nvenc |
| H.265 | libx265 | hevc_vaapi / hevc_qsv | hevc_amf / hevc_vaapi | hevc_nvenc |
| VP8 | libvpx | — | — | — |
| VP9 | libvpx-vp9 | vp9_vaapi | — | — |
| AV1 | libaom, SVT-AV1 | av1_vaapi | av1_amf | av1_nvenc |

No single codec is best on every machine. The correct choice depends on:
- Which hardware encoders are physically present and functional
- Driver quality and actual observed latency (probed differs from claimed)
- Whether the machine is headless, has integrated graphics, or has a discrete GPU
- Available CPU cores and memory bandwidth for software fallbacks

Rather than hardcoding a priority list that may perform poorly on a specific machine, the benchmark measures **actual throughput and latency** on the target hardware and writes the result as a ranked recommendation.

---

## What the Benchmark Does

### Phase 1 — Hardware Discovery (fast, ~30 seconds)

- Enumerate DRM cards (`/dev/dri/card*`) and render nodes (`/dev/dri/renderD*`)
- Probe VA-API: query `vaQueryConfigEntrypoints` for all profiles across all render nodes
- Probe NVENC: attempt `cuInit` / `NvEncOpenEncodeSessionEx` (Linux + Windows)
- Probe AMF: attempt `AMFCreateContext` (Windows primary, Linux via ROCm)
- Probe Intel QSV: attempt `MFXInitEx` or `VPLInitExe` (Windows)
- Probe ffmpeg: run `ffmpeg -encoders` and parse the encoder list
- Probe software: assume always available; detect SIMD level (SSE4.2, AVX2, AVX-512)
- Probe capture: determine which capture backends are available (KMS, DXGI, etc.)

Output: `discovery.json` — a machine-readable inventory of what is available.

### Phase 2 — Capture Benchmark (~5 minutes)

For each available capture backend:
- Capture 300 frames (10 seconds at 30fps, or 5 seconds at 60fps)
- Measure: per-frame latency (p50, p95, p99), throughput (fps), CPU usage
- Test at 1080p and native resolution

Output: ranked capture backends by latency.

### Phase 3 — Encode Benchmark (~20–50 minutes)

For each available encoder × codec combination:
- Encode 300 frames of reference content (synthetic gradient + motion, representative of desktop use)
- Test configurations: QP 20, QP 26, QP 32 (or equivalent CRF/bitrate targets)
- Measure per-frame:
  - **Latency** (p50, p95, p99, max) — critical for real-time streaming
  - **CPU usage** during encode
  - **GPU usage** during encode (where measurable)
  - **Output size** (bytes/frame, bitrate at given QP)
- Optionally measure **quality** (SSIM against reference) — slower but valuable

Codec × QP combinations tested:
```
h264_openh264  qp=20,26,32
h264_libx264   qp=20,26,32   (if ffmpeg available)
h264_vaapi     qp=20,26,32   (if Intel VA-API available)
h265_libx265   qp=20,26,32   (if libx265 available)
h265_vaapi     qp=20,26,32   (if HEVC VA-API available)
vp8_libvpx     crf=20,26,32
vp9_libvpx     crf=20,26,32
av1_libaom     crf=20,26,32  (warning: slow)
av1_svtav1     qp=20,26,32   (if SVT-AV1 available)
av1_vaapi      qp=20,26,32   (if AV1 VA-API available)
```

### Phase 4 — Recommendation Engine

Scores each encoder using a weighted formula:
```
score = (1/p95_latency_ms) × 0.50
      + quality_ssim        × 0.25
      + (1/cpu_usage_pct)   × 0.15
      + (1/bytes_per_frame) × 0.10
```

Produces a ranked list per resolution and outputs the top pick for each quality tier:
- **Streaming tier** (priority: latency)
- **Quality tier** (priority: SSIM per bit)
- **CPU-light tier** (priority: CPU usage)

---

## Output File Format

The benchmark writes `.featherdesk-bench.json` to the user's config directory:

```json
{
  "version": 1,
  "generated_at": "2026-06-22T10:00:00Z",
  "machine_id": "sha256:...",
  "hardware": {
    "cpu": "Intel Core i7-7700K",
    "gpus": [{ "name": "Intel HD 630", "vaapi_render_node": "/dev/dri/renderD128" }]
  },
  "recommendations": {
    "default": {
      "encoder": "h264_vaapi",
      "codec": "h264",
      "render_node": "/dev/dri/renderD128",
      "qp": 26,
      "reason": "Lowest p95 latency (2.1ms) with acceptable quality (SSIM 0.94)"
    },
    "software_fallback": {
      "encoder": "h264_openh264",
      "codec": "h264",
      "qp": 26,
      "reason": "Best software encoder: 7.2ms p95, in-process CGo, no subprocess"
    },
    "high_quality": {
      "encoder": "h265_vaapi",
      "codec": "h265",
      "qp": 22,
      "reason": "Best SSIM per bit when latency target is relaxed"
    }
  },
  "full_results": [ ... ]
}
```

The main FeatherDesk server reads this file at startup and uses it to auto-select encoders, falling back to defaults if the file is absent.

---

## Architecture

```
cmd/
└── benchmark/
    └── main.go            # CLI entry point: runs phases, writes output

internal/
└── benchmark/
    ├── discovery.go       # Hardware probing (VA-API, NVENC, AMF, QSV, ffmpeg)
    ├── capture_bench.go   # Phase 2: capture backend timing
    ├── encode_bench.go    # Phase 3: encoder × codec matrix
    ├── reference.go       # Synthetic reference frame generator
    ├── quality.go         # SSIM calculation (optional)
    ├── score.go           # Recommendation scoring formula
    └── report.go          # JSON + markdown output writer
```

The benchmark binary is independent of the main server binary. It can be distributed separately or bundled as a sub-command: `viewport-rds benchmark`.

---

## Current Branch Status

This branch contains only this documentation. No code has been written yet.

**Next steps for whoever picks this up:**

1. Read `specs/CENTRAL_SPEC.md` and `specs/MODULE_ENCODE.md` in the `featherdesk-refactor` branch for the encoder interface contracts the benchmark must exercise.
2. Start with `internal/benchmark/discovery.go` — build the probing layer first; nothing else can proceed without knowing what hardware is available.
3. Use the existing `internal/encode/` implementations as the basis for the encode benchmark — they already wrap the actual encoders.
4. The encode benchmark must test against the SAME `Encoder` interface used by the server so results are directly transferable.

---

## Related Branches

| Branch | Purpose |
|--------|---------|
| `feature-libav-vp8s8` | Main working codebase |
| `featherdesk-refactor` | Modular refactor specs (10-module redesign) |
| `feature-benchmark` | **This branch** — GPU benchmarking + encoder recommendation tool |
