// NvFBC capture benchmark — legacy API (NvFBC_Create).
// Pure Go, no CGo.

package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"
	"unsafe"
)

// NvFBC legacy API types
// The legacy NvFBC_Create returns a NvFBCToSys or NvFBCToDx object via COM-like interface.

const (
	NVFBC_SHARED_SURFACE = 0
	NVFBC_TO_SYS         = 1
	NVFBC_TO_DX9         = 2
	NVFBC_TO_DX11        = 3
)

// NVFBC_CREATE_PARAMS
type nvfbcCreateParams struct {
	dwVersion        uint32
	dwInterfaceType  uint32 // NVFBC_TO_SYS etc.
	dwMaxDisplayW    uint32
	dwMaxDisplayH    uint32
	pDevice          uintptr
	pPrimaryOutput   uintptr
	bHWCursor        int32
	dwOutputId       uint32
	dwInterfaceVersion uint32
	ppNvFBC          uintptr // pointer to output interface
	dwAdapterIdx     uint32
	dwPrivateDataSize uint32
	pPrivateData     uintptr
	_pad             [64]byte
}

// NvFBCToSys COM-like interface
type comObj struct {
	vtbl *[64]uintptr
}

func (c *comObj) call(method int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(c.vtbl[method], append([]uintptr{uintptr(unsafe.Pointer(c))}, args...)...)
	return r
}

func (c *comObj) release() {
	if c != nil && c.vtbl != nil {
		syscall.SyscallN(c.vtbl[2], uintptr(unsafe.Pointer(c)))
	}
}

// NVFBC_TOSYS_SETUP_PARAMS — simplified
type toSysSetupParams struct {
	dwVersion    uint32
	eMode        uint32 // 0 = NVFBC_TOSYS_ARGB
	bWithHWCursor int32
	bDiffMap     int32
	ppBuffer     uintptr
	ppDiffMap    uintptr
	dwDiffMapScalingFactor uint32
	_pad         [128]byte
}

// NVFBC_TOSYS_GRAB_FRAME_PARAMS
type toSysGrabParams struct {
	dwVersion    uint32
	dwFlags      uint32 // 0 = blocking, 1 = NOWAITMOUSE, 2 = NOWAIT
	dwTargetWidth  uint32
	dwTargetHeight uint32
	dwStartX     uint32
	dwStartY     uint32
	eGMode       uint32
	pNvFBCFrameGrabInfo uintptr
	dwTimeoutMs  uint32
	_pad         [128]byte
}

var nvfbcDLL = syscall.NewLazyDLL("nvfbc64.dll")

