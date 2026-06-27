# FeatherDesk - Central Architecture Specification

## Product Overview

**Name:** FeatherDesk (binary: `featherdesk`)
**Type:** Low-latency remote desktop streaming server (Linux primary, Windows + macOS planned)
**Language:** Go 1.26+ with CGo
**Deployment:** Single host binary with embedded web client + optional add-on shared libraries (capture / encode / input / audio), loaded at runtime
**Target:** Parsec/Sunshine-level latency on LAN

## Product Goals

- Motion-to-photon latency: <20ms on LAN at 1080p60
- Resolution: up to 2560x1440
- Framerate: 60fps (hardware), 30fps (software fallback)
- Bandwidth: 5-15 Mbps/viewer
- Memory: <50MB RSS
- Concurrent viewers: up to 25 (1 controller + 24 passive)

---

## Pluggable Architecture (Zero-by-Default)

FeatherDesk is a **fully pluggable, add-on based project**. The default binary
on every platform ships with **zero encoders and zero capture backends**. Every
backend — software, hardware, or capture — is an opt-in **add-on shared library**
that the host **loads at runtime** (`dlopen` on Linux/macOS, `LoadLibraryW` on
Windows). The host binary is never recompiled to add, remove, or swap a backend.

Users compose the deployment they need by **dropping the add-on shared libraries
they want into the add-ons directory** (`[addons] dir`). The same host binary
serves wildly different environments (commercial BSD-only deployments, home
installs with GPL x264, NVIDIA-only servers, AMD workstations, headless Windows
VMs) by loading a different set of libraries — no `#ifdef` spaghetti, no
per-deployment rebuild of the host.

### Add-on loading model (v1: dlopen)

- Each add-on is built **standalone** as a C-ABI shared library
  (`.so` / `.dylib` / `.dll`) via `go build -buildmode=c-shared`. The add-on's
  own native dependencies (CGo, vendor SDKs) are linked into **that library**,
  never into the host.
- Every add-on library exports one C entry point — `FeatherDeskAddonOpen` —
  returning **(1)** an `ABIVersion`, **(2)** a capability descriptor
  (kind = capture / encode / hwencode / audio / input; codec(s); platform), and
  **(3)** a vtable of function pointers implementing the add-on's interface. The
  host adapts that vtable back into the Go interface (`Capturer`, `Encoder`,
  `HardwareEncoder`, …) the pipeline consumes.
- At startup the host **scans the add-ons directory**, `dlopen`s each library,
  checks `ABIVersion` (by default a mismatch is skipped with a warning, not a
crash; `[addons] abi_strict = true` makes a mismatch abort startup instead), and
  registers its capability descriptor. There is **no build-tag `init()` registry
  and no stub files** — an absent add-on is simply a library that isn't in the
  directory.
- **Filename convention:** `featherdesk-addon-<id>.{so,dylib,dll}`, where `<id>`
  (`kms_egl`, `openh264`, `nvenc`, …) is the **add-on ID** — it names the library
  and its `[addon_module_<id>]` config section.
- **Hot-swap = drop a library + restart.** v1 resolves the add-on set once at
  startup; live reload without restart is out of scope for v1.

### Add-on ABI contract

The loader and every add-on share a **flat C ABI** defined in `pkg/addon`
(a Go package + generated C header). The contract is deliberately narrow because
of one hard constraint:

> **Two-runtime constraint (the key v1 risk).** A `-buildmode=c-shared` Go
> library carries its **own** Go runtime (GC, scheduler, signal handlers).
> `dlopen`-ing it into a host that is itself a Go program means **two Go runtimes
> in one process**, and **Go pointers, slices, channels, closures, and
> `error` sentinel values MUST NOT cross the boundary** (cgo pointer rules +
> distinct per-library sentinel addresses). If this proves unworkable in
> practice, that is exactly the trigger for the **v2 fallback** below. v1 ships
> behind this risk on purpose.

Therefore everything crossing the boundary is plain C:

- **Entry point:** `FeatherDeskAddonOpen` returns `ABIVersion` (a single
  `uint32`; the host accepts the add-on iff `addon.ABIVersion == host.ABIVersion`
  — **exact match, no forward/backward compat in v1**), a **capability
  descriptor** (kind = capture / encode / hwencode / audio / input; codec id(s);
  os+arch), and a **vtable** of C function pointers.
- **Buffers** cross as `(ptr, len, cap)` triples with explicit ownership: the
  caller allocates, or the callee returns a borrowed pointer plus a `release`
  function pointer. No `[]byte` from a Go `sync.Pool` and no Go-closure
  `Release` crosses the line; the **host** wraps the C buffer/`release` into the
  Go `Capturer`/`Encoder` interfaces it hands the pipeline.
- **Errors** cross as a stable `int` code enum (e.g. `ABI_ERR_FALLBACK_TO_SOFTWARE`,
  `ABI_ERR_CHROMA_UNSUPPORTED`, `ABI_ERR_REQUIRES_RESTART`); the **host loader
  translates each code back into the canonical `stream.Err*` sentinel** so
  `errors.Is` in the pipeline works.
- **Channels** never cross: an `AudioCapturer.Chunks()` channel is produced
  **host-side** by a goroutine that pumps a C `next_chunk` vtable call.

**Load-failure taxonomy** (default = skip the library with a WARN; `[addons]
abi_strict = true` aborts startup for any of these):

| Failure | Default behavior |
|---------|------------------|
| `dlopen`/`LoadLibraryW` fails (corrupt, wrong **os/arch**, missing transitive dep) | skip + warn |
| no `FeatherDeskAddonOpen` export (stray `.so` in dir) | skip + warn |
| `FeatherDeskAddonOpen` returns error / null | skip + warn |
| `ABIVersion` mismatch | skip + warn |
| capability descriptor names an unknown kind, or a codec with no wire type (e.g. AV1) | skip + warn |
| add-ons dir does not exist | treated as empty + warn |

An add-on library MUST match the host's **OS *and* CPU arch** (an x86_64 `.dylib`
will not load into an arm64 host); CGo add-ons are therefore built natively per
target, not cross-composed.

**Security — add-on directory trust.** `dlopen` executes native code from a
directory at startup, and the host often runs elevated (KMS+EGL needs
root/`CAP_SYS_ADMIN`; Interception/SendSAS needs SYSTEM). The add-ons directory
and every library in it **MUST be owned by, and writable only by, the host's
privilege level**; the loader verifies this on startup (as MODULE_AUTH already
does for the TLS key) and refuses (or warns) on a world-writable dir. The default
dir is therefore an admin-owned location (`/usr/lib/featherdesk/addons`,
`%PROGRAMDATA%\FeatherDesk\addons`), **not** a user-writable `$XDG_DATA_HOME`
path, to avoid a local privilege-escalation vector.

### Why zero-by-default

- **Smallest possible default binary** — no unwanted dependencies, no
  unused codecs in the wire format
- **License-agnostic host** — the host **never links any add-on's code** (each is
  a separate library or, for `x264`, a separate subprocess the add-on spawns), so
  the host's permissive license is independent of which add-ons are dropped in.
  GPL paths stay isolated regardless of mechanism.
- **Deployment flexibility** — one host binary, many add-on sets, swappable in the field
- **Simpler probing** — only loaded add-ons get probed at runtime

### Build / deploy matrix

Each add-on is built once, then dropped into the add-ons directory. Examples
(the host binary is the same in every row):

| Deployment | Add-on libraries to drop in |
|------------|------------------------------|
| Commercial Windows, generic | `dxgi_dd`, `openh264`, `mf_hw` |
| Home Windows, NVIDIA | `dxgi_dd`, `x264`, `nvenc` |
| Commercial Linux, AMD | `kms_egl`, `openh264`, `libva`, `amf_rocm` |
| Apple Silicon Mac | `sck`, `vt_hw` |

Build one add-on with, e.g.,
`go build -buildmode=c-shared -o featherdesk-addon-kms_egl.so ./internal/capture/kms`.
See each platform's `encoders/README.md` and `capture/README.md` for recommended
combinations.

### Runtime probe and selection

When multiple add-ons are loaded, the pipeline picks at runtime based on:

1. `[capture] force_addon` / `[encode] force_addon` in TOML (forces a specific add-on by ID)
2. Probe order (HEVC HW > H.264 HW > x264 SW > VT SW > OpenH264 SW)
3. Hardware presence (NVENC only fires if NVIDIA GPU present, etc.)
4. `ErrFallbackToSoftware` from HW encoder triggers SW fallback for the session

Per-add-on tuning lives in `[addon_module_<id>]` TOML sections, not
in code. See [`./core/MODULE_CONFIG.md`](./core/MODULE_CONFIG.md).

### v2 fallback

