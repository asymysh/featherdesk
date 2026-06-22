# Windows SW Encoder Add-On: OpenH264 CGo

## Purpose

Software H.264 Baseline encoder via Cisco's OpenH264 library on Windows. **Identical
Go code to the Linux and macOS OpenH264 CGo add-ons** — one of FeatherDesk's main
cross-platform consistency points.

When the Windows machine has no GPU at all (rare, mostly VMs and headless test
machines) or no vendor HW encoder add-on installed, this is the encoder that runs.

---

## License & Royalties

| Component | License | Notes |
|-----------|---------|-------|
| OpenH264 library | BSD-2 | Permissive |
| MPEG-LA H.264 royalties | **Cisco pays** | Same on every platform |
| Our CGo binding | MIT | Same Go file as Linux + macOS |

Zero royalty concern — see `Linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md` for the
full licensing background. This applies identically on Windows.

---

## Hardware Compatibility

**Any CPU.** Pure software.

| Architecture | Performance |
|-------------|------------|
| x86_64 (Intel/AMD desktop & laptop) | ~4–8ms p50 @ 1080p |
| ARM64 (Snapdragon Copilot+ PCs) | ~8–12ms p50 @ 1080p (NEON path) |

Same encoder, same code on both x86 and ARM Windows.

---

## Build & Distribution

### Go build tag

```bash
go build -tags openh264 -o viewport-rds-windows-openh264.exe ./cmd/server
```

The build tag matches the Linux build tag. Same encoder, same Go file.

### Runtime dependencies

- `openh264-X.X.X-win64.dll` (Cisco distributes prebuilt DLLs)
- Must be in `%PATH%` or alongside the .exe

The Windows binary needs the OpenH264 DLL shipped alongside it. Cisco's official
prebuilt DLLs are downloadable from the OpenH264 GitHub releases. Their license
permits redistribution.

### CGo configuration (Windows)

```go
/*
#cgo CFLAGS: -I${SRCDIR}/openh264/include
#cgo LDFLAGS: -L${SRCDIR}/openh264/win64 -lopenh264

#include <wels/codec_api.h>
#include <wels/codec_app_def.h>
*/
import "C"
```

The OpenH264 SDK headers and import library are vendored in the source tree under
`internal/encode/openh264/openh264/` (Cisco's license permits this for binary
distribution).

---

## Implementation

Identical to the Linux spec. See
[`../../Linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md`](../../../Linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md)
for:
- CGo session setup
- Per-frame encode loop
- Force-keyframe path
- IDR-on-demand configuration

The Go code is byte-for-byte the same across Linux + Windows + macOS. Only the
CGo `#cgo` directives differ for finding the OpenH264 library at link time.

---

## Performance (Benchmarked)

Measured on this Windows machine during the benchmark session:

| Encoder config | Resolution | FPS | p50 | Notes |
|---------------|-----------|-----|-----|-------|
| OpenH264 (estimated from Linux + macOS data, same code) | 1080p | ~225 | ~4–8ms | Range covers Ryzen 9 5900X to Ryzen 5 3600 |
| Reference: libx264 ultrafast (ffmpeg subprocess) | 1080p | 226 | 4.3ms | What OpenH264 is competing against — GPL-2 |
| Reference: libx264 veryfast (ffmpeg subprocess) | 1080p | 169 | 5.7ms | GPL-2 |
| Reference: MF SW (`h264_mf`) | 1080p | 201 | ~5ms | Built into Windows |

OpenH264 is comparable to libx264 ultrafast and slightly faster than MediaFoundation
SW on x86. The advantages — no GPL, no ffmpeg subprocess, cross-platform same code —
make it the right SW default.

---

## File Structure

```
internal/encode/openh264/
├── openh264.go              // shared with Linux + macOS
├── openh264_cgo.go          // CGo binding (build tag: openh264)
├── openh264_stub.go         // build tag: !openh264
├── probe.go
├── openh264/                // vendored OpenH264 SDK (headers + import libs)
│   ├── include/wels/*.h
│   ├── win64/openh264.lib   // Windows MSVC import library
│   └── win64/openh264-X.dll // Cisco's official prebuilt DLL
└── openh264_test.go
```

The vendored `openh264/win64/openh264-X.dll` ships alongside the .exe at install
time. Build process copies it from `internal/encode/openh264/openh264/win64/`
into the install directory.

---

## When to use this add-on

Use this add-on when:
- Need a guaranteed SW H.264 path that doesn't depend on Windows version (works
  on Windows 10 and 11, both x86 and ARM)
- Want cross-platform binary consistency (same encoder used on every OS)
- Container deployments where MediaFoundation isn't reliable

Skip when:
- MediaFoundation SW add-on is acceptable — slightly slower but ships built into
  Windows with no DLL distribution concern

---

## Status

📋 Specced — implementation exists today as the default SW encoder in
`internal/encode/openh264.go`. Refactor moves it to `internal/encode/openh264/`
under a Go build tag, with Windows-specific DLL vendoring added.
