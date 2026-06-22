package db

// Schema defines every table used by the FeatherDesk benchmark tool.
//
// Design rules:
//   - Every table has an `additional_args TEXT` column that stores arbitrary
//     JSON. Use it for anything that doesn't fit the fixed columns rather than
//     adding new migrations.
//   - All latency values are stored in milliseconds (REAL) at the aggregate
//     level. Per-frame raw tables use microseconds (INTEGER) for precision.
//   - Foreign keys use session_id (TEXT UUID) not row IDs so results survive
//     export/import without id collisions.
//   - Boolean fields are INTEGER (0/1/NULL). NULL means "not measured".

const Schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

-- ─────────────────────────────────────────────────────────────
-- SESSION  (one row per benchmark run / invocation)
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS benchmark_sessions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id          TEXT    NOT NULL UNIQUE,   -- UUID v4
    session_name        TEXT,                      -- human label, e.g. "laptop-wifi-test"
    benchmark_version   TEXT    NOT NULL,          -- semver of the benchmark tool
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at        DATETIME,

    -- Host OS
    platform            TEXT    NOT NULL,          -- 'linux' | 'windows' | 'macos'
    os_name             TEXT,                      -- "Windows 11 Enterprise"
    os_version          TEXT,                      -- "10.0.26200"
    os_arch             TEXT,                      -- "amd64"

    -- CPU
    cpu_model           TEXT,
    cpu_cores           INTEGER,
    cpu_threads         INTEGER,
    cpu_freq_mhz        INTEGER,

    -- Memory
    ram_total_mb        INTEGER,
    ram_available_mb    INTEGER,

    -- Display (primary)
    display_w           INTEGER,
    display_h           INTEGER,
    display_hz          INTEGER,
    display_adapter     TEXT,                      -- adapter name

    -- Spare columns for unforeseen metadata
    additional_args     TEXT                       -- JSON
);

-- ─────────────────────────────────────────────────────────────
-- GPU DEVICES  (one row per discovered GPU/adapter)
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS gpu_devices (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id          TEXT    NOT NULL REFERENCES benchmark_sessions(session_id) ON DELETE CASCADE,
    device_index        INTEGER NOT NULL,
    device_name         TEXT,
    device_type         TEXT,   -- 'discrete' | 'integrated' | 'virtual' | 'software' | 'cpu'
    vendor              TEXT,   -- 'intel' | 'nvidia' | 'amd' | 'microsoft' | 'unknown'
    vram_mb             INTEGER,
    driver_version      TEXT,

    -- Linux-specific
    drm_card_path       TEXT,   -- /dev/dri/card0
    drm_render_node     TEXT,   -- /dev/dri/renderD128
    vaapi_vendor_str    TEXT,
    vaapi_profiles      TEXT,   -- JSON array: ["H264Main","HEVCMain","VP9Profile0",...]

    -- Windows-specific
    dxgi_adapter_luid   TEXT,
    d3d_feature_level   TEXT,   -- "11.1", "12.0", etc.
    dxgi_dedicated_vram_mb INTEGER,
    dxgi_shared_mem_mb  INTEGER,

    -- Probed encode capabilities (1=yes, 0=no, NULL=not probed)
    enc_h264            INTEGER,
    enc_h265            INTEGER,
    enc_vp8             INTEGER,
    enc_vp9             INTEGER,
    enc_av1             INTEGER,

    additional_args     TEXT    -- JSON
);

-- ─────────────────────────────────────────────────────────────
-- CAPTURE RESULTS  (aggregate stats per capture-backend run)
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS capture_results (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id          TEXT    NOT NULL REFERENCES benchmark_sessions(session_id) ON DELETE CASCADE,
    run_at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Backend identification
    backend             TEXT    NOT NULL,  -- 'gdi'|'dxgi'|'wgc'|'kms'|'x11grab'|'pipewire'|'screencapturekit'
    backend_variant     TEXT,              -- e.g. "dxgi_v1" vs "dxgi_v2", "gdi_bitblt" vs "gdi_printwindow"
    platform            TEXT    NOT NULL,

    -- Capture parameters
    resolution_w        INTEGER NOT NULL,
    resolution_h        INTEGER NOT NULL,
    target_fps          INTEGER NOT NULL,
    frames_attempted    INTEGER NOT NULL,
    warmup_frames       INTEGER,           -- frames discarded before measurement

    -- Output
    frames_captured     INTEGER NOT NULL,
    frames_dropped      INTEGER NOT NULL DEFAULT 0,
    total_duration_ms   REAL    NOT NULL,
    fps_actual          REAL    NOT NULL,

    -- Frame latency (milliseconds)
    latency_mean_ms     REAL,
    latency_p50_ms      REAL,
    latency_p95_ms      REAL,
    latency_p99_ms      REAL,
    latency_min_ms      REAL,
    latency_max_ms      REAL,
    latency_stddev_ms   REAL,

    -- Resource usage (averages over the run)
    cpu_usage_pct       REAL,
    memory_delta_mb     REAL,   -- RSS change from start to end

    -- Frame sizes (raw RGBA bytes per frame)
    bytes_per_frame_avg REAL,

    -- Quality / correctness
    error_count         INTEGER NOT NULL DEFAULT 0,
    error_last          TEXT,   -- last error message if any

    -- Raw data file (per-frame CSV, see raw_frame_timings or file on disk)
    raw_file_path       TEXT,   -- absolute path to the raw CSV

    additional_args     TEXT    -- JSON: e.g. {"colorspace":"BGRA","pitch_bytes":5120,"hwnd":"0x1234"}
);