If runtime `dlopen` loading proves problematic in v1 — most likely because of
the **two-runtime constraint** above — v2 may switch to statically-composed
**edition binaries** (build-tag composition) or **subprocess sidecars** (which
sidestep the shared-process Go-runtime issue entirely). The add-on **interface
contracts are identical** under all three mechanisms — only how the host obtains
the implementation changes — so this decision does not affect any add-on's spec
beyond its build/packaging step.

---

## Module Map (Core Modules)

The system is decomposed into 18 module specs — 15 numbered core modules plus three
cross-cutting/support specs (Auth, Stream Params, Audio). Each module has its own spec
sheet with complete interface contracts, internal architecture, and refactoring directives.

| # | Module | Spec File | Responsibility |
|---|--------|-----------|----------------|
| 1 | **Capture** | [`./media/MODULE_CAPTURE.md`](./media/MODULE_CAPTURE.md) | Cross-platform `Capturer` interface contract (concrete impls are add-ons per OS) |
| 2 | **Encode** | [`./media/MODULE_ENCODE.md`](./media/MODULE_ENCODE.md) | Software encoder interface contract (concrete impls are add-ons) |
| 3 | **Hardware Encode** | [`./media/MODULE_HARDWARE_ENCODE.md`](./media/MODULE_HARDWARE_ENCODE.md) | Hardware encoder interface contract (concrete impls are add-ons) |
| 4 | **Protocol** | [`./core/MODULE_PROTOCOL.md`](./core/MODULE_PROTOCOL.md) | Wire protocol (framing, serialization, versioning) — transport-agnostic; shared by the browser (v1) and native (v2) clients, see [`./client/MODULE_NATIVE_CLIENT.md`](./client/MODULE_NATIVE_CLIENT.md) |
| 5 | **Transport** | [`./core/MODULE_TRANSPORT.md`](./core/MODULE_TRANSPORT.md) | HTTP/3 + WebTransport (QUIC); datagrams + reliable streams; auth handshake |
| 6 | **Server** | [`./core/MODULE_SERVER.md`](./core/MODULE_SERVER.md) | Session management, broadcast fan-out, keyframe/bootstrap cache, role gating (consumes Transport; transport/TLS owned by #5) |
| 7 | **Web Client** | [`./client/MODULE_WEB_CLIENT.md`](./client/MODULE_WEB_CLIENT.md) | Browser-based viewer (WebCodecs) — v1 |
| 8 | **Pipeline** | [`./core/MODULE_PIPELINE.md`](./core/MODULE_PIPELINE.md) | Orchestrator: probe + select loaded add-ons, lifecycle, pacing, frame drops, wiring |
| 9 | **Config** | [`./core/MODULE_CONFIG.md`](./core/MODULE_CONFIG.md) | TOML config schema, parsing, validation, hot reload |
| 10 | **Input** | [`./interaction/MODULE_INPUT.md`](./interaction/MODULE_INPUT.md) | Binary input wire decode + dispatcher + HID-usage contract (injection impls are add-ons per OS) |
| 11 | **Clipboard** | [`./interaction/MODULE_CLIPBOARD.md`](./interaction/MODULE_CLIPBOARD.md) | Bidirectional text + rich-HTML clipboard sync (core; per-OS clipboard access) |
| 12 | **File Transfer** | [`./interaction/MODULE_FILETRANSFER.md`](./interaction/MODULE_FILETRANSFER.md) | Drag-drop transfer to a fixed folder carried as QUIC bidirectional streams on the main WebTransport session (core) |
| 13 | **Gamepad** | [`./interaction/MODULE_GAMEPAD.md`](./interaction/MODULE_GAMEPAD.md) | Browser Gamepad-API redirection contract + rumble (virtual-controller injection is per-OS add-ons; casual-gaming-grade only) |
| 14 | **Network** | [`./v2/MODULE_NETWORK.md`](./v2/MODULE_NETWORK.md) | v2 connectivity (NAT traversal / relay / signaling for the native client). Requirements + listener-provider contract documented; **mechanism not chosen** (tsnet vs pion vs other — evaluated at v2 start). |
| 15 | **Native Client** | [`./client/MODULE_NATIVE_CLIENT.md`](./client/MODULE_NATIVE_CLIENT.md) | v2 native desktop client plan — same QUIC protocol, full-HID gamepad, reliable 4:4:4, sub-ms input. **Split final; impl deferred.** |
| 16 | **Auth** *(support)* | [`./core/MODULE_AUTH.md`](./core/MODULE_AUTH.md) | Authentication modes, session tokens, in-band resume credentials, role gating |
| 17 | **Stream Params** *(support)* | [`./core/MODULE_STREAM_PARAMS.md`](./core/MODULE_STREAM_PARAMS.md) | Dynamic stream parameters, adaptive bitrate, chroma negotiation (shared `pkg/stream`) |
| 18 | **Audio** *(deferred)* | [`./media/MODULE_AUDIO.md`](./media/MODULE_AUDIO.md) | Host→client system audio: Opus/PCM, stereo / 5.1 / 7.1, audio-master A/V sync. **Design locked; impl deferred.** |

> **Encoder, capture, and input implementations are not core modules.**
> Every encoder (OpenH264 CGo, x264 subprocess, VideoToolbox, libva, NVENC, AMF,
> QSV, MediaFoundation HW), every capture backend (KMS+EGL, NvFBC, SCK, DXGI DD),
> and every input injector (interception, uinput, cgevent, win_touch, vigem,
> gcvirtual) is an add-on shared library under
> [`specs/addons/{platform}/{capture,encoders,input,audio}/`](./addons/).
> The default binary ships with zero of each — users drop in what they need.
> A binary with no input add-on is **view-only**. See the index below.

> **Clipboard + File Transfer are core (not add-ons).** Their OS surface is small
> (clipboard APIs, file I/O) and they are baseline remote-desktop expectations,
> so they live in core with per-OS files behind build constraints.

> **Removed from the module map:**
> - **Logger** — replaced by stdlib `log/slog`. No dedicated module spec needed.
>   Server/Pipeline take a `*slog.Logger` directly. Behavior (text vs JSON,
>   level, output) is set via the `[log]` config section.

> **Deferred to future versions:**
> - **Native client (v2)** — the v1=browser / v2=native split is **final**; the
>   native client speaks the identical wire protocol (via `quic-go` directly) and
>   adds full-HID gamepad, reliable 4:4:4, and sub-ms input. Design plan in
>   `MODULE_NATIVE_CLIENT.md`; implementation deferred to v2. Connectivity
>   (Tailscale `tsnet` vs alternatives) is the one open item there.
> - **Audio** — **design LOCKED** (host→client system audio, pluggable per-OS
>   capture add-ons, pluggable Opus/PCM codec, stereo / 5.1 / 7.1, realtime
>   **audio-master** A/V sync; see `MODULE_AUDIO.md`). **Implementation** is
>   deferred behind the same trigger (video capture+encode working end-to-end on
>   all three OSes). Client→host mic is out of scope.
> - **Webcam redirection (client→host virtual camera)** — stripped from v1 to
>   keep scope tight. Open questions before re-introduction: server-side decoder
>   choice (recommend OpenH264 decoder, reusing the existing encoder add-on's
>   dependency); Windows DirectShow vs `MFCreateVirtualCamera` for modern Teams
>   compatibility; macOS NSXPCConnection to a Mach service for the Camera
>   Extension; lifecycle (`Stop` vs `Close`, `webcam_reconfigure`, abnormal
>   termination cleanup); microphone handling. **Wire type `0x50` is reserved**
>   for the future webcam frame and MUST NOT be reused without a protocol
>   version bump. The deleted `MODULE_WEBCAM.md` and the three sink add-ons
>   (`v4l2loopback`, `dshow_vcam`, `cmio_ext`) can be reconstructed from git
>   history if/when revived.
>
> (**Input is no longer deferred** — see module 10.)

> **Not supported (permanently out of scope):**
> - **Generic USB redirection** — needs kernel drivers on both ends, cannot work
>   from a browser, and is the single largest attack surface (BadUSB-class). Every
>   peer (Parsec, Sunshine, Moonlight) skips it. Device-class features are handled
>   at the API level instead.
> - **DRM-protected content** (Netflix/Disney+ L1) — renders through the OS
>   Protected Media Path and bypasses the compositor (black frames) on
>   Windows/macOS; no legal bypass exists. Linux L3 captures naturally with no
>   special handling. FeatherDesk neither circumvents nor markets DRM streaming.
> - **TUN/VPN tunnel** — browsers cannot use a TUN device; full LAN exposure is a
>   serious security surface. Recommend Tailscale alongside FeatherDesk instead.
> - **Smart card / FIDO2 redirection** — no demonstrated demand; out of scope.

---

## Platform & Add-On Spec Index

OS-specific platform specs and vendor-specific add-on encoder specs live under
[`specs/addons/`](./addons/). This index is the **single source of truth**
for where any platform or add-on document lives — never duplicate specs, always link here.

### Platform specs

| Platform | Spec | Capture add-on(s) | Encoder add-on(s) |
|----------|------|------------------|-------------------|
| **Cross-platform compat** | [`specs/PLATFORM_COMPAT.md`](./PLATFORM_COMPAT.md) | — | — |
| **Linux** | [`specs/addons/linux/LINUX_SPEC.md`](./addons/linux/LINUX_SPEC.md) | `kms_egl`, `nvfbc` | `openh264`, `x264`, `libva`, `nvenc`, `amf_rocm` |
| **macOS** | [`specs/addons/macos/MACOS_SPEC.md`](./addons/macos/MACOS_SPEC.md) | `sck` | `openh264`, `x264`, `vt_sw`, `vt_hw` |
| **Windows** | [`specs/addons/windows/WINDOWS_SPEC.md`](./addons/windows/WINDOWS_SPEC.md) | `dxgi_dd` | `openh264`, `x264`, `mf_hw`, `nvenc`, `amf`, `qsv` |

> **Default binary on every platform contains zero capture backends and zero
> encoders.** Every backend is an opt-in add-on shared library. Users compose the
> binary they need by combining one or more capture add-ons with one or more
> encoder add-ons. See each platform's `capture/README.md` and `encoders/README.md`
> for recommended combinations.

### Linux capture add-on specs

| Add-on | Add-on ID | Spec | Hardware | Status |
|--------|-----------|------|---------|--------|
| KMS+EGL DMA-BUF | `kms_egl` | [`specs/addons/linux/capture/KMS_EGL_LINUX_SPEC.md`](./addons/linux/capture/KMS_EGL_LINUX_SPEC.md) | Universal — every GPU, any display server | ✅ Working |
| NvFBC | `nvfbc` | [`specs/addons/linux/capture/NVFBC_LINUX_SPEC.md`](./addons/linux/capture/NVFBC_LINUX_SPEC.md) | NVIDIA proprietary driver | 📋 Specced |

> **KMS+EGL is the recommended default capture add-on.** Works on X11, Wayland
> (all compositors), and headless — it operates below the display server, so
> display server choice is irrelevant. Requires root / `CAP_SYS_ADMIN`. No-root
> fallback paths (XShm, PipeWire portal, wlr-screencopy, X11grab) were considered
> and explicitly rejected — none beat KMS+EGL when root is available, and no-root
> deployment is not currently a target.
>
> **Intel / AMD do not need capture add-ons** — neither vendor has a proprietary
> capture API on Linux. KMS+EGL is the entire path.

See [`specs/addons/linux/capture/README.md`](./addons/linux/capture/README.md)
for the full rationale.

### Linux encoder add-on specs

| Add-on | Path | License | Spec | Hardware | Status |
|--------|------|---------|------|---------|--------|
| OpenH264 CGo | SW | BSD-2 (Cisco) | [`linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md`](./addons/linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md) | Any CPU (x86_64, ARM64) | ✅ Working |
| x264 subprocess | SW | GPL-2 (isolated) | [`linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md`](./addons/linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md) | Any CPU; needs ffmpeg | ✅ Benchmarked |
| libva direct | HW | MIT | [`linux/encoders/HW/LIBVA_LINUX_SPEC.md`](./addons/linux/encoders/HW/LIBVA_LINUX_SPEC.md) | Intel + AMD + NVIDIA (via wrapper) | 📋 Specced |
| NVENC direct | HW | NVIDIA SDK | [`linux/encoders/HW/NVENC_LINUX_SPEC.md`](./addons/linux/encoders/HW/NVENC_LINUX_SPEC.md) | NVIDIA Kepler+ | 📋 Specced |
| AMF on ROCm | HW | Apache 2.0 | [`linux/encoders/HW/AMF_ROCM_SPEC.md`](./addons/linux/encoders/HW/AMF_ROCM_SPEC.md) | AMD GCN+ via ROCm | 📋 Specced |

> **SW encoder choice:** OpenH264 for commercial deployments (BSD).
> x264 for home / OSS — 2× faster on multi-core CPUs but GPL contamination
> on the ffmpeg subprocess.
>
> **Intel on Linux is not a separate HW add-on** — Intel Quick Sync is exposed
> exclusively through VA-API. The `libva` add-on covers Intel Sandy Bridge through Arc.

### macOS capture add-on specs

ScreenCaptureKit is the only capture API on macOS. All legacy alternatives
(CGDisplayStream, CGWindowListCreateImage, etc.) were removed. SCK is still a
add-on shared library (`sck`) for architectural consistency -- it just happens to
be the only capture option.

| Add-on | Add-on ID | Spec | Hardware | Status |
|--------|-----------|------|---------|--------|
| ScreenCaptureKit | `sck` | [`specs/addons/macos/capture/SCK_MACOS_SPEC.md`](./addons/macos/capture/SCK_MACOS_SPEC.md) | All Macs (macOS 12.3+) | 📋 Specced |

See [`specs/addons/macos/capture/README.md`](./addons/macos/capture/README.md)
for the full rationale.

### macOS encoder add-on specs

| Add-on | Path | License | Spec | Hardware | Status |
|--------|------|---------|------|---------|--------|
| OpenH264 CGo | SW | BSD-2 (Cisco) | [`macos/encoders/SW/OPENH264_CGO_MACOS_SPEC.md`](./addons/macos/encoders/SW/OPENH264_CGO_MACOS_SPEC.md) | Any CPU; cross-platform | 📋 Specced |
| x264 subprocess | SW | GPL-2 (isolated) | [`macos/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md`](./addons/macos/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md) | Any CPU; needs ffmpeg | 📋 Specced |
| VideoToolbox SW | SW | Apple system | [`macos/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md`](./addons/macos/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md) | Any Mac (macOS 12.3+) | 📋 Specced |
| VideoToolbox HW | HW | Apple system | [`macos/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md`](./addons/macos/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md) | All Macs 2011+ (HW H.264), Skylake+/Apple Silicon (HW HEVC). No AV1 HW encode on any current Apple Silicon. | 📋 Specced |

> **No vendor-specific HW add-ons on macOS** — Apple controls the entire graphics stack.
> VideoToolbox is the single API for Intel Quick Sync, AMD VCE, and Apple Media Engine.
>
> **For maximum HW performance on macOS, use VideoToolbox.** For cross-platform
> binary consistency with Linux/Windows, OpenH264 or x264 can be used as SW
> fallback (especially useful for the GPL/BSD licensing differentiation).

### Windows capture add-on specs

| Add-on | Add-on ID | Spec | Hardware | Status |
|--------|-----------|------|---------|--------|
| DXGI Desktop Duplication (with integrated IddCx headless install) | `dxgi_dd` | [`specs/addons/windows/capture/DXGI_DD_WINDOWS_SPEC.md`](./addons/windows/capture/DXGI_DD_WINDOWS_SPEC.md) | Any GPU (WDDM 1.2+, Win 8+) | ✅ Benchmarked |

> **DXGI DD is the only Windows capture mechanism.** Benchmarking proved its
> raw acquisition overhead is sub-microsecond on every GPU, leaving no room
> for vendor-specific capture APIs (NvFBC, AMF Display Capture) to improve on.
> Output is `ID3D11Texture2D`, directly consumable by every Windows HW encoder
> (MF HW, NVENC, AMF, QSV) with zero-copy.
>
> For **headless deployments** (no physical display), the add-on bundles a
> pre-signed IddCx virtual display driver that auto-installs on first launch
> via `pnputil` (one-time UAC). Same approach as Sunshine/Moonlight.
>
> **No elevation required for standard use.** First-launch driver install
> needs one UAC prompt; subsequent runs need none.

See [`specs/addons/windows/capture/README.md`](./addons/windows/capture/README.md)
for the full rationale, recommended combinations, and headless install flow.

### Windows encoder add-on specs

| Add-on | Path | License | Spec | Hardware | Status |
|--------|------|---------|------|---------|--------|
| OpenH264 CGo | SW | BSD-2 (Cisco) | [`windows/encoders/SW/OPENH264_CGO_WINDOWS_SPEC.md`](./addons/windows/encoders/SW/OPENH264_CGO_WINDOWS_SPEC.md) | Any CPU | ✅ Benchmarked |
| x264 subprocess | SW | GPL-2 (isolated) | [`windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md`](./addons/windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md) | Any CPU; needs ffmpeg | ✅ Benchmarked |
| MediaFoundation HW | HW | Microsoft system | [`windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md`](./addons/windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md) | All vendors (cross-vendor via MFT routing) | ✅ Benchmarked |
| NVENC | HW | NVIDIA SDK | [`windows/encoders/HW/NVENC_WINDOWS_SPEC.md`](./addons/windows/encoders/HW/NVENC_WINDOWS_SPEC.md) | NVIDIA Kepler+ | ✅ Benchmarked |
| AMF | HW | Apache 2.0 | [`windows/encoders/HW/AMF_WINDOWS_SPEC.md`](./addons/windows/encoders/HW/AMF_WINDOWS_SPEC.md) | AMD GCN+ | ✅ Benchmarked |
| QSV (oneVPL) | HW | MIT | [`windows/encoders/HW/QSV_WINDOWS_SPEC.md`](./addons/windows/encoders/HW/QSV_WINDOWS_SPEC.md) | Intel Sandy Bridge+ (covers Arc) | 📋 Specced |


> **MediaFoundation HW is the recommended cross-vendor default for Windows** —
> closest equivalent to VA-API on Linux. Ship `mf_hw` for one-binary-covers-everything;
> add vendor SDKs (NVENC/AMF/QSV) for peak performance and vendor-specific features.

### Input add-on specs

Implement `input.KeyMouseInjector` / `input.TouchInjector` / `input.GamepadInjector`.
The core decodes the binary input protocol; the add-on performs OS injection.
No input add-on loaded → **view-only** binary. See
[`MODULE_INPUT.md`](./interaction/MODULE_INPUT.md) and [`MODULE_GAMEPAD.md`](./interaction/MODULE_GAMEPAD.md).

| Add-on | Add-on ID | OS | Spec | Capability | Status |
|--------|-----------|----|----- |------------|--------|
| Interception | `interception` | Windows | [`windows/input/INTERCEPTION_WINDOWS_SPEC.md`](./addons/windows/input/INTERCEPTION_WINDOWS_SPEC.md) | KeyMouse (filter driver + SendSAS, injects below UIPI) | 📋 Specced |
| Win Touch | `win_touch` | Windows | [`windows/input/WIN_TOUCH_WINDOWS_SPEC.md`](./addons/windows/input/WIN_TOUCH_WINDOWS_SPEC.md) | Touch (`InjectTouchInput`; pen→touch with pressure) | 📋 Specced |
| ViGEmBus | `vigem` | Windows | [`windows/input/VIGEM_WINDOWS_SPEC.md`](./addons/windows/input/VIGEM_WINDOWS_SPEC.md) | Gamepad (Xbox 360 virtual controller; signed driver install) | 📋 Specced |
| uinput | `uinput` | Linux | [`linux/input/UINPUT_LINUX_SPEC.md`](./addons/linux/input/UINPUT_LINUX_SPEC.md) | KeyMouse + Gamepad (kernel `/dev/uinput`, X11+Wayland) | 📋 Specced |
| CGEvent | `cgevent` | macOS | [`macos/input/CGEVENT_MACOS_SPEC.md`](./addons/macos/input/CGEVENT_MACOS_SPEC.md) | KeyMouse (`CGEventPost`; needs Accessibility) | 📋 Specced |
| GCVirtual | `gcvirtual` | macOS | [`macos/input/GCVIRTUAL_MACOS_SPEC.md`](./addons/macos/input/GCVIRTUAL_MACOS_SPEC.md) | Gamepad (GCVirtualController, macOS 14+; GameController-framework apps only) | 📋 Specced |

### Where to register a new add-on

When adding a new vendor-specific encoder:
1. Write the spec at `specs/addons/{platform}/encoders/{HW,SW}/{NAME}_SPEC.md`
2. Add a row to the relevant table in **this** section of CENTRAL_SPEC.md
3. Add a row to the compat matrix in `specs/PLATFORM_COMPAT.md`
4. Build as a c-shared library from `internal/encode/{name}/`
5. Wire the runtime probe order in `MODULE_PIPELINE.md`

---

## System Architecture Diagram

> **Note.** The ASCII diagram below is a high-level sketch of the original
> 5-box architecture (Capture / Encode / Audio / Server / Input). There are now
> 18 module specs — the diagram does NOT reflect Clipboard, File Transfer,
> Gamepad, Stream Params, Auth, Network, Native Client, or the addon registry.
> For the authoritative list of modules see the **Module Map** above; for the
> addon tree see **Platform & Add-On Spec Index**; for the dependency graph see
> the **Module Dependency Graph** further down. A rewritten diagram is
> deferred — the prose modules are the source of truth.

```
                    ┌──────────────────────────────────────────────────────────────┐
                    │                   PIPELINE MODULE (Orchestrator)              │
                    │  Probes capabilities, selects backends, manages lifecycle    │
                    │  Frame pacing + drop decisions (floor: 5 FPS)               │
                    └───┬──────────┬──────────┬──────────┬───────────┬────────────┘
                        │          │          │          │           │
        ┌───────────────┘          │          │          │           └───────────┐
        ▼                          ▼          ▼          ▼                       ▼
┌─────────────────┐      ┌──────────────┐ ┌───────────┐ ┌───────────┐  ┌────────────┐
│  CAPTURE MODULE │      │   ENCODE     │ │   AUDIO   │ │  SERVER   │  │   INPUT    │
│                 │      │  (Software)  │ │   MODULE  │ │  MODULE   │  │   MODULE   │
│ NextFrame()     │─────▶│ Convert()    │ │           │ │           │  │            │
│ -> *Frame       │ RGBA │ Encode()     │ │ Chunks()  │ │ HTTP/3+WT │  │ uinput     │
│   (borrowed)    │      │ -> []byte AU │ │ ->[]byte  │ │ Broadcast │  │ injection  │
│                 │      └──────┬───────┘ └─────┬─────┘ └─────┬─────┘  └────────────┘
│ NextSurface()   │──┐         │                │             │
│ -> *FBInfo      │  │  AnxB   │                │ PCM         │
└─────────────────┘  │         │                │             │
                     │         ▼                ▼             │
                     │  ┌──────────────────────────────┐      │
                     │  │       PROTOCOL MODULE        │      │
                     │  │  v1 | 22-byte header         │◀─────┘
                     │  │  Seq + Timestamp + Annex B AU│
                     │  └──────────────┬───────────────┘
                     │                 │
                     │                 ▼
                     │  ┌──────────────────────────────┐
                     │  │       CLIENT (Browser)        │
                     │  │  WebTransport datagrams + streams → decode → canvas │
                     │  │  #control / #view modes      │
                     │  │  Client-side cursor render   │
                     │  └──────────────────────────────┘
                     │
                     │  ┌──────────────────────────────┐
                     └─▶│   HARDWARE ENCODE MODULE     │
                 DMA-BUF │  VA-API zero-copy path       │
                   fd    │  DMA-BUF → VASurface →       │
                         │  encode → Annex B (GPU-only) │
                        └──────────────────────────────┘
```

### Two Encoding Paths (exactly two tiers — no ffmpeg-vaapi middle tier)

```
PATH B — Hardware (zero-copy, GPU-resident)  [preferred]:
    capturer.NextSurface() → FBInfo{fd/IOSurface/D3DTexture, timestamp}
    → hwEncoder.EncodeSurface(fbInfo) → EncodedFrame (GPU→CPU: ~30KB compressed only)
    Use when: HW encoder available AND capturer implements SurfaceCapturer
    cursorMode = "separate" (client-side cursor)

         │  on ErrFallbackToSoftware (DMA-BUF import unsupported, GPU reset, etc.)
         ▼
PATH A — Software (CPU round-trip)  [fallback / [encode] force_addon = "openh264" or "x264"]:
    capturer.NextFrame() → BGRA []byte (GPU→CPU: ~24MB at 1440p)
    → converter.Convert() → I420 (CPU, SIMD libyuv ARGBToI420)
    → encoder.Encode() → []byte Annex B AU + keyframe bool (CPU; OpenH264 CGo or x264 subprocess — VP8/libavcodec/in-process-x264 rejected)
    cursorMode = "embedded" (server-side blend) OR "separate"
```

**Decision (confirmed):** the legacy ffmpeg-`h264_vaapi` subprocess path (which still did a CPU round-trip via `glReadPixels`→libyuv→stdin→`hwupload`) is REMOVED. Hardware = zero-copy `hwencode` module only; software = in-process OpenH264 (VP8/libvpx/libavcodec rejected). There is no third tier and no ffmpeg dependency anywhere.

---

## Module Interface Contracts

### Contract 1: Capture -> Encode

```go
// Capture produces raw pixel frames (BGRA on macOS/Windows, RGBA on Linux GL)
type Capturer interface {
    NextFrame() (*Frame, error)
    Close() error
}

type Frame struct {
    Data      []byte       // Pixel buffer (Stride * Height bytes)
    Stride    int          // Bytes per row; MAY exceed Width*4 (padded readback)
    PixelFmt  PixelFormat  // PixelBGRA (macOS/Windows) or PixelRGBA (Linux GL)
    Width     int          // pixels
    Height    int          // pixels
    Timestamp uint64       // CLOCK_MONOTONIC nanoseconds
}

type PixelFormat uint8
const (
    PixelBGRA PixelFormat = iota  // BGRA in memory = libyuv ARGB → use ARGBToI420
    PixelRGBA                     // RGBA in memory = libyuv ABGR → use ABGRToI420
)
```

**Data Flow:** `capturer.NextFrame()` -> `converter.Convert(frame)` -> `encoder.Encode(i420Frame)`
- Converter selects `libyuv.ARGBToI420` (BGRA) or `libyuv.ABGRToI420` (RGBA) based on `frame.PixelFmt`.

**Contract Rules:**
- `Frame.Data` is BORROWED — only valid until the next `NextFrame()` call. Caller must copy before calling again.
- Dimensions must remain stable across frames (no mid-stream resize without signaling)
- Timestamp must be monotonically increasing (sourced from `CLOCK_MONOTONIC`)
- `NextFrame()` may return `nil, nil` to indicate "no new frame available" (frame pacing / static screen optimization)

---

### Contract 2: Encode -> Server

```go
// Encode produces ONE contiguous Annex B access unit + a keyframe flag.
type Encoder interface {
    Encode(frame *I420Frame) (data []byte, keyframe bool, err error)
    ForceKeyframe()
    Close() error
}
```

**Data Flow:** `encoder.Encode(frame)` returns `(data, keyframe)` -> pipeline wraps as `EncodedFrame{Data, Keyframe, …}` -> `server.Broadcast(codecType, EncodedFrame)`

**Contract Rules:**
- `nil, false, nil` return means the frame was skipped (no error, no output).
- First frame after `ForceKeyframe()` MUST be a keyframe (H.264: SPS+PPS+IDR; HEVC: VPS+SPS+PPS+IDR).
- `data` is **ONE complete access unit**, contiguous Annex B (start codes retained), **NOT** split per-NAL. The old `[][]byte` per-NAL contract is rejected.
- `keyframe` is set BY THE ENCODER (it knows when it emitted an IDR/IRAP); the server never re-scans NALs.
- `data` is **borrowed from a `sync.Pool`** — it must NOT be retained past the next `Encode()` call. `server.Broadcast` copies it into the per-session frame-granular out-queue before the loop calls `Encode()` again.

---

### Contract 3: Server -> Client (Wire Protocol)

See [`MODULE_PROTOCOL.md`](./core/MODULE_PROTOCOL.md) for the authoritative definition. Summary:

```
[Header: 22 bytes][Payload: variable]

Header layout (little-endian):
  Byte 0:      Version (uint8, =1)
  Byte 1:      Type (uint8)
  Bytes 2-5:   Sequence (uint32, server-owned, per-type)
  Bytes 6-13:  Timestamp (uint64, CLOCK_MONOTONIC ns, stamped at capture)
  Bytes 14-15: Width (uint16)
  Bytes 16-17: Height (uint16)
  Bytes 18-21: PayloadSize (uint32)
```

**Frame Types (all server → client):**
The 22-byte FrameHeader is **media-only** now (datagram fragment 0 + the bootstrap
stream). Config and Clipboard are NOT FrameHeader types anymore.

| Type | Value | Channel | Payload |
|------|-------|---------|---------|
| VideoH264 | 1 | datagram + bootstrap | One H.264 access unit, Annex B (keyframe = SPS+PPS+IDR type 5) |
| Ping | 2 | datagram | 8-byte nonce |
| _(reserved)_ | 3 | — | Reserved (client Pong is a JSON line on the **control stream**) |
| AudioPCM | 4 | datagram media | Raw S16LE PCM, fragmented (the no-codec fallback). Carries the FrameHeader for capture Timestamp; codec/rate/channels in `config`. (impl deferred) |
| _(reserved)_ | 5 | — | Formerly VideoVP8 — VP8 rejected. Do not reuse without protocol version bump. |
| _(retired)_ | 6 | — | Was Config — now a `{"type":"config"}` JSON line on the control stream |
| VideoHEVC | 7 | datagram + bootstrap | One HEVC access unit, Annex B (keyframe = VPS+SPS+PPS+IDR types 19-20) |
| AudioOpus | 8 | datagram media | One 20 ms Opus packet, single datagram (default audio codec). Carries the FrameHeader for capture Timestamp. (impl deferred) |
| CursorUpdate | 11 | datagram | Cursor position + optional image (client-side cursor) |
| _(retired)_ | 12 | — | Was Clipboard — now `[u32 Len][JSON]` on the **clipboard stream** |
| InputAck | 14 | input stream | 13-byte echo of client input seq + server timestamp (RTT) |
| GamepadRumble | 15 | datagram | 9-byte rumble payload (index + magnitudes + duration; see [`./interaction/MODULE_GAMEPAD.md`](./interaction/MODULE_GAMEPAD.md)) |

One datagram fragment-chain carries exactly one access unit; fragmentation is purely byte-level within that AU (NALs are never reordered or dropped individually).

---

### Contract 4: Client -> Server (per-stream framing)

Client→server messages are discriminated by **which stream** they arrive on
(every stream's first byte is a StreamType tag — see
[`MODULE_TRANSPORT.md`](./core/MODULE_TRANSPORT.md), [`MODULE_PROTOCOL.md`](./core/MODULE_PROTOCOL.md),
[`MODULE_INPUT.md`](./interaction/MODULE_INPUT.md)):

- **Input stream (tag 0x01):** `[u16 RecLen]`-prefixed binary input records
  (6-byte record header + payload, types `0x01-0x4F`). Binary for performance +
  security (~121× faster decode, zero-alloc, smaller attack surface). Type `0x50`
  is **reserved** for a future webcam frame and is dropped by current binaries.
- **Control stream (tag 0x00):** newline-delimited JSON, rare human-triggered control.
- **Clipboard stream (tag 0x02):** `[u32 Len][JSON]` clipboard messages (off the
  control stream because payloads reach 1 MiB).

```
INPUT  [u16 RecLen][Version=1][Type][Seq u32][…record…]  // input stream (MODULE_INPUT)
CTRL   {"type":"keyframe"}                                 // control stream — request IDR
CTRL   {"type":"resize","width":1280,"height":720}         // control stream
CTRL   {"type":"pong","nonce":12345}                       // control stream
CLIP   {"type":"clipboard","format":"text/plain","text":"…"} // clipboard stream
```

**Coordinate-space rule:** absolute pointer `X`/`Y` are in the **stream
coordinate space** advertised by the latest `config` message (`width`/`height`). The
injector's absolute range MUST equal that range. Capture, encode, Config, and
input dims must all agree (no hidden scaling); the pipeline calls
`Dispatcher.Resize` on a resolution change.

---

### Contract 5: Audio -> Server  🔒 DESIGN LOCKED · ⏸️ IMPL DEFERRED (see MODULE_AUDIO)

```go
// Audio: a per-OS capture add-on delivers PCM chunks; an AudioEncoder (Opus or
// PCM passthrough) turns them into wire payloads. host→client only. No subprocess.
type AudioCapturer interface {
    Chunks() <-chan PCMChunk
    Format() Format          // canonical 48k/stereo (add-on resamples to this)
    Close() error
}

type PCMChunk struct {
    Data      []byte // FrameSamples*Channels*2, S16LE interleaved (3840 B @ 20 ms)
    Timestamp uint64 // CLOCK_MONOTONIC ns, sampled AT CAPTURE in the add-on read loop
}
```

**Contract Rules:**
- `Timestamp` MUST be sampled at capture time (in the capture add-on's read loop), NOT when the pipeline reads it from the channel — stamping late breaks A/V sync. Buffers are small (~60-80 ms capture, ~40 ms client) for realtime.
- `Timestamp` uses the SAME `CLOCK_MONOTONIC` epoch as video frames. **Audio is the master clock**; video presentation slaves to the audio playout time (see MODULE_AUDIO / MODULE_PROTOCOL "A/V Synchronization").
- The encoder output is borrowed from a `sync.Pool` (copy before reuse); audio is a **media** datagram type (carries the FrameHeader), single-datagram for Opus.

---

### Contract 6: Capture -> Hardware Encode (Zero-Copy Path)

```go
// SurfaceCapturer is the cross-platform zero-copy contract. Capture add-ons
// that can produce GPU surfaces (KMS+EGL DMA-BUF, NvFBC CUDA buffer, SCK
// IOSurface, DXGI DD ID3D11Texture2D) implement this in addition to Capturer.
//
// The HW encoder add-on reads the populated per-OS field of FBInfo to
// determine which platform-specific import path to take.
type SurfaceCapturer interface {
    Capturer

    // NextSurface returns a GPU-resident surface handle. Ownership passes to the
    // HW encoder: EncodeSurface calls fb.Release() exactly once on every path
    // (M-1). The caller releases it ONLY if it is never handed to an encoder.
    NextSurface() (*FBInfo, error)
}

// FBInfo is the platform-specific surface handle. Only the field for the
// current OS is populated. HW encoder add-ons read the appropriate field.
type FBInfo struct {
    // Generic fields (always set)
    Width, Height int
    Timestamp     uint64       // CLOCK_MONOTONIC ns, stamped at capture
    Release       func()       // Platform-specific cleanup (close DMA-BUF fd, release IOSurface, etc.)

    // Linux fields (set when platform == "linux")
    DMAFD    int    // File descriptor (caller transfers ownership)
    Stride   int
    Format   uint32 // DRM fourcc (e.g., DRM_FORMAT_XRGB8888)
    Modifier uint64 // Tiling/compression modifier

    // macOS field (set when platform == "darwin")
    IOSurface uintptr // CVPixelBufferRef (CMSampleBuffer-backed)

    // Windows field (set when platform == "windows")
    D3DTexture uintptr // ID3D11Texture2D*
}

// HardwareEncoder is the contract every HW encoder add-on satisfies.
// See specs/media/MODULE_HARDWARE_ENCODE.md for the full contract.
type HardwareEncoder interface {
    // EncodeSurface takes a GPU-resident surface handle and returns ONE
    // contiguous Annex B access unit (EncodedFrame.Keyframe set by the encoder).
    // The encoder calls handle.Release() exactly once on EVERY return path
    // (success, error, fallback). Returns ErrFallbackToSoftware if the handle
    // cannot be imported (still releasing it).
    EncodeSurface(handle *FBInfo) (*EncodedFrame, error)

    // ForceKeyframe requests that the next encoded frame be an IDR.
    ForceKeyframe()

    // Codec returns the WebCodecs codec string for the Config handshake
    // (e.g. "avc1.42E01F" for H.264 Constrained Baseline 3.1).
    Codec() string

    // Close releases all encoder resources.
    Close() error
}
```

**Data Flow:** `capturer.NextSurface()` -> `hwEncoder.EncodeSurface(handle)` (which calls `handle.Release()` internally, exactly once) -> `server.Broadcast(codecType, *encodedFrame)`

**Contract Rules:**
- `FBInfo` ownership is transferred from the capture add-on to the HW encoder add-on. The HW encoder calls `handle.Release()` **exactly once on every path** (success/error/fallback); the capturer and pipeline never release it (single-owner rule, M-1).
- If hardware encode returns `ErrFallbackToSoftware`, pipeline degrades permanently to software path for the rest of the session.
- Codec advertisement: the HW encoder advertises its codec via `Codec()`; the pipeline matches against browser handshake preferences.

---

### Contract 7: Pipeline -> Server (Encoded Frame)

```go
// The pipeline pairs encoder output with the frame's metadata before broadcasting.
// EncodedFrame lives in pkg/stream/ -- shared by SW and HW paths.
type EncodedFrame struct {
    Data      []byte // Contiguous Annex B bitstream (start codes retained).
                     // NOT split into per-NAL slices -- avoids decompose/recompose
                     // copy overhead. The server prepends the 22-byte FrameHeader
                     // and copies Data into the per-session frame-granular
                     // out-queue (the pump fragments it into datagrams later).
    Width     uint16
    Height    uint16
    Timestamp uint64 // CLOCK_MONOTONIC ns, carried through from capture
    Keyframe  bool   // true if this access unit is a keyframe
    CodecType uint8  // FrameTypeVideoH264 or FrameTypeVideoHEVC
}

type Server interface {
    // Broadcast assembles one access unit and fans it out to per-session queues.
    // codecType is FrameTypeVideoH264 or FrameTypeVideoHEVC.
    // The server assigns the video Sequence and uses f.Keyframe (encoder-set)
    // for IDR caching + the bootstrap stream.
    Broadcast(codecType uint8, f EncodedFrame)
    // Already-encoded audio (Opus packet or raw PCM). codecType = AudioOpus(8) |
    // AudioPCM(4); server assigns the independent audio Sequence + FrameHeader.
    BroadcastAudio(codecType uint8, payload []byte, captureTs uint64)
    // ...
}
```

**Contract Rules:**
- The pipeline carries `Width/Height/Timestamp` from the capture step through encode to here (they are NOT recomputed).
- The server owns the per-type sequence counters; the pipeline never sets them.
- `f.Keyframe` (set by the encoder) lets the server cache the keyframe access unit (SPS+PPS+IDR / VPS+SPS+PPS+IDR) for the bootstrap stream. The server does **not** re-parse NALs (M-2).

---

## Module Dependency Graph

```
                 pkg/stream (shared types: Params, EncodedFrame, error sentinels)
                    │
    ┌───────────────┼───────────────────────────────────┬──────────┐
    │               │               │           │       │          │
    ▼               ▼               ▼           ▼       ▼          ▼
 capture         encode          hwencode    server  config   transport
    │            │    │          │    │         │ │              │
    │            │    └──────────┘    │         │ ├── protocol ◀─┘ (close codes)
    │            │     (hwencode      │         │ ├── auth
    │            │      imports       │         │ ├── stream
    │            │      capture       │         │ └── transport (Transport/Session)
    │            └── capture          │
    │            (Converter takes     │
    │             *capture.Frame)     │
    ▼                                 ▼
 (per OS,                          (per OS,
  add-on)                           add-on)

              pipeline (imports ALL core interfaces + builds transport + probes add-ons)
                │
    ┌───────────┼───────────┼───────────┼───────────┼───────────┐
    ▼           ▼           ▼           ▼           ▼           ▼
 capture    encode      hwencode     server      config     transport
```

> Import edges documented:
> - `encode -> capture` (Converter takes `*capture.Frame`)
> - `hwencode -> capture` (`SurfaceHandle = capture.FBInfo`)
> - `encode, hwencode, capture -> stream` (Params, error sentinels)
> - `transport -> protocol` (transport references `protocol.Close*`; protocol is the sole owner of the close codes)
> - `server -> {transport, protocol, auth, stream, input, clipboard, filetransfer}` (transport surface + types + auth gate + input dispatch + clipboard/file-transfer stream dispatch)
> - `pipeline -> transport` (the pipeline builds the QUIC transport via `transport.New` and hands it to the server)
> - `input` injection add-ons -> `input` core (KeyMouseInjector/TouchInjector + HID table)
> - `clipboard` is a core leaf (per-OS files); `filetransfer -> transport` (its `ServeStream` takes a `transport.Stream`); `server` calls both
> - No cycles. `stream` is the shared leaf. `pipeline` is the sole orchestrator.
> - Audio not shown — deferred from the core dependency graph (Webcam was removed entirely).
> - No `ffmpeg`, no `libavcodec`, no `libvpx` -- all rejected.
> - No custom `logger` module -- every module takes `*slog.Logger` directly.

**Key Properties:**
- Each domain module is a leaf or near-leaf (depends only on stdlib + system libs via CGo)
- Modules NEVER import each other (zero import cycles)
- Only `pipeline` imports all modules — it's the sole wiring point
- `protocol` is shared between `server` and `client` (pure data, no logic deps)
- `hwencode` and `encode` are sibling modules, not parent-child (both define separate interfaces; add-ons implement one)

---

## Orchestrator Responsibilities (Pipeline Module)

The orchestrator is now a proper module (`MODULE_PIPELINE.md`) — not inline in main.go. It:

1. Loads config via `config.Load(--config path)` per [`./core/MODULE_CONFIG.md`](./core/MODULE_CONFIG.md) — the only CLI flag is `--config`
2. Probes loaded capture + encoder add-ons (no static enum; the runtime asks each loaded add-on whether its prerequisites are met)
3. Selects capture add-on per `[capture]` config (auto-probe order or forced)
4. Selects encode path per `[encode]` config:
   - **Hardware add-on** picked when it accepts the zero-copy surface handle produced by the selected capture add-on (DMA-BUF / IOSurface / D3D11 texture)
   - **Software add-on** picked when no compatible HW add-on is loaded OR `force_addon` names a SW add-on
5. Creates and connects all modules
6. Manages lifecycle (signal handling, graceful shutdown, SIGHUP config reload)
7. Runs the frame pipeline loop with pacing and drop logic
8. Enforces 5 FPS minimum floor under all conditions
9. Exports rolling-window statistics via Prometheus on the metrics port (see `[metrics]` in MODULE_CONFIG)

---

## Cross-Cutting Concerns

### Error Handling Strategy
- Modules return errors; orchestrator decides recovery strategy
- Transient errors (capture hiccup, encode skip): log and continue
- Fatal errors (device lost, context cancelled): propagate for shutdown
- Never panic in hot path

### Buffer Ownership Model
- **Capture (CPU):** Returned `Frame.Data` is BORROWED — valid only until the next `NextFrame()` call. Caller must copy if retaining.
- **Capture (surface):** `NextSurface()` returns an `*FBInfo` whose `Release()` is called **exactly once** by the HW encoder's `EncodeSurface` on every path (success/error/fallback). The capturer and pipeline never release it (single-owner rule, M-1).
- **Convert:** Returned `*I420Frame` is BORROWED — valid only until the next `Convert()` call. Zero-alloc steady state.
- **Encode:** Returned `data []byte` is ONE contiguous Annex B access unit, BORROWED from a `sync.Pool` — must NOT be held past the next `Encode()` call. The encoder also returns a `keyframe bool`. (The old "`[][]byte` NALs, freshly allocated, safe to hold" contract is rejected.)
- **Broadcast:** Server assembles header+payload into one `[]byte` access unit and copies it into the per-session **frame-granular** out-queue; the per-session pump fragments it into datagrams at send time.

**Rule:** Any function that returns borrowed data must document it in the interface comment. The caller must never store borrowed slices beyond the next call boundary.

### Concurrency Model
- Capture loop: single goroutine, `runtime.LockOSThread()` (X11/EGL requirement)
- Encode: synchronous call within capture goroutine (frame drops preferred over pipeline latency)
- Server broadcast: fan-out via per-session frame-granular out-queues (drop-oldest); a per-session pump fragments + sends datagrams
- Audio: separate goroutine with channel delivery
- Input: synchronous handler in server's read goroutine

### Frame Drop Strategy
- The system prioritizes realtime delivery over frame completeness
- If encode takes longer than the frame interval, the NEXT capture is skipped (not queued)
- Minimum floor: 5 FPS — the system will never drop below 5 FPS regardless of load
- Frame sequence numbers in the protocol allow clients to detect drops and request IDR if needed
- A dropped frame never enters the encode pipeline — it's discarded at capture

### Canonical Media Clock (A/V Sync)
- A single `CLOCK_MONOTONIC` epoch is established at process start.
- EVERY media timestamp on the wire — every video frame from every capture backend, and every audio chunk — is sampled from this clock in nanoseconds, AT CAPTURE TIME.
- **Audio is the master clock.** When audio is present the client plays it gaplessly from a small (~40 ms) buffer and presents the **video** frame nearest the audio playout time (video holds/drops to track audio). Audio is never held/dropped for sync. See MODULE_PROTOCOL / MODULE_AUDIO. (No audio ⇒ video presents on its own capture clock.)
- The logger uses wall-clock (UTC) for human-readable lines; this is a SEPARATE clock and must never be used for media timestamps.
- Anti-pattern (current code, to be removed): `time.Now().UnixMilli()` for frame/audio timestamps — wrong clock domain and wrong unit.

### Cursor Model
- `Config.cursorMode` tells the client how the cursor is delivered:
  - `"embedded"` — cursor is alpha-blended into the video frame server-side (software path default).
  - `"separate"` — cursor is NOT in the video; the server sends `CursorUpdate` (position every frame, image only on change) and the client renders it as an overlay. Required for the zero-copy hardware path (frame never touches CPU); lowest latency.
- The client MUST handle both modes based on the handshake.

### Connection / Join Flow (no keyframe storm)
```
client opens control stream (tag 0x00) + auths
  → server sends {"type":"config"} JSON line on the control stream
  → server opens a bootstrap stream (tag 0x10) and writes the cached IDR (if present)
  → if NO cached IDR exists: server forces a keyframe; that first IDR goes on the bootstrap stream
  → otherwise: NO forced keyframe (the cached IDR is self-contained: SPS+PPS+IDR)
  → client decodes the bootstrap IDR (reliable); sets lastSeq = its seq
  → live datagram frames flow; gap detection starts from the 2nd live datagram frame
```
- The bootstrap stream is reliable, so the join keyframe is always decodable even though live video is lossy datagrams.
- A join NEVER restarts the capturer (the old `capturer.Restart()` behavior is removed).
- Keyframe requests (from gap detection or join) are rate-limited by the server (e.g., max 1 forced IDR per 500 ms) to prevent storms when many clients join at once.

### Resolution-Change Flow
```
capturer detects resolution change (monitor hotplug / mode switch)
  → NextFrame/NextSurface returns new Width/Height (pipeline detects by comparison)
  → pipeline: applyParams on the frame loop — reconfigure/rebuild encoder, call input.Resize(w,h)
  → server: send a fresh {"type":"config"} message (new dims) + force a keyframe
  → client: reconfigure VideoDecoder, update input coordinate scaling
```
The pipeline owns this orchestration; no module drives it alone.

### Configuration
- Single TOML config file at a known OS-conventional path; only `--config <path>` CLI flag exists. Full schema in [`./core/MODULE_CONFIG.md`](./core/MODULE_CONFIG.md).
- Compile-time constants for protocol parameters (Version byte, header layout).
- Runtime capability probing for loaded add-on detection.
- Hot reload via `SIGHUP` (Linux/macOS) — most sections reload without restart; TLS / port / `force_addon` need a restart (marked in MODULE_CONFIG).

---

## Refactoring Principles

1. **Interface-First:** Every module exposes a Go interface. Implementations are private.
2. **Zero Import Cycles:** Modules never import each other (only the orchestrator imports all).
3. **Testable in Isolation:** Each module has unit tests that run without hardware.
4. **Swappable (restart-scoped in v1):** Changing a capture or encoder add-on is a config change (`[capture] force_addon`, `[encode] force_addon`) or swapping the add-on library in the add-ons directory, **then a restart** — never a code change in the pipeline. (v1 resolves the add-on set once at startup; live reload is out of scope.)
5. **Error Propagation:** All errors flow up to the orchestrator with context (`fmt.Errorf("capture: %w", err)`).
6. **No Global State:** No package-level mutable variables except the add-on registry, which the dlopen loader populates at startup from the add-ons directory.
7. **Explicit Lifecycle:** Every module has `New()` (create), optional `Start()` (begin work), and `Close()` (cleanup).
8. **Buffer Contracts:** Document whether returned slices are owned or borrowed.

---

## File Structure (Post-Refactor Target)

```
featherdesk/
├── cmd/
│   └── server/
│       ├── main.go              # config.Load() → pipeline.New() → pipeline.Start()
│       └── client/              # Embedded web client (//go:embed all:client)
│           ├── index.html
│           └── compositor.js    # Single bundle today; future modular split deferred
├── pkg/                         # Public interfaces (importable by future native clients)
│   ├── capture/
│   │   └── capture.go          # Capturer + zero-copy surface handle abstraction
│   │                           #   (DMA-BUF / IOSurface / D3D11 texture)
│   ├── encode/
│   │   └── encode.go           # Encoder interface + I420Frame + EncoderConfig
│   ├── hwencode/
│   │   └── hwencode.go         # HardwareEncoder interface + per-OS surface types
│   ├── stream/
│   │   └── stream.go           # Params, EncodedFrame, error sentinels (shared leaf)
│   ├── input/
│   │   ├── input.go            # KeyMouseInjector/TouchInjector + Event + Dispatcher
│   │   └── hid.go              # Shared HID-usage tables (neutral keycode contract)
│   ├── protocol/
│   │   └── protocol.go         # Wire types + marshal/unmarshal (v1, 22-byte header)
│   ├── addon/
│   │   ├── abi.go              # C-ABI contract: FeatherDeskAddonOpen sig, ABIVersion,
│   │   │                       #   capability descriptor + vtable structs, error-code enum
│   │   └── featherdesk_addon.h # Generated C header every add-on builds against
│   └── config/
│       └── config.go           # TOML schema types + Load + Watch
├── internal/                    # Private implementations
│   ├── addon/
│   │   ├── loader_unix.go      # dlopen scan + ABI check + vtable→Go-interface adapters
│   │   └── loader_windows.go   # LoadLibraryW equivalent
│   ├── capture/
│   │   ├── kms/                # KMS+DRM+EGL Linux capture add-on (add-on ID: kms_egl)
│   │   ├── nvfbc/              # NvFBC Linux capture add-on (add-on ID: nvfbc)
│   │   ├── sck/                # ScreenCaptureKit macOS capture add-on (add-on ID: sck)
│   │   └── cursor/             # Cursor compositing (software) + cursor protocol (hardware)
│   ├── encode/
│   │   ├── openh264/           # OpenH264 SW encoder add-on (add-on ID: openh264)
│   │   ├── libva/              # libva Linux HW add-on (add-on ID: libva)
│   │   ├── nvenc/              # NVENC HW add-on (add-on ID: nvenc)
│   │   ├── amf/                # AMD AMF HW add-on (add-on ID: amf)
│   │   ├── qsv/                # Intel oneVPL HW add-on, Windows (add-on ID: qsv)
│   │   ├── vt/                 # VideoToolbox macOS SW+HW add-on (add-on IDs: vt_sw, vt_hw)
│   │   ├── mf/                 # MediaFoundation Windows HW add-on (add-on ID: mf_hw)
│   │   └── convert/            # libyuv color conversion (used by every SW encoder add-on)
│   ├── input/                 # Input injection add-ons (zero-by-default → view-only)
│   │   ├── interception/      # Windows filter driver + SendSAS (add-on ID: interception)
│   │   ├── wintouch/          # Windows Touch Injection (add-on ID: win_touch)
│   │   ├── vigem/             # Windows ViGEmBus gamepad (add-on ID: vigem)
│   │   ├── uinput/            # Linux /dev/uinput (kbd/mouse + gamepad) (add-on ID: uinput)
│   │   ├── cgevent/           # macOS CGEventPost (add-on ID: cgevent)
│   │   └── gcvirtual/         # macOS GCVirtualController gamepad (add-on ID: gcvirtual)
│   ├── clipboard/             # CORE clipboard sync (per-OS files behind build constraints)
│   │   ├── clipboard.go        # Monitor interface, Content, sanitization
│   │   ├── clipboard_windows.go # AddClipboardFormatListener + CF_HTML
│   │   ├── clipboard_linux.go   # XFixes / wlr-data-control
│   │   └── clipboard_darwin.go  # NSPasteboard changeCount polling
│   ├── filetransfer/          # CORE file transfer (carried on the main WebTransport session)
│   │   ├── service.go          # Transfer service, windowed flow control, SHA-256
│   │   └── sandbox.go          # Fixed-folder path-traversal guard
│   ├── transport/             # HTTP/3 + WebTransport server (QUIC) — quic-go + webtransport-go
│   │   └── transport.go        # Transport interface impl, session accept loop
│   ├── server/
│   │   ├── server.go           # Per-session orchestration (consumes transport.Session)
│   │   ├── session.go          # Per-session goroutines (control, input, datagram pump, stream acceptor)
│   │   ├── auth.go             # First-frame auth on the control stream
│   │   └── metrics.go          # Prometheus /metrics handler (separate port)
│   ├── config/
│   │   ├── load.go             # TOML parse + validate
│   │   └── watch.go            # SIGHUP / Service Control hot reload
│   └── pipeline/
│       ├── pipeline.go         # Pipeline struct, Start(), shutdown
│       ├── frameloop.go        # Main frame loop, pacing, drop logic
│       ├── probe.go            # Add-on probe + selection (capture/encode/input)
│       └── stats.go            # Rolling-window statistics + Prometheus metric registration
│
│  (audio/ subdir deferred — added when the audio module is un-paused)
├── specs/                       # This spec directory
├── go.mod
├── go.sum
└── Makefile                     # Per-platform targets: builds each add-on as a c-shared library
```

> `internal/logger/` is **gone** — replaced by stdlib `log/slog`. Every module
> that needs a logger takes `*slog.Logger` in its constructor.

---

## Known Technical Debt (Current Codebase)

> Many of the original TDs referenced files that are being **deleted entirely**
> as part of the architecture refactor (`x11grab.go`, `screencast.py`,
> `ffmpeg.go`, `vp8.go`, `vaapi.go`, `internal/logger/`). Those TDs are marked
> *obsolete* — the issue is resolved by deletion, not refactor.

| ID | Severity | Location | Issue | Resolution |
|----|----------|----------|-------|------------|
| TD-01 | High | `kms.go:99-111` | DMA-BUF fd leak on EGL import failure | Fold into `KMS_EGL_LINUX_SPEC.md` known-issues section; fix during kms add-on extraction |
| TD-02 | ~~High~~ obsolete | `x11grab.go:113` | Recursive retry without limit | `x11grab.go` being deleted (subprocess capture rejected) |
| TD-03 | High | `egl.go:22-25` | Static C globals prevent thread safety | Fold into `KMS_EGL_LINUX_SPEC.md` known-issues; fix during extraction |
| TD-04 | ~~High~~ obsolete | `ffmpeg.go:240` | ForceKeyframe stores flag but never signals ffmpeg | `ffmpeg.go` being deleted (subprocess encoders rejected) |
| TD-05 | Medium | `main.go:158` | Hardcoded 2560x1440 for input device | Resolved by MODULE_INPUT — Dispatcher.Resize follows stream dims, pipeline derives from capture |
| TD-06 | Medium | `compositor.js:22+292` | Duplicate init() function (dead code) | R-CLI-01 |
| TD-07 | ~~Medium~~ obsolete | `server.go:148+client.js` | Codec type mismatch (H264 constant for VP8 data) | VP8 rejected; mismatch source eliminated |
| TD-08 | Medium | `audio/capture.go` | Race condition on cmd/stdout fields | Deferred — Audio module deferred per TECHSTACK |
| TD-09 | ~~Medium~~ obsolete | `x11grab.go:165` | Hardcoded developer path `/home/aseem/...` | `x11grab.go` being deleted |
| TD-10 | Medium | `protocol.go` | No version/sequence in wire protocol | Fixed in new protocol spec (v1, 22-byte header) |
| TD-11 | Medium | `input/protocol.go:23-51` | All Inject errors silently discarded | Resolved by MODULE_INPUT — Dispatcher.Dispatch returns errors; server logs at warn |
| TD-12 | Low | `server.go:286-306` | Custom itoa() reimplements strconv | R-SRV-03 |
| TD-13 | Low | `kms.go:39` | fps parameter accepted but unused | Fold into `KMS_EGL_LINUX_SPEC.md` (orchestrator handles pacing externally) |
| TD-14 | Low | `main.go:176` | Unbounded stats slice grows forever | R-PIP-02 (rolling window) + Prometheus export |

### New Issues Found During Design Review

| ID | Severity | Location | Issue | Resolution |
|----|----------|----------|-------|------------|
| TD-15 | High | Architecture | GPU→CPU→GPU round-trip on hardware encode path | MODULE_HARDWARE_ENCODE (zero-copy) |
| TD-16 | High | Protocol | No A/V sync mechanism | Protocol v1: shared CLOCK_MONOTONIC timestamps |
| TD-17 | Medium | Server | IDR cache missing SPS/PPS (undecodable by new clients) | R-SRV IDR cache update |
| TD-18 | Medium | Protocol | Multiple NALs per frame need grouping into one access unit | One message per frame, Annex B concatenation (TD-23); length-prefix rejected (TD-28) |
| TD-19 | Medium | Client | Always requests ?role=control (no viewer mode) | R-CLI role selection via URL hash |
| TD-20 | Medium | Architecture | No orchestrator spec (complex wiring logic undocumented) | MODULE_PIPELINE.md |
| TD-21 | Low | Server | No connection handshake (client guesses codec) | R-PRO-01: `{"type":"config"}` control-stream message on connect |
| TD-22 | Low | Pipeline | No frame drop strategy (unbounded latency under load) | Pipeline: 5 FPS floor + skip logic |

### Round-2 Review Findings (verified against source)

| ID | Severity | Location | Issue | Resolution |
|----|----------|----------|-------|------------|
| TD-23 | High | `server.go:149-178` | Broadcast sends ONE message PER NAL → multi-NAL H.264 yields partial access units; breaks WebCodecs | One message per frame, concatenate NALs (Annex B) |
| TD-24 | High | `server.go:164-168` | IDR cache stores only the IDR NAL; SPS/PPS (separate messages) lost → undecodable | Cache whole per-frame keyframe message (contains SPS+PPS+IDR) |
| TD-25 | High | `main.go:295` (video timestamping in main loop) | Video + Audio stamped at consumption with wall-ms; spec required monotonic-ns at capture → A/V sync impossible | Canonical CLOCK_MONOTONIC ns, stamped at capture by the capture add-on; the audio PCMChunk carries the capture timestamp (audio design locked; impl deferred) |
| TD-26 | High | `main.go:249-252` | New-client handler forces keyframe + `capturer.Restart()` (respawns capture) → storm for all viewers | Serve cached IDR; conditional keyframe; never restart capture; rate-limit |
| TD-27 | Med | `main.go:158` | Input device hardcoded 2560×1440 ≠ stream dims → cursor offset | Resolved by MODULE_INPUT — input dims = stream dims; Dispatcher.Resize on resolution change |
| TD-28 | Med | Protocol/round-1 | Length-prefix NAL framing added client AVCC complexity for no browser benefit | Reverted to Annex B per-frame concatenation |
| TD-29 | Med | Pipeline (round-1 spec) | Frame loop discarded W/H/timestamp; `continue` didn't skip capture; dead frameSeq | EncodedFrame struct; skip-before-capture; server owns sequence |
| TD-30 | Med | hwencode (round-1 spec) | Duplicate `config` field; non-existent `vaCreateSurfaceFromFD` | Renamed `vaConfig`; use `vaCreateSurfaces`+ExternalBuffers |
| TD-31 | Med | Client (round-1 spec) | Config described as JSON text vs binary frame 6; codec "h264" too short for WebCodecs | Config = `{"type":"config"}` JSON on the control stream (binary type 6 retired in the QUIC switch), full codec string |
| TD-32 | Med | Protocol/Input | InputAck had nothing to echo (no input seq) | Input messages carry `seq`; server echoes in 13-byte InputAck |
| TD-33 | Low | Protocol | KeyframeReq/Resize as binary types vs JSON client channel | Keyframe via control-stream JSON; Resize via a fresh `config` message |
| TD-34 | Low | Pipeline (round-1 spec) | `FramesCaptures` typo; unused `minInterval`; undefined Stats methods | Corrected in MODULE_PIPELINE |
