# High-Level Design

System-level view of FeatherDesk: one host desktop captured, encoded, and streamed
to one or more browser clients over a single QUIC/WebTransport session per client —
or, where UDP is blocked, over the degraded WebSocket fallback carrier. Derived from
`specs/CENTRAL_SPEC.md` (Module Dependency Graph, Frame Types) and
`specs/core/MODULE_TRANSPORT.md` (channel model, carrier selection).

```mermaid
flowchart LR
    subgraph HOST["Host (featherdesk-host binary)"]
        CAP["Capture add-on<br/>(kms_egl / nvfbc / sck / dxgi_dd)<br/>Capturer + SurfaceCapturer + CursorCapturer"]
        SWENC["Software Encoder<br/>(openh264 / vt_sw / x264 opt-in)"]
        HWENC["Hardware Encoder<br/>(libva / nvenc / amf / qsv / vt_hw)"]
        SRV["Server<br/>(broadcast fan-out, IDR cache,<br/>auth gate, direction/role gating)"]
        PIPE["Pipeline<br/>(probe + select capturer/encoder,<br/>wires everything, owns the error ladders<br/>+ Level-2 add-on crash recovery)"]
        CLIP["Clipboard Monitor<br/>(arboard + per-OS change events)"]
    end

    TCP["TCP HTTPS listener<br/>/ &middot; /cert-hashes &middot; /auth &middot; /ws<br/>emits Alt-Svc: h3=&quot;:port&quot;; ma=86400"]
    QUIC["QUIC / WebTransport<br/>(datagrams + reliable streams)"]
    WS["WebSocket fallback carrier<br/>(same tags, same framing; video and the<br/>datagram-only types as reliable messages<br/>&mdash; degraded, TCP head-of-line blocking; only the 0x20 datagram lane is S&rarr;C-only)"]

    subgraph CLIENT["Browser Client"]
        WC["WebCodecs decode<br/>+ canvas render"]
        UI["Input / Clipboard / File-transfer UI"]
    end

    CAP -- "CPU frame (BGRA/RGBA)" --> SWENC
    CAP -- "GPU surface (DMA-BUF / IOSurface / D3D11)" --> HWENC
    CAP -- "CursorUpdate (CursorCapturer;<br/>polled before the frame-skip check)" --> SRV
    SWENC -- "EncodedUnit (Annex B)" --> SRV
    HWENC -- "EncodedUnit (Annex B, or OBU for AV1)" --> SRV
    CLIP -- "Monitor::changes() &rarr; send_clipboard<br/>(host &rarr; client)" --> SRV

    TCP -- "page load, cert hashes, session token" --> CLIENT
    SRV -- "video/audio/cursor/ping/rumble datagrams,<br/>bootstrap IDR stream (0x10), cursor stream (0x11),<br/>clipboard stream (0x02)" --> QUIC
    SRV -- "same stream tags, same message framing;<br/>datagram-only types become reliable messages" --> WS
    QUIC -- "datagrams + control/input/clipboard/<br/>file/bootstrap/cursor streams" --> WC
    WS -- "control/input/clipboard/file/bootstrap/cursor<br/>+ video, all on one TCP connection" --> WC
    UI -- "input stream (0x01), clipboard stream (0x02),<br/>file-transfer streams (0x03)" --> QUIC
    QUIC -- "InputMessage, clipboard, file chunks" --> SRV
    UI -- "input stream (0x01), clipboard stream (0x02),<br/>file-transfer streams (0x03)" --> WS
    WS -- "InputMessage, clipboard, file chunks" --> SRV
    SRV -- "set_clipboard_callback<br/>(client &rarr; host)" --> CLIP

    PIPE -. "constructs + wires" .-> CAP
    PIPE -. "constructs + wires" .-> SWENC
    PIPE -. "constructs + wires" .-> HWENC
    PIPE -. "constructs + wires" .-> SRV
    PIPE -. "constructs + drains changes()" .-> CLIP
    PIPE -. "builds" .-> TCP
    PIPE -. "builds" .-> QUIC
    PIPE -. "builds" .-> WS
```

**Cursor and clipboard are the two flows that are easy to draw wrong.** Both are
*bidirectional or off-path*, so a diagram that only follows the video pipeline
misses their producer side entirely:

- **Cursor** is produced by the **capture add-on** (`CursorCapturer`), not by the
  encoder — it deliberately bypasses encode so the pointer keeps moving while
  video is skipped, dropped, or static. It is split across two lanes: position and
  visibility are a fixed 14-byte latest-wins datagram, while the bitmap is a
  reliable record on the per-session cursor stream (tag `0x11`), because a
  half-delivered cursor image is useless. See CENTRAL_SPEC Contract 8.
- **Clipboard** is genuinely symmetric and wired two *different* ways: host→client
  is a **push** the pipeline drains from `Monitor::changes()`, client→host is a
  **callback** the server invokes. See CENTRAL_SPEC Contract 9.

**Two encode paths, one output contract.** Software encoding (CPU-resident
frames, converted to I420 via libyuv) and hardware encoding (zero-copy GPU
surfaces) are mutually exclusive per session — the Pipeline probes hardware
encoders first and falls back to software — but both produce the same
`EncodedUnit` type the Server consumes, so the Server, Transport, and Client
paths are identical regardless of which encoder ran.

**One connection carries everything — but two listeners answer.** The page itself,
the self-signed cert hashes, and the `/auth` token exchange are served over TCP
HTTPS, because no browser speaks HTTP/3 to an origin it has never seen; that
response carries `Alt-Svc: h3=":<port>"; ma=86400`, which is how the browser
learns to try QUIC at all. Once it does, video, audio, cursor, input, clipboard,
and file transfer all multiplex over a single WebTransport session per client.
Where UDP is blocked, the same streams ride a WebSocket fallback carrier — same
stream tags, same message framing, only the carrier differs, and the datagram-only
types (`PING`, cursor position, `GAMEPAD_RUMBLE`) become reliable messages. That
mode is **degraded, not equivalent**: TCP head-of-line blocking means one lost
segment stalls every stream behind it. See `NETWORK_FLOW.md` for the lifecycle and
`DATA_FLOW.md` for one frame's journey end-to-end.
