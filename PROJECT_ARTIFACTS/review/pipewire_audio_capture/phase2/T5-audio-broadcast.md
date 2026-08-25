# Review: T5 - Implement audio broadcast in server

## Changes Made
- `Server.BroadcastAudio(pcm []byte, timestamp uint64)` (internal/server/server.go, commit 830e3b4) wraps the PCM chunk in a `FrameHeader` and sends it to every connected client via the same `clients.Range` fan-out used for video — audio and video genuinely share the single WebSocket connection per client, matching the spec exactly.
- Called from `main.go`'s audio goroutine: `for chunk := range audioCap.Chunks() { srv.BroadcastAudio(chunk, uint64(time.Now().UnixMilli())) }`.

## Files Created
- Modified: `internal/server/server.go`, `cmd/server/main.go`

## Test Results
No dedicated tests for `BroadcastAudio`; it is a thin wrapper around the already-tested `MarshalHeader`/client-send path used by video.

## Concerns / Trade-offs
- Timestamp uses `time.Now().UnixMilli()` (wall-clock ms) — same A/V-sync anti-pattern already catalogued as **TD-25** in `specs/CENTRAL_SPEC.md` for the video path. This confirms TD-25 applies equally to the audio path, which the existing TD catalog entry doesn't call out explicitly (it cites `main.go` + `x11grab.go`, not the audio broadcast path).

## Verdict
PASS — broadcast works as specced (shared connection, fan-out to all clients); the timestamp mechanism has the same known TD-25 defect as the video path.
