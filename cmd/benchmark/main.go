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

	// ── Summary JSON + Markdown ─────────────────────────────────────────
	log.Println()
	log.Println("── Writing Reports ─────────────────────────────────────────")

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

	// Add capture entries.
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
