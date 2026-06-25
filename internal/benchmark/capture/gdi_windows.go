//go:build windows

// Package capture provides screen-capture benchmarks for each available
// Windows capture backend.
package capture

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ────────────────────────────────────────────────────────────────────────────
// Windows API bindings — GDI
// ────────────────────────────────────────────────────────────────────────────

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procGetDesktopWindow       = user32.NewProc("GetDesktopWindow")
	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
)

const (
	srccopy   = 0x00CC0020
	smCxScreen = 0
	smCyScreen = 1

	biRgb         = 0
	dibRgbColors  = 0
)

// BITMAPINFOHEADER is the Windows BITMAPINFOHEADER struct.
type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

// ────────────────────────────────────────────────────────────────────────────
// GDI Capture Benchmark
// ────────────────────────────────────────────────────────────────────────────

// GDIResult is returned by RunGDI.
type GDIResult struct {
	Backend         string
	BackendVariant  string
	Platform        string
	ResolutionW     int
	ResolutionH     int
	TargetFPS       int
	FramesAttempted int
	WarmupFrames    int
	FramesCaptured  int
	FramesDropped   int
	TotalDurationMs float64
	FPSActual       float64
	LatencyMeanMs   float64
	LatencyP50Ms    float64
	LatencyP95Ms    float64
	LatencyP99Ms    float64
	LatencyMinMs    float64
	LatencyMaxMs    float64
	LatencyStddevMs float64
	CPUUsagePct     float64
	MemoryDeltaMb   float64
	BytesPerFrame   float64
	ErrorCount      int
	ErrorLast       string
	RawFilePath     string
	// Frame timings in microseconds, one per frame.
	FrameTimingsUs []int64
	// JSON metadata about the run.
	AdditionalArgs string
}

// GDIConfig controls the GDI benchmark.
type GDIConfig struct {
	// Number of frames to capture (not counting warmup).
	Frames int
	// Number of warmup frames (discarded before stats).
	WarmupFrames int
	// Whether to actually copy pixels to a CPU buffer (true = more realistic,
	// false = just tests the BitBlt call without the readback).
	ReadPixels bool
	// Directory to write the raw CSV file.
	RawDir string
}

// DefaultGDIConfig returns sensible defaults.
func DefaultGDIConfig() GDIConfig {
	return GDIConfig{
		Frames:       300,
		WarmupFrames: 30,
		ReadPixels:   true,
		RawDir:       ".",
	}
}

