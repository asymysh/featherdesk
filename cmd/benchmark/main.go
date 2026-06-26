//go:build windows

// benchmark is a standalone FeatherDesk tool that probes the host machine,
// runs screen-capture and video-encode benchmarks, and stores results in a
// local SQLite database alongside raw per-frame CSV files.
//
// Usage:
//
//	benchmark [flags]
//
// Flags:
//
//	-db      path to SQLite database (default: featherdesk_bench.db)
//	-out     directory for raw CSV and JSON output (default: ./bench_out)
//	-name    human-readable session label
//	-frames  frames per benchmark run (default: 300)
//	-warmup  warmup frames discarded before measurement (default: 30)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/uuid"

	benchdb "github.com/aseem/viewport-rds/internal/benchmark/db"
	"github.com/aseem/viewport-rds/internal/benchmark/capture"
	"github.com/aseem/viewport-rds/internal/benchmark/encode"
	"github.com/aseem/viewport-rds/internal/benchmark/probe"
	"github.com/aseem/viewport-rds/internal/benchmark/report"
)

const benchmarkVersion = "0.1.0"

func main() {
	dbPath  := flag.String("db",     "featherdesk_bench.db",   "SQLite database path")
	outDir  := flag.String("out",    "bench_out",              "output directory for raw CSV + JSON")
	name    := flag.String("name",   "",                       "human-readable session name")
	frames  := flag.Int("frames",    300,                      "frames per benchmark run")
	warmup  := flag.Int("warmup",    30,                       "warmup frames (discarded)")
	flag.Parse()

	if *name == "" {
		host, _ := os.Hostname()
		*name = fmt.Sprintf("%s-%s", host, time.Now().Format("2006-01-02"))
	}

	log.SetFlags(0)
	log.Printf("FeatherDesk Benchmark %s", benchmarkVersion)
	log.Printf("Platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	log.Printf("Database: %s", *dbPath)
	log.Printf("Output:   %s", *outDir)
	log.Println()

	// ── Open database ───────────────────────────────────────────────────
	db, err := benchdb.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	log.Printf("✓ Database opened: %s", db.Path)

	// ── Probe system ────────────────────────────────────────────────────
	log.Println("Probing system hardware...")
	sys, err := probe.Gather()
	if err != nil {
		log.Fatalf("probe: %v", err)
	}

	ffmpegEncoders := probe.ProbeFFmpegEncoders()
	log.Printf("  CPU:     %s (%d cores, %d threads)", sys.CPUModel, sys.CPUCores, sys.CPUThreads)
	log.Printf("  RAM:     %d MB total, %d MB available", sys.RAMTotalMB, sys.RAMAvailableMB)
	log.Printf("  Display: %dx%d @ %dHz — %s", sys.DisplayW, sys.DisplayH, sys.DisplayHz, sys.DisplayAdapter)
	for i, g := range sys.GPUs {
		log.Printf("  GPU %d:   %s [%s] driver %s", i, g.DeviceName, g.Vendor, g.DriverVersion)
	}
	if len(ffmpegEncoders) > 0 {
		log.Printf("  ffmpeg:  %d video encoders available", len(ffmpegEncoders))
	} else {
		log.Println("  ffmpeg:  not found in PATH (encoding benchmarks limited)")
	}

	// ── Create session ──────────────────────────────────────────────────
	sessionID := uuid.New().String()
	extraJSON := "{}"
	if b, err := json.Marshal(map[string]interface{}{
		"ffmpeg_encoders": ffmpegEncoders,
	}); err == nil {
		extraJSON = string(b)
	}

	sessionRow := &benchdb.SessionRow{
		SessionID:        sessionID,
		SessionName:      *name,
		BenchmarkVersion: benchmarkVersion,
		Platform:         "windows",
		OSName:           sys.OSName,
		OSVersion:        sys.OSVersion,
		OSArch:           sys.OSArch,
		CPUModel:         sys.CPUModel,
		CPUCores:         sys.CPUCores,
		CPUThreads:       sys.CPUThreads,
		CPUFreqMHz:       sys.CPUFreqMHz,
		RAMTotalMB:       sys.RAMTotalMB,
		RAMAvailableMB:   sys.RAMAvailableMB,
		DisplayW:         sys.DisplayW,
		DisplayH:         sys.DisplayH,
		DisplayHz:        sys.DisplayHz,
		DisplayAdapter:   sys.DisplayAdapter,
		AdditionalArgs:   extraJSON,
	}
	_, err = db.InsertSession(sessionRow)
	if err != nil {
		log.Fatalf("insert session: %v", err)
	}
	log.Printf("✓ Session created: %s", sessionID)

	// Insert GPU rows.
	for _, g := range sys.GPUs {
		probe.EncodeCapabilities(&g, ffmpegEncoders)
		gpuRow := &benchdb.GPURow{
			SessionID:     sessionID,
			DeviceIndex:   g.DeviceIndex,
			DeviceName:    g.DeviceName,
			DeviceType:    g.DeviceType,
			Vendor:        g.Vendor,
			VRAMMb:        g.VRAMMb,
			DriverVersion: g.DriverVersion,
			EncH264:       g.EncH264,
			EncH265:       g.EncH265,
			EncVP8:        g.EncVP8,
			EncVP9:        g.EncVP9,
			EncAV1:        g.EncAV1,
			AdditionalArgs: g.AdditionalArgs,
		}
		if _, err := db.InsertGPU(gpuRow); err != nil {
			log.Printf("  warn: insert gpu %d: %v", g.DeviceIndex, err)
		}
	}

	// Paths.
	rawDir := filepath.Join(*outDir, sessionID)
	os.MkdirAll(rawDir, 0o755)

	// ── GDI Capture Benchmark ───────────────────────────────────────────
	log.Println()
	log.Println("── GDI Capture Benchmark ──────────────────────────────────")
	memBefore := capture.MemoryUsageMB()

	gdiResults, err := capture.RunGDI(capture.GDIConfig{
		Frames:       *frames,
		WarmupFrames: *warmup,
		ReadPixels:   true,
		RawDir:       rawDir,
	}, rawDir)
	if err != nil {
		log.Printf("  GDI error: %v", err)
	}
	memAfter := capture.MemoryUsageMB()

	for _, r := range gdiResults {
		log.Printf("  [%s/%s] fps=%.1f  p50=%.2fms  p95=%.2fms  p99=%.2fms  frames=%d  errors=%d",
			r.Backend, r.BackendVariant,
			r.FPSActual, r.LatencyP50Ms, r.LatencyP95Ms, r.LatencyP99Ms,
			r.FramesCaptured, r.ErrorCount)

		row := &benchdb.CaptureResultRow{
			SessionID:        sessionID,
			Backend:          r.Backend,
			BackendVariant:   r.BackendVariant,
			Platform:         r.Platform,
			ResolutionW:      r.ResolutionW,
			ResolutionH:      r.ResolutionH,
			TargetFPS:        r.TargetFPS,
			FramesAttempted:  r.FramesAttempted,
			WarmupFrames:     r.WarmupFrames,
			FramesCaptured:   r.FramesCaptured,
			FramesDropped:    r.FramesDropped,
			TotalDurationMs:  r.TotalDurationMs,
			FPSActual:        r.FPSActual,
			LatencyMeanMs:    r.LatencyMeanMs,
			LatencyP50Ms:     r.LatencyP50Ms,
			LatencyP95Ms:     r.LatencyP95Ms,
			LatencyP99Ms:     r.LatencyP99Ms,
			LatencyMinMs:     r.LatencyMinMs,
			LatencyMaxMs:     r.LatencyMaxMs,
			LatencyStddevMs:  r.LatencyStddevMs,
			MemoryDeltaMb:    memAfter - memBefore,
			BytesPerFrameAvg: r.BytesPerFrame,
			ErrorCount:       r.ErrorCount,
			RawFilePath:      r.RawFilePath,
			AdditionalArgs:   r.AdditionalArgs,
		}
		resultID, err := db.InsertCaptureResult(row)
		if err != nil {
			log.Printf("  warn: insert capture result: %v", err)
			continue
		}

		// Bulk-insert frame timings.
		timingRows := make([]benchdb.FrameTimingRow, len(r.FrameTimingsUs))
		base := time.Now().UnixMicro()
		for i, t := range r.FrameTimingsUs {
			timingRows[i] = benchdb.FrameTimingRow{
				ResultID:    resultID,
				ResultType:  "capture",
				FrameIndex:  i,
				WallTsUs:    base + int64(i)*t,
				DurationUs:  t,
				OutputBytes: int64Ptr(int64(r.BytesPerFrame)),
			}
		}
		if err := db.InsertFrameTimings(timingRows); err != nil {
			log.Printf("  warn: insert frame timings: %v", err)
		}
	}

	// ── DXGI Capture Benchmark ──────────────────────────────────────────
	log.Println()
	log.Println("── DXGI Desktop Duplication Benchmark ─────────────────────")
	dxgiResults, _ := capture.RunDXGI(capture.DefaultDXGIConfig())
	for _, r := range dxgiResults {
		if !r.Available {
			log.Printf("  [%s/%s] UNAVAILABLE — %s", r.Backend, r.BackendVariant, r.UnavailableReason)
			// Still record in DB with 0 frames so we know it was attempted.
			row := &benchdb.CaptureResultRow{
				SessionID:      sessionID,
				Backend:        r.Backend,
				BackendVariant: r.BackendVariant,
				Platform:       "windows",
				ResolutionW:    0,
				ResolutionH:    0,
				FramesAttempted: 0,
				FramesCaptured: 0,
				ErrorCount:     1,
				ErrorLast:      r.UnavailableReason,
				AdditionalArgs: r.AdditionalArgs,
			}
			db.InsertCaptureResult(row)
		}
	}

	// ── Build summary struct (used by both encoder benchmarks and reports) ──
	gpuNames := make([]string, len(sys.GPUs))
	for i, g := range sys.GPUs {
		gpuNames[i] = g.DeviceName
	}
	sum := &report.Summary{
		SessionID:   sessionID,
		SessionName: *name,
		GeneratedAt: time.Now().UTC(),
		Platform:    "windows",
		Machine: report.Machine{
			OS:         sys.OSName + " " + sys.OSVersion,
			CPU:        sys.CPUModel,
			Cores:      sys.CPUCores,
			Threads:    sys.CPUThreads,
			RAMtotalMB: sys.RAMTotalMB,
			Display:    fmt.Sprintf("%dx%d@%dHz %s", sys.DisplayW, sys.DisplayH, sys.DisplayHz, sys.DisplayAdapter),
			GPUs:       gpuNames,
		},
	}
	// Add capture entries to summary
	for _, r := range gdiResults {
		sum.Captures = append(sum.Captures, report.CaptureEntry{
			Backend:       r.Backend,
			Variant:       r.BackendVariant,
			ResolutionW:   r.ResolutionW,
			ResolutionH:   r.ResolutionH,
			FPSActual:     r.FPSActual,
			LatencyP50Ms:  r.LatencyP50Ms,
			LatencyP95Ms:  r.LatencyP95Ms,
			LatencyP99Ms:  r.LatencyP99Ms,
			BytesPerFrame: r.BytesPerFrame,
			ErrorCount:    r.ErrorCount,
			Available:     r.ErrorCount < r.FramesCaptured || r.FramesCaptured > 0,
		})
	}
	for _, r := range dxgiResults {
		sum.Captures = append(sum.Captures, report.CaptureEntry{
			Backend:       r.Backend,
			Variant:       r.BackendVariant,
			Available:     r.Available,
			Notes:         r.UnavailableReason,
		})
	}

	// ── Encoder Benchmarks ─────────────────────────────────────────────
	log.Println()
	log.Println("── Encoder Benchmarks ─────────────────────────────────────")

	// OpenH264 SW benchmarks (per-frame timing)
	// Per-frame SW encoder benchmarks
	perFrameEncoders := []struct {
		enc           encode.EncoderBench
		width, height int
	}{
		// OpenH264 thread scaling
		{encode.NewOpenH264EncoderThreads(1), 1920, 1080},
		{encode.NewOpenH264EncoderThreads(2), 1920, 1080},
		{encode.NewOpenH264EncoderThreads(4), 1920, 1080},
		{encode.NewOpenH264EncoderThreads(8), 1920, 1080},
		{encode.NewOpenH264EncoderThreads(12), 1920, 1080},
		// OpenH264 1440p key configs
		{encode.NewOpenH264EncoderThreads(4), 2560, 1440},
		{encode.NewOpenH264EncoderThreads(12), 2560, 1440},
	}

	for _, cfg := range perFrameEncoders {
		enc := cfg.enc
		log.Printf("  [%s] %dx%d ...", enc.Name(), cfg.width, cfg.height)
		result, timings := encode.RunBenchmark(enc, cfg.width, cfg.height, 60, *warmup, *frames)

		if result.Error != "" {
			log.Printf("    ERROR: %s", result.Error)
		} else {
			log.Printf("    P50=%.1fms  P80=%.1fms  P95=%.1fms  P99=%.1fms  FPS=%.1f  NAL=%.0fKB",
				result.LatencyP50Ms, result.LatencyP80Ms, result.LatencyP95Ms, result.LatencyP99Ms,
				result.FPSActual, result.BytesPerFrameAvg/1024)
		}

		// Insert into DB
		encRow := &benchdb.EncodeResultRow{
			SessionID:        sessionID,
			Encoder:          result.Encoder,
			Codec:            result.Codec,
			EncoderType:      result.EncoderType,
			ResolutionW:      result.Width,
			ResolutionH:      result.Height,
			TargetFPS:        60,
			QP:               intPtr(26),
			Threads:          1,
			WarmupFrames:     *warmup,
			FramesEncoded:    result.FramesEncoded,
			TotalDurationMs:  result.TotalMs,
			FPSActual:        result.FPSActual,
			LatencyMeanMs:    result.LatencyMeanMs,
			LatencyP50Ms:     result.LatencyP50Ms,
			LatencyP95Ms:     result.LatencyP95Ms,
			LatencyP99Ms:     result.LatencyP99Ms,
			LatencyMinMs:     result.LatencyMinMs,
			LatencyMaxMs:     result.LatencyMaxMs,
			BytesPerFrameAvg: result.BytesPerFrameAvg,
			BitrateActualKbps: result.BitrateKbps,
			ErrorCount:       0,
			AdditionalArgs:   "{}",
		}
		if result.Error != "" {
			encRow.ErrorCount = 1
			encRow.ErrorLast = result.Error
		}

		resultID, err := db.InsertEncodeResult(encRow)
		if err != nil {
			log.Printf("    warn: insert encode result: %v", err)
		}

		// Insert per-frame timings
		if resultID > 0 && len(timings) > 0 {
			rows := make([]benchdb.FrameTimingRow, len(timings))
			for i, t := range timings {
				rows[i] = benchdb.FrameTimingRow{
					ResultID:    resultID,
					ResultType:  "encode",
					FrameIndex:  t.FrameIndex,
					DurationUs:  t.DurationUs,
					OutputBytes: int64Ptr(int64(t.OutputBytes)),
				}
			}
			db.InsertFrameTimings(rows)
		}

		// Add to summary
		sum.Encoders = append(sum.Encoders, report.EncodeEntry{
			Encoder:      result.Encoder,
			Codec:        result.Codec,
			Type:         result.EncoderType,
			ResolutionW:  result.Width,
			ResolutionH:  result.Height,
			FPSActual:    result.FPSActual,
			LatencyP50Ms: result.LatencyP50Ms,
			LatencyP95Ms: result.LatencyP95Ms,
			LatencyP99Ms: result.LatencyP99Ms,
			FrameKB:      result.BytesPerFrameAvg / 1024,
			BitrateKbps:  result.BitrateKbps,
			Available:    result.Error == "",
			Notes:        result.Error,
		})
	}

	// ── GPU Encoder Benchmarks (via ffmpeg) ────────────────────────────
	log.Println()
	log.Println("── GPU Encoder Benchmarks (ffmpeg) ────────────────────────")

	ffBatches := []struct {
		name      string
		ffEncoder string
		codec     string
		encType   string
		extra     []string
		width     int
		height    int
	}{
		// ── NVENC on GTX 1080 Ti (gpu 0) ──
		{"nvenc-1080ti-h264-qp26", "h264_nvenc", "h264", "nvenc", []string{"-gpu", "0", "-qp", "26", "-preset", "p1", "-tune", "ull"}, 1920, 1080},
		{"nvenc-1080ti-h264-qp26", "h264_nvenc", "h264", "nvenc", []string{"-gpu", "0", "-qp", "26", "-preset", "p1", "-tune", "ull"}, 2560, 1440},
		{"nvenc-1080ti-hevc-qp26", "hevc_nvenc", "hevc", "nvenc", []string{"-gpu", "0", "-qp", "26", "-preset", "p1", "-tune", "ull"}, 1920, 1080},
		{"nvenc-1080ti-hevc-qp26", "hevc_nvenc", "hevc", "nvenc", []string{"-gpu", "0", "-qp", "26", "-preset", "p1", "-tune", "ull"}, 2560, 1440},

		// ── NVENC on Quadro RTX 4000 (gpu 1) ──
		{"nvenc-quadro-h264-qp26", "h264_nvenc", "h264", "nvenc", []string{"-gpu", "1", "-qp", "26", "-preset", "p1", "-tune", "ull"}, 1920, 1080},
		{"nvenc-quadro-h264-qp26", "h264_nvenc", "h264", "nvenc", []string{"-gpu", "1", "-qp", "26", "-preset", "p1", "-tune", "ull"}, 2560, 1440},
		{"nvenc-quadro-hevc-qp26", "hevc_nvenc", "hevc", "nvenc", []string{"-gpu", "1", "-qp", "26", "-preset", "p1", "-tune", "ull"}, 1920, 1080},
		{"nvenc-quadro-hevc-qp26", "hevc_nvenc", "hevc", "nvenc", []string{"-gpu", "1", "-qp", "26", "-preset", "p1", "-tune", "ull"}, 2560, 1440},

		// ── AMF on RX 6800 XT ──
		{"amf-6800xt-h264-qp26", "h264_amf", "h264", "amf", []string{"-rc", "cqp", "-qp_i", "26", "-qp_p", "26", "-quality", "speed"}, 1920, 1080},
		{"amf-6800xt-h264-qp26", "h264_amf", "h264", "amf", []string{"-rc", "cqp", "-qp_i", "26", "-qp_p", "26", "-quality", "speed"}, 2560, 1440},
		{"amf-6800xt-hevc-qp26", "hevc_amf", "hevc", "amf", []string{"-rc", "cqp", "-qp_i", "26", "-qp_p", "26", "-quality", "speed"}, 1920, 1080},
		{"amf-6800xt-hevc-qp26", "hevc_amf", "hevc", "amf", []string{"-rc", "cqp", "-qp_i", "26", "-qp_p", "26", "-quality", "speed"}, 2560, 1440},

		// ── MediaFoundation on each GPU (d3d11va:0=AMD, d3d11va:1=1080Ti, d3d11va:2=Quadro) ──
		{"mf-amd-h264", "h264_mf", "h264", "mf-amd", []string{"-init_hw_device", "d3d11va:0", "-rate_control", "quality", "-quality", "70"}, 1920, 1080},
		{"mf-1080ti-h264", "h264_mf", "h264", "mf-nvidia", []string{"-init_hw_device", "d3d11va:1", "-rate_control", "quality", "-quality", "70"}, 1920, 1080},
		{"mf-quadro-h264", "h264_mf", "h264", "mf-nvidia", []string{"-init_hw_device", "d3d11va:2", "-rate_control", "quality", "-quality", "70"}, 1920, 1080},
		{"mf-amd-h264", "h264_mf", "h264", "mf-amd", []string{"-init_hw_device", "d3d11va:0", "-rate_control", "quality", "-quality", "70"}, 2560, 1440},
		{"mf-1080ti-h264", "h264_mf", "h264", "mf-nvidia", []string{"-init_hw_device", "d3d11va:1", "-rate_control", "quality", "-quality", "70"}, 2560, 1440},
		{"mf-quadro-h264", "h264_mf", "h264", "mf-nvidia", []string{"-init_hw_device", "d3d11va:2", "-rate_control", "quality", "-quality", "70"}, 2560, 1440},

		// ═══════════════════════════════════════════════════════════
		// x264 H.264 — varying presets, threads, CRF (all 1080p)
		// ═══════════════════════════════════════════════════════════

		// Thread scaling (ultrafast, CRF 26)
		{"x264-ultrafast-1t", "libx264", "h264", "x264-sw", []string{"-preset", "ultrafast", "-tune", "zerolatency", "-crf", "26", "-threads", "1"}, 1920, 1080},
		{"x264-ultrafast-2t", "libx264", "h264", "x264-sw", []string{"-preset", "ultrafast", "-tune", "zerolatency", "-crf", "26", "-threads", "2"}, 1920, 1080},
		{"x264-ultrafast-4t", "libx264", "h264", "x264-sw", []string{"-preset", "ultrafast", "-tune", "zerolatency", "-crf", "26", "-threads", "4"}, 1920, 1080},
		{"x264-ultrafast-8t", "libx264", "h264", "x264-sw", []string{"-preset", "ultrafast", "-tune", "zerolatency", "-crf", "26", "-threads", "8"}, 1920, 1080},
		{"x264-ultrafast-12t", "libx264", "h264", "x264-sw", []string{"-preset", "ultrafast", "-tune", "zerolatency", "-crf", "26", "-threads", "12"}, 1920, 1080},

		// Preset scaling (4 threads, CRF 26)
		{"x264-superfast-4t", "libx264", "h264", "x264-sw", []string{"-preset", "superfast", "-tune", "zerolatency", "-crf", "26", "-threads", "4"}, 1920, 1080},
		{"x264-veryfast-4t", "libx264", "h264", "x264-sw", []string{"-preset", "veryfast", "-tune", "zerolatency", "-crf", "26", "-threads", "4"}, 1920, 1080},
		{"x264-faster-4t", "libx264", "h264", "x264-sw", []string{"-preset", "faster", "-tune", "zerolatency", "-crf", "26", "-threads", "4"}, 1920, 1080},
		{"x264-fast-4t", "libx264", "h264", "x264-sw", []string{"-preset", "fast", "-tune", "zerolatency", "-crf", "26", "-threads", "4"}, 1920, 1080},
		{"x264-medium-4t", "libx264", "h264", "x264-sw", []string{"-preset", "medium", "-tune", "zerolatency", "-crf", "26", "-threads", "4"}, 1920, 1080},

		// CRF scaling (ultrafast, 4 threads)
		{"x264-ultrafast-4t-crf20", "libx264", "h264", "x264-sw", []string{"-preset", "ultrafast", "-tune", "zerolatency", "-crf", "20", "-threads", "4"}, 1920, 1080},
		{"x264-ultrafast-4t-crf32", "libx264", "h264", "x264-sw", []string{"-preset", "ultrafast", "-tune", "zerolatency", "-crf", "32", "-threads", "4"}, 1920, 1080},

		// 1440p key configs
		{"x264-ultrafast-4t", "libx264", "h264", "x264-sw", []string{"-preset", "ultrafast", "-tune", "zerolatency", "-crf", "26", "-threads", "4"}, 2560, 1440},
		{"x264-ultrafast-12t", "libx264", "h264", "x264-sw", []string{"-preset", "ultrafast", "-tune", "zerolatency", "-crf", "26", "-threads", "12"}, 2560, 1440},
	}

	for _, cfg := range ffBatches {
		enc := encode.NewFFmpegBatch(cfg.name, cfg.ffEncoder, cfg.codec, cfg.encType, cfg.extra...)
		log.Printf("  [%s] %dx%d ...", cfg.name, cfg.width, cfg.height)
		result, timings := enc.RunBatch(cfg.width, cfg.height, 60, *warmup, *frames)

		if result.Error != "" {
			log.Printf("    ERROR: %s", result.Error)
		} else {
			log.Printf("    FPS=%.1f  avg=%.1fms  (%d frames in %.0fms)",
				result.FPSActual, result.LatencyMeanMs, result.FramesEncoded, result.TotalMs)
		}

		encRow := &benchdb.EncodeResultRow{
			SessionID:         sessionID,
			Encoder:           result.Encoder,
			Codec:             result.Codec,
			EncoderType:       result.EncoderType,
			ResolutionW:       result.Width,
			ResolutionH:       result.Height,
			TargetFPS:         60,
			QP:                intPtr(26),
			Threads:           4,
			WarmupFrames:      *warmup,
			FramesEncoded:     result.FramesEncoded,
			TotalDurationMs:   result.TotalMs,
			FPSActual:         result.FPSActual,
			LatencyMeanMs:     result.LatencyMeanMs,
			LatencyP50Ms:      result.LatencyP50Ms,
			LatencyP95Ms:      result.LatencyP95Ms,
			LatencyP99Ms:      result.LatencyP99Ms,
			LatencyMinMs:      result.LatencyMinMs,
			LatencyMaxMs:      result.LatencyMaxMs,
			BitrateActualKbps: result.BitrateKbps,
			ErrorCount:        0,
			AdditionalArgs:    fmt.Sprintf("{\"ffmpeg_encoder\":\"%s\"}", cfg.ffEncoder),
		}
		if result.Error != "" {
			encRow.ErrorCount = 1
			encRow.ErrorLast = result.Error
		}
		resultID, _ := db.InsertEncodeResult(encRow)

		if resultID > 0 && len(timings) > 0 {
			rows := make([]benchdb.FrameTimingRow, len(timings))
			for i, t := range timings {
				rows[i] = benchdb.FrameTimingRow{
					ResultID:   resultID,
					ResultType: "encode",
					FrameIndex: t.FrameIndex,
					DurationUs: t.DurationUs,
				}
			}
			db.InsertFrameTimings(rows)
		}

		sum.Encoders = append(sum.Encoders, report.EncodeEntry{
			Encoder:      result.Encoder,
			Codec:        result.Codec,
			Type:         result.EncoderType,
			ResolutionW:  result.Width,
			ResolutionH:  result.Height,
			FPSActual:    result.FPSActual,
			LatencyP50Ms: result.LatencyP50Ms,
			LatencyP95Ms: result.LatencyP95Ms,
			LatencyP99Ms: result.LatencyP99Ms,
			FrameKB:      result.BytesPerFrameAvg / 1024,
			BitrateKbps:  result.BitrateKbps,
			Available:    result.Error == "",
			Notes:        result.Error,
		})
	}

	// ── Summary JSON + Markdown ─────────────────────────────────────────
	log.Println()
	log.Println("── Writing Reports ─────────────────────────────────────────")

	jsonPath, err := report.WriteJSON(*outDir, sum)
	if err != nil {
		log.Printf("  warn: write JSON: %v", err)
	} else {
		log.Printf("  JSON: %s", jsonPath)
	}

	mdPath, err := report.WriteMarkdown(*outDir, sum)
	if err != nil {
		log.Printf("  warn: write markdown: %v", err)
	} else {
		log.Printf("  Markdown: %s", mdPath)
	}

	// ── Finish ──────────────────────────────────────────────────────────
	db.CompleteSession(sessionID)
	log.Println()
	log.Printf("✓ Benchmark complete. Database: %s", *dbPath)
}

func int64Ptr(v int64) *int64 { return &v }
func intPtr(v int) *int { return &v }
