# Low-Level Design — Module / Crate Dependency Graph

Converts the ASCII "Module Dependency Graph" in `specs/CENTRAL_SPEC.md`
(~line 812) into Mermaid, with the crate/module distinction from the "File
Structure" and "Crate ↔ spec map" sections preserved: solid boxes are
standalone library crates; dashed boxes are modules living inside the
`featherdesk-host` binary (`server` and `pipeline` are **not** separate
crates).

```mermaid
graph TD
    stream["featherdesk-stream\n(Params, EncodedFrame, StreamError)\nSHARED LEAF"]

    capture["featherdesk-capture"]
    encode["featherdesk-encode"]
    hwencode["featherdesk-hwencode"]
    protocol["featherdesk-protocol"]
    transport["featherdesk-transport"]
    config["featherdesk-config"]
    auth["featherdesk-auth"]
    input["featherdesk-input"]
    clipboard["featherdesk-clipboard\n(core leaf, per-OS)"]
    filetransfer["featherdesk-filetransfer"]
    abi["featherdesk-abi\n(ABI contract, built into\nhost AND every add-on)"]

    server(("server\n[host-internal module]"))
    pipeline(("pipeline\n[host-internal module,\nSOLE ORCHESTRATOR]"))

    capture --> stream
    encode --> stream
    hwencode --> stream
    encode --> capture
    hwencode --> capture
    transport --> protocol

    server --> transport
    server --> protocol
    server --> auth
    server --> stream
    server --> input
    server --> clipboard
    server --> filetransfer
    filetransfer --> transport

    pipeline --> capture
    pipeline --> encode
    pipeline --> hwencode
    pipeline --> server
    pipeline --> config
    pipeline --> transport

    abi -.->|"stable contract used by\nadd-on loader"| capture
    abi -.->|"stable contract used by\nadd-on loader"| encode
    abi -.->|"stable contract used by\nadd-on loader"| hwencode
    abi -.->|"stable contract used by\nadd-on loader"| input

    classDef leaf fill:#2d5,stroke:#131,color:#000
    class stream leaf
```

**Invariants this graph must preserve** (from `CENTRAL_SPEC.md`):

- **No cycles** — Cargo forbids them anyway; every crate is a leaf or
  near-leaf depending only on `std` + system libs via FFI.
- **`stream` is the shared leaf** — every media/server crate depends on it for
  `Params` / `EncodedFrame` / `StreamError`; it depends on nothing else here.
- **`pipeline` is the sole orchestrator** — it is the only node that imports
  every core interface (capture, encode, hwencode, server, config, transport)
  and builds the `Transport`. No other module reaches across this many
  boundaries.
- **`hwencode` and `encode` are siblings, not parent/child** — both depend on
  `capture` + `stream` independently; an add-on implements one or the other,
  never both.
- **`protocol` is shared with the client** — it's pure data (wire types,
  encode/decode), no logic dependencies, which is why the browser client can
  use the identical frame/message definitions.
- Audio (`featherdesk-audio`) is omitted from the graph above for the same
  reason `CENTRAL_SPEC.md` omits it: implementation is deferred.
