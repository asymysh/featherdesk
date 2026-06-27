# Module Spec: Network (v2 Connectivity)

> # ⏸️ v2 — WRITTEN WHEN THE NATIVE CLIENT WORK STARTS
>
> This document captures the **requirements, interface contract, and candidate
> mechanisms** for the native client's connectivity layer. The **mechanism is not
> chosen** — it will be evaluated and locked when v2 implementation begins.
> Nothing in v1 depends on this module.

---

## What the native client needs (requirements)

| Requirement | Why |
|---|---|
| **NAT traversal (hole-punch)** | Both the host and the native client may be behind a home router or even CGNAT. A direct UDP path must be established without manual port-forwarding whenever possible. |
| **Relay fallback** | When hole-punching fails (double symmetric NAT / corporate firewall / carrier-grade NAT both sides), traffic must still flow — via a relay server the operator runs. Latency rises but connectivity is guaranteed. |
| **Coordination / signaling** | The two peers need to discover each other's endpoints and exchange ICE-style candidates (or equivalent). This requires a lightweight coordination service (self-hostable). |
| **Encryption** | Already satisfied — QUIC mandates TLS 1.3. The overlay (if any) adds an additional layer but the application protocol is already encrypted end-to-end. |
| **One-step connection UX** | The user pastes ONE thing (a sharing key / invite code / QR) and is connected. No manual IP entry, no port numbers, no VPN client setup. This is the "auth-key paste flow." |
| **Self-hostable (no vendor dependency)** | The operator must be able to run every component (coordination, relay, the client) without depending on a third-party service. Commercial hosted options are acceptable as a convenience, not a requirement. |
| **Zero-config for the viewer** | The viewer (native client) should need nothing beyond the sharing key. No pre-installed daemons, no tailnet membership setup (unless the mechanism requires it). |

---

## Interface contract (listener-provider pattern)

The server doesn't know or care *how* it got a connection — it asks for a
listener and gets one:

```go
package network

// Listener abstracts the network surface. The default is a plain UDP listener
// (net.ListenPacket); a connectivity add-on provides an overlay-backed one.
type Listener interface {
    // Accept blocks until a new peer connects. Returns a net.PacketConn (or
    // equivalent) that the transport module wraps as a QUIC connection.
    Accept(ctx context.Context) (net.PacketConn, net.Addr, error)
    Addr() net.Addr
    Close() error
}

// Provider is the factory the pipeline calls at startup. Build-tagged add-ons
// register themselves; the pipeline picks the compiled-in one (or plain UDP).
type Provider interface {
    // Listen returns a Listener. For plain UDP this is trivial; for an overlay
    // this may involve joining a tailnet / registering with a signaling server.
    Listen(cfg Config) (Listener, error)
}

// Config carries [network] TOML fields + the sharing-key material.
type Config struct {
    // ... TBD per mechanism
}
```

This keeps connectivity **pluggable and build-tagged** — same zero-by-default
pattern as capture/encode/input/audio:
- **Default binary** (`network` tag absent): plain UDP listener. User provides
  reachability (LAN, port-forward, Cloudflare Tunnel). This is v1's model.
- **`tailscale` add-on**: tsnet-backed listener.
- **`pion` add-on**: ICE hole-punch + TURN relay.
- (Others possible: Nebula, libp2p, custom.)

---

## Candidate mechanisms (evaluated, not chosen)

### Option A: tsnet + Headscale (leading candidate)

| Aspect | Detail |
|---|---|
| What it is | Embed Tailscale's `tsnet` library (BSD-3); the host becomes a tailnet node; the native client embeds `tsnet` and joins too |
| Coordination | Headscale (BSD-3, self-hostable) or Tailscale's hosted control plane |
| Relay | DERP (self-hostable relay servers) |
| NAT success rate | ~94% direct (aggressive probing beyond standard ICE) |
| CGNAT-both-sides | Often punches through; DERP relay fallback |
| UX | Auth-key paste: host generates pre-auth key + session token + address, bundled; client decodes, joins tailnet, connects in one paste |
| Self-hostable | ✅ fully (Headscale + own DERP) |
| Binary size | ~15 MB added |
| Trade-off | Heavier dep; the client effectively "joins a VPN" (may spook enterprise users); Headscale is community-maintained |

