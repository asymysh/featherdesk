package encode

import (
	"fmt"
	"math/rand"
	"sort"
	"time"
)

// Result holds aggregate stats for one encoder run.
type Result struct {
	Encoder       string
	Codec         string
	EncoderType   string // "software" | "hardware"
	Width, Height int
	FramesEncoded int
	TotalMs       float64
	FPSActual     float64

	LatencyMeanMs   float64
	LatencyP50Ms    float64
	LatencyP80Ms    float64
	LatencyP95Ms    float64
	LatencyP99Ms    float64
	LatencyMinMs    float64
	LatencyMaxMs    float64

	BytesPerFrameAvg float64
	BitrateKbps      float64

	Error string
}

// RawTiming is one per-frame measurement.
type RawTiming struct {
	FrameIndex int
	DurationUs int64
	OutputBytes int
	IsKeyframe bool
}

// EncoderBench is implemented by each encoder backend.
type EncoderBench interface {
	Name() string
	Codec() string
	Type() string // "software" | "hardware"
	Init(width, height, fps int) error
	Encode(y, u, v []byte, width, height int) ([]byte, error)
	Close()
}

// GenerateI420Frame creates a synthetic I420 frame with pseudo-random content.
func GenerateI420Frame(width, height, frameIdx int) (y, u, v []byte) {
	ySize := width * height
	uvSize := (width / 2) * (height / 2)
	y = make([]byte, ySize)
	u = make([]byte, uvSize)
	v = make([]byte, uvSize)

	// Gradient + noise pattern to exercise the encoder
	r := rand.New(rand.NewSource(int64(frameIdx)))
	for j := 0; j < height; j++ {
		for i := 0; i < width; i++ {
			grad := byte((i + j + frameIdx*3) & 0xFF)
			noise := byte(r.Intn(16))
			y[j*width+i] = grad + noise
		}
	}
	for j := 0; j < height/2; j++ {
		for i := 0; i < width/2; i++ {
			u[j*(width/2)+i] = byte(128 + (j+frameIdx)%64)
			v[j*(width/2)+i] = byte(128 + (i+frameIdx)%64)
		}
	}
	return
}

// RunBenchmark runs an encoder through warmup + measurement.
func RunBenchmark(enc EncoderBench, width, height, fps, warmupFrames, measureFrames int) (*Result, []RawTiming) {
	res := &Result{
		Encoder:     enc.Name(),
		Codec:       enc.Codec(),
		EncoderType: enc.Type(),
		Width:       width,
		Height:      height,
	}

	if err := enc.Init(width, height, fps); err != nil {
		res.Error = fmt.Sprintf("init: %v", err)
		return res, nil
	}
	defer enc.Close()

	// Warmup
	for i := 0; i < warmupFrames; i++ {
		y, u, v := GenerateI420Frame(width, height, i)
		_, err := enc.Encode(y, u, v, width, height)
		if err != nil {
			res.Error = fmt.Sprintf("warmup frame %d: %v", i, err)
			return res, nil
		}
	}

	// Measure
	timings := make([]RawTiming, 0, measureFrames)
	var totalBytes int64

	for i := 0; i < measureFrames; i++ {
		y, u, v := GenerateI420Frame(width, height, warmupFrames+i)

		start := time.Now()
		out, err := enc.Encode(y, u, v, width, height)
		dur := time.Since(start)

		if err != nil {
			res.Error = fmt.Sprintf("frame %d: %v", i, err)
			return res, timings
		}

		timings = append(timings, RawTiming{
			FrameIndex:  i,
			DurationUs:  dur.Microseconds(),
			OutputBytes: len(out),
			IsKeyframe:  i == 0, // simplified
		})
		totalBytes += int64(len(out))
	}

	// Compute stats
	n := len(timings)
	if n == 0 {
		res.Error = "no frames encoded"
		return res, nil
	}

	latencies := make([]float64, n)
	var sumMs float64
	for i, t := range timings {
		ms := float64(t.DurationUs) / 1000.0
		latencies[i] = ms
		sumMs += ms
	}
	sort.Float64s(latencies)

	res.FramesEncoded = n
	res.TotalMs = sumMs
	res.FPSActual = float64(n) / (sumMs / 1000.0)
	res.LatencyMeanMs = sumMs / float64(n)
	res.LatencyP50Ms = latencies[n*50/100]
	res.LatencyP80Ms = latencies[n*80/100]
	res.LatencyP95Ms = latencies[n*95/100]
	res.LatencyP99Ms = latencies[n*99/100]
	res.LatencyMinMs = latencies[0]
	res.LatencyMaxMs = latencies[n-1]
	res.BytesPerFrameAvg = float64(totalBytes) / float64(n)
	res.BitrateKbps = float64(totalBytes) * 8.0 / (sumMs / 1000.0) / 1000.0

	return res, timings
}