// RunGDI runs the GDI (BitBlt) screen-capture benchmark.
// It captures the entire primary display and measures per-frame latency.
//
// Two variants are run:
//   - "bitblt_only"   — BitBlt to a memory DC, no pixel readback (measures
//                       driver overhead only)
//   - "bitblt_getdib" — BitBlt + GetDIBits to bring BGRA pixels to CPU memory
//                       (measures what a software encoder would actually pay)
func RunGDI(cfg GDIConfig, rawDir string) ([]GDIResult, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Get screen dimensions.
	sw, _, _ := procGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCyScreen)
	W, H := int(sw), int(sh)
	if W == 0 || H == 0 {
		return nil, fmt.Errorf("gdi: GetSystemMetrics returned 0 — no display?")
	}
	bytesPerFrame := int64(W * H * 4) // BGRA

	// Obtain the desktop device context.
	hwnd, _, _ := procGetDesktopWindow.Call()
	screenDC, _, _ := procGetDC.Call(hwnd)
	if screenDC == 0 {
		return nil, fmt.Errorf("gdi: GetDC failed")
	}
	defer procReleaseDC.Call(hwnd, screenDC)

	// Create a compatible memory DC and bitmap.
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("gdi: CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	bmp, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(W), uintptr(H))
	if bmp == 0 {
		return nil, fmt.Errorf("gdi: CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bmp)

	oldBmp, _, _ := procSelectObject.Call(memDC, bmp)
	defer procSelectObject.Call(memDC, oldBmp)

	// CPU pixel buffer for GetDIBits variant.
	pixelBuf := make([]byte, bytesPerFrame)

	bmi := bitmapInfoHeader{
		BiSize:      uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:     int32(W),
		BiHeight:    -int32(H), // negative = top-down DIB
		BiPlanes:    1,
		BiBitCount:  32,
		BiCompression: biRgb,
	}

	totalFrames := cfg.WarmupFrames + cfg.Frames

	// ── Variant 1: BitBlt only ──────────────────────────────────────────
	timings1 := make([]int64, 0, cfg.Frames)
	var errors1 int

	for i := 0; i < totalFrames; i++ {
		t0 := time.Now()
		ret, _, _ := procBitBlt.Call(
			memDC, 0, 0, uintptr(W), uintptr(H),
			screenDC, 0, 0,
			srccopy,
		)
		dt := time.Since(t0).Microseconds()
		if ret == 0 {
			errors1++
			continue
		}
		if i >= cfg.WarmupFrames {
			timings1 = append(timings1, dt)
		}
	}

	res1 := buildResult("gdi", "bitblt_only", W, H, timings1, bytesPerFrame,
		cfg, errors1, rawDir)

	// ── Variant 2: BitBlt + GetDIBits ──────────────────────────────────
	timings2 := make([]int64, 0, cfg.Frames)
	var errors2 int

	for i := 0; i < totalFrames; i++ {
		t0 := time.Now()

		// Step 1: BitBlt the screen into the memory DC.
		ret, _, _ := procBitBlt.Call(
			memDC, 0, 0, uintptr(W), uintptr(H),
			screenDC, 0, 0,
			srccopy,
		)
		if ret == 0 {
			errors2++
			continue
		}

		// Step 2: Copy pixels from the GDI bitmap to CPU memory.
		lines, _, _ := procGetDIBits.Call(
			memDC, bmp, 0, uintptr(H),
			uintptr(unsafe.Pointer(&pixelBuf[0])),
			uintptr(unsafe.Pointer(&bmi)),
			dibRgbColors,
		)
		dt := time.Since(t0).Microseconds()
		if lines == 0 {
			errors2++
			continue
		}
		if i >= cfg.WarmupFrames {
			timings2 = append(timings2, dt)
		}
	}

	res2 := buildResult("gdi", "bitblt_getdib", W, H, timings2, bytesPerFrame,
		cfg, errors2, rawDir)

	return []GDIResult{res1, res2}, nil
}

// buildResult computes stats and writes the raw CSV, then returns a GDIResult.
func buildResult(backend, variant string, W, H int, timingsUs []int64,
	bytesPerFrame int64, cfg GDIConfig, errCount int, rawDir string) GDIResult {

	n := len(timingsUs)
	r := GDIResult{
		Backend:         backend,
		BackendVariant:  variant,
		Platform:        "windows",
		ResolutionW:     W,
		ResolutionH:     H,
		TargetFPS:       0, // GDI runs as fast as possible; no target
		FramesAttempted: cfg.WarmupFrames + cfg.Frames,
		WarmupFrames:    cfg.WarmupFrames,
		FramesCaptured:  n,
		FramesDropped:   cfg.Frames - n,
		BytesPerFrame:   float64(bytesPerFrame),
		ErrorCount:      errCount,
		FrameTimingsUs:  timingsUs,
	}

	if n == 0 {
		r.ErrorLast = "no frames captured"
		return r
	}

	// Total time.
	var sumUs int64
	for _, t := range timingsUs {
		sumUs += t
	}
	r.TotalDurationMs = float64(sumUs) / 1000.0
	r.FPSActual = float64(n) / (r.TotalDurationMs / 1000.0)

	// Latency stats.
	r.LatencyMeanMs = r.TotalDurationMs / float64(n)
	sorted := make([]int64, n)
	copy(sorted, timingsUs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	r.LatencyMinMs = float64(sorted[0]) / 1000.0
	r.LatencyMaxMs = float64(sorted[n-1]) / 1000.0
	r.LatencyP50Ms = pctileMs(sorted, 0.50)
	r.LatencyP95Ms = pctileMs(sorted, 0.95)
	r.LatencyP99Ms = pctileMs(sorted, 0.99)

	// Std dev.
	var variance float64
	for _, t := range timingsUs {
		d := float64(t)/1000.0 - r.LatencyMeanMs
		variance += d * d
	}
	r.LatencyStddevMs = math.Sqrt(variance / float64(n))

	// Additional args JSON.
	extra := map[string]interface{}{
		"bytes_per_frame": bytesPerFrame,
		"color_format":    "BGRA32",
		"read_pixels":     variant == "bitblt_getdib",
	}
	b, _ := json.Marshal(extra)
	r.AdditionalArgs = string(b)

	// Write raw CSV.
	csvPath, err := writeRawCSV(rawDir, backend+"_"+variant, timingsUs, bytesPerFrame)
	if err == nil {
		r.RawFilePath = csvPath
	}

	return r
}

func pctileMs(sorted []int64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(float64(n-1) * p)
	return float64(sorted[idx]) / 1000.0
}

// writeRawCSV writes one row per frame to a CSV file and returns the path.
// Columns: frame_index, wall_ts_us, duration_us, output_bytes
func writeRawCSV(dir, name string, timingsUs []int64, outputBytes int64) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s/%s_%d.csv", dir, name, time.Now().UnixMilli())
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	w.Write([]string{"frame_index", "duration_us", "output_bytes"})
	baseTs := time.Now().UnixMicro()
	for i, t := range timingsUs {
		w.Write([]string{
			fmt.Sprintf("%d", i),
			fmt.Sprintf("%d", t),
			fmt.Sprintf("%d", outputBytes),
		})
		baseTs += t
	}
	w.Flush()
	return path, w.Error()
}

// MemoryUsageMB returns the current process RSS in megabytes using Windows API.
func MemoryUsageMB() float64 {
	type processMemoryCounters struct {
		cb                         uint32
		pageFaultCount             uint32
		peakWorkingSetSize         uintptr
		workingSetSize             uintptr
		quotaPeakPagedPoolUsage    uintptr
		quotaPagedPoolUsage        uintptr
		quotaPeakNonPagedPoolUsage uintptr
		quotaNonPagedPoolUsage     uintptr
		pagefileUsage              uintptr
		peakPagefileUsage          uintptr
	}
	psapi := windows.NewLazySystemDLL("psapi.dll")
	proc := psapi.NewProc("GetProcessMemoryInfo")
	var mc processMemoryCounters
	mc.cb = uint32(unsafe.Sizeof(mc))
	handle, _ := windows.GetCurrentProcess()
	proc.Call(uintptr(handle), uintptr(unsafe.Pointer(&mc)), uintptr(mc.cb))
	return float64(mc.workingSetSize) / 1024 / 1024
}
