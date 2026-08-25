# Review: T7 - Implement input ACK for latency measurement

## Changes Made
None. This feature was never implemented.

## Files Created
None.

## Test Results
N/A — nothing to test.

## Concerns / Trade-offs
Confirmed absent by direct inspection:
- `InputMessage` (`protocol.go`) has no `ack_id` field
- No `"input_ack"` (or any ack-related) message type exists in `HandleMessage`'s switch
- `compositor.js` never sends an `ack_id` on any input message, and has no handler for a server-sent ack frame
- This directly fails **Acceptance Criterion #4** from `spec.md`: "Input ACK round-trip measurable in client UI"
- The dependent Phase 4 task (T11, "Implement input latency display") is consequently also unimplementable as specced — see that review

## Verdict
FAIL — not implemented.
