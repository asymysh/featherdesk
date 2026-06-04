package main

import (
	"fmt"

	"github.com/opd-ai/vp8"
)

type PureVP8BenchEnc struct {
	enc *vp8.Encoder
	kf  int
}

func NewPureVP8Bench(w, h, keyframeInterval int) (*PureVP8BenchEnc, error) {
	enc, err := vp8.NewEncoder(w, h, 30)
	if err != nil {
		return nil, fmt.Errorf("pure vp8 init: %w", err)
	}
	enc.SetBitrate(2_000_000)
	if keyframeInterval > 0 {
		enc.SetKeyFrameInterval(keyframeInterval)
		enc.SetLoopFilterLevel(20)
	}
	return &PureVP8BenchEnc{enc: enc, kf: keyframeInterval}, nil
}

func (e *PureVP8BenchEnc) Name() string {
	if e.kf > 0 {
		return fmt.Sprintf("purego-vp8-inter-kf%d", e.kf)
	}
	return "purego-vp8-iframe"
}
func (e *PureVP8BenchEnc) Library() string { return "opd-ai/vp8" }
func (e *PureVP8BenchEnc) Codec() string   { return "vp8" }

func (e *PureVP8BenchEnc) Encode(y, u, v []byte, w, h int) ([]byte, error) {
	yuv := make([]byte, len(y)+len(u)+len(v))
	copy(yuv, y)
	copy(yuv[len(y):], u)
	copy(yuv[len(y)+len(u):], v)
	return e.enc.Encode(yuv)
}

func (e *PureVP8BenchEnc) Close() {}
