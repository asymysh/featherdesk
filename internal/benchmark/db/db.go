package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGo required
)

// DB wraps *sql.DB with helpers specific to the benchmark schema.
type DB struct {
	*sql.DB
	Path string
}

// Open opens (or creates) the SQLite database at path, applies the schema,
// and returns a ready-to-use DB. The directory is created if it doesn't exist.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("db: mkdir %s: %w", filepath.Dir(path), err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}

	// Single writer connection is fine for a benchmark tool.
	raw.SetMaxOpenConns(1)

	if err := applySchema(raw); err != nil {
		raw.Close()
		return nil, fmt.Errorf("db: apply schema: %w", err)
	}

	return &DB{DB: raw, Path: path}, nil
}

func applySchema(db *sql.DB) error {
	_, err := db.Exec(Schema)
	return err
}

// InsertSession inserts a new benchmark session row and returns the auto-
// assigned integer ID. session_id must be a unique string (UUID recommended).
func (d *DB) InsertSession(s *SessionRow) (int64, error) {
	res, err := d.Exec(`
		INSERT INTO benchmark_sessions
			(session_id, session_name, benchmark_version,
			 platform, os_name, os_version, os_arch,
			 cpu_model, cpu_cores, cpu_threads, cpu_freq_mhz,
			 ram_total_mb, ram_available_mb,
			 display_w, display_h, display_hz, display_adapter,
			 additional_args)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.SessionID, s.SessionName, s.BenchmarkVersion,
		s.Platform, s.OSName, s.OSVersion, s.OSArch,
		s.CPUModel, s.CPUCores, s.CPUThreads, s.CPUFreqMHz,
		s.RAMTotalMB, s.RAMAvailableMB,
		s.DisplayW, s.DisplayH, s.DisplayHz, s.DisplayAdapter,
		s.AdditionalArgs,
	)
	if err != nil {
		return 0, fmt.Errorf("insert session: %w", err)
	}
	return res.LastInsertId()
}

// CompleteSession sets completed_at for a session.
func (d *DB) CompleteSession(sessionID string) error {
	_, err := d.Exec(
		`UPDATE benchmark_sessions SET completed_at = CURRENT_TIMESTAMP WHERE session_id = ?`,
		sessionID,
	)
	return err
}

// InsertGPU inserts a gpu_devices row and returns its ID.
func (d *DB) InsertGPU(g *GPURow) (int64, error) {
	res, err := d.Exec(`
		INSERT INTO gpu_devices
			(session_id, device_index, device_name, device_type, vendor,
			 vram_mb, driver_version,
			 drm_card_path, drm_render_node, vaapi_vendor_str, vaapi_profiles,
			 dxgi_adapter_luid, d3d_feature_level, dxgi_dedicated_vram_mb, dxgi_shared_mem_mb,
			 enc_h264, enc_h265, enc_vp8, enc_vp9, enc_av1,
			 additional_args)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		g.SessionID, g.DeviceIndex, g.DeviceName, g.DeviceType, g.Vendor,
		g.VRAMMb, g.DriverVersion,
		g.DRMCardPath, g.DRMRenderNode, g.VAAAPIVendorStr, g.VAAPIProfiles,
		g.DXGIAdapterLUID, g.D3DFeatureLevel, g.DXGIDedicatedVRAMMb, g.DXGISharedMemMb,
		g.EncH264, g.EncH265, g.EncVP8, g.EncVP9, g.EncAV1,
		g.AdditionalArgs,
	)
	if err != nil {
		return 0, fmt.Errorf("insert gpu: %w", err)
	}
	return res.LastInsertId()
}

