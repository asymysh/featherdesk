# macOS Encoder Add-Ons

## No separate encoder add-ons are needed on macOS.

Unlike Linux (where vendor-specific paths like NVENC, AMF, and Vulkan Video each have
their own add-on spec) and unlike Windows (where NVENC, AMF, QSV, and MediaFoundation
are separate vendor APIs), **macOS uses a single unified encoder API for everything**:

> **VideoToolbox** is the only encoder API on macOS. It transparently routes to:
> - Intel Quick Sync (Intel Macs, Sandy Bridge 2011+)
> - AMD VCE via Apple's GVA framework (Intel Macs with AMD discrete GPU)
> - Apple Media Engine (all Apple Silicon Macs)
> - Apple's own software H.264/HEVC encoder (CPU fallback)
>
> All four paths use the same `VTCompressionSession` API. The driver picks the
> appropriate hardware at runtime based on what the Mac has.

## Why this folder exists

This folder is kept **empty** intentionally to maintain a symmetric directory structure
across all platforms:

```
ADD-ON-SPECS/
├── Linux/encoders/       ← 3 add-on specs (NVENC, AMF, Vulkan)
├── macOS/encoders/       ← this README only (no add-ons needed)
└── Windows/encoders/     ← pending architecture discussion
```

This way every implementor knows the encoder spec location for every OS follows the
same path pattern: `ADD-ON-SPECS/{Platform}/encoders/`.

## Where VideoToolbox is specced

The single VideoToolbox encoder spec lives in the platform spec itself:

**📄 [`../MACOS_SPEC.md`](../MACOS_SPEC.md)** — see the "Video Encoding" section for:
- VideoToolbox hardware support matrix (which Macs get which codecs)
- Confirmed fallback order (HEVC HW → H.264 HW → H.264 SW)
- C-callback API pattern (closure variant is broken in macOS 26)
- WebCodecs codec strings (`avc1.42E01E`, `hvc1.1.6.L93.B0`)
- Benchmark results (Hackintosh measured + Apple Silicon estimated)

## If a separate encoder is ever needed on macOS

In the unlikely event that a future workload requires an encoder outside VideoToolbox
(for example, a third-party AV1 implementation before Apple ships one), follow the
Linux add-on pattern:

1. Write the spec at `ADD-ON-SPECS/macOS/encoders/{NAME}_MACOS_SPEC.md`
2. Add a row to the macOS table in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
3. Implement under `internal/hwencode/{name}/` with a Go build tag
4. Wire the runtime probe order in `MODULE_PIPELINE.md`

Until then, this folder remains intentionally empty.
