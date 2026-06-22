// Package report writes benchmark output in multiple formats:
//   - SQLite (via the db package)
//   - Per-run raw CSV (frame timings)
//   - JSON summary (human + machine readable)
//   - Markdown table (for pasting into docs / GitHub)
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Summary is a complete view of one benchmark session suitable for JSON output.
type Summary struct {
	SessionID   string    `json:"session_id"`
	SessionName string    `json:"session_name"`
	GeneratedAt time.Time `json:"generated_at"`
	Platform    string    `json:"platform"`
	Machine     Machine   `json:"machine"`
	Captures    []CaptureEntry `json:"captures,omitempty"`
	Encoders    []EncodeEntry  `json:"encoders,omitempty"`
}

type Machine struct {
	OS         string `json:"os"`
	CPU        string `json:"cpu"`
	Cores      int    `json:"cores"`
	Threads    int    `json:"threads"`
	RAMtotalMB int64  `json:"ram_total_mb"`
	Display    string `json:"display"`
	GPUs       []string `json:"gpus"`
}

type CaptureEntry struct {
	Backend        string  `json:"backend"`
	Variant        string  `json:"variant"`
	ResolutionW    int     `json:"width"`
	ResolutionH    int     `json:"height"`
	FPSActual      float64 `json:"fps_actual"`
	LatencyP50Ms   float64 `json:"latency_p50_ms"`
	LatencyP95Ms   float64 `json:"latency_p95_ms"`
	LatencyP99Ms   float64 `json:"latency_p99_ms"`
	CPUUsagePct    float64 `json:"cpu_usage_pct,omitempty"`
	BytesPerFrame  float64 `json:"bytes_per_frame"`
	ErrorCount     int     `json:"error_count"`
	Available      bool    `json:"available"`
	Notes          string  `json:"notes,omitempty"`
}

type EncodeEntry struct {
	Encoder       string   `json:"encoder"`
	Codec         string   `json:"codec"`
	Type          string   `json:"type"`
	ResolutionW   int      `json:"width"`
	ResolutionH   int      `json:"height"`
	QP            *int     `json:"qp,omitempty"`
	FPSActual     float64  `json:"fps_actual"`
	LatencyP50Ms  float64  `json:"latency_p50_ms"`
	LatencyP95Ms  float64  `json:"latency_p95_ms"`
	LatencyP99Ms  float64  `json:"latency_p99_ms"`
	FrameKB       float64  `json:"frame_kb_avg"`
	BitrateKbps   float64  `json:"bitrate_kbps"`
	CPUUsagePct   float64  `json:"cpu_usage_pct,omitempty"`
	SSIM          *float64 `json:"ssim,omitempty"`
	Available     bool     `json:"available"`
	Notes         string   `json:"notes,omitempty"`
}

// WriteJSON writes the summary to <dir>/summary_<timestamp>.json.
func WriteJSON(dir string, s *Summary) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("summary_%d.json", time.Now().UnixMilli()))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return path, enc.Encode(s)
}

// WriteMarkdown writes a human-friendly Markdown report.
func WriteMarkdown(dir string, s *Summary) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("report_%d.md", time.Now().UnixMilli()))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var b strings.Builder

	b.WriteString(fmt.Sprintf("# FeatherDesk Benchmark Report\n\n"))
	b.WriteString(fmt.Sprintf("**Session:** `%s`  \n", s.SessionID))
	b.WriteString(fmt.Sprintf("**Generated:** %s  \n", s.GeneratedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("**Platform:** %s  \n\n", s.Platform))

	b.WriteString("## Machine\n\n")
	b.WriteString(fmt.Sprintf("| Field | Value |\n|-------|-------|\n"))
	b.WriteString(fmt.Sprintf("| OS | %s |\n", s.Machine.OS))
	b.WriteString(fmt.Sprintf("| CPU | %s |\n", s.Machine.CPU))
	b.WriteString(fmt.Sprintf("| Cores / Threads | %d / %d |\n", s.Machine.Cores, s.Machine.Threads))
	b.WriteString(fmt.Sprintf("| RAM | %d MB |\n", s.Machine.RAMtotalMB))
	b.WriteString(fmt.Sprintf("| Display | %s |\n", s.Machine.Display))
	for i, g := range s.Machine.GPUs {
		b.WriteString(fmt.Sprintf("| GPU %d | %s |\n", i, g))
	}
	b.WriteString("\n")

	if len(s.Captures) > 0 {
		b.WriteString("## Capture Benchmarks\n\n")
		b.WriteString("| Backend | Variant | Resolution | FPS | p50 ms | p95 ms | p99 ms | Bytes/Frame | Available |\n")
		b.WriteString("|---------|---------|------------|-----|--------|--------|--------|-------------|----------|\n")
		for _, c := range s.Captures {
			avail := "✅"
			if !c.Available {
				avail = "❌"
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %dx%d | %.1f | %.2f | %.2f | %.2f | %.0f | %s |\n",
				c.Backend, c.Variant, c.ResolutionW, c.ResolutionH,
				c.FPSActual, c.LatencyP50Ms, c.LatencyP95Ms, c.LatencyP99Ms,
				c.BytesPerFrame, avail))
		}
		b.WriteString("\n")
	}

	if len(s.Encoders) > 0 {
		b.WriteString("## Encode Benchmarks\n\n")
		b.WriteString("| Encoder | Codec | Type | Resolution | QP | FPS | p50 ms | p95 ms | p99 ms | KB/frame | Kbps | Available |\n")
		b.WriteString("|---------|-------|------|------------|----|-----|--------|--------|--------|----------|------|-----------|\n")
		for _, e := range s.Encoders {
			avail := "✅"
			if !e.Available {
				avail = "❌"
			}
			qpStr := "-"
			if e.QP != nil {
				qpStr = fmt.Sprintf("%d", *e.QP)
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %dx%d | %s | %.1f | %.2f | %.2f | %.2f | %.1f | %.0f | %s |\n",
				e.Encoder, e.Codec, e.Type, e.ResolutionW, e.ResolutionH,
				qpStr, e.FPSActual, e.LatencyP50Ms, e.LatencyP95Ms, e.LatencyP99Ms,
				e.FrameKB, e.BitrateKbps, avail))
		}
		b.WriteString("\n")
	}

	_, err = f.WriteString(b.String())
	return path, err
}
