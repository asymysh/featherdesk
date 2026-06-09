# AutoMem Homelab Setup TODO

## Context

AutoMem was running locally via Docker (FalkorDB + Qdrant + Flask API). Docker has been
removed from this machine to save resources. The stack needs to be re-deployed on the
homelab and this machine needs to point its MCP client at the homelab instance.

The automem source lives at `/home/aseem/automem/`.

---

## 1. Homelab: Deploy the Stack

Copy `docker-compose.yml` from `/home/aseem/automem/` to the homelab and bring it up.

```bash
# On homelab
git clone https://github.com/verygoodplugins/automem.git
cd automem
cp .env.example .env   # or create .env manually (see section 2)
docker compose up -d
```

Services and ports:
- Flask API: `8001`
- FalkorDB: `6379` (Redis protocol), `3000` (graph browser UI)
- Qdrant: `6333`

---

## 2. Homelab: Configure Environment Variables

Create a `.env` file in the automem directory on the homelab with at minimum:

```env
AUTOMEM_API_TOKEN=<choose a strong token>
ADMIN_API_TOKEN=<choose a strong admin token>
FALKORDB_PASSWORD=<optional, leave blank to disable auth>
QDRANT_API_KEY=<optional, leave blank for no auth>

# Embedding provider - pick one:
EMBEDDING_PROVIDER=local        # uses fastembed, no API key needed (recommended for homelab)
# EMBEDDING_PROVIDER=voyage     # requires VOYAGE_API_KEY
# EMBEDDING_PROVIDER=openai     # requires OPENAI_API_KEY

# Only needed if not using local embeddings:
# OPENAI_API_KEY=
# VOYAGE_API_KEY=
```

`EMBEDDING_PROVIDER=local` uses fastembed (bundled) and requires no external API key.
The model cache is persisted in the `fastembed_models` Docker volume.

---

## 3. Homelab: Expose the API

Make port `8001` (Flask API) reachable from this machine. Options:

- Open port `8001` in the homelab firewall and access via LAN IP directly
- Put it behind a reverse proxy (nginx/Caddy) with a local domain, e.g. `http://automem.home`
- Use Tailscale/WireGuard if the homelab is not on the same LAN

Note the final URL, e.g. `http://192.168.x.x:8001` or `http://automem.home`.

---

## 4. This Machine: Update opencode MCP Config

Edit `/home/aseem/.config/opencode/opencode.jsonc`.

Current config (points to local npx wrapper):
```json
"automem": {
  "type": "local",
  "command": ["npx", "-y", "@verygoodplugins/mcp-automem"]
}
```

Change to point at the homelab API:
```json
"automem": {
  "type": "local",
  "command": ["npx", "-y", "@verygoodplugins/mcp-automem"],
  "env": {
    "AUTOMEM_BASE_URL": "http://<homelab-ip-or-hostname>:8001",
    "AUTOMEM_API_TOKEN": "<same token set in homelab .env>"
  }
}
```

Replace `<homelab-ip-or-hostname>` and `<same token set in homelab .env>` with real values.

---

## 5. Verify

```bash
# From this machine, confirm the API is reachable
curl http://<homelab-ip>:8001/health

# Restart opencode and confirm the automem MCP connects without errors
```

---

---

## MCP Credentials Setup

### Spotify MCPs (`spotify` and `spotify-bulk`)

