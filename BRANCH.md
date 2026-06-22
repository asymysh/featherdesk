# Branch: `featherdesk-refactor`

## Purpose

This branch is the **architectural refactor** of FeatherDesk. The working codebase on `feature-libav-vp8s8` has all five original tracks implemented and running, but accumulated several structural problems during iterative development. This branch contains the full modular redesign spec before any refactor code is written.

**No production code changes are in this branch yet.** It is a spec-first branch — implementation follows once the specs are reviewed and finalized.

---

## Why the Refactor Is Needed

The current codebase has these confirmed issues (tracked as TD-01 through TD-34 in `specs/CENTRAL_SPEC.md`):

**High severity (bugs, crashes, data loss):**
- `kms.go` — DMA-BUF file descriptor leak on EGL import failure (TD-01)
- `x11grab.go` — recursive `NextFrame()` retry has no depth limit; stack overflow on sustained failure (TD-02)
- `egl.go` — static C globals mean EGL state is not thread-safe (TD-03)
- `ffmpeg.go` — `ForceKeyframe()` stores a flag but never signals ffmpeg; IDR never actually forced (TD-04)
- `server.go` — broadcasts one WebSocket message **per NAL unit**, not per frame; multi-NAL H.264 frames produce partial access units that break WebCodecs (TD-23)
- `server.go` — IDR cache stores only the IDR NAL; SPS and PPS (sent as separate messages) are lost; new clients receive an undecodable keyframe (TD-24)
- `main.go` + `x11grab.go` — video and audio timestamps use `time.Now().UnixMilli()` (wall-clock, milliseconds); A/V sync is broken by design (TD-25)
- `main.go` — new-client handler forces a keyframe AND restarts the capture subprocess; disrupts all existing viewers on every join (TD-26)

**Medium severity (architectural gaps):**
- All modules are tightly coupled through `main.go`; no module boundaries or interfaces
- No protocol versioning; no connection handshake; client must guess codec
- VP8 is the default encoder despite OpenH264 being planned (VP8 was switched in to avoid SPS/PPS complexity, which is now properly handled in the new spec)
- GPU→CPU→GPU round-trip on the hardware encode path wastes ~36MB/frame in memory bandwidth
- No frame-drop strategy; latency accumulates unboundedly under load
- No orchestrator spec; wiring logic lives inline in `main.go`

---

## What This Branch Contains

```
specs/
├── CENTRAL_SPEC.md          # Master architecture doc: all module contracts, dependency
│                            # graph, encoding paths, cross-cutting concerns, TD catalog
├── MODULE_PIPELINE.md       # Orchestrator: lifecycle, frame loop, pacing, drop logic
├── MODULE_CAPTURE.md        # Screen capture: KMS/DRM/EGL, X11grab, PipeWire
├── MODULE_ENCODE.md         # Software encoding: OpenH264, VP8, FFmpeg libx264
├── MODULE_HARDWARE_ENCODE.md # Zero-copy hardware: VA-API DMA-BUF direct (no CPU copy)
├── MODULE_PROTOCOL.md       # Wire protocol: 22-byte header, versioning, A/V sync
├── MODULE_SERVER.md         # WebSocket server: per-frame broadcast, IDR cache, lifecycle
├── MODULE_CLIENT.md         # Browser viewer: WebCodecs, AudioWorklet, cursor, input
├── MODULE_INPUT.md          # Input injection: uinput, keymap, InputAck latency
├── MODULE_AUDIO.md          # Audio capture: PipeWire, AudioChunk with capture timestamp
└── MODULE_LOGGER.md         # Logging: interface, UTC timestamps, thread safety
```

---

## Key Design Decisions (recorded here for future implementors)

| Decision | Choice | Reason |
|----------|--------|--------|
| NAL framing | Annex B per-frame (start codes retained) | WebCodecs consumes whole access units; length-prefix adds AVCC complexity for no benefit |
| Default software encoder | OpenH264 (H.264 Baseline) | Better browser compat, in-process CGo (no subprocess), better quality/bit than VP8 |
| Software fallback order | OpenH264 → VP8 → FFmpeg libx264 | VP8 is fallback, not default |
| Hardware path | Zero-copy DMA-BUF → VA-API (no glReadPixels) | Eliminates ~36MB/frame GPU↔CPU copies |
| Encoder tiers | Two only: hardware OR software (no ffmpeg-vaapi middle tier) | Clean separation; ffmpeg-vaapi still did CPU copies |
| A/V sync clock | CLOCK_MONOTONIC nanoseconds, stamped at capture | Single epoch across all capturers and audio; wall-clock ms is broken |
| Frame drop | Skip-before-capture, 5 FPS minimum floor | Stay realtime even at 1 FPS rather than accumulate latency |
| Cursor (hardware path) | Client-side overlay via CursorUpdate frames | Frame never touches CPU on zero-copy path; cursor moves without waiting for video frame |
| Protocol versioning | Version byte (byte 0) + Config handshake | Handshake sends full WebCodecs codec string; client never hardcodes codec |
| Input latency | InputMessage carries `seq`; server echoes in InputAck | Enables measured round-trip latency display |

---

## How to Use These Specs

Each spec file is self-contained with:
- **Public interface** — Go interface definition the module must implement
- **Internal architecture** — data flow, components, system interactions
- **Refactoring directives** — numbered `R-XXX-NN` items, each a concrete task
- **Testing strategy** — unit/integration/benchmark breakdown
- **Performance targets** — measurable latency/CPU/memory goals

**For an agent starting implementation:**

1. Read `CENTRAL_SPEC.md` first — it defines all cross-module contracts and the dependency graph.
2. Pick a leaf module (Logger, Protocol, or Input — they have no internal dependencies).
3. Implement the module's `pkg/` interface and `internal/` implementation.
4. Write tests against the interface, not the implementation.
5. The Pipeline module is last — it wires everything together.

**Codec expansion (minimum required set):**  
The refactor must support at minimum: H.264, H.265 (libx265 + hardware HEVC), VP8, VP9, AV1.  
Encoder selection is driven by the `feature-benchmark` output file (`.featherdesk-bench.json`) when present.

---

## Relationship to Other Branches

| Branch | Purpose |
|--------|---------|
| `feature-libav-vp8s8` | Main working codebase (all 5 tracks implemented) |
| `featherdesk-refactor` | **This branch** — spec-first redesign, no code yet |
| `feature-benchmark` | Standalone GPU benchmark tool; its output drives encoder auto-selection |

The refactored code should eventually replace `feature-libav-vp8s8` as the main branch. The benchmark tool (`feature-benchmark`) integrates with the refactored codebase, not the current one.

---

## Current Status

| | |
|-|-|
| Specs written | ✅ All 11 modules |
| Specs reviewed | ✅ Two full review passes (all 34 issues addressed) |
| Code written | ❌ None yet |
| Tests written | ❌ None yet |

The specs went through two rigorous review rounds: all cross-module contracts were verified against the actual source code, confirmed source-level bugs were incorporated, and four architectural decisions were made with the project owner.
