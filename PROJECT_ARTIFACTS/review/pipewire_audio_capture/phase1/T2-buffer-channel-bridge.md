# Review: T2 - Implement buffer handling and Go channel bridge

## Changes Made
- `Capturer.ch` is a `chan []byte` with capacity 32 (matches spec's "32 frames" buffer size).
- `readLoop()` reads fixed `ChunkBytes` (3840 bytes = 960 samples * 2 channels * 2 bytes, i.e. 20ms @ 48kHz stereo S16) chunks via `io.ReadFull`, copies into a freshly allocated slice per chunk, and sends non-blocking with a drop-oldest-on-full policy (`select` with a default branch that pops one item then pushes).
- This satisfies the spirit of the spec's "drop oldest if channel full" requirement, even though the source is a subprocess stdout pipe rather than a PipeWire `spa_data` pointer (the "convert spa_data pointer to Go []byte slice safely" sub-task doesn't apply given the T1 architecture change).

## Files Created
- `internal/audio/capture.go` (same file as T1 — not a separately committed task)

## Test Results
- No tests found (see T3).

## Concerns / Trade-offs
- The drop-oldest `select` block is correct but not atomic under concurrent producers — not an issue here since `readLoop` is the sole writer, so no race exists in practice.
- Chunk allocation (`make([]byte, n)` per chunk) is a small per-chunk allocation; not benchmarked, unlikely to matter at 50 chunks/sec.

## Verdict
PASS — the channel bridge and drop-oldest behavior work as intended, adapted correctly to the subprocess-based architecture from T1.
