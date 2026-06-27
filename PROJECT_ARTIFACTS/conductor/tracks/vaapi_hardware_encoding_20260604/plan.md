# Implementation Plan: VA-API Hardware Encoding

## Phase 1: VA-API Probing

- [ ] Task: Implement VA-API capability detection
    - [ ] Open /dev/dri/renderD128 and initialize VA display (cgo libva bindings)
    - [ ] Query VAProfileH264High + VAEntrypointEncSliceLP
    - [ ] Return bool indicating hardware encode availability
    - [ ] Log VA-API vendor string and encode support
    - [ ] Write unit test (mock or build-tag gated)
- [ ] Task: Implement encoder selection logic
    - [ ] Parse `--hardware` and `--software` flags
    - [ ] If `--hardware` and not available: fatal error with clear message
    - [ ] If no flag: auto-detect, log selection
    - [ ] Wire into main.go startup
- [ ] Task: Conductor - User Manual Verification 'VA-API Probing' (Protocol in workflow.md)

## Phase 2: ffmpeg Pipe Encoder

- [ ] Task: Implement ffmpeg subprocess management
    - [ ] Build ffmpeg command string from capture dimensions and FPS
    - [ ] Spawn with stdin/stdout/stderr pipes via os/exec
    - [ ] Redirect stderr to log file (ffmpeg_err.log)
    - [ ] Implement clean shutdown (close stdin, wait, terminate if needed)
- [ ] Task: Implement frame write and NAL read
    - [ ] Write raw BGRA frames to ffmpeg stdin (handle partial writes)
    - [ ] Read stdout in goroutine, accumulate into buffer
    - [ ] Parse NAL start codes (00 00 00 01) to split access units
    - [ ] Detect frame boundaries (new VCL NAL after previous VCL)
    - [ ] Return complete frames as NAL unit slices
- [ ] Task: Implement encode.Encoder interface for ffmpeg
    - [ ] Same interface as OpenH264: `Encode(*Frame) ([][]byte, error)`
    - [ ] IDR on demand: restart ffmpeg (forces new keyframe sequence)
    - [ ] Implement automatic restart on ffmpeg crash/pipe break
    - [ ] Backoff on repeated failures (don't spin)
- [ ] Task: Write encoder tests
    - [ ] Test NAL parsing with known H.264 bitstream samples
    - [ ] Test frame boundary detection
    - [ ] Test crash recovery (kill subprocess, verify restart)
    - [ ] Integration test: encode synthetic frame, verify output is valid H.264
- [ ] Task: Conductor - User Manual Verification 'ffmpeg Pipe Encoder' (Protocol in workflow.md)

## Phase 3: Integration

- [ ] Task: Wire hardware encoder into pipeline
    - [ ] Replace encoder in main.go based on selection logic
    - [ ] Update /status endpoint to report encoder type
    - [ ] Verify browser client works identically with both encoders
    - [ ] Profile CPU usage: confirm <5% in hardware mode
- [ ] Task: Conductor - User Manual Verification 'Integration' (Protocol in workflow.md)
