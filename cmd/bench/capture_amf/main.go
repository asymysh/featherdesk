// AMF Display Capture benchmark. Pure Go — no CGo.
// Loads amfrt64.dll via syscall and calls AMF COM-style interfaces.
//
// Usage: go run ./cmd/bench/capture_amf [--frames N]

package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AMF result codes
const (
	AMF_OK                = 0
	AMF_REPEAT            = 1
	AMF_EOF               = 2
	AMF_FAIL              = 1 << 31
	AMF_NOT_FOUND         = AMF_FAIL | 1
	AMF_NO_DEVICE         = AMF_FAIL | 7
	AMF_NOT_INITIALIZED   = AMF_FAIL | 4
)

// AMF memory types
const (
	AMF_MEMORY_UNKNOWN = 0
	AMF_MEMORY_HOST    = 1
	AMF_MEMORY_DX9     = 2
	AMF_MEMORY_DX11    = 3
)

// AMF surface formats
const (
	AMF_SURFACE_BGRA = 11
	AMF_SURFACE_RGBA = 12
)

// AMF versions
const AMF_FULL_VERSION = uint64(1)<<48 | uint64(4)<<32 | uint64(34)<<16 | uint64(0)

var (
	amfDLL = windows.NewLazySystemDLL("amfrt64.dll")
)

// COM vtable helper
type comObj struct {
	vtbl *[256]uintptr
}

func (c *comObj) call(method int, args ...uintptr) uintptr {
	if c == nil || c.vtbl == nil {
		return 0xFFFFFFFF
	}
	r, _, _ := syscall.SyscallN(c.vtbl[method], append([]uintptr{uintptr(unsafe.Pointer(c))}, args...)...)
	return r
}

func (c *comObj) release() {
	if c != nil && c.vtbl != nil {
		syscall.SyscallN(c.vtbl[2], uintptr(unsafe.Pointer(c))) // Release
	}
}

func main() {
	nFrames := flag.Int("frames", 300, "frames to capture")
	flag.Parse()

	fmt.Println("=== AMF Display Capture Benchmark ===")
	fmt.Println()

	// Load AMF
	procInit := amfDLL.NewProc("AMFInit")
	if err := procInit.Find(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: amfrt64.dll not found: %v\n", err)
		os.Exit(1)
	}

	// AMFInit(version, **factory)
	var factory *comObj
	r, _, _ := procInit.Call(
		uintptr(AMF_FULL_VERSION),
		uintptr(unsafe.Pointer(&factory)),
	)
	if int64(r) < 0 {
		fmt.Fprintf(os.Stderr, "FATAL: AMFInit failed: 0x%08x\n", uint32(r))
		os.Exit(1)
	}
	fmt.Println("AMF Factory created")

	// AMFFactory::CreateContext — method index 3
	var context *comObj
	r = factory.call(3, uintptr(unsafe.Pointer(&context)))
	if int64(r) < 0 {
		fmt.Fprintf(os.Stderr, "FATAL: CreateContext failed: 0x%08x\n", uint32(r))
		os.Exit(1)
	}
	defer context.release()

	// AMFContext::InitDX11 — method index 5 (with NULL = auto-create device)
	r = context.call(5, 0, 0)
	if int64(r) < 0 {
		fmt.Fprintf(os.Stderr, "FATAL: InitDX11 failed: 0x%08x (no AMD GPU?)\n", uint32(r))
		os.Exit(1)
	}
	fmt.Println("AMF DX11 context initialized")

	// AMFFactory::CreateComponent — method index 4
	// Component ID for display capture: L"AMFDisplayCapture"
	captureID, _ := syscall.UTF16PtrFromString("AMFDisplayCapture")
	var capture *comObj
	r = factory.call(4, uintptr(unsafe.Pointer(context)), uintptr(unsafe.Pointer(captureID)), uintptr(unsafe.Pointer(&capture)))
	if int64(r) < 0 {
		fmt.Fprintf(os.Stderr, "FATAL: CreateComponent(AMFDisplayCapture) failed: 0x%08x\n", uint32(r))
		fmt.Fprintln(os.Stderr, "  This may mean DisplayCapture is not available on this driver version.")
		os.Exit(1)
	}
	defer capture.release()
	fmt.Println("AMF DisplayCapture component created")

	// AMFComponent::Init — method index 3 (surface format, width, height)
	r = capture.call(3, AMF_SURFACE_BGRA, 1920, 1080)
	if int64(r) < 0 {
		fmt.Fprintf(os.Stderr, "FATAL: DisplayCapture Init failed: 0x%08x\n", uint32(r))
		os.Exit(1)
	}
	fmt.Println("AMF DisplayCapture initialized (1920x1080 BGRA)")
	fmt.Printf("Capturing %d frames...\n\n", *nFrames)

	// Warmup
	for i := 0; i < 5; i++ {
		var data *comObj
		r = capture.call(9, uintptr(unsafe.Pointer(&data))) // QueryOutput
		if r == AMF_OK && data != nil {
			data.release()
		}
		time.Sleep(16 * time.Millisecond)
	}

	// Benchmark — QueryOutput polling
	fmt.Println("[Test] QueryOutput polling")
	latencies := make([]float64, 0, *nFrames)
	repeats := 0
	errors := 0
	deadline := time.Now().Add(30 * time.Second)

	for len(latencies) < *nFrames && time.Now().Before(deadline) {
		start := time.Now()
		var data *comObj
		r = capture.call(9, uintptr(unsafe.Pointer(&data))) // QueryOutput is typically method 9
		elapsed := time.Since(start)

		if r == AMF_OK && data != nil {
			latencies = append(latencies, float64(elapsed.Microseconds())/1000.0)
			data.release()
		} else if r == AMF_REPEAT {
			repeats++
			time.Sleep(500 * time.Microsecond)
		} else {
			errors++
			time.Sleep(time.Millisecond)
		}
	}

	if len(latencies) == 0 {
		fmt.Fprintf(os.Stderr, "No frames captured (%d repeats, %d errors)\n", repeats, errors)
		os.Exit(1)
	}

	sort.Float64s(latencies)
	n := len(latencies)
	fmt.Printf("  Frames: %d (%d AMF_REPEAT polls, %d errors)\n", n, repeats, errors)
	fmt.Printf("  P50: %.3f ms\n", latencies[n*50/100])
	fmt.Printf("  P95: %.3f ms\n", latencies[n*95/100])
	fmt.Printf("  P99: %.3f ms\n", latencies[n*99/100])
	fmt.Printf("  Min: %.3f ms  Max: %.3f ms\n", latencies[0], latencies[n-1])
	fmt.Println()

	fmt.Println("--- Summary ---")
	fmt.Printf("AMF Capture P50: %.3f ms\n", latencies[n*50/100])
}
