package encode

// I420Frame holds planar YUV 4:2:0 data with pre-allocated buffers.
type I420Frame struct {
	Y      []byte
	U      []byte
	V      []byte
	Width  int
	Height int
}

// Encoder accepts I420 frames and produces encoded NAL units.
type Encoder interface {
	Encode(frame *I420Frame) ([][]byte, error)
	ForceKeyframe()
	Close() error
}

// EncoderConfig holds parameters for the software H.264 encoder.
type EncoderConfig struct {
	Width      int
	Height     int
	FPS        int
	BitrateBps int
	QP         int // 0 means use rate control; >0 means fixed QP (disables RC)
}
