# Review: T6 - Implement JSON input message parsing

## Changes Made
- `InputMessage` struct + `ParseMessage([]byte) (*InputMessage, error)` in `protocol.go`
- `HandleMessage` dispatches on `msg.Type`: `"key"` → `InjectKey`, `"mousemove"`/`"mousedown"`/`"mouseup"` → the matching mouse method, `"wheel"` → `InjectWheel`
- Unmapped/unknown `Type` values fall through the `switch` with no default case — satisfies "ignore unknown message types (forward compatibility)"
- Wired into the server in `main.go` via `srv.SetInputCallback(...)` → `input.ParseMessage` → `inputDev.HandleMessage`
- Server startup is non-fatal if `/dev/uinput` is unavailable (`input: ... (input disabled)` log, no injection attempted)

## Files Created
- `internal/input/protocol.go`
- `cmd/server/main.go` (modified — input wiring)

## Test Results
- `TestParseMessage` covers all 5 real message shapes (key down/up, mousemove, mousedown, mouseup, wheel) — pass
- `TestParseMessageInvalid` confirms malformed JSON returns an error rather than panicking — pass

## Concerns / Trade-offs
- **Wire format doesn't match `spec.md`.** The spec documents a nested shape (`{"type":"mouse","event":"move","x":100,"y":200}`), but the actual implementation (both server `protocol.go` and client `compositor.js`) uses flat top-level types instead: `{"type":"mousemove","x":100,"y":200}`, `{"type":"mousedown","button":0}`, etc. Client and server agree with each other, so this is not a functional bug — but `spec.md` is stale documentation of a protocol shape that was never actually built this way.
- No `"input_ping"` handling exists (see T7 — the ACK feature it belongs to was never implemented).

## Verdict
PASS — functionally correct and tested; the spec.md wire-format documentation should be corrected to match what was actually shipped.
