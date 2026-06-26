# macOS Software Encoder Add-On: OpenH264 CGo (Cisco, BSD)

## Purpose

Cisco's OpenH264 H.264 software encoder via direct CGo binding. The
**BSD-licensed** software fallback for macOS deployments where GPL is not
acceptable or where VideoToolbox is unavailable.

On macOS, VideoToolbox SW/HW is the primary software encoder (Apple-tuned
for ARM via Accelerate.framework). OpenH264 is a cross-platform fallback
for edge cases where VT is not available or when binary consistency across
Linux/macOS/Windows is needed.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| libopenh264 | BSD-2-Clause | Cisco pays MPEG-LA royalties on behalf of all users |
| Cisco prebuilt binary | BSD-2-Clause | Downloaded from `ciscobinary.openh264.org` |
| Our CGo binding | Proprietary | In-process, no GPL contamination |

**Key advantage over x264:** No GPL. The main binary stays fully proprietary.
Cisco's royalty arrangement means no H.264 patent fees for the user.

---

## Performance (Benchmarked on Windows, Ryzen 9 5900X)

Cross-platform encoder — same performance characteristics on macOS:

### 1920x1080

| Threads | OpenH264 ms | OpenH264 FPS | x264 ms | x264 FPS | x264 speedup |
|---------|------------|-------------|---------|---------|-------------|
| 1 | 23.4 | 42 | 10.6 | 94 | 2.2x |
| 2 | 13.1 | 77 | 5.6 | 179 | 2.3x |
| **4** | **7.9** | **125** | **4.3** | **234** | **1.9x** |
| 8 | 7.5 | 131 | 3.5 | 285 | 2.2x |
| 12 | 7.4 | 138 | 3.3 | 302 | 2.2x |

### 2560x1440

| Threads | OpenH264 ms | OpenH264 FPS |
|---------|------------|-------------|
| 4 | 13.7 | 73 |
| 12 | 13.4 | 74 |

**Thread scaling saturates at 4 threads** — slice-based parallelism with
diminishing returns beyond 4 slices. On Apple Silicon (M1 = 8 cores,
M2 Pro = 12 cores), 4 threads is the practical ceiling.

---

## Build & Distribution

### Go build tag

```bash
GOOS=darwin go build -tags openh264 -o viewport-rds-macos ./cmd/server
```

### Runtime dependency

Cisco prebuilt binary downloaded from `ciscobinary.openh264.org`:
```bash
curl -O http://ciscobinary.openh264.org/libopenh264-2.4.1-mac-arm64.dylib.bz2
bunzip2 libopenh264-2.4.1-mac-arm64.dylib.bz2
```

### CGo configuration

```go
/*
#cgo CFLAGS: -I${SRCDIR}/vendor/openh264/include
#cgo LDFLAGS: -L${SRCDIR}/vendor/openh264 -lopenh264
#include <wels/codec_api.h>
*/
import "C"
```

---

## When to use this add-on

| Scenario | Use OpenH264? | Use x264? | Use VideoToolbox? |
|----------|--------------|----------|------------------|
| Commercial deployment | ✅ BSD safe | ❌ GPL | ✅ if macOS-only |
| Home / personal use | Works | ✅ faster | ✅ if macOS-only |
| Cross-platform binary consistency | ✅ same code all OSes | ✅ same | ❌ macOS-only |
| No GPU / headless Mac | ✅ | ✅ faster | ✅ VT SW works without GPU |
| Apple Silicon M1+ | Slower than VT | Slower than VT | ✅ best on macOS |

---

## Status

📋 **Specced.** Benchmarked on Windows (same cross-platform code). macOS
native benchmarks pending on Hackintosh. OpenH264 verified working on
macOS in prior sessions (3.56ms P50 at 1080p on Hackintosh — but that was
single-frame, not sustained encode).

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_openh264]` section
of the TOML config (see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is compiled into the binary`;` unknown
keys in this section will cause startup to fail.