-- ─────────────────────────────────────────────────────────────
-- ENCODE RESULTS  (aggregate stats per encoder/codec/QP run)
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS encode_results (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id          TEXT    NOT NULL REFERENCES benchmark_sessions(session_id) ON DELETE CASCADE,
    run_at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Encoder identification
    encoder             TEXT    NOT NULL,  -- 'openh264'|'libx264'|'libvpx'|'libvpx-vp9'|'libx265'|'libaom-av1'|'svt-av1'|'h264_vaapi'|'h265_vaapi'|'h264_nvenc'|'h265_nvenc'|'h264_amf'|'h264_qsv'
    codec               TEXT    NOT NULL,  -- 'h264'|'h265'|'vp8'|'vp9'|'av1'
    encoder_type        TEXT    NOT NULL,  -- 'software'|'vaapi'|'nvenc'|'amf'|'qsv'|'videotoolbox'
    encoder_version     TEXT,

    -- Encode parameters
    resolution_w        INTEGER NOT NULL,
    resolution_h        INTEGER NOT NULL,
    target_fps          INTEGER NOT NULL,
    qp                  INTEGER,           -- quantization param (null if bitrate mode)
    target_bitrate_kbps INTEGER,           -- null if QP mode
    gop_size            INTEGER,
    b_frames            INTEGER,
    threads             INTEGER,
    preset              TEXT,              -- 'ultrafast'|'fast'|'medium'|'slow' etc.
    profile             TEXT,              -- 'baseline'|'main'|'high' etc.
    warmup_frames       INTEGER,

    -- Output
    frames_encoded      INTEGER NOT NULL,
    total_duration_ms   REAL    NOT NULL,
    fps_actual          REAL    NOT NULL,

    -- Frame encode latency (milliseconds)
    latency_mean_ms     REAL,
    latency_p50_ms      REAL,
    latency_p95_ms      REAL,
    latency_p99_ms      REAL,
    latency_min_ms      REAL,
    latency_max_ms      REAL,
    latency_stddev_ms   REAL,

    -- Bitrate / output size
    bytes_per_frame_avg REAL,
    keyframe_bytes_avg  REAL,
    pframe_bytes_avg    REAL,
    bitrate_actual_kbps REAL,

    -- Resource usage
    cpu_usage_pct       REAL,
    gpu_usage_pct       REAL,
    memory_delta_mb     REAL,

    -- Quality (null = not measured; measuring adds ~2x runtime)
    ssim                REAL,
    psnr_db             REAL,
    vmaf                REAL,

    -- Status
    error_count         INTEGER NOT NULL DEFAULT 0,
    error_last          TEXT,

    raw_file_path       TEXT,

    -- Spare: encoder init flags, ffmpeg command, CGo params, etc.
    additional_args     TEXT    -- JSON
);

-- ─────────────────────────────────────────────────────────────
-- RAW FRAME TIMINGS  (one row per frame, for distributions)
-- ─────────────────────────────────────────────────────────────
-- NOTE: this table grows large. One capture run at 60fps × 300 frames = 300 rows.
-- A full session with 20 encoder runs × 300 frames = 6,000 rows. Manageable.
CREATE TABLE IF NOT EXISTS raw_frame_timings (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    result_id       INTEGER NOT NULL,       -- FK to capture_results.id or encode_results.id
    result_type     TEXT    NOT NULL,       -- 'capture' | 'encode'
    frame_index     INTEGER NOT NULL,

    -- Timing (microseconds for sub-millisecond precision)
    wall_ts_us      INTEGER,                -- wall clock at frame start (unix micros)
    duration_us     INTEGER NOT NULL,       -- how long this frame took

    -- Frame properties
    is_keyframe     INTEGER,                -- 1=yes 0=no NULL=unknown
    output_bytes    INTEGER,                -- compressed size (encode) or raw size (capture)

    -- Instantaneous resource usage at this frame (sampled, not averaged)
    cpu_pct         REAL,

    additional_args TEXT                    -- JSON: {"nal_count":3,"pts":12345,"dts":12340}
);

-- ─────────────────────────────────────────────────────────────
-- RECOMMENDATIONS  (scoring output)
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS recommendations (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT    NOT NULL REFERENCES benchmark_sessions(session_id) ON DELETE CASCADE,
    generated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    tier            TEXT    NOT NULL,   -- 'streaming'|'quality'|'cpu_light'|'balanced'
    priority_rank   INTEGER NOT NULL,   -- 1 = best for this tier

    encoder         TEXT    NOT NULL,
    codec           TEXT    NOT NULL,
    encoder_type    TEXT    NOT NULL,
    resolution      TEXT,               -- "1920x1080" etc.
    target_fps      INTEGER,
    qp              INTEGER,

    -- Key metrics that drove the decision
    score           REAL    NOT NULL,
    latency_p95_ms  REAL,
    fps_actual      REAL,
    bitrate_kbps    REAL,
    ssim            REAL,
    cpu_usage_pct   REAL,

    reason          TEXT,               -- human-readable explanation
    additional_args TEXT                -- JSON: full scoring breakdown, weights used
);

-- ─────────────────────────────────────────────────────────────
-- INDEXES
-- ─────────────────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_capture_session  ON capture_results(session_id);
CREATE INDEX IF NOT EXISTS idx_encode_session   ON encode_results(session_id);
CREATE INDEX IF NOT EXISTS idx_frame_result     ON raw_frame_timings(result_id, result_type);
CREATE INDEX IF NOT EXISTS idx_reco_session     ON recommendations(session_id);
CREATE INDEX IF NOT EXISTS idx_gpu_session      ON gpu_devices(session_id);
`
