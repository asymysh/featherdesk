# Network Flow — Connection Lifecycle

Sequence of a client connecting, authenticating, and joining an in-progress
stream, per `specs/core/MODULE_TRANSPORT.md` ("Connection Lifecycle",
"Stream Identification") and `specs/core/MODULE_SERVER.md` ("Keyframe Caching
Strategy"). Includes the second-client-join case, which is the fix for TD-26
(the old Go server forced a keyframe *and* restarted the capture subprocess on
every join, disrupting all existing viewers).

```mermaid
sequenceDiagram
    participant C as Client (browser)
    participant T as Transport (QUIC/WebTransport)
    participant S as Server

    Note over C,S: Connect + Auth
    C->>T: POST /auth (HTTPS, credentials)
    T-->>C: {"session_token", "ttl_sec"}
    C->>T: new WebTransport(".../wt")
    T->>T: TLS 1.3 handshake (1-RTT)
    T->>S: AcceptSession (arms 5s auth deadline)
    C->>T: open bidi stream, write tag 0x00 (control)
    T->>S: route to control handler
    C->>S: {"type":"auth","token":"...","role":"control|view|player"}
    alt token valid
        S-->>C: {"type":"auth_ok","session":{...}}
        S-->>C: {"type":"config", codec, width, height, ...}
    else token invalid
        S--xC: close_with_error(AUTH_FAILED)
    else no valid auth within 5s
        S--xC: close_with_error(AUTH_TIMEOUT)
    end

    Note over C,S: Fast-Join (first client -- no keyframe cached yet)
    S->>S: no cached IDR -> invoke new-client callback\n(pipeline forces ONE keyframe)
    S->>T: open uni stream, tag 0x10 (bootstrap)
    T-->>C: [FrameHeader || IDR], stream closes
    S->>T: begin live datagrams (video/audio/cursor/ping)
    T-->>C: datagrams, fragment-reassembled per FrameID

    Note over C,S: Second Client Joins (TD-26 fix)
    Note right of S: A keyframe IS already cached --\ndo NOT force a new one,\ndo NOT restart capture.\nExisting viewers are undisturbed.
    S->>T: open uni stream, tag 0x10 (bootstrap)
    T-->>C: [FrameHeader || cached IDR], stream closes
    S->>T: live datagrams continue (shared with all clients)

    Note over C,S: Steady State
    S->>T: Ping (datagram, 8-byte nonce)
    T-->>C: Ping datagram
    C->>S: {"type":"pong","nonce":...} (control stream)

    C->>T: open bidi stream, write tag 0x01 (input)
    C->>S: [u16 RecLen] InputMessage (seq=N)
    S-->>C: [u16 RecLen] InputAck (echoes seq=N + server timestamp)

    Note over C,S: Resume (reconnect with existing session_token)
    C->>T: new WebTransport session
    C->>S: {"type":"auth","token":"...","resume":true}
    S->>S: SessionCache hit + within TTL
    S-->>C: {"type":"auth_ok"} then {"type":"config","resumed":true}
    S->>T: fresh bootstrap stream with current cached IDR
    Note right of S: No last_video_seq needed --\nbootstrap always seeds a\ndecodable keyframe
```

**Why joins never disturb existing viewers.** The keyframe cache (an assembled
`FrameHeader || Annex B` access unit, keyed under `idr_mu`) means a join only
needs to *read* the cache and open a bootstrap stream — it forces a new
keyframe **only** when no client has ever connected. Every subsequent join,
including the second, tenth, or after a client resumes mid-session, is served
from cache with zero impact on the live capture/encode loop or the datagrams
already flowing to other clients.

**Stream identity, not accept order.** Because QUIC doesn't guarantee streams
arrive in the order they were opened, every stream self-identifies with a
1-byte `StreamType` tag before any payload (`0x00` control, `0x01` input,
`0x02` clipboard, `0x03` file transfer, `0x10` server-opened bootstrap) — the
server dispatches on that tag, never on which stream was accepted first.
