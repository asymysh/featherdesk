package db

// Row types mirror the database tables. Every table's row struct has an
// AdditionalArgs field (stored as JSON text) for extension without migrations.

// SessionRow corresponds to benchmark_sessions.
type SessionRow struct {
	SessionID        string
	SessionName      string
	BenchmarkVersion string
	Platform         string
	OSName           string
	OSVersion        string
	OSArch           string
	CPUModel         string
	CPUCores         int
	CPUThreads       int
	CPUFreqMHz       int
	RAMTotalMB       int64
	RAMAvailableMB   int64
	DisplayW         int
	DisplayH         int
	DisplayHz        int
	DisplayAdapter   string
	AdditionalArgs   string // JSON
}

// GPURow corresponds to gpu_devices.
type GPURow struct {
	SessionID          string
	DeviceIndex        int
	DeviceName         string
	DeviceType         string
	Vendor             string
	VRAMMb             *int64
	DriverVersion      string
	DRMCardPath        string
	DRMRenderNode      string
	VAAAPIVendorStr    string
	VAAPIProfiles      string // JSON array
	DXGIAdapterLUID    string
	D3DFeatureLevel    string
	DXGIDedicatedVRAMMb *int64
	DXGISharedMemMb    *int64
	EncH264            *int // 1/0/nil
	EncH265            *int
	EncVP8             *int
	EncVP9             *int
	EncAV1             *int
	AdditionalArgs     string // JSON
}

// CaptureResultRow corresponds to capture_results.
type CaptureResultRow struct {
	SessionID        string
	Backend          string
	BackendVariant   string
	Platform         string
	ResolutionW      int
	ResolutionH      int
	TargetFPS        int
	FramesAttempted  int
	WarmupFrames     int
	FramesCaptured   int
	FramesDropped    int
	TotalDurationMs  float64
	FPSActual        float64
	LatencyMeanMs    float64
	LatencyP50Ms     float64
	LatencyP95Ms     float64
	LatencyP99Ms     float64
	LatencyMinMs     float64
	LatencyMaxMs     float64
	LatencyStddevMs  float64
	CPUUsagePct      float64
	MemoryDeltaMb    float64
	BytesPerFrameAvg float64
	ErrorCount       int
	ErrorLast        string
	RawFilePath      string
	AdditionalArgs   string // JSON
}

// EncodeResultRow corresponds to encode_results.
type EncodeResultRow struct {
	SessionID          string
	Encoder            string
	Codec              string
	EncoderType        string
	EncoderVersion     string
	ResolutionW        int
	ResolutionH        int
	TargetFPS          int
	QP                 *int
	TargetBitrateKbps  *int
	GOPSize            int
	BFrames            int
	Threads            int
	Preset             string
	Profile            string
	WarmupFrames       int
	FramesEncoded      int
	TotalDurationMs    float64
	FPSActual          float64
	LatencyMeanMs      float64
	LatencyP50Ms       float64
	LatencyP95Ms       float64
	LatencyP99Ms       float64
	LatencyMinMs       float64
	LatencyMaxMs       float64
	LatencyStddevMs    float64
	BytesPerFrameAvg   float64
	KeyframeBytesAvg   float64
	PFrameBytesAvg     float64
	BitrateActualKbps  float64
	CPUUsagePct        float64
	GPUUsagePct        float64
	MemoryDeltaMb      float64
	SSIM               *float64
	PSNRdB             *float64
	VMAF               *float64
	ErrorCount         int
	ErrorLast          string
	RawFilePath        string
	AdditionalArgs     string // JSON
}

// FrameTimingRow corresponds to raw_frame_timings.
type FrameTimingRow struct {
	ResultID       int64
	ResultType     string // "capture" | "encode"
	FrameIndex     int
	WallTsUs       int64  // unix microseconds at frame start
	DurationUs     int64  // frame duration in microseconds
	IsKeyframe     *int   // 1/0/nil
	OutputBytes    *int64
	CPUPct         *float64
	AdditionalArgs string // JSON
}

// RecommendationRow corresponds to recommendations.
type RecommendationRow struct {
	SessionID     string
	Tier          string
	PriorityRank  int
	Encoder       string
	Codec         string
	EncoderType   string
	Resolution    string
	TargetFPS     int
	QP            *int
	Score         float64
	LatencyP95Ms  float64
	FPSActual     float64
	BitrateKbps   float64
	SSIM          *float64
	CPUUsagePct   float64
	Reason        string
	AdditionalArgs string // JSON
}