// InsertCaptureResult inserts a capture_results row and returns its ID.
func (d *DB) InsertCaptureResult(r *CaptureResultRow) (int64, error) {
	res, err := d.Exec(`
		INSERT INTO capture_results
			(session_id, backend, backend_variant, platform,
			 resolution_w, resolution_h, target_fps,
			 frames_attempted, warmup_frames, frames_captured, frames_dropped,
			 total_duration_ms, fps_actual,
			 latency_mean_ms, latency_p50_ms, latency_p95_ms, latency_p99_ms,
			 latency_min_ms, latency_max_ms, latency_stddev_ms,
			 cpu_usage_pct, memory_delta_mb, bytes_per_frame_avg,
			 error_count, error_last, raw_file_path, additional_args)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.SessionID, r.Backend, r.BackendVariant, r.Platform,
		r.ResolutionW, r.ResolutionH, r.TargetFPS,
		r.FramesAttempted, r.WarmupFrames, r.FramesCaptured, r.FramesDropped,
		r.TotalDurationMs, r.FPSActual,
		r.LatencyMeanMs, r.LatencyP50Ms, r.LatencyP95Ms, r.LatencyP99Ms,
		r.LatencyMinMs, r.LatencyMaxMs, r.LatencyStddevMs,
		r.CPUUsagePct, r.MemoryDeltaMb, r.BytesPerFrameAvg,
		r.ErrorCount, r.ErrorLast, r.RawFilePath, r.AdditionalArgs,
	)
	if err != nil {
		return 0, fmt.Errorf("insert capture result: %w", err)
	}
	return res.LastInsertId()
}

// InsertEncodeResult inserts an encode_results row and returns its ID.
func (d *DB) InsertEncodeResult(r *EncodeResultRow) (int64, error) {
	res, err := d.Exec(`
		INSERT INTO encode_results
			(session_id, encoder, codec, encoder_type, encoder_version,
			 resolution_w, resolution_h, target_fps,
			 qp, target_bitrate_kbps, gop_size, b_frames, threads, preset, profile, warmup_frames,
			 frames_encoded, total_duration_ms, fps_actual,
			 latency_mean_ms, latency_p50_ms, latency_p95_ms, latency_p99_ms,
			 latency_min_ms, latency_max_ms, latency_stddev_ms,
			 bytes_per_frame_avg, keyframe_bytes_avg, pframe_bytes_avg, bitrate_actual_kbps,
			 cpu_usage_pct, gpu_usage_pct, memory_delta_mb,
			 ssim, psnr_db, vmaf,
			 error_count, error_last, raw_file_path, additional_args)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.SessionID, r.Encoder, r.Codec, r.EncoderType, r.EncoderVersion,
		r.ResolutionW, r.ResolutionH, r.TargetFPS,
		r.QP, r.TargetBitrateKbps, r.GOPSize, r.BFrames, r.Threads, r.Preset, r.Profile, r.WarmupFrames,
		r.FramesEncoded, r.TotalDurationMs, r.FPSActual,
		r.LatencyMeanMs, r.LatencyP50Ms, r.LatencyP95Ms, r.LatencyP99Ms,
		r.LatencyMinMs, r.LatencyMaxMs, r.LatencyStddevMs,
		r.BytesPerFrameAvg, r.KeyframeBytesAvg, r.PFrameBytesAvg, r.BitrateActualKbps,
		r.CPUUsagePct, r.GPUUsagePct, r.MemoryDeltaMb,
		r.SSIM, r.PSNRdB, r.VMAF,
		r.ErrorCount, r.ErrorLast, r.RawFilePath, r.AdditionalArgs,
	)
	if err != nil {
		return 0, fmt.Errorf("insert encode result: %w", err)
	}
	return res.LastInsertId()
}

// InsertFrameTimings bulk-inserts raw_frame_timings rows inside a transaction.
func (d *DB) InsertFrameTimings(rows []FrameTimingRow) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO raw_frame_timings
			(result_id, result_type, frame_index,
			 wall_ts_us, duration_us, is_keyframe, output_bytes, cpu_pct, additional_args)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		if _, err := stmt.Exec(
			r.ResultID, r.ResultType, r.FrameIndex,
			r.WallTsUs, r.DurationUs, r.IsKeyframe, r.OutputBytes, r.CPUPct,
			r.AdditionalArgs,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("insert frame timing row %d: %w", r.FrameIndex, err)
		}
	}
	return tx.Commit()
}

// InsertRecommendation inserts a recommendations row.
func (d *DB) InsertRecommendation(r *RecommendationRow) (int64, error) {
	res, err := d.Exec(`
		INSERT INTO recommendations
			(session_id, tier, priority_rank,
			 encoder, codec, encoder_type, resolution, target_fps, qp,
			 score, latency_p95_ms, fps_actual, bitrate_kbps, ssim, cpu_usage_pct,
			 reason, additional_args)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.SessionID, r.Tier, r.PriorityRank,
		r.Encoder, r.Codec, r.EncoderType, r.Resolution, r.TargetFPS, r.QP,
		r.Score, r.LatencyP95Ms, r.FPSActual, r.BitrateKbps, r.SSIM, r.CPUUsagePct,
		r.Reason, r.AdditionalArgs,
	)
	if err != nil {
		return 0, fmt.Errorf("insert recommendation: %w", err)
	}
	return res.LastInsertId()
}
