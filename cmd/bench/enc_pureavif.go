package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"

	"github.com/KarpelesLab/goavif"
)

type PureAV1BenchEnc struct {
	w, h  int
	inter bool
	prev  *image.NRGBA
}

func NewPureAV1Bench(w, h int, inter bool) (*PureAV1BenchEnc, error) {
	return &PureAV1BenchEnc{w: w, h: h, inter: inter}, nil
}

func (e *PureAV1BenchEnc) Name() string {
	if e.inter {
		return "purego-av1-inter"
	}
	return "purego-av1-intra"
}
func (e *PureAV1BenchEnc) Library() string { return "goavif-pure" }
func (e *PureAV1BenchEnc) Codec() string   { return "av1" }

func (e *PureAV1BenchEnc) Encode(y, u, v []byte, w, h int) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			yy := y[row*w+col]
			uu := u[(row/2)*(w/2)+col/2]
			vv := v[(row/2)*(w/2)+col/2]
			r, g, b := yuvToRGB(yy, uu, vv)
			img.SetNRGBA(col, row, color.NRGBA{R: r, G: g, B: b, A: 255})
		}
	}

	var buf bytes.Buffer
	err := goavif.Encode(&buf, img, nil)
	if err != nil {
		return nil, fmt.Errorf("goavif encode: %w", err)
	}
	return buf.Bytes(), nil
}

func (e *PureAV1BenchEnc) Close() {}

func yuvToRGB(y, u, v byte) (uint8, uint8, uint8) {
	yf := float64(y)
	uf := float64(u) - 128
	vf := float64(v) - 128
	r := yf + 1.402*vf
	g := yf - 0.344136*uf - 0.714136*vf
	b := yf + 1.772*uf
	return clamp(r), clamp(g), clamp(b)
}

func clamp(v float64) uint8 {
	if v < 0 { return 0 }
	if v > 255 { return 255 }
	return uint8(v)
}
