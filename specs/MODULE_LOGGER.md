# Module Spec: Logger

## Overview

The Logger module provides a lightweight, level-filtered logging subsystem used by all other modules. It outputs timestamped plain-text log lines to a configurable writer.

---

## Public Interface

```go
package logger

// Logger interface (to be extracted for modularity)
type Logger interface {
    Debug(module, msg string)
    Info(module, msg string)
    Warn(module, msg string)
    Error(module, msg string)
}

// Level represents log severity.
type Level int

const (
    DEBUG Level = 0
    INFO  Level = 1
    WARN  Level = 2
    ERROR Level = 3
)

// New creates a Logger that writes to out, filtering messages below minLevel.
func New(out io.Writer, minLevel Level) *LoggerImpl
```

---

## Internal Architecture

### Output Format

```
2026-06-04 12:30:45.123 INFO  [capture] frame acquired
2026-06-04 12:30:45.128 DEBUG [encode]  NAL size: 4521 bytes
```

Pattern: `{YYYY-MM-DD HH:MM:SS.mmm} {LEVEL} [{module}] {message}\n`

### Level Filtering

Messages below the configured minimum level are silently discarded:
```go
func (l *Logger) Log(level Level, module, msg string) {
    if level < l.level {
        return
    }
    fmt.Fprintf(l.out, "%s %s [%s] %s\n", timestamp, level, module, msg)
}
```

### Module Tags

Each caller identifies itself with a module string:
- `"capture"` — screen capture subsystem
- `"encode"` — video encoding
- `"server"` — WebSocket/HTTP server
- `"audio"` — audio capture
- `"input"` — input injection
- `"main"` — orchestrator/main

---

## Refactoring Directives

### R-LOG-01: Add Mutex for Thread Safety
Add `sync.Mutex` to protect `fmt.Fprintf` calls. Multiple goroutines (capture, audio, server) log concurrently and can interleave output with non-atomic writers.

### R-LOG-02: Extract Logger Interface
Define `Logger` as an interface in `pkg/logger/` so modules depend on the interface, not the concrete implementation. This enables:
- Test loggers (capture to buffer)
- Noop loggers (benchmarks)
- Structured logging backends (JSON, syslog)

### R-LOG-03: Add Structured Fields
Support key-value pairs for machine-parseable output:
```go
type Logger interface {
    Debug(module, msg string, fields ...Field)
    Info(module, msg string, fields ...Field)
    // ...
}

type Field struct {
    Key   string
    Value any
}
```

Output: `2026-06-04 12:30:45.123 INFO [encode] frame encoded size=4521 latency_ms=3.2`

### R-LOG-04: Use UTC Timestamps
Replace `time.Now()` with `time.Now().UTC()` for consistent timestamps regardless of host timezone.

> Clock separation: the logger intentionally uses **wall-clock UTC** for human-readable lines. This is a DIFFERENT clock from the media pipeline's `CLOCK_MONOTONIC` epoch (used for video/audio timestamps and A/V sync). Never use the logger's clock for media timestamps, and never use the monotonic media clock for log lines.

### R-LOG-05: Add Runtime Level Change
Support runtime level adjustment (e.g., via signal or HTTP endpoint) for debugging production instances without restart:
```go
func (l *Logger) SetLevel(level Level)
```

### R-LOG-06: Add io.Writer Adapter
Provide a `WriterAt(level Level, module string) io.Writer` adapter so subprocess stderr can be piped through the logger:
```go
cmd.Stderr = log.WriterAt(WARN, "ffmpeg")
```
This is partially implemented as `logWriter` in the current codebase but not extracted.

### R-LOG-07: File Rotation Support
For long-running server deployments, support log file rotation:
- Size-based rotation (e.g., 100MB)
- Or integrate with system log rotation (logrotate-friendly: reopen on SIGHUP)

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | Level string representation | No |
| Unit | Level filtering (all 16 combinations) | No |
| Unit | Output format validation | No |
| Unit | Convenience methods (Debug/Info/Warn/Error) | No |
| Unit | Concurrent write safety (after R-LOG-01) | No |
| Benchmark | Logging throughput (messages/sec) | No |
| Benchmark | Discarded messages (below-level) cost | No |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Log message (above level) | <1μs |
| Discarded message (below level) | <10ns |
| Memory per logger | <100 bytes |
| Concurrent writers | Safe (after mutex addition) |
