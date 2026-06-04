package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"
)

type BenchResult struct {
	Name       string
	Library    string
	Codec      string
	Frames     int
	Dropped    int
	AvgMs      float64
	P50Ms      float64
	P80Ms      float64
	P95Ms      float64
	P99Ms      float64
	AvgNALSize int
}

type EncoderBench interface {
	Name() string
	Library() string
	Codec() string
	Encode(y, u, v []byte, width, height int) ([]byte, error)
	Close()
}

type yuvFrame struct {
	y, u, v []byte
}

var (
	width  = flag.Int("w", 2560, "frame width")
	height = flag.Int("h", 1440, "frame height")
	frames = flag.Int("n", 60, "frames to bench (after warmup)")
	warmup = flag.Int("warmup", 5, "warmup frames")
	filter = flag.String("filter", "", "only run configs whose name contains this substring")
)

func main() {
	flag.Parse()
	fmt.Printf("=== COMPREHENSIVE ENCODER BENCHMARK ===\n")
	fmt.Printf("Resolution: %dx%d | Frames: %d (+%d warmup)\n", *width, *height, *frames, *warmup)
	fmt.Printf("Primary metric: P80\n\n")

	total := *frames + *warmup
	frms := genFrames(*width, *height, total)
	fmt.Printf("Generated %d test frames (%.1f MB/frame)\n\n", total,
		float64(len(frms[0].y)+len(frms[0].u)+len(frms[0].v))/1024/1024)

	type encoderFactory struct {
		name    string
		factory func() (EncoderBench, error)
	}

	all := []encoderFactory{}

	// === LIBRARY 1: OpenH264 (direct cgo) ===
	for _, qp := range []int{20, 26, 32} {
		qp := qp
		all = append(all, encoderFactory{
			name: fmt.Sprintf("openh264-qp%d", qp),
			factory: func() (EncoderBench, error) {
				return NewOpenH264Bench(*width, *height, qp)
			},
		})
	}

	// === LIBRARY 2: x264 (direct cgo) ===
	x264Configs := []struct {
		preset, tune string
		threads      int
	}{
		{"ultrafast", "zerolatency", 1},
		{"ultrafast", "zerolatency", 2},
		{"ultrafast", "zerolatency", 4},
		{"superfast", "zerolatency", 1},
		{"superfast", "zerolatency", 4},
		{"veryfast", "zerolatency", 1},
		{"veryfast", "zerolatency", 4},
		{"faster", "zerolatency", 4},
		{"fast", "zerolatency", 4},
		{"ultrafast", "fastdecode", 1},
		{"ultrafast", "fastdecode", 4},
	}
	for _, cfg := range x264Configs {
		cfg := cfg
		all = append(all, encoderFactory{
			name: fmt.Sprintf("x264-%s-%s-%dt", cfg.preset, cfg.tune[:2], cfg.threads),
			factory: func() (EncoderBench, error) {
				return NewX264Bench(*width, *height, cfg.preset, cfg.tune, cfg.threads)
			},
		})
	}

	// === LIBRARY 3: libavcodec (direct cgo) ===
	libavConfigs := []struct {
		name, codec string
		opts        map[string]string
		threads     int
	}{
		{"libav-x264-ultrafast-1t", "libx264", map[string]string{"preset": "ultrafast", "tune": "zerolatency", "crf": "26"}, 1},
		{"libav-x264-ultrafast-4t", "libx264", map[string]string{"preset": "ultrafast", "tune": "zerolatency", "crf": "26"}, 4},
		{"libav-x264-superfast-4t", "libx264", map[string]string{"preset": "superfast", "tune": "zerolatency", "crf": "26"}, 4},
		{"libav-x264-veryfast-4t", "libx264", map[string]string{"preset": "veryfast", "tune": "zerolatency", "crf": "26"}, 4},
		{"libav-x265-ultrafast-1t", "libx265", map[string]string{"preset": "ultrafast", "tune": "zerolatency", "crf": "28"}, 1},
		{"libav-x265-ultrafast-4t", "libx265", map[string]string{"preset": "ultrafast", "tune": "zerolatency", "crf": "28"}, 4},
		{"libav-x265-superfast-1t", "libx265", map[string]string{"preset": "superfast", "tune": "zerolatency", "crf": "28"}, 1},
		{"libav-x265-medium-1t", "libx265", map[string]string{"preset": "medium", "tune": "zerolatency", "crf": "28"}, 1},
		{"libav-vp8-speed4-1t", "libvpx", map[string]string{"quality": "realtime", "cpu-used": "4", "crf": "26"}, 1},
		{"libav-vp8-speed8-1t", "libvpx", map[string]string{"quality": "realtime", "cpu-used": "8", "crf": "26"}, 1},
		{"libav-vp8-speed8-4t", "libvpx", map[string]string{"quality": "realtime", "cpu-used": "8", "crf": "26"}, 4},
		{"libav-vp8-speed16-1t", "libvpx", map[string]string{"quality": "realtime", "cpu-used": "16", "crf": "26"}, 1},
		{"libav-vp9-speed6-1t", "libvpx-vp9", map[string]string{"quality": "realtime", "cpu-used": "6", "crf": "30"}, 1},
		{"libav-vp9-speed8-1t", "libvpx-vp9", map[string]string{"quality": "realtime", "cpu-used": "8", "crf": "30"}, 1},
		{"libav-vp9-speed7-1t", "libvpx-vp9", map[string]string{"quality": "realtime", "cpu-used": "7", "crf": "30"}, 1},
		{"libav-vp9-speed8-4t", "libvpx-vp9", map[string]string{"quality": "realtime", "cpu-used": "8", "crf": "30"}, 4},
		{"libav-svtav1-p8", "libsvtav1", map[string]string{"preset": "8", "crf": "30"}, 1},
		{"libav-svtav1-p10", "libsvtav1", map[string]string{"preset": "10", "crf": "30"}, 1},
		{"libav-svtav1-p11", "libsvtav1", map[string]string{"preset": "11", "crf": "30"}, 1},
		{"libav-svtav1-p12", "libsvtav1", map[string]string{"preset": "12", "crf": "30"}, 1},
		{"libav-aom-speed8", "libaom-av1", map[string]string{"cpu-used": "8", "crf": "30", "usage": "realtime"}, 1},
		{"libav-aom-speed10", "libaom-av1", map[string]string{"cpu-used": "10", "crf": "30", "usage": "realtime"}, 1},
		{"libav-aom-speed8-4t", "libaom-av1", map[string]string{"cpu-used": "8", "crf": "30", "usage": "realtime"}, 4},
		{"libav-rav1e-speed10", "librav1e", map[string]string{"speed": "10", "qp": "80"}, 1},
		{"libav-mpeg4-q4", "mpeg4", map[string]string{"q:v": "4"}, 1},
		{"libav-snow", "snow", map[string]string{}, 1},
	}
	for _, cfg := range libavConfigs {
		cfg := cfg
		all = append(all, encoderFactory{
			name: cfg.name,
			factory: func() (EncoderBench, error) {
				return NewLibavBench(cfg.name, *width, *height, cfg.codec, cfg.opts, cfg.threads)
			},
		})
	}

	// === LIBRARY 4: ffmpeg subprocess ===
	ffmpegConfigs := []struct {
		name string
		args []string
	}{
		{"ffsub-x264-ultrafast", []string{"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "26"}},
		{"ffsub-x264-superfast", []string{"-c:v", "libx264", "-preset", "superfast", "-tune", "zerolatency", "-crf", "26"}},
		{"ffsub-x264-veryfast", []string{"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-crf", "26"}},
		{"ffsub-x265-ultrafast", []string{"-c:v", "libx265", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "28"}},
		{"ffsub-x265-superfast", []string{"-c:v", "libx265", "-preset", "superfast", "-tune", "zerolatency", "-crf", "28"}},
		{"ffsub-vp8-speed8", []string{"-c:v", "libvpx", "-quality", "realtime", "-cpu-used", "8", "-crf", "26"}},
		{"ffsub-vp8-speed16", []string{"-c:v", "libvpx", "-quality", "realtime", "-cpu-used", "16", "-crf", "26"}},
		{"ffsub-vp9-speed8", []string{"-c:v", "libvpx-vp9", "-quality", "realtime", "-cpu-used", "8", "-crf", "30"}},
		{"ffsub-vp9-speed7", []string{"-c:v", "libvpx-vp9", "-quality", "realtime", "-cpu-used", "7", "-crf", "30"}},
		{"ffsub-svtav1-p10", []string{"-c:v", "libsvtav1", "-preset", "10", "-crf", "30"}},
		{"ffsub-svtav1-p12", []string{"-c:v", "libsvtav1", "-preset", "12", "-crf", "30"}},
		{"ffsub-aom-speed8", []string{"-c:v", "libaom-av1", "-cpu-used", "8", "-usage", "realtime", "-crf", "30"}},
		{"ffsub-aom-speed10", []string{"-c:v", "libaom-av1", "-cpu-used", "10", "-usage", "realtime", "-crf", "30"}},
		{"ffsub-rav1e-speed10", []string{"-c:v", "librav1e", "-speed", "10", "-qp", "80"}},
		{"ffsub-mpeg4-q4", []string{"-c:v", "mpeg4", "-q:v", "4"}},
	}
	for _, cfg := range ffmpegConfigs {
		cfg := cfg
		all = append(all, encoderFactory{
			name: cfg.name,
			factory: func() (EncoderBench, error) {
				return NewFFmpegSubBench(cfg.name, *width, *height, cfg.args)
			},
		})
	}

	// === LIBRARY 5: opd-ai/vp8 (pure Go) ===
	all = append(all, encoderFactory{
		name: "purego-vp8-iframe",
		factory: func() (EncoderBench, error) {
			return NewPureVP8Bench(*width, *height, 0)
		},
	})
	all = append(all, encoderFactory{
		name: "purego-vp8-inter-kf30",
		factory: func() (EncoderBench, error) {
			return NewPureVP8Bench(*width, *height, 30)
		},
	})

	// === LIBRARY 6: KarpelesLab/goavif (pure Go AV1) ===
	all = append(all, encoderFactory{
		name: "purego-av1-intra",
		factory: func() (EncoderBench, error) {
			return NewPureAV1Bench(*width, *height, false)
		},
	})
	all = append(all, encoderFactory{
		name: "purego-av1-inter",
		factory: func() (EncoderBench, error) {
			return NewPureAV1Bench(*width, *height, true)
		},
	})

	// === LIBRARY 7: VA-API H.264 via libavcodec (hw encode) ===
	vaapiConfigs := []struct {
		name     string
		profile  string
		qp       int
		threads  int
		lowPower bool
	}{
		{"vaapi-h264-qp26-lp", "high", 26, 1, true},
		{"vaapi-h264-qp26-full", "high", 26, 1, false},
		{"vaapi-h264-qp20-lp", "high", 20, 1, true},
		{"vaapi-h264-qp32-lp", "high", 32, 1, true},
		{"vaapi-h264-baseline-qp26-lp", "constrained_baseline", 26, 1, true},
		{"vaapi-h264-main-qp26-lp", "main", 26, 1, true},
		{"vaapi-h264-qp26-lp-async2", "high", 26, 2, true},
		{"vaapi-h264-qp26-lp-async4", "high", 26, 4, true},
	}
	for _, cfg := range vaapiConfigs {
		cfg := cfg
		all = append(all, encoderFactory{
			name: cfg.name,
			factory: func() (EncoderBench, error) {
				return NewVAAPIBench(cfg.name, *width, *height, "/dev/dri/renderD128", cfg.profile, cfg.qp, cfg.threads)
			},
		})
	}

	// === LIBRARY 8: ffmpeg subprocess VA-API ===
	ffmpegHWConfigs := []struct {
		name string
		args []string
	}{
		{"ffsub-vaapi-qp26-lp", []string{
			"-init_hw_device", "vaapi=va:/dev/dri/renderD128",
			"-filter_hw_device", "va",
			"-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi", "-qp", "26", "-low_power", "1",
		}},
		{"ffsub-vaapi-qp26-full", []string{
			"-init_hw_device", "vaapi=va:/dev/dri/renderD128",
			"-filter_hw_device", "va",
			"-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi", "-qp", "26", "-low_power", "0",
		}},
		{"ffsub-vaapi-qp20-lp", []string{
			"-init_hw_device", "vaapi=va:/dev/dri/renderD128",
			"-filter_hw_device", "va",
			"-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi", "-qp", "20", "-low_power", "1",
		}},
		{"ffsub-vaapi-qp32-lp", []string{
			"-init_hw_device", "vaapi=va:/dev/dri/renderD128",
			"-filter_hw_device", "va",
			"-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi", "-qp", "32", "-low_power", "1",
		}},
		{"ffsub-vaapi-baseline-qp26-lp", []string{
			"-init_hw_device", "vaapi=va:/dev/dri/renderD128",
			"-filter_hw_device", "va",
			"-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi", "-qp", "26", "-low_power", "1", "-profile:v", "constrained_baseline",
		}},
	}
	for _, cfg := range ffmpegHWConfigs {
		cfg := cfg
		all = append(all, encoderFactory{
			name: cfg.name,
			factory: func() (EncoderBench, error) {
				return NewFFmpegSubBench(cfg.name, *width, *height, cfg.args)
			},
		})
	}

	fmt.Printf("Total encoder configurations: %d\n\n", len(all))

	var results []BenchResult
	for i, ef := range all {
		if *filter != "" && !strings.Contains(ef.name, *filter) {
			continue
		}
		enc, err := ef.factory()
		if err != nil {
			fmt.Printf("[%2d/%d] %-30s SKIP (%v)\n", i+1, len(all), ef.name, err)
			continue
		}
		fmt.Printf("[%2d/%d] %-30s ", i+1, len(all), enc.Name())
		r := runBench(enc, frms, *width, *height, *warmup)
		results = append(results, r)
		enc.Close()
		if r.Frames > 0 {
			fmt.Printf("P80=%6.1fms avg=%6.1fms nal=%dKB\n", r.P80Ms, r.AvgMs, r.AvgNALSize/1024)
		} else {
			fmt.Printf("FAILED (0 frames)\n")
		}
	}

	output := formatTable(results)
	fmt.Println("\n" + output)
	os.WriteFile("/tmp/bench_full_results.txt", []byte(output), 0644)
	fmt.Println("Results saved to /tmp/bench_full_results.txt")
}

func genFrames(w, h, n int) []yuvFrame {
	ySize := w * h
	uvSize := (w / 2) * (h / 2)
	frames := make([]yuvFrame, n)
	for i := range frames {
		frames[i] = yuvFrame{
			y: make([]byte, ySize),
			u: make([]byte, uvSize),
			v: make([]byte, uvSize),
		}
		for j := range frames[i].y {
			frames[i].y[j] = byte((j*3 + i*7 + rand.Intn(10)) % 256)
		}
		for j := range frames[i].u {
			frames[i].u[j] = byte(128 + rand.Intn(30) - 15)
			frames[i].v[j] = byte(128 + rand.Intn(30) - 15)
		}
	}
	return frames
}

func runBench(enc EncoderBench, frames []yuvFrame, w, h, warmup int) BenchResult {
	var times []float64
	var totalBytes int64
	var nalCount, dropped int

	for i, f := range frames {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		done := make(chan struct{})
		var nal []byte
		var err error
		var elapsed time.Duration

		go func() {
			start := time.Now()
			nal, err = enc.Encode(f.y, f.u, f.v, w, h)
			elapsed = time.Since(start)
			close(done)
		}()

		select {
		case <-done:
			cancel()
		case <-ctx.Done():
			cancel()
			dropped++
			continue
		}

		if i < warmup {
			continue
		}
		if err != nil {
			dropped++
			continue
		}
		times = append(times, float64(elapsed.Microseconds()))
		if nal != nil && len(nal) > 0 {
			totalBytes += int64(len(nal))
			nalCount++
		}
	}

	if len(times) == 0 {
		return BenchResult{Name: enc.Name(), Library: enc.Library(), Codec: enc.Codec(), Dropped: dropped}
	}

	sort.Float64s(times)
	n := len(times)
	var sum float64
	for _, t := range times {
		sum += t
	}
	avgNAL := 0
	if nalCount > 0 {
		avgNAL = int(totalBytes / int64(nalCount))
	}

	return BenchResult{
		Name: enc.Name(), Library: enc.Library(), Codec: enc.Codec(),
		Frames: n, Dropped: dropped,
		AvgMs: sum / float64(n) / 1000,
		P50Ms: pct(times, 0.50) / 1000,
		P80Ms: pct(times, 0.80) / 1000,
		P95Ms: pct(times, 0.95) / 1000,
		P99Ms: pct(times, 0.99) / 1000,
		AvgNALSize: avgNAL,
	}
}

func pct(sorted []float64, p float64) float64 {
	n := len(sorted)
	idx := int(math.Ceil(float64(n)*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

func formatTable(results []BenchResult) string {
	s := fmt.Sprintf("%-32s %-14s %-8s %4s %4s %7s %7s %7s %7s %7s %7s\n",
		"NAME", "LIBRARY", "CODEC", "N", "DROP", "AVG", "P50", "P80*", "P95", "P99", "NAL")
	s += fmt.Sprintf("%-32s %-14s %-8s %4s %4s %7s %7s %7s %7s %7s %7s\n",
		"---", "---", "---", "---", "---", "---", "---", "---", "---", "---", "---")
	for _, r := range results {
		nal := "-"
		if r.AvgNALSize > 0 {
			if r.AvgNALSize > 1024 {
				nal = fmt.Sprintf("%dKB", r.AvgNALSize/1024)
			} else {
				nal = fmt.Sprintf("%dB", r.AvgNALSize)
			}
		}
		s += fmt.Sprintf("%-32s %-14s %-8s %4d %4d %5.1fms %5.1fms %5.1fms %5.1fms %5.1fms %7s\n",
			r.Name, r.Library, r.Codec, r.Frames, r.Dropped, r.AvgMs, r.P50Ms, r.P80Ms, r.P95Ms, r.P99Ms, nal)
	}
	s += "\n* P80 = primary comparison metric\n"
	s += fmt.Sprintf("Total tested: %d configurations\n", len(results))
	return s
}
