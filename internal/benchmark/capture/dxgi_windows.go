//go:build windows

package capture

// DXGIResult mirrors GDIResult for the DXGI Desktop Duplication backend.
// The actual DXGI capture implementation requires D3D11 COM interface bindings
// which are non-trivial to express in pure Go. This file provides the result
// type and a stub so the rest of the benchmark tool can compile and plan for
// DXGI results.
//
// Implementation status: PENDING
// When implementing, use:
//   - IDXGIOutput1::DuplicateOutput() to create an IDXGIOutputDuplication
//   - IDXGIOutputDuplication::AcquireNextFrame() per frame
//   - ID3D11DeviceContext::CopyResource() to a staging texture
//   - ID3D11DeviceContext::Map(D3D11_MAP_READ) for CPU pixel access
//   - IDXGIOutputDuplication::ReleaseFrame() after use
//
// References:
//   https://learn.microsoft.com/en-us/windows/win32/direct3ddxgi/desktop-dup-api

// DXGIResult contains results for a DXGI Desktop Duplication capture benchmark.
type DXGIResult struct {
	Available bool   // false if DXGI duplication is not accessible (e.g., virtual display)
	UnavailableReason string

	Backend        string
	BackendVariant string
	Platform       string
	ResolutionW    int
	ResolutionH    int
	TargetFPS      int

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

	CPUUsagePct    float64
	MemoryDeltaMb  float64
	BytesPerFrame  float64

	ErrorCount  int
	ErrorLast   string
	RawFilePath string
	FrameTimingsUs []int64
	AdditionalArgs string // JSON
}

// DXGIConfig controls the DXGI benchmark (mirrors GDIConfig for consistency).
type DXGIConfig struct {
	Frames       int
	WarmupFrames int
	// CopyToStagingTexture: if true, also copies to a CPU-accessible staging
	// texture (the full cost a software encoder would pay). If false, only
	// measures AcquireNextFrame / ReleaseFrame overhead.
	CopyToStagingTexture bool
	RawDir               string
}

// DefaultDXGIConfig returns sensible defaults.
func DefaultDXGIConfig() DXGIConfig {
	return DXGIConfig{
		Frames:               300,
		WarmupFrames:         30,
		CopyToStagingTexture: true,
		RawDir:               ".",
	}
}

// RunDXGI attempts to benchmark the DXGI Desktop Duplication API.
// On machines where DXGI duplication is unavailable (e.g., Parsec virtual
// display, RDP sessions, or certain driver configurations) it returns a result
// with Available=false and an explanatory reason rather than an error.
//
// NOTE: This is a STUB. The full implementation is pending.
// See the file-level comment for the required COM interface calls.
func RunDXGI(cfg DXGIConfig) ([]DXGIResult, error) {
	// DXGI Desktop Duplication requires:
	//   1. A real hardware display adapter (not virtual/software)
	//   2. D3D11 device creation on that adapter
	//   3. IDXGIOutput1 from the adapter
	//   4. DuplicateOutput() — this FAILS on virtual displays (E_ACCESSDENIED
	//      or DXGI_ERROR_UNSUPPORTED)
	//
	// On this machine (Parsec Virtual Display Adapter), DXGI duplication is
	// expected to return DXGI_ERROR_UNSUPPORTED.
	//
	// TODO: implement full D3D11 + DXGI COM bindings.
	// Suggested approach: use golang.org/x/sys/windows for COM IUnknown calls
	// and define the required DXGI/D3D11 GUID + vtable structs.

	notAvail := DXGIResult{
		Available:         false,
		UnavailableReason: "DXGI capture implementation pending; virtual display adapter detected — DXGI_ERROR_UNSUPPORTED expected",
		Backend:           "dxgi",
		BackendVariant:    "duplication_v1",
		Platform:          "windows",
		AdditionalArgs:    `{"status":"pending","reason":"com_bindings_not_implemented"}`,
	}
	return []DXGIResult{notAvail}, nil
}