1. Go to [developer.spotify.com/dashboard](https://developer.spotify.com/dashboard) and create an app.
2. Set the redirect URI to `http://127.0.0.1:8888/callback` and enable **Web API**.
3. Copy the **Client ID** (and **Client Secret** for spotify-bulk).
4. Fill in `/home/aseem/.config/opencode/opencode.jsonc`:
   - `spotify` → replace `YOUR_SPOTIFY_CLIENT_ID`
   - `spotify-bulk` → replace `YOUR_SPOTIFY_CLIENT_ID` and `YOUR_SPOTIFY_CLIENT_SECRET`
5. First-use OAuth: both MCPs open a browser for login automatically on first tool call.
6. For `spotify-bulk`, also run the one-time auth setup:
   ```bash
   cd /home/aseem/spotify-bulk-actions-mcp
   source venv/bin/activate
   python setup_auth.py
   ```

### AI Diagram Maker MCP (`ai-diagram-maker`)

1. Create an account at [aidiagrammaker.com](https://aidiagrammaker.com) and get an API key.
2. Fill in `/home/aseem/.config/opencode/opencode.jsonc`:
   - `ai-diagram-maker` → replace `YOUR_ADM_API_KEY`

---

## Optional: Persistent Backups on Homelab

The compose file mounts `./backups/falkordb` and `./backups/qdrant` for backup targets.
Set up a cron job or systemd timer on the homelab to run the backup scripts in
`/home/aseem/automem/scripts/` periodically.

---

# FeatherDesk - Encoder Roadmap

## Software Encoding (benchmarked, branches created)

Main branch: ffmpeg subprocess H.264 (current working implementation).
Benchmark ran 59 configs at 2560x1440. Results drove branch decisions:

### Branch: feature-x264go-ultrafast
- Direct x264 cgo (gen2brain/x264-go)
- Preset: ultrafast, tune: zerolatency, 1 thread
- Expected P80: ~26ms (benchmark: 25.9ms 1t)
- NAL size: ~586KB/frame
- Status: branch created, implementation pending

### Branch: feature-libav-vp8s8
- libavcodec cgo (go-astiav)
- Codec: VP8, speed 8, 1 thread
- Expected P80: ~30ms (benchmark: 29ms 1t)
- NAL size: ~54KB/frame (10x smaller than H.264)
- Client needs VP8 WebCodecs (codec string "vp8")
- Status: branch created, implementation pending

## Hardware Encoding (benchmarked)

Benchmark ran 13 configs on Intel HD 630 (Kaby Lake, i3-7100) at 2560x1440.
Only H.264 encode supported (EncSliceLP). VP8/VP9/HEVC/AV1 decode-only. QSV broken on Kaby Lake.

### Results (P80 sorted):

| Config | Library | P80 | NAL |
|--------|---------|-----|-----|
| vaapi-baseline-qp26-lp | libavcodec-cgo | 6.3ms | 476KB |
| vaapi-qp26-lp-async2 | libavcodec-cgo | 6.4ms | 487KB |
| vaapi-main-qp26-lp | libavcodec-cgo | 6.5ms | 404KB |
| vaapi-qp32-lp | libavcodec-cgo | 6.5ms | 211KB |
| vaapi-qp26-lp | libavcodec-cgo | 6.7ms | 487KB |
| vaapi-qp26-full | libavcodec-cgo | 6.9ms | 487KB |
| ffsub-vaapi-qp32-lp | ffmpeg-sub | 8.7ms | 249KB |
| ffsub-vaapi-qp26-full | ffmpeg-sub | 9.2ms | 477KB |
| ffsub-vaapi-qp26-lp | ffmpeg-sub | 10.4ms | 493KB |

### Key findings:
- libavcodec-cgo is 30-40% faster than ffmpeg subprocess (eliminates pipe overhead)
- low_power vs full mode: negligible on Kaby Lake (EncSliceLP only path)
- async_depth: no benefit (1-frame HW pipeline)
- Baseline profile is fastest; Main gives 17% smaller NALs
- QP32 halves NAL size with minimal latency cost
- Direct libva: not worth the complexity (same HW, hundreds of lines of slice params)
- VPL/QSV: broken on Kaby Lake, requires newer GPU

### Branch: feature-vaapi-libav-cgo (pending)
- Replace ffmpeg subprocess with libavcodec cgo VA-API
- Profile: High, QP: 26, low_power: true
- Expected P80: ~6.5ms (vs current ~10ms ffmpeg pipe)