func main() {
	nFrames := flag.Int("frames", 300, "frames to capture")
	flag.Parse()

	fmt.Println("=== NvFBC Capture Benchmark (Legacy API) ===")
	fmt.Println()

	procCreate := nvfbcDLL.NewProc("NvFBC_Create")
	if err := procCreate.Find(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}

	// Create NvFBCToSys interface
	var iface *comObj
	var createParams nvfbcCreateParams
	createParams.dwVersion = 0x00010004 // NVFBC_CREATE_PARAMS_VER
	createParams.dwInterfaceType = NVFBC_TO_SYS
	createParams.ppNvFBC = uintptr(unsafe.Pointer(&iface))
	createParams.dwMaxDisplayW = 3840
	createParams.dwMaxDisplayH = 2160
	createParams.dwOutputId = 0

	r, _, _ := procCreate.Call(uintptr(unsafe.Pointer(&createParams)))
	if r != 0 || iface == nil {
		fmt.Fprintf(os.Stderr, "FATAL: NvFBC_Create failed: %d\n", r)
		if r == 8 {
			fmt.Fprintln(os.Stderr, "  → NVFBC_ERR_UNSUPPORTED: GeForce card without driver patch")
		}
		os.Exit(1)
	}
	defer iface.release()
	fmt.Println("NvFBC ToSys interface created")

	// Setup — NvFBCToSys::SetUp is typically method index 3
	var buffer uintptr
	var setup toSysSetupParams
	setup.dwVersion = 0x00010002
	setup.eMode = 0 // ARGB
	setup.ppBuffer = uintptr(unsafe.Pointer(&buffer))

	r = iface.call(3, uintptr(unsafe.Pointer(&setup)))
	if r != 0 {
		fmt.Fprintf(os.Stderr, "FATAL: NvFBCToSys::SetUp failed: %d\n", r)
		os.Exit(1)
	}
	fmt.Println("NvFBC ToSys setup complete")
	fmt.Printf("Capturing %d frames...\n\n", *nFrames)

	// Warmup
	for i := 0; i < 5; i++ {
		var grab toSysGrabParams
		grab.dwVersion = 0x00010002
		grab.dwFlags = 0 // blocking
		iface.call(4, uintptr(unsafe.Pointer(&grab))) // GrabFrame is method 4
	}

	// Benchmark — blocking
	fmt.Println("[Test 1] Blocking grab")
	blockLat := make([]float64, 0, *nFrames)
	for i := 0; i < *nFrames; i++ {
		var grab toSysGrabParams
		grab.dwVersion = 0x00010002
		grab.dwFlags = 0

		start := time.Now()
		r = iface.call(4, uintptr(unsafe.Pointer(&grab)))
		elapsed := time.Since(start)
		if r != 0 {
			continue
		}
		blockLat = append(blockLat, float64(elapsed.Microseconds())/1000.0)
	}

	if len(blockLat) > 0 {
		sort.Float64s(blockLat)
		n := len(blockLat)
		fmt.Printf("  Frames: %d\n", n)
		fmt.Printf("  P50: %.3f ms\n", blockLat[n*50/100])
		fmt.Printf("  P95: %.3f ms\n", blockLat[n*95/100])
		fmt.Printf("  P99: %.3f ms\n", blockLat[n*99/100])
		fmt.Printf("  Min: %.3f ms  Max: %.3f ms\n", blockLat[0], blockLat[n-1])
	}
	fmt.Println()

	// Benchmark — NOWAIT polling
	fmt.Println("[Test 2] Polling grab (NOWAIT)")
	pollLat := make([]float64, 0, *nFrames)
	polls := 0
	deadline := time.Now().Add(20 * time.Second)
	for len(pollLat) < *nFrames && time.Now().Before(deadline) {
		var grab toSysGrabParams
		grab.dwVersion = 0x00010002
		grab.dwFlags = 2 // NOWAIT

		start := time.Now()
		r = iface.call(4, uintptr(unsafe.Pointer(&grab)))
		elapsed := time.Since(start)
		if r != 0 {
			polls++
			time.Sleep(500 * time.Microsecond)
			continue
		}
		pollLat = append(pollLat, float64(elapsed.Microseconds())/1000.0)
	}

	if len(pollLat) > 0 {
		sort.Float64s(pollLat)
		n := len(pollLat)
		fmt.Printf("  Frames: %d (%d empty polls)\n", n, polls)
		fmt.Printf("  P50: %.3f ms\n", pollLat[n*50/100])
		fmt.Printf("  P95: %.3f ms\n", pollLat[n*95/100])
		fmt.Printf("  P99: %.3f ms\n", pollLat[n*99/100])
		fmt.Printf("  Min: %.3f ms  Max: %.3f ms\n", pollLat[0], pollLat[n-1])
	}
	fmt.Println()

	fmt.Println("--- Summary ---")
	if len(blockLat) > 0 {
		fmt.Printf("Blocking P50: %.3f ms\n", blockLat[len(blockLat)*50/100])
	}
	if len(pollLat) > 0 {
		fmt.Printf("Polling  P50: %.3f ms\n", pollLat[len(pollLat)*50/100])
	}
}
