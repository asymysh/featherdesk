# FeatherDesk - Central Architecture Specification

## Product Overview

**Name:** FeatherDesk (binary: `featherdesk`)
**Type:** Low-latency remote desktop streaming server (Linux primary, Windows + macOS planned)
**Language:** Rust (edition 2021), async via Tokio; FFI to platform + vendor C/C++/ObjC SDKs
**Deployment:** Single host binary with embedded web client + optional add-on shared libraries (capture / encode / input / audio), loaded at runtime
**Target:** Parsec/Sunshine-level latency on LAN

## Product Goals

- Motion-to-photon latency: <20 ms on LAN at 1080p60, audio disabled; ~65 ms with audio enabled — see "Motion-to-photon budget"
- Resolution: up to 2560x1440
- Framerate: 60fps (hardware), 30fps (software fallback)
- Bandwidth: 5-15 Mbps/viewer
- Memory: <50MB RSS
- Concurrent viewers: up to 25 (1 controller + 24 passive)

---

## Implementation Language & Conventions (Rust)

FeatherDesk is implemented in **Rust** (edition 2021). Rust was chosen over Go
specifically because the **add-on model is the core of the architecture**, and a
Go host cannot cleanly `dlopen` Go add-ons (two Go runtimes + GC in one process,
no Go values across the boundary). Rust has **no runtime and no GC**, so a
runtime-loaded add-on behaves like a normal native library — validated by a
working spike (host scans a dir, loads a `cdylib`, frame buffers + error codes
cross the boundary deterministically). The same spike confirmed the QUIC/
WebTransport stack and C-SDK FFI build and run.

These conventions are global; every module spec below assumes them.

| Concern | Choice |
|---------|--------|
| Edition / toolchain | Rust edition 2021, stable toolchain |
| Async runtime | **Tokio** (multi-threaded). Server, transport, frame loop, audio loop are async tasks. |
| Errors | `Result<T, E>` with `thiserror`-derived enums per crate. **Across the add-on ABI**, errors cross as a stable `u32` code (`RResult<_, u32>`) the host maps back to its own error enum. |
| Logging / tracing | **`tracing`** + `tracing-subscriber` (text in a TTY, JSON otherwise; level/output from `[log]`). Replaces the old `slog` logger. |
| Concurrency primitives | `tokio::sync::mpsc` (param funnel, bound 32), `tokio::sync::watch` (applied params, config reload, clipboard changes), `tokio::sync::Notify` + `crossbeam_queue::ArrayQueue` (per-session media rings — the only structure that can do producer-side drop-oldest), `arc_swap` (controller slot, authenticator, session defaults), `Arc<Mutex<…>>` / `Arc<RwLock<…>>` for shared state. No global mutable state. |
| Buffers | `bytes::Bytes` / `BytesMut` internally (cheap clones, zero-copy slicing). Across the ABI: `abi_stable::std_types::RVec<u8>` (ownership transfers, deterministic drop). |
| Wire (de)serialization | Frame headers / binary records: manual `bytes`-based encode/decode. JSON control messages: **`serde` + `serde_json`**. |
| Add-on ABI | **`abi_stable`** — stable-ABI `cdylib`s, `#[sabi_trait]` objects, layout+version checked on load. (See "Pluggable Architecture".) |
| Platform / vendor FFI | Windows: **`windows`** (windows-rs). macOS: **`objc2`** + ScreenCaptureKit/VideoToolbox bindings. Linux: **`nix`**, `drm`/`gbm`/EGL via `bindgen`/raw FFI. Vendor SDKs (NVENC, VA-API, AMF) via `bindgen` or a `cc`-built shim. |
| Optional in-host features | Cargo **features** (rare — almost everything pluggable is an add-on `cdylib`, not a feature). |
| No `init()` registries | Rust has no package `init()`. The add-on registry is populated by the loader scanning the add-ons directory at startup. |
| Testing / benchmarks | `criterion` for microbenchmarks (protocol marshal, pipeline bookkeeping — the perf targets stated per spec); `proptest` for round-trip / property tests |
| Fuzzing | **`cargo-fuzz`** (libFuzzer) on the untrusted wire decoders — the 22-byte header, datagram reassembly, and binary input records (the bytes-from-the-network surface) |
| Global allocator | **`mimalloc`**, set once in `featherdesk-host`'s `main` — lower RSS + steadier tail latency than the system allocator (serves the <50 MB / low-latency targets) |

**Crate layout (Cargo workspace).** Public contracts are library crates; the
host is a binary crate; each add-on is its own `cdylib` crate. See
"File Structure" below for the full tree. Naming: workspace crates are
`featherdesk-<name>`; add-on crates build to `featherdesk-addon-<id>.{so,dylib,dll}`.

---

## Technology Choices & Risks

Each choice below was validated against the current Rust ecosystem and prior art —
chiefly **RustDesk** (117k★, 67% Rust, production remote desktop) and **Sunshine**
(38.7k★, the game-streaming gold standard). Where we diverge from them, it is
deliberate and noted.

