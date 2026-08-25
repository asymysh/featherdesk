# Phase 3: WebSocket Integration - Summary

## T6 - Implement JSON input message parsing
- **Commit:** d5d6d9b
- **Changes:** `InputMessage`/`ParseMessage`/`HandleMessage` in `protocol.go`, dispatching key/mousemove/mousedown/mouseup/wheel; wired into `main.go` via `SetInputCallback`, non-fatal if `/dev/uinput` is missing. Wire shape is flat (`type:"mousemove"`) rather than the nested shape `spec.md` documents (`type:"mouse",event:"move"`) — client and server agree with each other, so this is a stale-doc issue, not a functional one.
- **Files:** `internal/input/protocol.go`, `cmd/server/main.go`
- **Why:** Routes incoming WebSocket text frames to the correct injection call.

## T7 - Implement input ACK for latency measurement
- **Commit:** N/A — not implemented
- **Changes:** None. No `ack_id` field, no ack response message type, on either server or client.
- **Files:** None
- **Why:** N/A — fails Acceptance Criterion #4 from `spec.md`.

## T8 - Write integration tests
- **Commit:** d5d6d9b (tests landed alongside T6)
- **Changes:** `TestParseMessage` (all 5 message shapes) and `TestParseMessageInvalid`. The specced "ACK response is well-formed" test is unachievable since T7 doesn't exist.
- **Files:** `internal/input/input_test.go`
- **Why:** Covers the JSON parsing surface, though these are unit- not integration-level tests.