### Option B: pion (ICE/STUN/TURN) + quic-go

| Aspect | Detail |
|---|---|
| What it is | Use pion's ICE agent for NAT traversal; once a direct UDP path is punched, run QUIC on it |
| Coordination | A minimal custom signaling server (WebSocket or HTTP) you operate — exchanges SDP/candidates |
| Relay | TURN server (self-operated; pion includes a Go TURN server) |
| NAT success rate | ~70–80% direct (standard ICE) |
| CGNAT-both-sides | ❌ direct fails → falls back to TURN relay (adds latency, costs relay bandwidth) |
| UX | Invite code: host registers with signaling server → generates a code → client pastes → signaling exchanges candidates → connection |
| Self-hostable | ✅ (signaling + TURN are small Go binaries) |
| Binary size | ~5 MB added |
| Trade-off | Lower direct-connection rate through hard NAT; you operate signaling + TURN; more code to maintain |

### Option C: plain QUIC hole-punch (DIY minimal)

| Aspect | Detail |
|---|---|
| What it is | A bare rendezvous server + simultaneous-open UDP hole-punching directly with quic-go |
| Coordination | Custom rendezvous (tiny HTTP/WS server) |
| Relay | None built-in (you'd add a TURN-like relay separately if needed) |
| NAT success rate | Variable — works for EIM/EIF NAT, fails for symmetric |
| Self-hostable | ✅ |
| Binary size | minimal (~1 MB) |
| Trade-off | Least robust; no relay = no connectivity guarantee through hard NAT |

### Option D: Nebula (overlay mesh)

| Aspect | Detail |
|---|---|
| What it is | Slack's overlay mesh; lighthouse-based coordination; UDP hole-punch |
| Coordination | Lighthouse (self-hosted) |
| Relay | Built-in relay mode (lighthouse or designated node) |
| NAT success rate | Good (~85–90%); lighthouse-assisted |
| Self-hostable | ✅ (MIT) |
| Binary size | ~8 MB |
| Trade-off | Designed as a "run a daemon" tool more than "embed a lib"; would need deeper integration work |

---

## Decision criteria (for when we choose)

1. **Direct-connection rate** through real-world NATs (especially CGNAT, common in mobile ISPs and parts of Asia/Europe).
2. **Self-hostability** without commercial dependency.
3. **Binary size / dep weight** (users compile in what they need).
4. **UX simplicity** — how close to "one paste" can we get?
5. **Maintenance cost** — how much coordination/relay infra does the operator run?
6. **Enterprise acceptability** — does the mechanism trigger security concerns (e.g. "joining a VPN")?

---

## What v1 does (no change needed)

v1's browser client uses **plain WebTransport** — the user provides reachability
(LAN, port-forward, Cloudflare Tunnel, Tailscale Funnel). That stays. The
`network` module is purely a v2/native-client concern. The server's plain UDP
listener is the default even in v2 when no network add-on is compiled in.

---

## What depends on this (blockers for v2)

- **Auth-key paste flow** (the one-paste UX): needs the mechanism to know what
  material goes into the key (pre-auth token? signaling server URL? peer ID?).
- **MODULE_NATIVE_CLIENT.md** connectivity section: currently says "under
  evaluation" — resolves when this module is finalized.
- **README roadmap step 2**: currently says "under evaluation."

---

## Status

📋 **Requirements + interface + candidates documented.** Mechanism NOT chosen.
Finalized when v2 native-client implementation starts. Nothing in v1 blocks on
this.
