# Network Flow — Connection Lifecycle

Sequence of a client loading the page, authenticating, upgrading to QUIC, and
joining an in-progress stream, per `specs/core/MODULE_SERVER.md` (its
HTTP-endpoints table and "Keyframe Caching Strategy") and
`specs/core/MODULE_TRANSPORT.md` ("Connection Lifecycle", "Stream
Identification", "Carrier selection"). Two things are easy to draw wrong and are
drawn explicitly here: **the first three requests are TCP**, because no browser
speaks HTTP/3 to an origin it has not already learned about via `Alt-Svc`; and
the **second-client join** never restarts capture and forces a keyframe only
when the cached one is stale, which is the fix for TD-26 (the old Go server did
both unconditionally on every join, disrupting all existing viewers).

```mermaid
sequenceDiagram
    participant C as Client (browser)
    participant H as TCP HTTPS listener
    participant T as Transport (QUIC/WebTransport)
    participant S as Server

    Note over C,S: Page load — TCP first, always
    C->>H: GET / (HTTP/1.1 or HTTP/2 over TLS 1.3)
    H-->>C: index.html + JS bundle<br/>Alt-Svc: h3=":port" (ma=86400)
    C->>H: GET /cert-hashes (self-signed mode)
    H-->>C: {"hashes":[base64 SHA-256(cert DER), ...]}
    C->>H: POST /auth (credentials)
    H-->>C: {"session_token", "ttl_sec"}

    Note over C,S: Upgrade to QUIC
    C->>T: new WebTransport(".../wt")<br/>serverCertificateHashes from /cert-hashes
    T->>T: TLS 1.3 handshake (1-RTT)
    T->>S: AcceptSession (arms 5s auth deadline)
    C->>T: open bidi stream, write tag 0x00 (control)
    T->>S: route to control handler
    C->>S: {"type":"auth","token":"...","role":"control|view|player"}
    alt token valid
        S-->>C: {"type":"auth_ok",...} — the EFFECTIVE role,<br/>gamepad slot, and a downgrade reason if it differs
        S-->>C: {"type":"config", codec, width, height, cursorMode, carrier, ...}
    else token invalid
        S--xC: close_with_error(AUTH_FAILED 4401)
    else server at max_clients
        S--xC: close_with_error(SERVER_FULL 4429)
    else no valid auth within 5s
        S--xC: close_with_error(AUTH_TIMEOUT 4408)
    end

    Note over C,S: UDP blocked — WebSocket fallback carrier
    alt WebTransport unreachable, or below its browser floor
        C->>H: WebSocket upgrade at /ws on the same port
        Note right of H: Same stream tags (0x00/0x01/0x02/0x03/0x10/0x11),<br/>same message framing. Video rides as reliable<br/>0x20 messages. PING, cursor position and<br/>GAMEPAD_RUMBLE become reliable messages too.<br/>DEGRADED: TCP head-of-line blocking under loss.
    end

    Note over C,S: Fast-Join (cache empty — nothing broadcast yet)
    S->>S: no cached IDR -> rate-limited keyframe request<br/>(pipeline forces ONE keyframe)
    S->>T: open uni stream, tag 0x10 (bootstrap)
    T-->>C: [u32 Len][FrameHeader || IDR], stream closes
    S->>T: open uni stream, tag 0x11 (cursor), seed shape + position
    S->>T: begin live datagrams (video/audio/cursor/ping)
    T-->>C: datagrams, fragment-reassembled per FrameID

    Note over C,S: Second Client Joins (TD-26 fix)
    Note right of S: A keyframe IS cached -- force one only if<br/>it is STALE (something was broadcast after it).<br/>Never restart capture. The 500 ms coalescer<br/>bounds joins to 2 IDR/s.
    S->>T: open uni stream, tag 0x10 (bootstrap)
    S->>T: open uni stream, tag 0x11 (cursor), seed shape + position
    T-->>C: [u32 Len][FrameHeader || fresh IDR], stream closes
    S->>T: live datagrams continue (shared with all clients)

    Note over C,S: Steady State
    S->>T: Ping (datagram type 2, 4-byte u32 LE nonce)
    T-->>C: Ping datagram
    C->>S: {"type":"pong","nonce":...} (control stream)
    S->>T: CursorUpdate (datagram type 11, 8-byte header + 14-byte body = 22 bytes on QUIC, 23 bytes as a `0x20` message on the WebSocket carrier, latest-wins)
    T-->>C: cursor overlay moves without a video frame
    C->>T: open bidi stream, write tag 0x01 (input)
    C->>S: [u16 RecLen] InputMessage (seq=N)
    S-->>C: [u16 RecLen] InputAck (echoes seq=N + server timestamp)

    Note over C,S: Resume (reconnect with existing session_token)
    C->>T: new WebTransport session
    C->>S: {"type":"auth","token":"...","resume":true}
    S->>S: SessionCache hit + within TTL --<br/>the entry is CONSUMED and a new token minted
    S-->>C: {"type":"auth_ok"} then {"type":"config","resumed":true}
    S->>T: fresh bootstrap stream, IDR forced if the cached one is stale
    S->>T: open uni stream, tag 0x11 (cursor), seed shape + position
    Note right of S: No last_video_seq needed --<br/>bootstrap always seeds a<br/>decodable keyframe
```

**Why joins never disturb existing viewers.** The keyframe cache (an assembled
`FrameHeader || Annex B` access unit plus its sequence, keyed under `idr_mu`)
means a join reads the cache and opens a bootstrap stream — it forces a new
keyframe only when the cached one is **stale**, meaning something has been
broadcast since it, which on an active screen is most joins. That cost is
bounded rather than avoided: every keyframe request — join, client-requested,
or queue-drop — funnels through the one 500 ms coalescer, so a burst of joins
costs at most 2 IDR/s in total, and a join never restarts the capturer or
interrupts the datagrams already flowing to other clients. Refusing to force at
all was the over-correction; forcing one per join unconditionally, and
restarting capture with it, was TD-26.

**Stream identity, not accept order.** Because QUIC doesn't guarantee streams
arrive in the order they were opened, every stream self-identifies with a
1-byte `StreamType` tag before any payload (`0x00` control, `0x01` input,
`0x02` clipboard, `0x03` file transfer, `0x10` server-opened bootstrap, `0x11`
server-opened cursor) — the server dispatches on that tag, never on which
stream was accepted first. A bad tag is a **stream-scope** error: the server
cancels that stream with `close::PROTOCOL_ERROR` and leaves the session and
every other stream running. Only the control stream can kill a session.

**The fallback carrier is the same protocol, not a second one.** When UDP is
blocked or the browser is below the WebTransport floor, the client opens a
WebSocket to `/ws` on the same port and speaks the identical stream tags and the
identical per-lane framing. Only two things differ. First, there are no
datagrams, so PING, CURSOR_UPDATE and GAMEPAD_RUMBLE arrive as `0x20`-tagged
binary messages carrying the same 8-byte DatagramHeader and body as on QUIC
(CURSOR_UPDATE = `[0x20][8][14]` = 23 bytes). They are coalesced by the
fallback send-queue policy — position is latest-wins. The `0x11` cursor lane
still carries only shape records and the single join-time Kind 0x02 position
record, on both carriers. Video is carried as one reliable message per access
unit. Second, every lane shares one TCP connection, so a lost segment
head-of-line-blocks all of them. It is a compatibility mode, not a peer of
the QUIC path.