| Area | Choice | Rationale / evidence |
|------|--------|----------------------|
| Language | **Rust 2021** | RustDesk proves Rust is production-viable for this domain; no-runtime/no-GC is what makes the dlopen add-on model work (Go can't) |
| Async | **Tokio** | quinn + wtransport require it; de-facto standard |
| QUIC | **quinn** | 5.1k★, de-facto Rust QUIC, tokio, rustls+ring, datagrams, cross-platform |
| WebTransport | **wtransport** | Only real Rust WebTransport server. ⚠️ **pre-1.0 + draft spec** (see Risks). Kept as the deliberate novel differentiator. |
| Plugin ABI | **abi_stable** | Spike-validated on Rust 1.96. Chosen over **stabby** because stabby has a documented Rust ≥1.78 trait-object regression (leaked global O(n) vtable set) — bad for a trait-object-heavy plugin app |
| Errors | `thiserror` + `Result`; `u32` code across the ABI | de-facto; the code↔enum mapping fixes what Go's `errors.Is` broke |
| Logging | `tracing` | de-facto for async Rust |
| Serialization | fixed-binary (media frame header + input records, hot path); `serde_json` (browser control plane); **protobuf/`prost` reserved for the v2 native-client protocol** (roadmap) | protobuf hurts the 60 fps fixed header and adds a JS dep to the v1 browser; it pays off in v2-native |
| Color convert | **libyuv** via FFI | exactly what RustDesk uses |
| Audio codec | **opus** | exactly what RustDesk uses |
| Clipboard (core) | **`arboard`** (MIT/Apache) | single cross-platform crate (Win/macOS/Linux X11+Wayland). **Avoids RustDesk's `libs/clipboard`, which is AGPL** and would force the whole host to AGPL |
| Input — kb/mouse **default** | **`enigo`** (all OS) | cross-platform user-space injection (SendInput / XTEST·libei / CGEvent). Matches **Sunshine's anti-cheat-safe default**; `enigo`'s macOS path *is* CGEvent |
| Input — kb/mouse **add-ons** | **Interception** (Windows, kernel — fast; ⚠️ anti-cheat risk, opt-in), **uinput** (Linux, kernel — Wayland + gaming-grade) | power-user paths that beat the default but carry tradeoffs (Interception ↔ anti-cheat; both need the kernel layer) |
| Input — touch / gamepad | add-ons: `win_touch` (touch), `vigem`/`gcvirtual`/`uinput-ff` (gamepad) | `enigo` does not cover touch or gamepad |
| Capture | **stays zero-by-default add-ons** (KMS+EGL, NvFBC, SCK, DXGI DD) | **Sunshine uses the same backends** — `scrap` was evaluated and rejected (X11-only on Linux, deprecated CGDisplayStream on macOS). DXGI DD is the only vendor-neutral Windows API that yields an `ID3D11Texture2D` zero-copy to every HW encoder; its acquisition cost has not been measured (the bench tool's DXGI backend is a stub) |
| HW encode | per-vendor FFI add-ons (NVENC/AMF/QSV/MF/VAAPI/VideoToolbox) | matches Sunshine's encoder matrix; bindings exist (`cros-libva`, etc.) |

**Risks (tracked):**
1. **WebTransport / wtransport maturity** — pre-1.0 on a still-draft spec. *Top external risk.* Mitigation: it sits behind the `featherdesk-transport` crate (one insulation point), the browser floor is already modern-only and the WebSocket fallback carrier covers browsers below the WebTransport floor, and `quinn` underneath is solid.
2. **Browser WebTransport + WebCodecs is novel** — no prior-art Rust project does it (RustDesk uses Flutter + its own protocol; Sunshine uses Moonlight clients). The risk lives on the *browser* side, not the Rust server. De-risk with the end-to-end vertical slice.
3. **Interception add-on anti-cheat risk** — documented; it is an opt-in add-on, never the default.

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

### Add-on loading model

- Each add-on is built **standalone** as a Rust `cdylib`
  (`.so` / `.dylib` / `.dll`) — `cargo build -p featherdesk-addon-<id>`. The
  add-on's own native dependencies (FFI to vendor SDKs) are linked into **that
  library**, never into the host.
- Every add-on uses `abi_stable` to export one **root module** (the entry point,
  conceptually `FeatherDeskAddonOpen`) exposing **(0)** an `init(HostServices)`
  the host calls first, carrying the log sink that makes the add-on's `tracing`
  output visible, **(1)** the ABI version (`abi_stable` checks this *and* a
  structural layout hash automatically; the version check is exact equality in
  v1), **(2)** a capability descriptor (`kind` — an `AddonKind` from the registry
  below; codec(s); os+arch), and   **(3)** the add-on's `#[sabi_trait]` object
  (`Capturer`, `Encoder`, `HardwareEncoder`, `AudioCapturer`, `AudioEncoder`, or
  an injector — `KeyMouseInjector` / `TouchInjector` / `GamepadInjector`, which
  `MODULE_INPUT` groups under the umbrella name `Injector`), selected by the
  capability descriptor's `kind`.
- At startup the host **scans the add-ons directory**, loads each library
  (`dlopen` / `LoadLibraryW` under the hood, via `abi_stable`'s loader), verifies
  the ABI version + layout (by default a mismatch is skipped with a warning;
  `[addons] abi_strict = true` aborts startup instead), and registers its
  capability descriptor. There is **no `init()` registry and no stub files** —
  an absent add-on is simply a library that isn't in the directory.
- **Filename convention:** `featherdesk-addon-<id>.{so,dylib,dll}`, where `<id>`
  (`kms_egl`, `openh264`, `nvenc`, …) is the **add-on ID** — it names the library
  and its `[addon_module_<id>]` config section.
- **Hot-swap = drop a library + restart.** v1 resolves the add-on set once at
  startup; live reload without restart is out of scope for v1.

### Add-on ABI contract (Rust + `abi_stable`)

Because the host and add-ons are **all Rust with no runtime/GC**, a
runtime-loaded add-on is just a native library — there is **no two-runtime
problem** (the reason Go was rejected). The loader and every add-on share the
stable ABI defined in the **`featherdesk-abi`** crate, built on
[`abi_stable`](https://crates.io/crates/abi_stable) and validated by a working
spike (a host scanned a dir, loaded a separately-compiled `cdylib`, and frame
buffers + an error code crossed the boundary with deterministic cleanup).

**The full ABI contract is its own spec:
[`./core/MODULE_ABI.md`](./core/MODULE_ABI.md)** — the two-layer model, the
root-module surface, rich-type/ownership rules, the `AddonKind` / `CodecId` /
`AbiErr` registries (the single source of truth in `featherdesk-abi`), fault
recovery (what `catch_unwind` covers, and what it does not), the load-failure
taxonomy, and ABI versioning. In one line: `abi_stable` verifies the ABI version
**and** a structural layout hash on load (the version check is exact equality in
v1; a mismatch is rejected, not miscompiled); buffers cross as owned `RVec<u8>`
(ownership transfers, deterministic drop); errors cross as `RResult<T, u32>` the
host maps back to its own enum; channels stay host-side; and an add-on library
MUST match the host's **OS and CPU arch**.

**Add-on directory (portable, user-controlled).** FeatherDesk is a **portable,
drop-anywhere** deployment: keep the host binary and its add-ons together in any
folder. The add-ons directory is whatever `[addons] dir` points to (any absolute
or relative path). **The default is `addons/` resolved relative to the running
core's own location** (the directory of the host executable), so dropping the
binary + an `addons/` folder side-by-side works wherever the project lives, with
no install step and no privileged system path. If the configured/derived dir
does not exist it is treated as empty (warn) — the binary still runs, just with
no backends loaded.

> **Security note (advisory, not enforced).** Loading a library executes its
> native code in the host process, and the host may run elevated (KMS+EGL needs
> root/`CAP_SYS_ADMIN`; Interception/SendSAS needs SYSTEM). FeatherDesk only
> *provides* the module-loading mechanism; **it does not police the add-ons
> directory** — it loads whatever trusted-by-you libraries you place there. Put
> only add-ons you trust in that folder, and, if you run the host elevated,
> secure the folder's permissions yourself. The loader does **not** refuse a
> world-writable directory.

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
| Home Windows, NVIDIA | `dxgi_dd`, `openh264`, `nvenc` (+ `x264`, opt-in via `force_addon`) |
| Commercial Linux, AMD | `kms_egl`, `openh264`, `libva`, `amf_rocm` |
| Apple Silicon Mac | `sck`, `vt_hw` |

Build one add-on with, e.g.,
`cargo build --release -p featherdesk-addon-kms_egl` (its crate is a `cdylib`,
producing `featherdesk-addon-kms_egl.so`). See each platform's
`encoders/README.md` and `capture/README.md` for recommended combinations.

### Runtime probe and selection

When multiple add-ons are loaded, the pipeline picks at runtime based on:

1. `[capture] force_addon` / `[encode] force_addon` in TOML (forces a specific add-on by ID)
2. Probe order — defined authoritatively in
   [`./core/MODULE_PIPELINE.md`](./core/MODULE_PIPELINE.md) startup step 3e. It is
   **per-OS**, because a single global order would list entries that can never fire
   (`libva` is Linux-only, `qsv`/`mf_hw` Windows-only, `vt_hw`/`vt_sw` macOS-only):
   Linux HW `nvenc → amf_rocm → libva`, Windows HW `nvenc → amf → qsv → mf_hw`,
   macOS HW `vt_hw`; SW `openh264 → vt_sw` (macOS only), with `x264` reachable
   solely via `[encode] force_addon = "x264"`
3. Hardware presence (NVENC only fires if NVIDIA GPU present, etc.)
4. `StreamError::FallbackToSoftware` from HW encoder triggers SW fallback for the session
5. Codec — the chosen encoder advertises H.264 (`avc1.*`) for every SDR session, on
   every platform, regardless of what HEVC hardware is present; HEVC Main10
   (`hvc1.2.*`) is emitted only when HDR is requested, because WebCodecs has no
   H.264 HDR profile (see [`./core/MODULE_STREAM_PARAMS.md`](./core/MODULE_STREAM_PARAMS.md)
   "HDR Pipeline"). HEVC is never selected to save bandwidth: Firefox's WebCodecs
   cannot decode it, and a codec no attached client can decode is a black screen,
   not a saving. The client reports its decode capability in the `auth` message and
   the server never advertises a codec no attached client can decode
   ([`./core/MODULE_PROTOCOL.md`](./core/MODULE_PROTOCOL.md) "Decode capability").

Per-add-on tuning lives in `[addon_module_<id>]` TOML sections, not
in code. See [`./core/MODULE_CONFIG.md`](./core/MODULE_CONFIG.md).

### Future: optional process isolation

The Rust + `abi_stable` in-process loading model is the proven default and has no
runtime-conflict risk. If a future deployment wants hard fault/security isolation
between the host and an add-on (e.g. a crashy vendor driver), the add-on can be
run as a **subprocess sidecar** instead, talking to the host over a local socket
with shared memory for the frame path. The add-on **trait contracts are
identical** either way — only how the host obtains the implementation changes —
so this is a packaging choice, not a spec change. (This replaces the Go-era "v2
fallback", which existed only to escape the two-runtime problem Rust doesn't have.)

---

## Module Map (Core Modules)

The system is decomposed into 19 module specs — 15 numbered core modules plus four
cross-cutting/support specs (ABI, Auth, Stream Params, Audio). Every module spec that
describes runtime behaviour carries a contract section and a Testing Strategy table;
internal architecture, `R-XXX-NN` refactoring directives, and numeric performance
targets appear where they apply rather than in every file.

| # | Module | Spec File | Responsibility |
|---|--------|-----------|----------------|
| 1 | **Capture** | [`./media/MODULE_CAPTURE.md`](./media/MODULE_CAPTURE.md) | Cross-platform `Capturer` interface contract (concrete impls are add-ons per OS) |
| 2 | **Encode** | [`./media/MODULE_ENCODE.md`](./media/MODULE_ENCODE.md) | Software encoder interface contract (concrete impls are add-ons) |
| 3 | **Hardware Encode** | [`./media/MODULE_HARDWARE_ENCODE.md`](./media/MODULE_HARDWARE_ENCODE.md) | Hardware encoder interface contract (concrete impls are add-ons) |
| 4 | **Protocol** | [`./core/MODULE_PROTOCOL.md`](./core/MODULE_PROTOCOL.md) | Wire protocol (framing, serialization, versioning) — transport-agnostic; shared by the browser (v1) and native (v2) clients, see [`./client/MODULE_NATIVE_CLIENT.md`](./client/MODULE_NATIVE_CLIENT.md) |
| 5 | **Transport** | [`./core/MODULE_TRANSPORT.md`](./core/MODULE_TRANSPORT.md) | HTTPS/TCP bootstrap + HTTP/3 + WebTransport (QUIC); datagrams + reliable streams; auth handshake |
| 6 | **Server** | [`./core/MODULE_SERVER.md`](./core/MODULE_SERVER.md) | Session management, broadcast fan-out, keyframe/bootstrap cache, role gating (consumes Transport; transport/TLS owned by #5) |
| 7 | **Web Client** | [`./client/MODULE_WEB_CLIENT.md`](./client/MODULE_WEB_CLIENT.md) | Browser-based viewer (WebCodecs) — v1 |
| 8 | **Pipeline** | [`./core/MODULE_PIPELINE.md`](./core/MODULE_PIPELINE.md) | Orchestrator: probe + select loaded add-ons, lifecycle, pacing, frame drops, wiring |
| 9 | **Config** | [`./core/MODULE_CONFIG.md`](./core/MODULE_CONFIG.md) | TOML config schema, parsing, validation, hot reload |
| 10 | **Input** | [`./interaction/MODULE_INPUT.md`](./interaction/MODULE_INPUT.md) | Binary input wire decode + dispatcher + HID-usage contract. **kb/mouse default = `enigo` (in core, all OS)**; kernel injectors (Interception/uinput), touch (win_touch), and gamepad (vigem/gcvirtual) are add-ons |
| 11 | **Clipboard** | [`./interaction/MODULE_CLIPBOARD.md`](./interaction/MODULE_CLIPBOARD.md) | Bidirectional text + rich-HTML clipboard sync (core; per-OS clipboard access) |
| 12 | **File Transfer** | [`./interaction/MODULE_FILETRANSFER.md`](./interaction/MODULE_FILETRANSFER.md) | Drag-drop transfer to a fixed folder carried as QUIC bidirectional streams on the main WebTransport session (core) |
| 13 | **Gamepad** | [`./interaction/MODULE_GAMEPAD.md`](./interaction/MODULE_GAMEPAD.md) | Browser Gamepad-API redirection contract + rumble (virtual-controller injection is per-OS add-ons; casual-gaming-grade only) |
| 14 | **Network** | [`./v2/MODULE_NETWORK.md`](./v2/MODULE_NETWORK.md) | v2 connectivity (NAT traversal / relay / signaling for the native client). Requirements + listener-provider contract documented; **mechanism not chosen** (tsnet vs pion vs other — evaluated at v2 start). **v1 has no connectivity module at all** — the server binds a UDP port and the operator supplies reachability; a host with no inbound UDP path is not reachable in v1. |
| 15 | **Native Client** | [`./client/MODULE_NATIVE_CLIENT.md`](./client/MODULE_NATIVE_CLIENT.md) | v2 native desktop client plan — same QUIC protocol, full-HID gamepad, reliable 4:4:4, sub-ms input. **Split final; impl deferred.** |
| 16 | **Auth** *(support)* | [`./core/MODULE_AUTH.md`](./core/MODULE_AUTH.md) | Authentication modes, session tokens, in-band resume credentials, role gating |
| 17 | **Stream Params** *(support)* | [`./core/MODULE_STREAM_PARAMS.md`](./core/MODULE_STREAM_PARAMS.md) | Dynamic stream parameters, adaptive bitrate, chroma negotiation (shared `featherdesk-stream`) |
| 18 | **Audio** *(deferred)* | [`./media/MODULE_AUDIO.md`](./media/MODULE_AUDIO.md) | Host→client system audio: Opus/PCM, stereo / 5.1 / 7.1, audio-master A/V sync. **Design locked; impl deferred.** |
| 19 | **ABI** *(support)* | [`./core/MODULE_ABI.md`](./core/MODULE_ABI.md) | The add-on ABI contract (`featherdesk-abi`): root-module surface + capability-descriptor registries (`AddonKind` / `CodecId` / `AbiErr`) — the single source of truth the host and every add-on compile against |

> **Encoder, capture, and input implementations are not core modules.**
> Every encoder (OpenH264 (FFI), x264 subprocess, VideoToolbox, libva, NVENC, AMF,
> QSV, MediaFoundation HW), every capture backend (KMS+EGL, NvFBC, SCK, DXGI DD),
> and the **override** input injectors (interception, uinput, win_touch, vigem,
> gcvirtual) are add-on shared libraries under
> [`specs/addons/{platform}/{capture,encoders,input,audio}/`](./addons/).
> The default binary ships with zero capture/encoder add-ons — users drop in what
> they need. **kb/mouse works by default** via the in-core `enigo` injector (all
> OS); the binary is view-only only when `[input] enabled = false`. (macOS kb/mouse
> is the in-core `enigo`/CGEvent default — there is no separate `cgevent` add-on;
> gamepad still needs `vigem`/`gcvirtual`/`uinput`.) See the index below.

> **Clipboard + File Transfer are core (not add-ons).** Their OS surface is small
> (clipboard APIs, file I/O) and they are baseline remote-desktop expectations,
> so they live in core with per-OS modules behind `cfg(target_os)`.

> **Removed from the module map:**
> - **Logger** — replaced by the `tracing` crate. No dedicated module spec
>   needed. Modules emit `tracing` events/spans; `tracing-subscriber` is
>   initialized once in the host binary. Behavior (text vs JSON, level, output)
>   is set via the `[log]` config section.

> **Deferred to future versions:**
> - **Native client (v2)** — the v1=browser / v2=native split is **final**; the
>   native client speaks the identical wire protocol (via `quinn`/`wtransport`
>   directly) and adds full-HID gamepad, reliable 4:4:4, and sub-ms input. Design plan in
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
| KMS+EGL DMA-BUF | `kms_egl` | [`specs/addons/linux/capture/KMS_EGL_LINUX_SPEC.md`](./addons/linux/capture/KMS_EGL_LINUX_SPEC.md) | Universal — every GPU, any display server | 📋 Specced — Go prototype abandoned on tiled 10-bit scanout formats (see spec "Status") |
| NvFBC | `nvfbc` | [`specs/addons/linux/capture/NVFBC_LINUX_SPEC.md`](./addons/linux/capture/NVFBC_LINUX_SPEC.md) | NVIDIA proprietary driver | 📋 Specced |
| wlr-screencopy | `wl_screencopy` | [`specs/addons/linux/capture/WL_SCREENCOPY_LINUX_SPEC.md`](./addons/linux/capture/WL_SCREENCOPY_LINUX_SPEC.md) | Any GPU; wlroots-family compositor — **no root** | 📋 Specced, not measured |
| Portal ScreenCast | `pw_portal` | [`specs/addons/linux/capture/PW_PORTAL_LINUX_SPEC.md`](./addons/linux/capture/PW_PORTAL_LINUX_SPEC.md) | Any GPU; any compositor with xdg-desktop-portal — **no root**, interactive consent | 📋 Specced, not measured |

> **KMS+EGL is the recommended default capture add-on.** Works on X11 and
> Wayland (all compositors) — it operates below the display server, so display
> server choice is irrelevant. It requires root / `CAP_SYS_ADMIN` **and a real
> KMS CRTC with a mode set**: it imports the DRM *scanout* framebuffer, so it is
> display-server *agnostic*, not display-server *optional*, and it does **not**
> work with Xvfb or any other in-memory X server. See
> [`PLATFORM_COMPAT.md`](./PLATFORM_COMPAT.md) "Headless on Linux".
>
> **Two no-root add-ons cover what it cannot** (`wl_screencopy`, `pw_portal`) —
> a host where `CAP_SYS_ADMIN` is not grantable, and headless Wayland with no
> forceable connector. Neither is selected ahead of KMS+EGL, and neither reopens
> containerized deployment: *no-root capture* and *run in Docker* are separate
> claims and only the first is in scope. X11grab and XShm remain rejected; `vkms`
> was evaluated and rejected (DRM planes but no accelerated, DMA-BUF-exportable
> framebuffer).
>
> **Intel / AMD do not need capture add-ons** — neither vendor has a proprietary
> capture API on Linux. KMS+EGL is the entire path.

See [`specs/addons/linux/capture/README.md`](./addons/linux/capture/README.md)
for the full rationale.

### Linux encoder add-on specs

| Add-on | Path | License | Spec | Hardware | Status |
|--------|------|---------|------|---------|--------|
| OpenH264 (FFI) | SW | BSD-2 (Cisco) | [`linux/encoders/SW/OPENH264_LINUX_SPEC.md`](./addons/linux/encoders/SW/OPENH264_LINUX_SPEC.md) | Any CPU (x86_64, ARM64) | ✅ Working |
| x264 subprocess (opt-in) | SW | GPL-2 (isolated) | [`linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md`](./addons/linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md) | Any CPU; needs ffmpeg | 📋 Specced — measured on Windows only |
| libva direct | HW | MIT | [`linux/encoders/HW/LIBVA_LINUX_SPEC.md`](./addons/linux/encoders/HW/LIBVA_LINUX_SPEC.md) | Intel + AMD + NVIDIA (via wrapper) | 📋 Specced |
| NVENC direct | HW | NVIDIA SDK | [`linux/encoders/HW/NVENC_LINUX_SPEC.md`](./addons/linux/encoders/HW/NVENC_LINUX_SPEC.md) | NVIDIA Kepler+ | 📋 Specced |
| AMF on ROCm | HW | Apache 2.0 | [`linux/encoders/HW/AMF_ROCM_SPEC.md`](./addons/linux/encoders/HW/AMF_ROCM_SPEC.md) | AMD GCN+ via ROCm | 📋 Specced |

> **SW encoder choice:** OpenH264 is the software **default** on every platform —
> in-process, BSD-licensed, no external binary, and able to force an IDR in place.
> x264 is an explicit **opt-in** (`[encode] force_addon = "x264"`, never chosen by
> `mode = "auto"`): ~2× faster on a CPU-bound host, but it needs a GPL ffmpeg binary
> and its only mechanism for an on-demand keyframe is killing and respawning that
> child process. Force it where the CPU cost has been **measured**.
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
| ScreenCaptureKit | `sck` | [`specs/addons/macos/capture/SCK_MACOS_SPEC.md`](./addons/macos/capture/SCK_MACOS_SPEC.md) | All Macs (macOS 12.3+) | 📋 Specced; Hackintosh-benchmarked |

See [`specs/addons/macos/capture/README.md`](./addons/macos/capture/README.md)
for the full rationale.

### macOS encoder add-on specs

| Add-on | Path | License | Spec | Hardware | Status |
|--------|------|---------|------|---------|--------|
| OpenH264 (FFI) | SW | BSD-2 (Cisco) | [`macos/encoders/SW/OPENH264_MACOS_SPEC.md`](./addons/macos/encoders/SW/OPENH264_MACOS_SPEC.md) | Any CPU; cross-platform | 📋 Specced |
| x264 subprocess (opt-in) | SW | GPL-2 (isolated) | [`macos/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md`](./addons/macos/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md) | Any CPU; needs ffmpeg | 📋 Specced |
| VideoToolbox SW | SW | Apple system | [`macos/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md`](./addons/macos/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md) | Any Mac (macOS 12.3+) | 📋 Specced |
| VideoToolbox HW | HW | Apple system | [`macos/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md`](./addons/macos/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md) | All Macs 2011+ (HW H.264), Skylake+/Apple Silicon (HW HEVC). No AV1 HW encode on any current Apple Silicon. | 📋 Specced |

> **No vendor-specific HW add-ons on macOS** — Apple controls the entire graphics stack.
> VideoToolbox is the single API for Intel Quick Sync, AMD VCE, and Apple Media Engine.
>
> **For maximum HW performance on macOS, use VideoToolbox.** The software order is
> `openh264` → `vt_sw`: OpenH264 is the cross-platform default, and `vt_sw` is
> preferred where it probes available because VideoToolbox is macOS-native. `x264`
> is opt-in here as on every OS (`[encode] force_addon = "x264"`).

### Windows capture add-on specs

| Add-on | Add-on ID | Spec | Hardware | Status |
|--------|-----------|------|---------|--------|
| DXGI Desktop Duplication (with integrated IddCx headless install) | `dxgi_dd` | [`specs/addons/windows/capture/DXGI_DD_WINDOWS_SPEC.md`](./addons/windows/capture/DXGI_DD_WINDOWS_SPEC.md) | Any GPU (WDDM 1.2+, Win 8+) | 📋 Specced — DXGI acquisition not yet measured (bench backend is a stub) |

> **DXGI DD is the only Windows capture mechanism.** Its output is
> `ID3D11Texture2D`, directly consumable zero-copy by every Windows HW encoder
> (MF HW, NVENC, AMF, QSV), so one add-on covers every GPU. Vendor-specific
> capture APIs (NvFBC, AMF Display Capture) are deferred pending a measured
> DXGI acquisition cost — none has been taken.
>
> **The host process must be `PER_MONITOR_AWARE_V2`.** It is declared in the
> Windows application manifest and asserted at startup (MODULE_PIPELINE startup
> step 0); `dxgi_dd.probe()` verifies it and reports unavailable if it is not.
> Without it `IDXGIOutput::GetDesc` returns DPI-scaled logical pixels, the
> advertised `config` dimensions do not match the panel, and every absolute
> pointer coordinate lands short by the scale factor. `win_touch` depends on the
> same declaration.
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
| OpenH264 (FFI) | SW | BSD-2 (Cisco) | [`windows/encoders/SW/OPENH264_WINDOWS_SPEC.md`](./addons/windows/encoders/SW/OPENH264_WINDOWS_SPEC.md) | Any CPU | ✅ Benchmarked |
| x264 subprocess (opt-in) | SW | GPL-2 (isolated) | [`windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md`](./addons/windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md) | Any CPU; needs ffmpeg | ✅ Benchmarked |
| MediaFoundation HW | HW | Microsoft system | [`windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md`](./addons/windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md) | All vendors (cross-vendor via MFT routing) | ✅ Benchmarked |
| NVENC | HW | NVIDIA SDK | [`windows/encoders/HW/NVENC_WINDOWS_SPEC.md`](./addons/windows/encoders/HW/NVENC_WINDOWS_SPEC.md) | NVIDIA Kepler+ | ✅ Benchmarked |
| AMF | HW | Apache 2.0 | [`windows/encoders/HW/AMF_WINDOWS_SPEC.md`](./addons/windows/encoders/HW/AMF_WINDOWS_SPEC.md) | AMD GCN+ | ✅ Benchmarked |
| QSV (oneVPL) | HW | MIT | [`windows/encoders/HW/QSV_WINDOWS_SPEC.md`](./addons/windows/encoders/HW/QSV_WINDOWS_SPEC.md) | Intel Sandy Bridge+ (covers Arc) | 📋 Specced |


> **MediaFoundation HW is the recommended cross-vendor default for Windows** —
> closest equivalent to VA-API on Linux. Ship `mf_hw` for one-binary-covers-everything;
> add vendor SDKs (NVENC/AMF/QSV) for peak performance and vendor-specific features.

### Input add-on specs

Implement `input::KeyMouseInjector` / `input::TouchInjector` / `input::GamepadInjector`.
The core decodes the binary input protocol; **kb/mouse is injected by the in-core
`enigo` default on every OS** — the add-ons below are optional **overrides/extensions**
(kernel-level kb/mouse, touch, or gamepad). The binary is view-only only when
`[input] enabled = false`. See
[`MODULE_INPUT.md`](./interaction/MODULE_INPUT.md) and [`MODULE_GAMEPAD.md`](./interaction/MODULE_GAMEPAD.md).

| Add-on | Add-on ID | OS | Spec | Capability | Status |
|--------|-----------|----|----- |------------|--------|
| Interception | `interception` | Windows | [`windows/input/INTERCEPTION_WINDOWS_SPEC.md`](./addons/windows/input/INTERCEPTION_WINDOWS_SPEC.md) | KeyMouse **override** (kernel filter driver + SendSAS, below UIPI; ⚠️ anti-cheat risk) | 📋 Specced |
| Win Touch | `win_touch` | Windows | [`windows/input/WIN_TOUCH_WINDOWS_SPEC.md`](./addons/windows/input/WIN_TOUCH_WINDOWS_SPEC.md) | Touch (`InjectTouchInput`; pen→touch with pressure) | 📋 Specced |
| ViGEmBus | `vigem` | Windows | [`windows/input/VIGEM_WINDOWS_SPEC.md`](./addons/windows/input/VIGEM_WINDOWS_SPEC.md) | Gamepad (Xbox 360 virtual controller; signed driver install) | 📋 Specced |
| uinput | `uinput` | Linux | [`linux/input/UINPUT_LINUX_SPEC.md`](./addons/linux/input/UINPUT_LINUX_SPEC.md) | KeyMouse **override** + Gamepad (kernel `/dev/uinput`, X11+Wayland) | 📋 Specced |
| GCVirtual | `gcvirtual` | macOS | [`macos/input/GCVIRTUAL_MACOS_SPEC.md`](./addons/macos/input/GCVIRTUAL_MACOS_SPEC.md) | Gamepad (GCVirtualController, macOS 14+; GameController-framework apps only) | 📋 Specced |

(macOS kb/mouse is the in-core `enigo`/CGEvent default — no separate `cgevent` add-on.)

### Where to register a new add-on

The same seven steps for all four kinds — capture, encoder (SW or HW), input and
audio. Steps 1-3 are documentation; step 4 is the code contract in
[`./core/MODULE_ABI.md`](./core/MODULE_ABI.md); steps 5-7 wire it up.

1. **Write the spec** at
   `specs/addons/{platform}/{capture,encoders/{HW,SW},input,audio}/{NAME}_SPEC.md`.
2. **Add a row** to the matching table in **this** section, and — if the add-on has
   tuning knobs — an `[addon_module_<id>]` block to
   [`./core/MODULE_CONFIG.md`](./core/MODULE_CONFIG.md).
3. **Add a row** to the compat matrix in [`./PLATFORM_COMPAT.md`](./PLATFORM_COMPAT.md).
4. **Implement the Layer-1 root module** (`featherdesk-abi`; MODULE_ABI "Root
   module surface"). This is the part a table row cannot capture:
   - `init(host)` — call `featherdesk_abi::install_log_sink(host.log)` **first**,
     or none of the add-on's `tracing` output ever reaches the operator.
   - `descriptor()` — `kind` is the one `AddonKind` this library is (`Capture`
     `0x01`, `SoftwareEncode` `0x02`, `HardwareEncode` `0x03`, `AudioCapture`
     `0x04`, `AudioCodec` `0x05`, `InputKeyMouse` `0x06`, `InputTouch` `0x07`,
     `InputGamepad` `0x08`); `id` is the add-on ID; `codecs` the `CodecId`s it
     emits or consumes (empty for capture and input); `abi_version` the build
     target's. Fill `os` from `abi::HOST_OS` and `arch` from `abi::HOST_ARCH` —
     the `Os` and `Arch` enums and both constants are declared in
     `featherdesk-abi`, and the host compares `descriptor().os` / `.arch`
     against the same two constants and rejects the library on a mismatch.
   - `probe()` — return `ROk(ProbeReport{..})` even when the backend is
     unavailable (`available: false` plus a `reason`); an `RErr` means the probe
     itself broke, not that the backend is missing. **Set every `AddonCaps` bit the
     add-on actually serves** — `caps` left at `0` means no zero-copy path, no
     separate cursor, no hot parameter change, no Ctrl+Alt+Del and no rumble,
     silently. Capture add-ons fill `displays`.
   - `construct(cfg)` — return the `AddonObject` variant matching
     `descriptor().kind`, and decode `cfg.section_toml` against the add-on's own
     `#[serde(deny_unknown_fields)]` struct, returning `AbiErr::BadConfig` (`10`)
     on an unknown key. The host has no schema for that section; the add-on is the
     validator.
   - Wrap **every** entry point and trait method in `std::panic::catch_unwind`
     returning `AbiErr::Unrecoverable` (`7`); build `panic = "unwind"`, never
     `panic = "abort"`.
5. **Build as a cdylib** from `addons/{capture,encode,input,audio}/{name}/`, output
   `featherdesk-addon-<id>.{so,dylib,dll}`, built natively per target — an add-on
   MUST match the host's OS **and** CPU arch. On macOS, sign it (ad-hoc at minimum)
   and see [`./addons/macos/MACOS_SPEC.md`](./addons/macos/MACOS_SPEC.md)
   "Add-ons and the hardened runtime".
6. **Wire the runtime selection** in
   [`./core/MODULE_PIPELINE.md`](./core/MODULE_PIPELINE.md): capture and encoder
   add-ons join the probe order in startup step 3d/3e; input add-ons are selected
   by `InputAddon::kind()` in step 8 (they are complementary, not ranked); audio
   add-ons by `AudioAddon::kind()` in step 11.
7. **Add the Testing Strategy row every add-on owes the ABI:** `probe()`'s `caps`
   equals the set of optional methods the constructed object actually serves. A bit
   it sets and cannot honour is a capability lie (MODULE_ABI "Misbehaving add-ons").

---

## System Architecture Diagram

> **Note.** The ASCII sketch below draws the original 5-box architecture
> (Capture / Encode / Audio / Server / Input). It is a picture of the frame path,
> not of the module set: it does not show ABI, Config, Transport, Clipboard,
> File Transfer, Gamepad, Stream Params, Auth, Network, Native Client, or the
> add-on registry. There are 19 module specs — for the authoritative list see
> the **Module Map** above; for the add-on tree see **Platform & Add-On Spec
> Index**. The **Module Dependency Graph** further down is a sketch of the
> media/server crate edges and is *not* complete — it omits `audio`, `abi`,
> `input`, `clipboard` and `filetransfer`. Wiring questions resolve against the
> `> Crate dependency edges documented` bullet list beneath it and against
> `PROJECT_ARTIFACTS/low-level-design/LOW_LEVEL_DESIGN.md`, which draws every
> node.

```
                    ┌──────────────────────────────────────────────────────────────┐
                    │                   PIPELINE MODULE (Orchestrator)              │
                    │  Probes capabilities, selects backends, manages lifecycle    │
                    │  Frame pacing + drop decisions + sustainable-rate control   │
                    └───┬──────────┬──────────┬──────────┬───────────┬────────────┘
                        │          │          │          │           │
        ┌───────────────┘          │          │          │           └───────────┐
        ▼                          ▼          ▼          ▼                       ▼
┌─────────────────┐      ┌──────────────┐ ┌───────────┐ ┌───────────┐  ┌────────────┐
│  CAPTURE MODULE │      │   ENCODE     │ │   AUDIO   │ │  SERVER   │  │   INPUT    │
│                 │      │  (Software)  │ │   MODULE  │ │  MODULE   │  │   MODULE   │
│ next_frame()    │─────▶│ convert()    │ │           │ │           │  │            │
│ -> Frame        │ RGBA │ encode()     │ │ next_chunk│ │ HTTP/3+WT │  │ uinput     │
│   (owned)       │      │ -> AnxB AU   │ │ -> RVec   │ │ Broadcast │  │ injection  │
│                 │      └──────┬───────┘ └─────┬─────┘ └─────┬─────┘  └────────────┘
│ next_surface()  │──┐         │                │             │
│ -> FbInfo       │  │  AnxB   │                │ PCM         │
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
    capturer.next_surface() → FbInfo{ DmaBuf/IoSurface/D3D11Texture, timestamp }
    → hw_encoder.encode_surface(fb_info) → EncodedUnit (GPU→CPU: ~30KB compressed only)
    Use when: HW encoder available AND capturer implements SurfaceCapturer
    cursor_mode = "separate" (client-side cursor; requires AddonCaps::CURSOR)
                OR "embedded" (the add-on composites onto the surface; requires
                AddonCaps::EMBED_CURSOR_SURF — e.g. sck showsCursor, nvfbc bWithCursor)

         │  on StreamError::FallbackToSoftware (DMA-BUF import unsupported, GPU reset, etc.)
         ▼
PATH A — Software (CPU round-trip)  [fallback / [encode] force_addon = "openh264" or "x264"]:
    capturer.next_frame() → BGRA RVec<u8> (GPU→CPU: ~24MB at 1440p)
    → converter.convert() → YuvFrame (CPU, SIMD libyuv *Matrix entry points, BT.709 limited range)
    → encoder.encode() → Annex B AU + keyframe bool (CPU; OpenH264 (FFI) by default, VT SW on macOS, or the opt-in x264 subprocess — VP8/libavcodec/in-process-x264 rejected)
    cursor_mode = "embedded" (add-on composites) OR "separate"
```

**Decision (confirmed):** the legacy ffmpeg-`h264_vaapi` subprocess path (which still did a CPU round-trip via `glReadPixels`→libyuv→stdin→`hwupload`) is REMOVED. Hardware = zero-copy `hwencode` module only; software defaults to in-process OpenH264 (VP8/libvpx/libavcodec rejected). There is no third tier. **No component on the default path depends on ffmpeg** — the opt-in `x264` add-on is the single exception, is never selected by `[encode] mode = "auto"`, and ships as its own GPL-isolated library.

---

## Module Interface Contracts

### Contract 1: Capture -> Encode

```rust
// Capture produces raw pixel frames (BGRA on macOS/Windows, RGBA on Linux GL).
// Cleanup is RAII (Drop) — no Close().
pub trait Capturer: Send {
    /// Ok(Some(frame)) = a new frame; Ok(None) = no new frame (pacing / static
    /// screen); Err = failure. Across the add-on ABI `frame.data` is an owned
    /// RVec<u8> whose ownership transfers to the host (deterministic drop).
    fn next_frame(&mut self) -> Result<Option<Frame>, StreamError>;
}

pub struct Frame {
    pub data: RVec<u8>,       // Pixel buffer (stride * height bytes), owned
    pub stride: u32,          // Bytes per row; MAY exceed width*4 (padded readback)
    pub pixel_fmt: PixelFormat, // Bgra (macOS/Windows) or Rgba (Linux GL)
    pub width: u32,
    pub height: u32,
    pub rotation: Rotation,   // how far CLOCKWISE this buffer must be turned to
                              // be upright. width/height describe the buffer AS
                              // DELIVERED — for R90/R270 they are the transposed
                              // (sideways) dimensions, and the UPRIGHT geometry
                              // is (height, width). The add-on REPORTS it and
                              // never rotates (see MODULE_CAPTURE "Display rotation").
    pub timestamp_ns: u64,    // CLOCK_MONOTONIC nanoseconds
}

pub use abi::Rotation;    // R0 | R90 | R180 | R270 (clockwise). DECLARED in
                          // featherdesk-abi, not here: it is a field of RFrame
                          // and RFbInfo, and featherdesk-abi may not name a type
                          // from a crate that imports it.

#[repr(u8)]
pub enum PixelFormat {
    Bgra = 0,  // BGRA in memory = libyuv "ARGB" → kArgb* constants
    Rgba = 1,  // RGBA in memory = libyuv "ABGR" → kAbgr* constants
}
```

**Data Flow:** `capturer.next_frame()` -> `converter.convert(&frame)` -> `encoder.encode(&yuv)`
- Converter selects the `ArgbConstants` for `(frame.pixel_fmt, ColorMatrix)` and calls `ARGBToI420Matrix` / `ARGBToI422Matrix` / `ARGBToI444Matrix`; the SDR matrix is BT.709 limited range on both the SW and HW paths.

**Contract Rules:**
- `Frame.data` ownership **transfers** to the caller (owned `RVec<u8>`); no manual copy-before-next-call footgun (the Go "borrowed slice" hazard is gone). For the CPU path the add-on copies its readback into the returned buffer; the HW path uses `SurfaceCapturer` (no copy).
- `stride` MAY exceed `width*4` (padded GPU readback, e.g. DXGI) — consumers MUST honor it.
- **Upright geometry.** `rotation` describes the buffer as delivered; the *upright*
  geometry is `(height, width)` for `R90`/`R270` and `(width, height)` otherwise. The
  pipeline derives the stream dimensions from the upright geometry, the Converter
  rotates on the software path, and the HW encoder rotates in its VPP or returns
  `StreamError::FallbackToSoftware`. Nothing downstream of the encoder ever sees a
  rotated picture, so the stream coordinate space is upright by construction.
- Dimensions must remain stable across frames (no mid-stream resize without signaling),
  as must `rotation`; a change in either is signalled through the resolution-change flow.
- `timestamp_ns` must be monotonically increasing (sourced from `CLOCK_MONOTONIC`).
- `Ok(None)` indicates "no new frame available" (frame pacing / static-screen optimization).

---

### Contract 2: Encode -> Server

```rust
// Encode produces ONE contiguous Annex B access unit + a keyframe flag.
// Cleanup is RAII (Drop) — no Close().
pub trait Encoder: Send {
    /// Ok(Some(unit)) = an access unit; Ok(None) = no complete access unit for
    /// this call (rate-control skip, or a PIPELINED encoder whose child still
    /// holds this frame); Err = failure.
    fn encode(&mut self, frame: &YuvFrame) -> Result<Option<EncodedUnit>, StreamError>;
    fn force_keyframe(&mut self);
    /// WebCodecs codec string for the `config` handshake. COMPUTED from the
    /// active profile and Params geometry, never a constant (see MODULE_ABI
    /// "Codec-string computation"): the Layer-2 adapter builds it with
    /// `codec_string(self.profile(), width, height, fps)`, where `profile()` is
    /// the Layer-1 `abi::Encoder` method that reports the `VideoProfile` the
    /// add-on actually configured.
    fn codec(&self) -> &str;
}

pub struct EncodedUnit {
    pub data: RVec<u8>,     // ONE contiguous Annex B access unit (owned)
    pub keyframe: bool,
    pub timestamp_ns: u64,  // CLOCK_MONOTONIC ns of the CAPTURE that produced this
                            // access unit. An in-process encoder echoes the input
                            // frame's timestamp; a PIPELINED encoder keeps a FIFO
                            // of submitted timestamps and attaches the head one to
                            // each completed AU.
}
```

**Data Flow:** `encoder.encode(&frame)` returns `Some(EncodedUnit{data, keyframe})` -> pipeline wraps as `EncodedFrame{ data, keyframe, … }` -> `server.broadcast(codec_type, encoded)`

**Contract Rules:**
- `Ok(None)` means no complete access unit for this call — either an intentional rate-control skip, or a pipelined out-of-process encoder whose child has not yet emitted this frame's access unit. It is never an error and never a dropped frame.
- First frame after `force_keyframe()` MUST be a keyframe (H.264: SPS+PPS+IDR; HEVC: VPS+SPS+PPS+IDR).
- `data` is **ONE complete access unit**, contiguous Annex B (start codes retained), **NOT** split per-NAL. The old per-NAL `Vec<Vec<u8>>` contract is rejected.
- `keyframe` is set BY THE ENCODER (it knows when it emitted an IDR/IRAP); the server never re-scans NALs.
- `YuvFrame.subsampling` is the chroma discriminator; an encoder handed a subsampling it did not configure returns `StreamError::ChromaUnsupported`.
- `codec()` is the SW path's producer for `config.codec` and for `EncodedFrame.codec_type`; the pipeline never assumes H.264 on either path.
- `timestamp_ns` travels **with** the access unit, not beside it. A pipelined encoder keeps a FIFO of submitted capture timestamps and attaches the head one to each completed AU — without it the pipeline would stamp that encoder's output with the wrong frame's capture time, by the pipe depth, which is exactly the TD-25 error reintroduced on the software path.
- `data` ownership **transfers** (owned `RVec<u8>`). `&mut self` means `encode` and reconfigure can never alias (borrow-checker enforced, replacing the Go M-6 mutex discipline). `force_keyframe` is likewise a frame-loop-thread call made between two `encode` calls: the cross-thread signal is an `Arc<AtomicBool>` owned by the HOST, which server tasks set and the frame loop reads and clears before the next `encode` (MODULE_PIPELINE step 0c). No add-on state is ever mutated concurrently.

---

### Contract 3: Server -> Client (Wire Protocol)

See [`MODULE_PROTOCOL.md`](./core/MODULE_PROTOCOL.md) for the authoritative definition.
Everything below travels as datagrams and streams over the WebTransport carrier, or as
tagged messages on the WebSocket fallback carrier — same tags, same framing (see
[`MODULE_TRANSPORT.md`](./core/MODULE_TRANSPORT.md) "Carrier selection"). Summary:

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
| Ping | 2 | datagram | 4-byte u32 LE nonce; pong replies on the control stream |
| _(reserved)_ | 3 | — | Reserved (client Pong is a JSON line on the **control stream**) |
| AudioPCM | 4 | datagram media | Raw S16LE PCM, fragmented (the no-codec fallback). Carries the FrameHeader for capture Timestamp; codec/rate/channels in `config`. (impl deferred) |
| _(reserved)_ | 5 | — | Formerly VideoVP8 — VP8 rejected. Do not reuse without protocol version bump. |
| _(retired)_ | 6 | — | Was Config — now a `{"type":"config"}` JSON line on the control stream |
| VideoHEVC | 7 | datagram + bootstrap | One HEVC access unit, Annex B (keyframe = VPS+SPS+PPS+IDR types 19-20) |
| AudioOpus | 8 | datagram media | One 20 ms Opus packet, single datagram (default audio codec). Carries the FrameHeader for capture Timestamp. (impl deferred) |
| CursorUpdate | 11 | datagram | 14-byte fixed record: hotspot position + visibility + ShapeID (bitmaps ride the cursor stream, tag 0x11) |
| _(retired)_ | 12 | — | Was Clipboard — now `[u32 Len][JSON]` on the **clipboard stream** |
| InputAck | 14 | input stream | 13-byte echo of client input seq + server timestamp (RTT) |
| GamepadRumble | 15 | datagram | 9-byte rumble payload (index + magnitudes + duration; see [`./interaction/MODULE_GAMEPAD.md`](./interaction/MODULE_GAMEPAD.md)) |
| VideoAV1 | 16 | datagram + bootstrap | One AV1 temporal unit (raw low-overhead OBU stream — **not** Annex B; keyframe = OBU_SEQUENCE_HEADER + key OBU_FRAME). Reserved for the `av1` HW add-on tier (see `PLATFORM_COMPAT.md`); no add-on implements it yet (impl deferred) |

One datagram fragment-chain carries exactly one access unit; fragmentation is purely byte-level within that AU (NALs are never reordered or dropped individually).

---

### Contract 4: Client -> Server (per-stream framing)

Client→server messages are discriminated by **which stream** they arrive on
(every stream's first byte is a StreamType tag — see
[`MODULE_TRANSPORT.md`](./core/MODULE_TRANSPORT.md), [`MODULE_PROTOCOL.md`](./core/MODULE_PROTOCOL.md),
[`MODULE_INPUT.md`](./interaction/MODULE_INPUT.md)). The same tags carry the same
framing on the WebSocket fallback carrier — only the carrier differs (see
[`MODULE_TRANSPORT.md`](./core/MODULE_TRANSPORT.md) "Carrier selection"):

- **Input stream (tag 0x01):** `[u16 RecLen]`-prefixed binary input records
  (6-byte record header + payload, types `0x01-0x4F`). Binary for performance +
  security (~121× faster decode, zero-alloc, smaller attack surface). Type `0x50`
  is **reserved** for a future webcam frame and is dropped by current binaries.
- **Control stream (tag 0x00):** newline-delimited JSON, rare human-triggered control.
- **Clipboard stream (tag 0x02):** `[u32 Len][JSON]` clipboard messages (off the
  control stream because payloads reach 1 MiB). The **controller** opens it eagerly,
  right after `auth_ok`, whenever `config.clipboard != "disabled"`.
- **Cursor stream (tag 0x11, server-opened uni):** `[u32 Len][record]` cursor shape
  and position records — the reliable half of the `"separate"` cursor (a bitmap is
  useless if partially delivered, so it cannot ride the datagram lane).
- **Bootstrap stream (tag 0x10, server-opened uni):** `[u32 Len][FrameHeader‖IDR]`,
  one per join.

```
INPUT  [u16 RecLen][Version=1][Type][Seq u32][…record…]  // input stream (MODULE_INPUT)
CTRL   {"type":"keyframe"}                                 // control stream — request IDR
CTRL   {"type":"resize","width":1280,"height":720}         // control stream (control role only)
CTRL   {"type":"pong","nonce":1234567}                     // control stream — u32 echoed verbatim
CTRL   {"type":"decode_unsupported","codec":"hvc1.2.4.L123.B0","chroma":"420","hdr":true} // control stream
CLIP   {"type":"clipboard","format":"text/plain","text":"…"} // clipboard stream
```

**Coordinate-space rule:** absolute pointer `X`/`Y` are in the **stream
coordinate space** advertised by the latest `config` message (`width`/`height`). The
injector's absolute range MUST equal that range. Capture, encode, Config, and
input dims must all agree (no hidden scaling); the pipeline calls
`Dispatcher.Resize` on a resolution change. The **cursor overlay uses the same
space**: `CursorUpdate.X/Y` are the hotspot in stream coordinates, so a pointer
event at `(X, Y)` and the overlay drawn at `(X, Y)` are the same pixel
(MODULE_CAPTURE "Cursor coordinate space").

---

### Contract 5: Audio -> Server  🔒 DESIGN LOCKED · ⏸️ IMPL DEFERRED (see MODULE_AUDIO)

```rust
// Audio: a per-OS capture add-on delivers PCM chunks; an encoder (Opus or PCM
// passthrough) turns them into wire payloads. host→client only. No subprocess.
// NO CHANNEL CROSSES THE BOUNDARY: the host's dedicated audio thread calls
// next_chunk() directly, and the ~4-chunk drop-oldest decoupling ring lives
// INSIDE the add-on, on its own side of the ABI. The trait has exactly these
// two methods.
pub trait AudioCapturer: Send {
    fn next_chunk(&mut self) -> Result<Option<PcmChunk>, AudioError>;
    fn format(&self) -> Format;   // canonical 48 kHz / S16LE / interleaved, Vorbis
                                  // channel order; the CHANNEL COUNT follows the host
                                  // output layout up to 7.1 (add-on resamples/reorders)
}

pub struct PcmChunk {
    pub data: RVec<u8>,    // frame_samples*channels*2, S16LE interleaved (3840 B @ 20 ms stereo)
    pub timestamp_ns: u64, // CLOCK_MONOTONIC ns, sampled AT CAPTURE in the add-on read loop
}
```

**Contract Rules:**
- `timestamp_ns` MUST be sampled at capture time (in the capture add-on's read loop), NOT when the host reads it from the channel — stamping late breaks A/V sync. Buffers are small (~60-80 ms capture, ~40 ms client) for realtime.
- `timestamp_ns` uses the SAME `CLOCK_MONOTONIC` epoch as video frames. **Audio is the master clock**; video presentation slaves to the audio playout time (see MODULE_AUDIO / MODULE_PROTOCOL "A/V Synchronization").
- Encoder output ownership transfers (owned `RVec<u8>`); audio is a **media** datagram type (carries the FrameHeader), single-datagram for Opus.

---

### Contract 6: Capture -> Hardware Encode (Zero-Copy Path)

```rust
// SurfaceCapturer is the cross-platform zero-copy contract. Capture add-ons
// that can produce GPU surfaces (KMS+EGL DMA-BUF, NvFBC CUDA buffer, SCK
// IOSurface, DXGI DD ID3D11Texture2D) implement it in addition to Capturer.
pub trait SurfaceCapturer: Capturer {
    /// A GPU-resident surface handle. Ownership MOVES to the caller, then into
    /// the HW encoder. Because FbInfo is passed BY VALUE into encode_surface and
    /// FbInfo's Drop releases the resource, it is released exactly once on every
    /// path automatically. This replaces the Go capturer's two-site fd discipline
    /// (`kms.go` closes the previous fd at the top of the next NextFrame and the
    /// last one in Close), which was correct but uncheckable — see TD-01.
    fn next_surface(&mut self) -> Result<Option<FbInfo>, StreamError>;
}

// Platform-specific surface handle as a tagged enum (not a struct of nullable
// fields). Drop releases the underlying resource exactly once (RAII replaces the
// manual `Release func()`).
pub struct FbInfo {
    pub width: u32,
    pub height: u32,
    pub rotation: Rotation,       // how far CLOCKWISE this surface must be turned to
                                  // be upright; width/height describe it AS DELIVERED.
                                  // The HW encoder applies it in the same VPP pass as
                                  // the downscale, or returns FallbackToSoftware.
    pub timestamp_ns: u64,        // CLOCK_MONOTONIC ns, stamped at capture
    pub handle: SurfaceHandle,
}

pub enum SurfaceHandle {
    // Linux: DMA-BUF. `OwnedFd` closes the fd on Drop (no manual close).
    DmaBuf { fd: std::os::fd::OwnedFd, stride: u32, fourcc: u32, modifier: u64 },
    // macOS: CVPixelBuffer-backed IOSurface (retained; released on Drop).
    IoSurface(objc2_io_surface::IOSurface),
    // Windows: ID3D11Texture2D (COM ref released on Drop via windows-rs).
    D3D11Texture(windows::Win32::Graphics::Direct3D11::ID3D11Texture2D),
}

// Every HW encoder add-on implements this. See MODULE_HARDWARE_ENCODE.md.
pub trait HardwareEncoder: Send {
    /// CONSUMES the surface (moved in → dropped here → released exactly once on
    /// success AND error). Returns one contiguous Annex B access unit, or
    /// StreamError::FallbackToSoftware if the surface cannot be imported.
    fn encode_surface(&mut self, surface: FbInfo) -> Result<EncodedUnit, StreamError>;
    fn force_keyframe(&mut self);
    /// WebCodecs codec string for the config handshake (e.g. "avc1.64002A" at
    /// 1080p60; computed from the active profile and geometry, never a constant —
    /// see MODULE_ABI "Codec-string computation"), built by the Layer-2 adapter
    /// with `codec_string(self.profile(), width, height, fps)` from the Layer-1
    /// `abi::HwEncoder::profile()`. An HDR session reports
    /// `VideoProfile::HevcMain10`.
    fn codec(&self) -> &str;
}
```

**Data Flow:** `capturer.next_surface()` -> `hw_encoder.encode_surface(surface)` (surface moved in, released on drop) -> `server.broadcast(codec_type, encoded)`

**Contract Rules:**
- `FbInfo` ownership transfers capture add-on → HW encoder add-on by value. It is released **exactly once on every path** by `Drop` — the capturer and pipeline never release it (single-owner rule, M-1, now compiler-guaranteed).
- If `encode_surface` returns `StreamError::FallbackToSoftware`, the pipeline degrades permanently to the software path for the rest of the session.
- Codec advertisement: the HW encoder advertises its active codec via `codec()` (H.264 for every SDR session; HEVC Main10 only when HDR is requested — see MODULE_STREAM_PARAMS); the server forwards that exact WebCodecs string to the client in the `config` message. The client reports its decode capability in the `auth` message and the server never advertises a codec no attached client can decode (MODULE_PROTOCOL "Decode capability"); the chroma downgrade is the other client-driven negotiation.
- `force_keyframe` is called only from the frame-loop thread, between two `encode_surface` calls: the cross-thread signal is an `Arc<AtomicBool>` owned by the host (MODULE_PIPELINE step 0c). No add-on state is ever mutated concurrently.

---

### Contract 7: Pipeline -> Server (Encoded Frame)

```rust
// The pipeline pairs encoder output with the frame's metadata before broadcasting.
// EncodedFrame lives in the featherdesk-stream crate — shared by SW and HW paths.
pub struct EncodedFrame {
    pub data: bytes::Bytes,// Contiguous Annex B (start codes retained), host-side. NOT
                           // split per-NAL. The server prepends the 22-byte
                           // FrameHeader and moves `data` into the per-session
                           // frame-granular out-queue (a task fragments it later).
    pub width: u16,
    pub height: u16,
    pub timestamp_ns: u64, // CLOCK_MONOTONIC ns, carried through from capture
    pub keyframe: bool,    // true if this access unit is a keyframe
    pub codec_type: u8,    // frame_type::VIDEO_H264 or VideoHevc
}

// The server is a concrete type the pipeline holds via a handle/Arc.
impl Server {
    // Assembles one access unit and fans it out to per-session queues.
    // The server assigns the video Sequence and uses f.keyframe (encoder-set)
    // for IDR caching + the bootstrap stream.
    pub fn broadcast(&self, codec_type: u8, f: EncodedFrame) { /* … */ } // non-blocking: pushes into per-session queues
    // Already-encoded audio (Opus packet or raw PCM). codec_type = AudioOpus(8)
    // | AudioPcm(4); server assigns the independent audio Sequence + FrameHeader.
    pub fn broadcast_audio(&self, codec_type: u8, payload: bytes::Bytes, capture_ts_ns: u64) { /* … */ }
}
```

**Contract Rules:**
- The pipeline carries `width/height/timestamp_ns` from the capture step through encode to here (they are NOT recomputed).
- The server owns the per-type sequence counters; the pipeline never sets them.
- `f.keyframe` (set by the encoder) lets the server cache the keyframe access unit (SPS+PPS+IDR / VPS+SPS+PPS+IDR) for the bootstrap stream. The server does **not** re-parse NALs (M-2).

---

### Contract 8: Cursor -> Server (client-side cursor overlay)

The producer half of the `cursorMode = "separate"` design decision. Until this
contract existed, every module *consumed* `CursorUpdate` (server `send_cursor`,
wire type 11, the client's `cursor.js`) but **nothing produced it**.

```rust
// OPTIONAL trait on a capture add-on (MODULE_CAPTURE). Advertised at probe time
// via ProbeReport.caps & AddonCaps::CURSOR — a sabi object cannot be downcast,
// so the flag is the only way the host knows this is real. The host reaches it
// through the ONE capture object it owns, via `CaptureHandle::as_cursor()` —
// `Some` iff `caps().has(abi::AddonCaps::CURSOR)` — never a second Box.
pub trait CursorCapturer: Capturer {
    /// Ok(Some(s)) = the pointer changed; Ok(None) = unchanged. Non-blocking.
    /// `s.shape` is Some only on a BITMAP change. Coordinates are capture pixels.
    /// FIRST CALL after this object is constructed MUST return Ok(Some(..))
    /// carrying the current position, the current visibility AND the current
    /// bitmap, even though nothing has changed — the host has no other way to
    /// learn the initial pointer state, and the join-resync cache is seeded from
    /// it. From the second call on, the "only if it changed" rule applies. The
    /// bitmap comes back at the OS's NATIVE size; the 128-pixel wire cap is the
    /// host's, enforced in `CursorPublisher::tick`.
    fn next_cursor(&mut self) -> Result<Option<CursorState>, StreamError>;
}
```

**Data Flow:** frame loop step (0b) → `self.capture.as_cursor()?.next_cursor()` →
`CursorPublisher::tick` (128-pixel wire clamp, capture pixels → stream pixels,
ShapeID over the POST-clamp bytes) →
`server.send_cursor_shape(..)` on the cursor stream (`stream_type::CURSOR`, 0x11,
reliable) then `server.send_cursor(..)` as datagram `frame_type::CURSOR_UPDATE`
(11) → client `cursor.js` overlay.

**Contract Rules:**
- **The bitmap is not on the datagram.** A control datagram is never fragmented,
  and a 64x64 RGBA cursor is 16384 bytes against a per-datagram budget of
  `max_datagram_size() − 8`, which is on the order of a kilobyte and is read from
  the transport at runtime (MODULE_TRANSPORT "Datagram Fragmentation"). Position
  and visibility are a fixed 14-byte latest-wins datagram; shapes are reliable records
  on the cursor stream, referenced by a content-hash `ShapeID`. A shape is
  useless if partially delivered, so it needs reliability, not latest-wins.
- Polled **before** the frame-skip check, so the pointer keeps moving while video
  is skipped, dropped, or static. This is the entire latency argument for
  `"separate"`; polling it after capture would freeze the cursor precisely when
  the stream is already degraded. The add-on must latch its pointer source so this
  works on a tick where no frame is taken — on Windows the pointer is metadata of
  `AcquireNextFrame`, so under `"separate"` `dxgi_dd` takes TWO zero-timeout
  acquires per tick: one in `next_cursor` before pacing (latch the pointer, and
  `CopyResource` the desktop texture if `LastPresentTime` advanced, because DXGI
  never re-delivers an already-acquired frame) and one in `next_frame` /
  `next_surface` after `sleep_to_interval`, so the encoded pixels are not stale by
  the pacing sleep (MODULE_CAPTURE "Cursor and frame acquisition order"). Under
  `"embedded"` `next_cursor` is not polled and the single acquire lives in
  `next_frame`.
- A shape is sent once per session per distinct bitmap; the add-on decides *that*
  the bitmap changed, and the host decides *which* bitmap it is. The pipeline does
  not diff bitmaps on the hot loop.
- Latest-wins, ordered by `DatagramHeader.FrameID` so a reordered datagram cannot
  undo a newer one. The **server** assigns that FrameID inside `send_cursor`, from
  its per-Type CURSOR_UPDATE counter — one `AtomicU32` per control datagram Type,
  owned by the Server, global across sessions, incremented once per message at
  serialization time, including an idle re-send (so a re-send is "newer" to the
  client's reorder check and is applied). The caller supplies no sequence:
  `protocol::CursorUpdate` has no field for one and `CursorPublisher` holds no
  `seq`. A lost update is superseded by the next; the client never requests one.
  Because the *last* update before the pointer goes idle has no successor, the
  publisher re-sends it at 250/500/750 ms and then stops.
- A joining or resuming client is seeded on its own cursor stream with the cached
  shape and a reliable position record — `next_cursor` reports "changed since the
  last call" per add-on, not per session, so an idle host would otherwise leave a
  new client with no pointer at all.
- Never fatal. `FrameLoop::on_cursor_error` is the single normative response to a
  `next_cursor` failure and to a `CURSOR` capability lie, and it runs in this
  order: (1) send the cached position with `visible = 0`, so the client hides a
  pointer it can no longer trust rather than freezing one at a stale place;
  (2) `self.cursor = None` and `clear_cap(abi::AddonCaps::CURSOR)` on the handle,
  so `as_cursor()` returns `None` and the poll is skipped by construction — the
  capture object is NEVER dropped, because on X11 and DXGI the cursor query
  borrows the same display/duplication handle the frame path is still using;
  (3) `featherdesk_cursor_errors_total` increments and one `warn!` fires;
  (4) flip to `cursor_mode = Embedded` and push a fresh `config` ONLY IF the
  selected add-on declares `AddonCaps::EMBED_CURSOR` AND the rebuild with
  `CaptureConfig.embed_cursor = true` succeeds. Otherwise `cursor_mode` stays
  `Separate`, NO `config` is pushed, and the session runs on without a
  client-side pointer — announcing `"embedded"` on a capturer that was built
  cursor-free would cost the user the pointer twice. Never retried per frame, and
  it never advances the capture-error escalation ladder or stops video.
- **Embedding is the add-on's job, not the host's.** The host cannot blend into a
  GPU surface, and three of the four add-ons embed by asking the OS or driver
  (`showsCursor`, `bWithCursor`, or a compositor that already did it). The one
  software compositor in the tree is `capture::blend_cursor`. A capture add-on
  that can deliver a cursor in neither mode is not an eligible capturer and is
  skipped at selection, rather than shipping a stream with no visible pointer.

---

### Contract 9: Clipboard <-> Server (bidirectional)

Clipboard is the one module with a **symmetric** contract, and the two halves are
wired differently — which is why the host→client half was missing until now.

```rust
// One clipboard, two directions, ONE session-long driver and one cheap handle.
// `spawn` creates both; nothing anywhere holds `&mut` on the monitor, so a paste
// arriving from any session task cannot contend with the running driver.
let (clipboard, driver) = clipboard::spawn(cfg.clipboard.clone(), cancel.clone())?;

// C→H (client copies → host clipboard): a CALLBACK the server invokes, from N
// session tasks, concurrently with the running driver. `set` takes `&self`.
server.set_clipboard_callback(Box::new({
    let clipboard = clipboard.clone();
    move |c| clipboard.set(c)
}));

// H→C (host copies → client clipboard): a PUSH the pipeline's clipboard task
// makes. `changes()` is a `watch` receiver — taken ONCE, ABOVE the loop.
let mut changes = clipboard.changes();
tokio::spawn(async move {
    tokio::select! {
        _ = cancel.cancelled() => {}
        r = driver.run(cancel.clone()) => { if let Err(e) = r {
            tracing::warn!(target: "clipboard", err = %e, "clipboard sync disabled for this session");
        }}
        _ = async {
            while changes.changed().await.is_ok() {
                if let Some(c) = changes.borrow_and_update().clone() {
                    server.send_clipboard(c);
                }
            }
        } => {}
    }
});
```

**Data Flow (H→C):** OS clipboard event → `ClipboardHandle::changes()` → pipeline
clipboard task → `server.send_clipboard` → `[u32 Len][JSON]` on the clipboard
stream (tag `0x02`, the controller's, opened at connect) → client.
**Data Flow (C→H):** client → clipboard stream → server `clipboardReader` →
`set_clipboard_callback` → `ClipboardHandle::set` → OS clipboard.

**Contract Rules:**
- **The server gates, not the caller.** The pipeline task pushes unconditionally and
  stays dumb; every gate lives inside `send_clipboard` (H→C) or the server's
  `clipboardReader` (C→H). The two gate lists are exhaustive and ordered:

  **H→C, evaluated inside `send_clipboard` once per connected session:**
  1. `[clipboard] enabled` — false → drop for everyone, `featherdesk_clipboard_drops_total{direction="h2c",reason="disabled"}`++.
  2. `[clipboard] direction ∈ {bidirectional, host_to_client}` — else drop, `reason="direction"`.
  3. Recipient's effective role **is** `Control` — else skip that recipient, `reason="role"`.
     (Set membership, not `!= view`.)
  4. Recipient has an open `0x02` stream — else skip that recipient, `reason="no_stream"`.
     Never queued for later.
  5. Format filter: if `"html"` ∉ `[clipboard] formats`, downgrade to the `Text` fallback.
  6. `clipboard::sanitize_html` — H→C is sanitized because the host process is
     not trusted to put safe markup on its own clipboard.
  7. Size cap — over cap → text truncated, HTML dropped with `reason="too_large"`.
  8. Write `[u32 Len][JSON]`. A full or closed stream drops **that** push for **that**
     session and bumps `reason="stream_full"`; a clipboard stall never back-pressures the
     monitor task.

  **C→H, evaluated inside `clipboardReader` before `set_clipboard_callback` is invoked:**
  1. Role gate at stream accept (MODULE_SERVER "Role gate table" row 5) — a `0x02` stream
     from a non-controller, or any `0x02` stream when `[clipboard] enabled = false`, is
     reset with `close::PROTOCOL_ERROR` at **stream** scope before a byte is read.
  2. `[u32 Len]` pre-allocation cap — over cap → reset the stream with
     `close::PROTOCOL_ERROR`, **before** allocating.
  3. `[clipboard] direction ∈ {bidirectional, client_to_host}` — else drop the message
     silently (the stream stays open), `featherdesk_clipboard_drops_total{direction="c2h",reason="direction"}`++.
  4. Format filter, as H→C step 5.
  5. `clipboard::sanitize_html` — the client is outside the trust boundary.
  6. Content cap.
  7. Invoke `set_clipboard_callback` → `ClipboardHandle::set`.

  A rejected clipboard message is never answered with an error message on the wire (there
  is no clipboard error type and adding one would leak whether a gate is enabled); it is
  dropped, counted, and logged at `debug` with the **byte length only**, never content.
- Viewers and co-op players never receive host clipboard pushes (host-secret leakage),
  regardless of `direction`, and cannot open the clipboard stream (`0x02`) at all.
- `send_clipboard` is non-blocking and infallible to the caller.
- The handle is `Send + Sync` and `set` takes `&self`; the driver is `Send` and is never
  shared. There is no lock on this path.
- Clipboard never rides the control stream — payloads reach 1 MiB, far past the
  4 KiB control-line cap.

---

## Module Dependency Graph

```
                 featherdesk-stream (shared types: Params, EncodedFrame, StreamError)
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

> Crate dependency edges documented:
> - `encode -> capture` (Converter takes `&capture::Frame`)
> - `hwencode -> capture` (`SurfaceHandle` / `capture::FbInfo`)
> - `encode, hwencode, capture -> stream` (Params, `StreamError`)
> - `transport -> protocol` (transport references `protocol::close::*`; protocol is the sole owner of the close codes)
> - `server -> {transport, protocol, auth, stream, input, clipboard, filetransfer}` (transport surface + types + auth gate + input dispatch + clipboard/file-transfer stream dispatch)
> - `pipeline -> transport` (the pipeline builds the QUIC transport via `transport::Transport::new` and hands it to the server)
> - `input` injection add-ons -> `input` core (KeyMouseInjector/TouchInjector + HID table)
> - `clipboard` is a core leaf (per-OS files); `filetransfer -> transport` (its `ServeStream` takes a `transport.Stream`); `server` calls both
> - No cycles. `stream` is the shared leaf. `pipeline` is the sole orchestrator.
> - `audio -> stream` (`StreamError`); `pipeline -> audio` (the pipeline SELECTS the audio capture and codec add-ons at startup step 11 into an `AudioPlan`; `AudioLoop::open()` constructs them **on the audio thread** at step 13 and `AudioLoop::run` drives them); `server` never imports `audio` — encoded chunks reach it as bytes through `broadcast_audio` (Webcam was removed entirely)
> - `abi` is imported by every add-on-facing crate (`capture`, `encode`, `hwencode`, `input`, `audio`) **and** by `pipeline`, which owns the Layer-2 adapters; it depends on nothing in this graph
> - No `libavcodec`, no `libvpx` -- both rejected. No `ffmpeg` on the default path; the opt-in `x264` add-on is the only component that shells out to it.
> - No custom `logger` module -- every module uses the `tracing` crate directly.

**Key Properties:**
- Each crate is a leaf or near-leaf (depends only on std + system libs via FFI)
- Crates NEVER cyclically depend on each other (Cargo forbids cycles anyway)
- Only `pipeline` imports all modules — it's the sole wiring point
- `protocol` is shared between `server` and `client` (pure data, no logic deps)
- `hwencode` and `encode` are sibling modules, not parent-child (both define separate interfaces; add-ons implement one)

---

## Orchestrator Responsibilities (Pipeline Module)

The orchestrator is now a proper module (`MODULE_PIPELINE.md`) — not inline in main.go. It:

1. Loads config via `config::load(--config path)` per [`./core/MODULE_CONFIG.md`](./core/MODULE_CONFIG.md) — the only CLI **flag** is `--config`; three subcommands (`hash-password`, `revoke-device`, `list-devices`) dispatch before config load, per MODULE_CONFIG "CLI surface"
2. Probes loaded capture + encoder add-ons (no static enum; the runtime asks each loaded add-on whether its prerequisites are met)
3. Selects capture add-on per `[capture]` config (auto-probe order or forced)
4. Selects encode path per `[encode]` config:
   - **Hardware add-on** picked when it accepts the zero-copy surface handle produced by the selected capture add-on (DMA-BUF / IOSurface / D3D11 texture)
   - **Software add-on** picked when no compatible HW add-on is loaded OR `force_addon` names a SW add-on
5. Creates and connects all modules
6. Manages lifecycle (signal handling, graceful shutdown, SIGHUP config reload — it registers `config::watch`, spawns the `fd-config` applier task (step 12b) and owns the frame-path applier; see MODULE_CONFIG "Hot reload behavior" for the per-row owner)
7. Runs the frame pipeline loop with pacing and drop logic
8. Runs sustainable-rate control: measures what the host actually sustains, lowers the advertised fps to it, and never goes below 5 fps
9. Exports rolling-window statistics via Prometheus on the metrics port (see `[metrics]` in MODULE_CONFIG)

---

## Cross-Cutting Concerns

### Error Handling Strategy
- Modules return errors; orchestrator decides recovery strategy
- Transient errors (capture hiccup, encode skip): log and continue
- Fatal errors (device lost, context cancelled): propagate for shutdown
- Never panic in hot path

### Buffer Ownership Model

Rust's ownership/`Drop` make this model compiler-enforced rather than
discipline-by-comment (the Go footguns here — borrowed slices held too long,
`Release()` missed on an error path — become impossible to write).

- **Capture (CPU):** `next_frame()` returns an **owned** `Frame { data: RVec<u8> }`; ownership transfers to the caller, dropped deterministically. (No "borrowed, copy before next call" hazard.) `stride` may exceed `width*4`.
- **Capture (surface):** `next_surface()` returns an **owned** `FbInfo`; the HW encoder's `encode_surface(surface: FbInfo)` takes it **by value**, so `Drop` releases it **exactly once on every path** (success/error/fallback). The capturer and pipeline never release it (single-owner rule, M-1, compiler-guaranteed).
- **Convert:** `convert(&frame)` writes into a reused `YuvFrame` the converter owns and lends as `&YuvFrame` for the duration of the encode call (zero-alloc steady state; the borrow checker forbids retaining it past the next `convert`).
- **Encode:** `encode()` returns `Some(EncodedUnit { data: RVec<u8>, keyframe })` — ONE contiguous Annex B access unit, **owned** (the old per-NAL `Vec<Vec<u8>>` contract is rejected).
- **Broadcast:** Server assembles header+payload into one buffer and **moves** it into the per-session **frame-granular** out-queue (`tokio::sync::mpsc`); the per-session task fragments it into datagrams at send time.

**Rule:** Any function that returns borrowed data must document it in the interface comment. The caller must never store borrowed slices beyond the next call boundary.

### Concurrency Model
- Capture loop: a dedicated OS thread (`std::thread`, not a Tokio worker) because EGL/X11/D3D contexts are thread-affine. It CONSTRUCTS, uses and drops the capture handle, the converter and the encoder itself — creation does not happen on the startup thread — and hands frames to the async world by pushing whole access units into per-session rings.
- Encode: synchronous call on the capture thread (frame drops preferred over pipeline latency).
- Server broadcast: fan-out into per-session frame-granular out-queues (`FrameOut`: a bounded ring with producer-side drop-oldest — see MODULE_SERVER); a per-session async task fragments + sends datagrams.
- Audio: a second dedicated OS thread on the same construct-use-drop rule; it calls `next_chunk()` → `encode()` → `broadcast_audio` with no host-side channel between them.
- Input: handled in the session's control/input task (async read loop), serialized into the shared Dispatcher by a `std::sync::Mutex` held for one record.
- macOS: the process main thread runs a CFRunLoop for AppKit-dependent add-ons; it is never parked in `block_on` (see MODULE_PIPELINE R-PIP-01).

**Thread-affinity rule (normative).** Every capture, converter and video-encoder
object is **constructed, used and dropped on the frame-loop thread**, and every
audio capture and audio encoder object on the audio thread. `pipeline::new()`
selects add-ons; it does not construct these objects. The first statement
executed on each worker thread is `open()`, which builds them; the last is the
struct's drop. The traits are nevertheless declared `Send`, because what
`std::thread::spawn` moves is the whole `FrameLoop` / `AudioLoop` struct — media
fields included, all `None` at that instant — and an auto trait is computed from
the field types, not from the runtime value. The objects themselves still never
cross a boundary: an EGL context bound with one `eglMakeCurrent`, a D3D11 device,
or a COM apartment stays valid for the object's entire life, and the
`unsafe impl Send` an add-on writes is justified by that single-thread discipline,
not by any real hand-off.

#### Which objects live where, and what each trait therefore declares

| Trait / type | Declared bound | Where the object lives |
|---|---|---|
| `capture::Capturer` | `: Send` | frame thread: built by `FrameLoop::open`, dropped by `FrameLoop`'s epilogue |
| `capture::SurfaceCapturer` / `CursorCapturer` / `ConfigurableCapturer` | `: Capturer` | the same object, reached through `CaptureHandle` accessors |
| `capture::CaptureHandle` | `: Capturer` | the one owning `Box` on `FrameLoop` |
| `encode::Encoder`, `encode::ConfigurableEncoder` | `: Send` / `: Encoder` (the sub-trait inherits it and does not repeat it) | frame thread |
| `encode::EncoderHandle` | `: Encoder` | the one owning `Box` on `FrameLoop` |
| `encode::Converter` | *(concrete struct — `Send` by auto-derivation)* | frame thread |
| `hwencode::HardwareEncoder`, `ConfigurableHardwareEncoder` | `: Send` / `: HardwareEncoder` (the sub-trait inherits it and does not repeat it) | frame thread |
| `hwencode::HwEncoderHandle` | `: HardwareEncoder` | the one owning `Box` on `FrameLoop` |
| `audio::AudioCapturer`, `audio::AudioEncoder` | `: Send` | audio thread: built by `AudioLoop::open`, dropped there |
| `input::Dispatcher` | `: Send` | `Arc<Mutex<Box<dyn Dispatcher>>>` — N session tasks call `dispatch`, the frame thread calls `resize` |
| `input::KeyMouseInjector` / `TouchInjector` / `GamepadInjector` | `: Send` | owned by the Dispatcher, so they move with it |
| `clipboard::Monitor` (the driver half) | `: Send` | moved onto the clipboard task |
| `clipboard::ClipboardHandle` (the write/read half) | `Clone + Send + Sync` (concrete type) | the server callback and the pipeline |
| `filetransfer::Service` | `: Send + Sync` | `Arc<dyn Service>` — N concurrent stream tasks call `serve_stream(&self, …)` |
| `stream::Manager` | `: Send` | `Mutex<Box<dyn Manager>>` captured by the params callback |
| `pipeline::CaptureAddon` / `EncoderAddon` / `InputAddon` / `AudioAddon` | `: Send + Sync` | `Arc<AddonRegistry>`, cloned into every worker |
| `abi::FeatherDeskAddon` (`#[sabi_trait]`) | `: Send + Sync` supertraits | the loaded library's factory object |
| `server::Server`, `transport::Transport` / `Session` / `Stream` / `UniStream`, `auth::Authenticator`, `server::SessionCache` | `: Send + Sync` (already declared) | shared |

**Thread-affine add-ons that are NOT on a worker thread.** Two add-on classes
own an OS API that pins work to a thread the host does not control. Each owns its
own thread internally and exposes a thread-agnostic surface; none of them gets an
`unsafe impl Send` on raw context state. `sck` is **not** one of them: it
implements no `capture::CursorCapturer` and never sets `AddonCaps::CURSOR`, so
there is no `NSCursor` query and no main-queue post on that path — it declares
`EMBED_CURSOR | EMBED_CURSOR_SURF`, macOS always resolves `cursorMode =
"embedded"`, and ScreenCaptureKit composites the pointer via
`SCStreamConfiguration.showsCursor` before the `CMSampleBuffer` is delivered.

| Add-on class | Pinned to | Internal arrangement |
|---|---|---|
| `wasapi` (Windows audio) | the thread that called `CoInitializeEx` | a dedicated `std::thread` owned by the add-on; `next_chunk` pops its ring |
| `win_touch`, `interception` (Windows input) | the thread that called `InitializeTouchInjection` / `interception_create_context` | a dedicated `std::thread` fed by a bounded channel; `interception` uses the same funnel |

#### Queues and buffers

Every queue in the system, with its capacity and its overflow policy. A queue not
in this table does not exist; a new one must be added here with both columns
filled.

| # | Queue / buffer | Type | Capacity | Overflow policy | Producer → consumer |
|---|---|---|---|---|---|
| 1 | per-session video out-queue (`Session.frame_out`) | `Arc<FrameOut>` | `[transport] datagram_send_queue_frames` = **8** whole access units | **drop-OLDEST**; `featherdesk_datagram_send_drops_total{kind="video"}` | frame thread → session `datagram_pump` |
| 2 | per-session audio out-queue (`Session.audio_out`) | `Arc<FrameOut>` | `[transport] audio_send_queue_chunks` = **25** chunks (≈500 ms at 20 ms) | **drop-OLDEST**; `…{kind="audio"}` | audio thread → same pump |
| 3 | parameter funnel (`param_tx`/`param_rx`) | `tokio::sync::mpsc` of `stream::ParamUpdate` | **32** | `try_send`; `Full` → `StreamError::Backend("param queue full")` to the caller + `featherdesk_param_updates_dropped_total`. **Never** drop-oldest — order is meaningful | `stream::Manager` (sole producer) → frame thread |
| 4 | applied params (`applied_tx`) | `tokio::sync::watch` of `stream::Applied` | **1** (latest-only by construction) | latest-wins; no drop counter — a superseded value is by definition uninteresting | frame thread → Manager, server, exporter |
| 5 | config hot reload (`cfg_tx`) | `tokio::sync::watch` of `Arc<config::Config>` | **1** | latest-wins | `config::watch` (SIGHUP) → the `fd-config` applier task + the frame loop's step-(0d) applier |
| 6 | keyframe request (`kf_req`) | `Arc<AtomicBool>` | **1 bit** | coalescing by construction — N requests in one frame interval produce one IDR | session tasks → frame thread |
| 7 | audio capture ring | **inside the add-on** | **4** frames (≈80 ms at 20 ms) | **drop-OLDEST**; the add-on counts drops and the host surfaces them as `featherdesk_audio_drops_total` | OS device callback → `next_chunk()` |
| 8 | host clipboard changes (`ClipboardHandle::changes`) | `tokio::sync::watch` of `Option<Content>` | **1** | latest-wins — the OS clipboard holds exactly one payload, so a superseded copy is not a loss; the receiver is cloneable and is taken once outside the drain loop | clipboard driver → clipboard task |
| 9 | pending clipboard writes (`ClipboardHandle::set`) | `tokio::sync::mpsc` of `(Content, oneshot)` | **16** | `try_send`; `Full` → `ClipboardError::Busy` returned to that session, which the server logs and drops. Never blocks a session task | N session tasks → clipboard driver |
| 10 | accepted sessions (`Transport::sessions`) | `tokio::sync::mpsc` of `Box<dyn Session>` | **16** | `try_send`; `Full` → the new session is closed immediately with `close::SERVER_FULL (4429)`, reason `"server_busy"`, rather than queued | QUIC listener → server accept loop |
| 11 | file-transfer accepted-but-waiting queue (`[filetransfer] queue_depth`) | FIFO | **8** (`0–64`; allocated once at `Service` construction) | `ERROR {code:"queue_full"}` at the depth cap | `stream_acceptor` (0x03 stream) → `filetransfer::Service` transfer slots |
| 12 | `win_touch` injection funnel | `std::sync::mpsc::sync_channel` | **64** touch frames | `try_send`; `Full` → drop the frame + `warn` once per second | add-on's reader → its pinned injection thread |
| 13 | datagram reassembly (client) | one buffer per `(Type, FrameID)` | **2** in-progress buffers per Type (the newest `FrameID` and one predecessor); total in-progress bytes ≤ `[transport] reassembly_max_bytes` (4 MiB); `FrameHeader.PayloadSize` **≤ 16 MiB checked before allocation** | a third `FrameID` evicts the oldest; over the byte cap, evict oldest-`FrameID`-first (`featherdesk_reassembly_evictions_total`); deadline `fragment_reassembly_ms` → drop + request keyframe | — |
| 14 | client input-latency map (`sentAt`) | JS `Map` | **≤ 4096** entries | on insert, if `size > 4096` delete the **1024 oldest** keys (a `Map` iterates in insertion order) — the server acks only records it injects, so exact-seq deletion alone leaks for the whole session | — |
| 15 | client present queue (backgrounded tab) | array of `VideoFrame` | **3** | drop-oldest **and `close()` the evicted `VideoFrame`** — an unclosed frame pins a GPU buffer | — |
| 16 | clipboard stream `[u32 Len]` payload | not a queue: a client-supplied allocation size | **`Len` ≤ 6 × `[clipboard] max_bytes` + 1024** (`max_bytes` default 1 MiB, validated `1024 ..= 4_194_304`; `6 ×` is the EXACT worst case for JSON escaping — a control byte frames as `\u00XX`, six bytes out for one in — and the `+1024` covers the envelope; content itself is capped at `max_bytes` after parsing), **checked before allocation** | over cap → `cancel_read` + `cancel_write(close::PROTOCOL_ERROR)`; the transfer is dropped | — |
| 17 | poisoned add-on set | `HashSet<String>` on `FrameLoop` | bounded by the number of loaded add-ons | n/a — insert-only for the process lifetime, frame thread only | — |
| 18 | bootstrap IDR cache | one access unit under `idr_mu` | **1** | latest-wins | — |
| 19 | Dispatcher pressed-set | `HashSet` inside `input::Dispatcher` | **≤ 512** entries (keys + buttons + live touch ids); a down-event beyond the cap is injected but not recorded, and logged once | — | — |
| 20 | per-session InputAck ring | `Arc<AckRing>` | **64** acks | drop-OLDEST; `featherdesk_input_acks_dropped_total` | input-reader task → ack-writer task |
| 21 | audio descriptor (`audio_desc_tx`) | `tokio::sync::watch` of `Option<pipeline::AudioDescriptor>` | **1** (latest-only by construction) | latest-wins | audio thread → frame thread |
| 22 | `sck` latest-only sample slot | `Mutex<Option<SampleBuf>>` inside the add-on (`SampleBuf` is a `Send` newtype over `CMSampleBufferRef`) | **1** | latest-wins: the delegate DROPS the occupant it replaces, releasing that retain — an unreleased buffer pins an IOSurface | ScreenCaptureKit `sampleHandlerQueue` → frame thread |

### Frame Drop Strategy
- The system prioritizes realtime delivery over frame completeness
- If encode takes longer than the frame interval, the NEXT capture is skipped (not queued)
- Skip accumulation is bounded at `fps/5 - 1` consecutive ticks (~200 ms), so one slow frame never stalls the stream; the *frame rate* floor is 5 fps and is enforced by sustainable-rate control, which also lowers `params.fps` so rate control, GOP length and `config.fps` stay calibrated to what is actually delivered
- Frame sequence numbers in the protocol allow clients to detect drops and request IDR if needed
- A dropped frame never enters the encode pipeline — it's discarded at capture

### Canonical Media Clock (A/V Sync)
- A single `CLOCK_MONOTONIC` epoch is established at process start.
- EVERY media timestamp on the wire — every video frame from every capture backend, and every audio chunk — is sampled from this clock in nanoseconds, AT CAPTURE TIME.
- **Audio is the master clock.** When audio is present the client plays it gaplessly from a small (~40 ms) buffer and presents the **video** frame nearest the audio playout time (video holds/drops to track audio). Audio is never held/dropped for sync. See MODULE_PROTOCOL / MODULE_AUDIO. (No audio ⇒ video presents on its own capture clock.)
- The logger uses wall-clock (UTC) for human-readable lines; this is a SEPARATE clock and must never be used for media timestamps.
- Anti-pattern (current code, to be removed): `time.Now().UnixMilli()` for frame/audio timestamps — wrong clock domain and wrong unit.

### Motion-to-photon budget

One budget, LAN, 1080p60, hardware encode. Every module's Performance Targets
table is a line of this table and may not contradict it.

| Stage | Owner | Budget | Source |
|-------|-------|-------:|--------|
| Capture acquire (p50) | capture add-on | 7.0 ms | MODULE_CAPTURE |
| Encode (HW, p50) | HW encoder add-on | 4.5 ms | MODULE_HARDWARE_ENCODE |
| Pipeline bookkeeping | pipeline | 0.1 ms | MODULE_PIPELINE |
| Broadcast fan-out (25 clients) | server | 2.0 ms | MODULE_SERVER |
| Network (LAN RTT/2) | — | 0.4 ms | MODULE_SERVER "Ping RTT (LAN) <5 ms" |
| Reassembly + decode | web client | 5.0 ms | MODULE_WEB_CLIENT |
| Present (drawImage) | web client | 1.0 ms | MODULE_WEB_CLIENT |
| **Total, audio disabled** | | **~20 ms** | the product target |
| Audio jitter buffer, when audio is enabled | web client | +40 ms | MODULE_AUDIO |
| Audio decode + worklet | web client | +5 ms | MODULE_AUDIO |
| **Total, audio enabled** | | **~65 ms** | |

**Enabling audio triples motion-to-photon**, because the renderer presents the
video frame nearest the audio playout time and audio playout is one jitter buffer
behind capture. That is the deliberate trade of audio-master sync — a 20 ms audio
dropout is instantly audible, a frame of video judder is not — but it is a trade,
not a free choice, and `[audio] enabled` is `false` by default partly for this
reason. An operator who wants both interactive pointer latency and sound should
run `[audio] frame_ms = 10`, which halves the packet interval and lets the client
target a ~20 ms buffer instead of ~40 ms, bringing the audio-enabled total to
~45 ms.

**Software encode** does not meet the 20 ms figure and is not expected to: the
project target is 60 fps on hardware, **30 fps on the software fallback**, and
the software path's budget is capture readback (see MODULE_CAPTURE "Readback
cost") + encode (3-8 ms) in place of the two hardware rows.

### Cursor Model
- `Config.cursorMode` tells the client how the cursor is delivered:
  - `"embedded"` — the pointer is composited into the video frame by the capture
    add-on (`SCStreamConfiguration.showsCursor`, NvFBC `bWithCursor`, a
    compositor's own software cursor, or `capture::blend_cursor` on the CPU path).
  - `"separate"` — the cursor is NOT in the video; the server sends a 14-byte
    `CursorUpdate` datagram for position and a reliable shape record on the cursor
    stream when the bitmap changes, and the client renders it as an overlay.
    Lowest latency. It is **not** a precondition of the zero-copy hardware path:
    that path runs under both modes, and on macOS the zero-copy pairing is `sck` +
    `vt_hw`, which always resolves `"embedded"`. `"separate"` is required only for
    an add-on that cannot embed onto a surface — one declaring `EMBED_CURSOR` but
    not `EMBED_CURSOR_SURF` can burn the pointer into a CPU frame only, and is
    paired with the software path when the resolved mode is `"embedded"`.
- The mode is **derived from capability** by one pure function,
  `pipeline::CursorMode::resolve(policy, caps) -> Option<CursorMode>`, with
  `[capture] cursor_mode` (`auto` | `separate` | `embedded`, default `auto`)
  supplying `policy` — see MODULE_CAPTURE "Cursor delivery and the
  `separate`/`embedded` decision" for the truth table. `CursorMode` has two
  inhabitants, `Separate` and `Embedded`; `None` means the add-on cannot satisfy
  the policy (ineligible at selection, a capability lie after construction).
  `CursorMode::as_str()` is what goes on the wire as `Config.cursorMode`.
- The client MUST handle both modes based on the handshake.

### Connection / Join Flow (no keyframe storm)
```
client opens control stream (tag 0x00) + auths
  → server sends {"type":"config"} JSON line on the control stream
  → server checks the cached IDR: it is FRESH iff idr.sequence == last_broadcast_seq
  → if STALE: request one through the SAME rate-limited path clients use
    (reason = "join") and wait up to [transport] join_idr_timeout (default 1s)
  → server opens a bootstrap stream (tag 0x10), writes [u32 Len][FrameHeader‖IDR]
    of the now-fresh access unit, closes it, and records bootstrap_seq
  → server opens a cursor stream (tag 0x11) and seeds it with the cached cursor
    shape + position
  → client decodes the bootstrap IDR (reliable); sets lastSeq = bootstrapSeq = its seq
  → live datagram frames flow; the client discards any datagram not newer than
    bootstrapSeq (the forced IDR is also broadcast), and gap detection runs from
    the first live datagram frame with no exception
```
- The bootstrap stream is reliable, so the join keyframe is always decodable even though live video is lossy datagrams.
- **Freshness is the invariant:** a joining or resuming client's first live delta frame MUST reference a picture derived from the IDR it received on the bootstrap stream. A stale cached IDR is followed by P-frames referencing frames the joiner never received, which on a clean LAN with `keyframe_interval = 0` never self-corrects.
- A join NEVER restarts the capturer (the old `capturer.Restart()` behavior is removed).
- Keyframe requests (from gap detection, join, or a per-session queue drop) all funnel through one 500 ms server-side coalescer, so a burst of joins costs at most 2 IDR/s in total. That limiter is what TD-26 was about; refusing to force at all was the over-correction.

### Resolution-Change Flow
```
capturer detects resolution change (monitor hotplug / mode switch)
  → NextFrame/NextSurface returns new Width/Height (pipeline detects by comparison)
  → pipeline: apply_params on the frame loop — reconfigure/rebuild encoder, call input.Resize(w,h)
  → server: send a fresh {"type":"config"} message (new dims) + force a keyframe
  → client: reconfigure VideoDecoder, update input coordinate scaling
```
The pipeline owns this orchestration; no module drives it alone.

### Configuration
- Single TOML config file at a known OS-conventional path; `--config <path>` is the only CLI **flag**, and three subcommands (`hash-password`, `revoke-device`, `list-devices`) dispatch before config load. Full schema and CLI surface in [`./core/MODULE_CONFIG.md`](./core/MODULE_CONFIG.md).
- Compile-time constants for protocol parameters (Version byte, header layout).
- Runtime capability probing for loaded add-on detection.
- Hot reload via `SIGHUP` (Linux/macOS) — most sections reload without restart; the TLS certificate **paths**, the port and `force_addon` need a restart (marked in MODULE_CONFIG); the certificate and key **files** at those paths are re-read on SIGHUP and swapped into the live listener by the `fd-config` task calling `Server::reload_tls()`, which forwards to `Transport::reload_tls` on the transport the Server owns — the same in-place swap the self-signed rotation uses. There is no argument: the paths are fixed at startup, and on error the previous key stays in place and the error is logged, so a bad certificate file never takes a listener down.

---

## Refactoring Principles

1. **Trait-First:** Every module exposes a Rust trait. Implementations are private to their crate.
2. **Zero Cyclic Deps:** Crates never cyclically depend (only the host crate depends on all; Cargo forbids cycles anyway).
3. **Testable in Isolation:** Each crate has unit tests that run without hardware.
4. **Swappable (restart-scoped in v1):** Changing a capture or encoder add-on is a config change (`[capture] force_addon`, `[encode] force_addon`) or swapping the add-on library in the add-ons directory, **then a restart** — never a code change in the pipeline. (v1 resolves the add-on set once at startup; live reload is out of scope.)
5. **Error Propagation:** All errors flow up to the orchestrator with context (`thiserror` enums + `?`; `.context(...)` via `anyhow` in the host binary only).
6. **No Global State:** No global mutable statics except the add-on registry, which the loader populates at startup from the add-ons directory.
7. **Explicit Lifecycle:** Every module type has a constructor (`new`/`builder`), optional `start()` (begin work), and `Drop` (cleanup) — RAII; no manual `Close()` needed.
8. **Buffer Contracts:** Document whether returned slices are owned or borrowed.

---

## File Structure (Post-Refactor Target)

```
featherdesk/                         # Cargo workspace
├── Cargo.toml                       # [workspace] members + shared dep versions
├── crates/                          # Library crates = the public contracts
│   ├── featherdesk-abi/             # Stable add-on ABI (abi_stable): root module + capability
│   │                                #   descriptor + #[sabi_trait] Capturer/Encoder/HardwareEncoder/
│   │                                #   AudioCapturer/Injector + AbiErr code enum. Also DECLARES every
│   │                                #   type that crosses (Os, Arch, HOST_OS, HOST_ARCH, Rotation,
│   │                                #   CursorState/CursorShape, ChannelLayout) — it may not name a type
│   │                                #   from a crate that imports it. Built by host AND add-ons.
│   ├── featherdesk-stream/          # Params, EncodedFrame, StreamError (shared leaf)
│   ├── featherdesk-protocol/        # Wire types + encode/decode (v1, 22-byte header); serde for JSON control
│   ├── featherdesk-capture/         # Capturer + Surface/Cursor/Configurable traits, CaptureHandle, Frame, FBInfo, blend_cursor;
│   │                                #   re-exports the boundary types (pub use abi::{Rotation, CursorState, CursorShape})
│   ├── featherdesk-encode/          # Encoder trait, YuvFrame, EncoderConfig, color convert (libyuv FFI)
│   ├── featherdesk-hwencode/        # HardwareEncoder trait + per-OS surface types
│   ├── featherdesk-input/           # Injector traits + Event + Dispatcher + HID tables
│   ├── featherdesk-config/          # TOML schema (serde) + load + Validate + Watch
│   ├── featherdesk-transport/       # HTTP/3 + WebTransport (quinn + wtransport)
│   ├── featherdesk-clipboard/       # CORE clipboard (cfg(target_os): windows/linux/macos modules)
│   ├── featherdesk-filetransfer/    # CORE file transfer (QUIC-native flow control, CRC32C per-chunk + SHA-256 whole-file, sandbox)
│   └── featherdesk-host/            # The BINARY crate
│       └── src/
│           ├── main.rs              # config::load() → tracing init → runtime build → Pipeline::new() → pipeline.start(); macOS: CFRunLoop on the main thread
│           ├── pipeline/            # frame loop, pacing, drop logic, probe + selection, cursor publisher, stats
│           ├── server/             # per-session tasks, broadcast fan-out, auth-on-control-stream, metrics
│           ├── addon/              # abi_stable loader (scan dir, load cdylibs, ABI check) + registry
│           └── client/             # embedded web client (rust-embed): index.html, compositor.js
└── addons/                          # Each add-on = its own cdylib crate → featherdesk-addon-<id>.{so,dylib,dll}
    ├── capture/{kms_egl, nvfbc}(Linux)  {sck}(macOS)  {dxgi_dd}(Windows)
    ├── encode/{openh264, x264}  {libva, amf_rocm}(Linux)  {nvenc}  {qsv, mf_hw, amf}(Windows)  {vt}(macOS: vt_sw|vt_hw)
    ├── audio/{pipewire}(Linux)  {wasapi}(Windows)  {sck_audio}(macOS)  {opus}(codec, all)
    └── input/{uinput}(Linux)  {interception, vigem, win_touch}(Windows)  {gcvirtual}(macOS)
          # kb/mouse default = in-core `enigo`; these add-ons are overrides/extensions
```

> The old Go `internal/logger/` is **gone** — replaced by the `tracing` crate.
> Every crate emits `tracing` events; `tracing-subscriber` is initialized once in
> `featherdesk-host`. Add-on `cdylib` crates depend only on `featherdesk-abi`
> (+ their native FFI), never on the host. Per-OS code uses `#[cfg(target_os =
> "…")]`, not Go build tags.

> **Crate-list clarifications (read with the tree above):**
> - `featherdesk-auth` (auth modes + session tokens + role gating) and
>   `featherdesk-audio` (host→client audio; **impl deferred**) are also library
>   crates — omitted from the art above only for brevity. The dependency graph
>   already shows `auth`.
> - `server` and `pipeline` are **not** separate crates. They are modules inside
>   the `featherdesk-host` binary (`src/server/`, `src/pipeline/`) — see
>   MODULE_SERVER R-SRV-07. Module specs use them as headings, not crate names.

### Crate ↔ spec map

Module specs are grouped **by concern** (`core/ media/ interaction/ client/ v2/`);
crates are organized **by compilation unit**. They are ~1:1 — this table is the
authoritative crossover (the three non-1:1 cases are called out). When code lands,
each crate gets a 3-line `README` that **links** to its spec here — never a copy.

| Crate | Authoritative spec(s) | Note |
|-------|------------------------|------|
| `featherdesk-abi` | [`./core/MODULE_ABI.md`](./core/MODULE_ABI.md) | ABI contract + registries (built into host **and** every add-on) |
| `featherdesk-stream` | [`./core/MODULE_STREAM_PARAMS.md`](./core/MODULE_STREAM_PARAMS.md) | `Params`, `EncodedFrame`, `StreamError` (shared leaf) |
| `featherdesk-protocol` | [`./core/MODULE_PROTOCOL.md`](./core/MODULE_PROTOCOL.md) | |
| `featherdesk-transport` | [`./core/MODULE_TRANSPORT.md`](./core/MODULE_TRANSPORT.md) | |
| `featherdesk-config` | [`./core/MODULE_CONFIG.md`](./core/MODULE_CONFIG.md) | |
| `featherdesk-auth` | [`./core/MODULE_AUTH.md`](./core/MODULE_AUTH.md) | |
| `featherdesk-capture` | [`./media/MODULE_CAPTURE.md`](./media/MODULE_CAPTURE.md) | `Capturer` + `SurfaceCapturer` traits |
| `featherdesk-encode` | [`./media/MODULE_ENCODE.md`](./media/MODULE_ENCODE.md) | SW `Encoder` trait + libyuv converter |
| `featherdesk-hwencode` | [`./media/MODULE_HARDWARE_ENCODE.md`](./media/MODULE_HARDWARE_ENCODE.md) | `HardwareEncoder` trait |
| `featherdesk-audio` | [`./media/MODULE_AUDIO.md`](./media/MODULE_AUDIO.md) | impl deferred |
| `featherdesk-input` | [`./interaction/MODULE_INPUT.md`](./interaction/MODULE_INPUT.md) **+** [`./interaction/MODULE_GAMEPAD.md`](./interaction/MODULE_GAMEPAD.md) | **non-1:1:** gamepad is part of the input crate, not its own crate |
| `featherdesk-clipboard` | [`./interaction/MODULE_CLIPBOARD.md`](./interaction/MODULE_CLIPBOARD.md) | core (compiled-in, not an add-on) |
| `featherdesk-filetransfer` | [`./interaction/MODULE_FILETRANSFER.md`](./interaction/MODULE_FILETRANSFER.md) | core |
| `featherdesk-network` | [`./v2/MODULE_NETWORK.md`](./v2/MODULE_NETWORK.md) | v2; mechanism not chosen |
| `featherdesk-host` (binary) | [`./core/MODULE_SERVER.md`](./core/MODULE_SERVER.md) + [`./core/MODULE_PIPELINE.md`](./core/MODULE_PIPELINE.md) + embedded [`./client/MODULE_WEB_CLIENT.md`](./client/MODULE_WEB_CLIENT.md) | **non-1:1:** `server` and `pipeline` are modules inside the host binary, not crates |
| add-on `cdylib`s | [`./addons/`](./addons/)`{os}/{kind}/*.md` | one spec per backend; each implements an `AddonKind` from MODULE_ABI |

> The native client ([`./client/MODULE_NATIVE_CLIENT.md`](./client/MODULE_NATIVE_CLIENT.md))
> is a **v2 product**, not a crate in the v1 workspace.

---

## Known Technical Debt (Current Codebase)

> Several original TDs referenced files that are **deleted entirely** by the
> architecture refactor (`x11grab.go`, `screencast.py`, `ffmpeg.go`, `vp8.go`,
> `vaapi.go`, `internal/logger/`). A "the file is going away" closure is only
> valid with a companion check that no spec in `specs/` reintroduces the
> mechanism — deleting the file that held a bug does not close the bug if the
> new design specifies the same shape somewhere else. Each obsolete row below
> names its companion check.

| ID | Severity | Location | Issue | Resolution |
|----|----------|----------|-------|------------|
| TD-01 | Low | `kms.go:103-106` + `kms.go:133-136` | DMA-BUF fd lifetime is a two-site manual discipline: `NextFrame` closes the *previous* fd at the top of the next call and `Close` closes the last one, so correctness depends on every early return leaving exactly one fd in `lastDMAFD`. No fd is leaked today — the import-failure path at `kms.go:109-111` returns with the fd already stored — but nothing enforces the invariant | Eliminated by construction rather than by review: `FbInfo` owns the handle, `encode_surface(surface: FbInfo)` takes it by value, and `FbInfo::Drop` releases it exactly once on success, error, and `StreamError::FallbackToSoftware` (Contract 6) |
| TD-02 | ~~High~~ closed by design | `x11grab.go:113` | `NextFrame` retries a dead capture subprocess by calling itself (`return c.NextFrame()`) with no depth limit — a sustained failure recurses until the stack is exhausted. Note this is the capturer `main.go:137` actually constructs; `NewKMSCapturer` has no non-test caller | Closed by the capture-error ladder, not by deletion: `MODULE_PIPELINE.md` "Error Recovery Strategy" makes retry the **caller's** loop, bounded at 3 consecutive errors → capturer restart and 10 consecutive → fatal, and `StreamError::Unrecoverable` drops the add-on and walks the capture probe order. Companion check: no spec specifies a self-call retry (`grep -rn "self\.next_frame()" specs/` → 0) |
| TD-03 | Medium | `egl.go:22-25` | Four file-scope C statics (`read_fbo_src`/`read_fbo_dst`/`read_rbo`/`read_rbo_w,h`) are shared by `ensure_blit_fbo` and `read_texture_pixels`, so only one EGL readback context can exist per process. Latent today — the Go program constructs one capturer and pins it with `runtime.LockOSThread` — and fatal the moment a second capturer or a second context is driven concurrently | Fold into `KMS_EGL_LINUX_SPEC.md` known-issues; the Rust add-on owns this state per instance (no statics), which is a precondition for the multi-monitor and re-probe paths |
| TD-04 | ~~High~~ closed by design | `ffmpeg.go:240-242` | `ForceKeyframe()` sets `e.idr.Store(true)` and nothing ever loads it — the flag is write-only, so a client keyframe request never reaches ffmpeg and the IDR is never forced | Closed by making the mechanism mandatory and named, not by deleting the file — subprocess encoders are **not** rejected (the opt-in `x264` add-on is one). Every encoder add-on must state how `force_keyframe()` reaches the encoder: in-process encoders call the library's force-IDR entry point; the `x264` add-on's only mechanism is killing and respawning the ffmpeg child, which is why it is opt-in rather than the software default (`X264_SUBPROCESS_LINUX_SPEC.md` "Codec Output", `MODULE_ENCODE.md` "Encoder Selection"). A write-only flag is not a conforming implementation. Companion check: `grep -rn "force_keyframe" specs/addons/` — every hit names a mechanism |
| TD-05 | Medium | `main.go:158` | Hardcoded 2560x1440 for input device | Resolved by MODULE_INPUT — Dispatcher.Resize follows stream dims, pipeline derives from capture |
| TD-06 | Medium | `compositor.js:22` and `:309` | Duplicate `init()` declaration — the second shadows the first, so the body at `:22` is dead and only `:309` is bound to `DOMContentLoaded` (`:318`) | R-CLI-01 |
| TD-07 | ~~Medium~~ obsolete | `internal/server/server.go:148` + `cmd/server/client/compositor.js` | Codec type mismatch (H264 wire constant carrying VP8 data) | Structurally impossible in v1: the codec is not a constant on either side — the server advertises the encoder's own `codec()` string in `{"type":"config"}` and the client configures `VideoDecoder` from that message (R-PRO-01, TD-21). VP8 is additionally rejected and wire slot 5 permanently retired |
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
| TD-22 | Low | Pipeline | No frame drop strategy (unbounded latency under load) | Pipeline: skip-accumulation bound + sustainable-rate control (see MODULE_PIPELINE) |

### Round-2 Review Findings (verified against source)

| ID | Severity | Location | Issue | Resolution |
|----|----------|----------|-------|------------|
| TD-23 | High | `server.go:149-178` | Broadcast sends ONE message PER NAL → multi-NAL H.264 yields partial access units; breaks WebCodecs. **Addendum (confirmed via `vaapi_hardware_encoding` track review, `internal/encode/ffmpeg.go`):** the same bug class shipped independently in the ffmpeg/VA-API encoder path (commit `3507455`) and was patched same-day (`99648f2`) by draining every NAL currently queued on the channel before returning — a timing heuristic, not true access-unit-boundary detection, so it could still resplit under bursty I/O. | One message per frame, concatenate NALs (Annex B). The new `Encoder::encode()`/`HardwareEncoder::encode_surface()` contract (`MODULE_ENCODE.md`/`MODULE_HARDWARE_ENCODE.md`) closes this **structurally**, not heuristically: an add-on MUST return exactly one complete `EncodedUnit` per call — there is no "whatever's in the channel right now" path available to a conforming add-on, and for a pipelined out-of-process encoder the boundary is the Access Unit Delimiter its child emits (see MODULE_ENCODE). |
| TD-24 | High | `server.go:164-168` | IDR cache stores only the IDR NAL; SPS/PPS (separate messages) lost → undecodable | Cache whole per-frame keyframe message (contains SPS+PPS+IDR) |
| TD-25 | High | Video: `internal/capture/kms.go:128` and `internal/capture/x11grab.go:120` (both `Timestamp: uint64(time.Now().UnixMilli())`; `cmd/server/main.go:279` broadcasts `frame.Timestamp` unmodified). Audio: `cmd/server/main.go:295` → `Server.BroadcastAudio(chunk, uint64(time.Now().UnixMilli()))`, **confirmed via the `pipewire_audio_capture` track review** | Video + Audio stamped with wall-clock ms — video at capture, audio at consumption; spec required monotonic-ns at capture → A/V sync impossible | Canonical CLOCK_MONOTONIC ns, stamped at capture by the capture add-on; the audio PCMChunk carries the capture timestamp (audio design locked; impl deferred) |
| TD-26 | High | `main.go:249-252` | New-client handler forces keyframe + `capturer.Restart()` (respawns capture) → storm for all viewers | Serve cached IDR; conditional keyframe; never restart capture; rate-limit |
| TD-27 | Med | `main.go:158` | Input device hardcoded 2560×1440 ≠ stream dims → cursor offset | Resolved by MODULE_INPUT — input dims = stream dims; Dispatcher.Resize on resolution change |
| TD-28 | Med | Protocol/round-1 | Length-prefix NAL framing added client AVCC complexity for no browser benefit | Reverted to Annex B per-frame concatenation |
| TD-29 | Med | Pipeline (round-1 spec) | Frame loop discarded W/H/timestamp; `continue` didn't skip capture; dead frameSeq | EncodedFrame struct; skip-before-capture; server owns sequence |
| TD-30 | Med | hwencode (round-1 spec) | Duplicate `config` field; non-existent `vaCreateSurfaceFromFD` | Renamed `vaConfig`; use `vaCreateSurfaces`+ExternalBuffers |
| TD-31 | Med | Client (round-1 spec) | Config described as JSON text vs binary frame 6; codec "h264" too short for WebCodecs | Config = `{"type":"config"}` JSON on the control stream (binary type 6 retired in the QUIC switch), full codec string |
| TD-32 | Med | Protocol/Input | InputAck had nothing to echo (no input seq) | Input messages carry `seq`; server echoes in 13-byte InputAck |
| TD-33 | Low | Protocol | KeyframeReq/Resize as binary types vs JSON client channel | Keyframe via control-stream JSON; Resize via a fresh `config` message |
| TD-34 | Low | Pipeline (round-1 spec) | `FramesCaptures` typo; unused `minInterval`; undefined Stats methods | Corrected in MODULE_PIPELINE |

### Round-3 Review Findings (verified against source, via conductor-track reviews)

| ID | Severity | Location | Issue | Resolution |
|----|----------|----------|-------|------------|
| TD-35 | Medium | `compositor.js` keydown/keyup handlers (input_injection_uinput track) | No `blur`/`visibilitychange` listener and no tracked pressed-key set; a key held down when the tab loses focus is never released, leaving it stuck down on the host via the uinput device indefinitely | Resolved on both sides, and closed only when both are wired. **Client:** R-CLI-12 tracks `heldKeys`/`heldButtons` and calls `releaseAllHeld()` on `window.blur` and `visibilitychange` while the transport is still open (`MODULE_WEB_CLIENT.md`). **Host:** `input::Dispatcher::release_all()` injects an up-event for every key/button/touch currently held, and the server invokes it whenever the controller slot is released or seized — controller takeover (`MODULE_SERVER.md` step 11), session close (step 21), and session timeout. Both halves are validated by US-INP-9 and US-MC-10; a `release_all` with zero callers does not close this row |
| TD-36 | Low | `compositor.js` `pointermove`/`wheel` handlers (input_injection_uinput track) | No throttling: every raw `pointermove` (plan specified 8ms) and `wheel` (plan specified 16ms batching) event is sent individually; a high-polling-rate mouse can flood the transport well beyond the specced rate | Resolved by MODULE_INPUT — server-side per-client rate limit (`server.input_rate_limit`, default 1000 ev/s) with `mousemove` coalescing |
| TD-37 | Low | `internal/input/keymap.go` `browserToLinux` map (input_injection_uinput track) | Only F1-F12 are mapped despite the plan explicitly requiring F1-F24; F13-F24 silently fail to inject (fall through the unmapped-code path, no crash but no input) | Resolved — MODULE_INPUT's keymap explicitly covers "letters, digits, F1-F24, modifiers..." |
| TD-38 | Medium | `internal/input/protocol.go` wheel handling, calling `InjectWheel` (input_injection_uinput track) | Browser `deltaY` magnitude is discarded entirely — only its sign survives, collapsed to a fixed `±1` `REL_WHEEL` step regardless of actual scroll speed (mouse notch vs. fast trackpad fling are indistinguishable on the host) | Resolved by MODULE_INPUT — the new Scroll message (type `0x23`) carries high-resolution signed `i16` `Dx`/`Dy` deltas end-to-end, magnitude-preserving by design |
| TD-39 | Medium | `internal/encode/ffmpeg.go` `restart()` (vaapi_hardware_encoding track) | No backoff on repeated subprocess crashes: `restart()` is a flat kill + 50 ms sleep + respawn with no attempt counter and no circuit breaker, and it is retried from `Encode()` on **every frame** for as long as the child keeps dying — so a persistently crashing encoder (e.g. driver fault) respawns once per frame period for the life of the session, burning CPU and spamming logs instead of degrading gracefully | **Fixed.** `MODULE_PIPELINE.md` "Add-On Crash Recovery" is now the normative two-level policy for every restartable add-on: **Level 1** (inside the add-on) is a bounded exponential ladder — 100/200/400/800/1600 ms ±20 % jitter, counter decaying after 60 s healthy, give up on the 6th failure; **Level 2** (inside the pipeline) never retries an add-on that reported the new `StreamError::Unrecoverable` (`AbiErr` code `7`, `MODULE_ABI.md`) — it poisons that add-on for the session, walks the startup probe order to the next candidate, forces an IDR + fresh `config` on a successful swap, and shuts down cleanly if none remain. `X264_SUBPROCESS_LINUX_SPEC.md` "Crash Recovery" binds it for the ffmpeg child (reaper task, stderr ring buffer for diagnostics, NAL-accumulator reset across the restart, drop-never-buffer during the gap, missing binary caught at `probe()`). Four integration test rows in `MODULE_PIPELINE.md` guard the ladder, the decay, the fall-through, and the no-candidate-left terminal case. |
| TD-40 | Low | `compositor.js` `PCMProcessor.process()`, historical (pipewire_audio_capture track) | The original per-chunk audio queue discarded ~83% of incoming samples per callback (128 of 960); shipped before being caught, fixed same development cycle in commit `523d108`. The regression existed in working code for some period without an automated test catching it. | **Fixed.** `MODULE_AUDIO.md`'s Testing Strategy gained two conservation rows: a server-side one (N seconds of synthetic samples through capture→normalize→frame-assembly with back-pressure disabled must yield `samples_out == samples_in`; any drop must be an explicit counted back-pressure event, never a buffer-size mismatch) and a client-side one covering the exact bug site — pushing `frame_ms`-sized chunks into the `AudioWorklet` ring buffer and pulling 128-sample render quanta, asserting nothing is lost when the chunk size is not a multiple of 128. |
