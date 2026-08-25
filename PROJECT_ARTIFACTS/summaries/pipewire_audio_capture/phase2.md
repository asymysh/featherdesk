# Phase 2: Audio Protocol & Broadcast - Summary

## T4 - Add audio frame type to wire protocol
- **Commit:** 830e3b4
- **Changes:** Added `FrameTypeAudioPCM = 4`. The specced dedicated sub-header (sample_rate/channels/bits/frame_count) was never built — sample rate and channel count are instead crammed into the video `FrameHeader`'s unrelated Width/Height fields; bits/frame_count aren't transmitted at all.
- **Files:** `internal/protocol/protocol.go`
- **Why:** Distinguishes audio frames from video/ping/pong on the shared connection.

## T5 - Implement audio broadcast in server
- **Commit:** 830e3b4
- **Changes:** `BroadcastAudio` fans PCM chunks out to all clients over the same WebSocket connection as video, exactly as specced. Uses `time.Now().UnixMilli()` for the timestamp — the same wall-clock anti-pattern already catalogued as TD-25 for video, now confirmed to apply to audio too.
- **Files:** `internal/server/server.go`, `cmd/server/main.go`
- **Why:** Delivers captured audio to connected browsers.

## T6 - Implement --no-audio flag
- **Commit:** 830e3b4
- **Changes:** `--no-audio` flag skips PipeWire capture entirely; `/status` reports current audio-enabled state.
- **Files:** `cmd/server/main.go`, `internal/server/server.go`
- **Why:** Lets operators disable audio capture/broadcast entirely.
