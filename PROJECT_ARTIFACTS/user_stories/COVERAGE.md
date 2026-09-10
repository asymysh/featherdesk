# Wire Coverage

Every message type, wire frame type, and add-on capability bit that exists on the
wire must be exercised by at least one acceptance criterion. This table is the
check: each row names the symbol, its wire value where it has one, and the story
that exercises it. Adding a wire type without adding a row here is an incomplete
change.

To re-run it: enumerate `pub const` names in `specs/core/MODULE_PROTOCOL.md`'s
`frame_type` module, the message `"type"` values in its "Control-stream JSON
messages" table, and the `pub const` names in `specs/core/MODULE_ABI.md`'s
`impl AddonCaps`, then confirm each appears both here and in at least one
`- Given …` line under `PROJECT_ARTIFACTS/user_stories/`. `AddonCaps::KNOWN` is a
mask over the other bits, not a capability, and is the one `pub const` in that
block with no row.

## Wire frame types (`MODULE_PROTOCOL.md`, `pub mod frame_type`)

| Symbol | Value | Exercised by |
|---|---|---|
| `VIDEO_H264` | 1 | US-VID-13 |
| `PING` | 2 | US-MC-8 |
| `AUDIO_PCM` | 4 | US-AUD-3 |
| `VIDEO_HEVC` | 7 | US-VID-13 |
| `AUDIO_OPUS` | 8 | US-AUD-2 |
| `CURSOR_UPDATE` | 11 | US-VID-12 |
| `INPUT_ACK` | 14 | US-INP-8 |
| `GAMEPAD_RUMBLE` | 15 | US-INP-11 |
| `VIDEO_AV1` | 16 | US-VID-13 |

Slots 3, 5, 6, 12 and 0x50 are reserved or retired and carry no traffic; they are
covered by the protocol-version rule, not by a story.

## Control-stream JSON messages (`MODULE_PROTOCOL.md`)

| Message | Direction | Exercised by |
|---|---|---|
| `auth` | C→S | US-CONN-1, US-CONN-3, US-CONN-7 |
| `auth_ok` | S→C | US-CONN-1, US-CONN-3, US-CONN-11, US-MC-3 |
| `auth_failed` | S→C | US-CONN-3 |
| `config` | S→C | US-CONN-1, US-CONN-7, US-VID-15 |
| `hdr_unavailable` | S→C | US-VID-7, US-VID-15, US-XP-3 |
| `codec_unavailable` | S→C | US-VID-8, US-VID-15, US-XP-6 |
| `resize_suppressed` | S→C | US-VID-6 |
| `server_shutdown` | S→C | US-RR-8 |
| `keyframe` | C→S | US-VID-13 |
| `pong` | C→S | US-MC-8 |
| `stats` | C→S | US-MC-8 |
| `decode_unsupported` | C→S | US-VID-8, US-RR-7, US-XP-6 |
| `resize` | C→S | US-VID-6 |
| `set_bitrate` | C→S | US-MC-9 |
| `set_fps` | C→S | US-MC-9 |
| `set_hdr` | C→S | US-VID-15 |

## Add-on capability bits (`MODULE_ABI.md`, `impl AddonCaps`)

| Bit | Exercised by |
|---|---|
| `SURFACE` | US-XP-13 |
| `CURSOR` | US-XP-13 |
| `CONFIGURABLE` | US-XP-13 |
| `EMBED_CURSOR` | US-VID-12, US-XP-13 |
| `EMBED_CURSOR_SURF` | US-XP-13 |
| `ENC_CONFIGURABLE` | US-XP-13 |
| `SECURE_ATTENTION` | US-XP-13 |
| `RUMBLE` | US-INP-11, US-XP-13 |
