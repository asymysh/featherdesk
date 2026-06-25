// DXGI Desktop Duplication capture benchmark v2.
// Separates vsync wait from actual frame acquisition overhead.
// Pure Go — no CGo.
//
// Usage: go run ./cmd/bench/capture_dxgi [--frames N] [--csv out.csv]

package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GUIDs
var (
	IID_IDXGIFactory1 = windows.GUID{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	IID_IDXGIOutput1  = windows.GUID{0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
)

const (
	D3D11_SDK_VERSION                = 7
	D3D11_CREATE_DEVICE_BGRA_SUPPORT = 0x20
	DXGI_ERROR_WAIT_TIMEOUT          = 0x88890006
)

type comObject struct {
	vtbl *[1024]uintptr
}

func (c *comObject) call(method int, args ...uintptr) (uintptr, error) {
	if c == nil || c.vtbl == nil {
		return 0, fmt.Errorf("nil COM object")
	}
	ret, _, _ := syscall.SyscallN(c.vtbl[method], append([]uintptr{uintptr(unsafe.Pointer(c))}, args...)...)
	if int32(ret) < 0 {
		return ret, fmt.Errorf("COM method %d failed: 0x%08x", method, uint32(ret))
	}
	return ret, nil
}

func (c *comObject) release() {
	if c != nil && c.vtbl != nil {
		syscall.SyscallN(c.vtbl[2], uintptr(unsafe.Pointer(c)))
	}
}

type DXGI_OUTDUPL_FRAME_INFO struct {
	LastPresentTime       int64
	LastMouseUpdateTime   int64
	AccumulatedFrames     uint32
	RectsCoalesced        int32
	ProtectedContentMaskedOut int32
	PointerPosition       [24]byte
}

type DXGI_ADAPTER_DESC1 struct {
	Description           [128]uint16
	VendorId              uint32
	DeviceId              uint32
	SubSysId              uint32
	Revision              uint32
	DedicatedVideoMemory  uint64
	DedicatedSystemMemory uint64
	SharedSystemMemory    uint64
	AdapterLuid           [8]byte
	Flags                 uint32
}

var (
	d3d11 = windows.NewLazySystemDLL("d3d11.dll")
	dxgiDLL = windows.NewLazySystemDLL("dxgi.dll")

	procD3D11CreateDevice  = d3d11.NewProc("D3D11CreateDevice")
	procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
)

func createFactory() *comObject {
	var f *comObject
	r, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&IID_IDXGIFactory1)), uintptr(unsafe.Pointer(&f)))
	if int32(r) < 0 {
		fmt.Fprintf(os.Stderr, "CreateDXGIFactory1 failed: 0x%08x\n", uint32(r))
		os.Exit(1)
	}
	return f
}

func enumAdapter(factory *comObject, idx uint32) *comObject {
	var a *comObject
	r, _ := factory.call(12, uintptr(idx), uintptr(unsafe.Pointer(&a)))
	if int32(r) < 0 {
		return nil
	}
	return a
}

func getDesc(a *comObject) DXGI_ADAPTER_DESC1 {
	var d DXGI_ADAPTER_DESC1
	a.call(10, uintptr(unsafe.Pointer(&d)))
	return d
}

func enumOutput(a *comObject, idx uint32) *comObject {
	var o *comObject
	r, _ := a.call(7, uintptr(idx), uintptr(unsafe.Pointer(&o)))
	if int32(r) < 0 {
		return nil
	}
	return o
}

func qi(obj *comObject, iid *windows.GUID) *comObject {
	var r *comObject
	obj.call(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&r)))
	return r
}

func createDevice(adapter *comObject) *comObject {
	var dev, ctx *comObject
	var fl uint32
	r, _, _ := procD3D11CreateDevice.Call(
		uintptr(unsafe.Pointer(adapter)), 0, 0,
		D3D11_CREATE_DEVICE_BGRA_SUPPORT, 0, 0, D3D11_SDK_VERSION,
		uintptr(unsafe.Pointer(&dev)), uintptr(unsafe.Pointer(&fl)), uintptr(unsafe.Pointer(&ctx)),
	)
	if ctx != nil {
		ctx.release()
	}
	if int32(r) < 0 {
		fmt.Fprintf(os.Stderr, "D3D11CreateDevice failed: 0x%08x\n", uint32(r))
		os.Exit(1)
	}
	return dev
}

func dupOutput(output1, device *comObject) *comObject {
	var d *comObject
	r, _ := output1.call(22, uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(&d)))
	if int32(r) < 0 {
		fmt.Fprintf(os.Stderr, "DuplicateOutput failed: 0x%08x\n", uint32(r))
		os.Exit(1)
	}
	return d
}

// acquireFrame with specified timeout. Returns resource (may be nil on timeout), frameInfo, isTimeout, error
func acquireFrame(dupl *comObject, timeoutMs uint32) (*comObject, *DXGI_OUTDUPL_FRAME_INFO, bool, error) {
	var fi DXGI_OUTDUPL_FRAME_INFO
	var res *comObject
	ret, _, _ := syscall.SyscallN(dupl.vtbl[8],
		uintptr(unsafe.Pointer(dupl)),
		uintptr(timeoutMs),
		uintptr(unsafe.Pointer(&fi)),
		uintptr(unsafe.Pointer(&res)),
	)
	if uint32(ret) == DXGI_ERROR_WAIT_TIMEOUT {
		return nil, nil, true, nil
	}
	if int32(ret) < 0 {
		return nil, nil, false, fmt.Errorf("0x%08x", uint32(ret))
	}
	return res, &fi, false, nil
}

func relFrame(dupl *comObject) {
	syscall.SyscallN(dupl.vtbl[14], uintptr(unsafe.Pointer(dupl)))
}

type frameSample struct {
	TotalMs    float64 // wall time from call to return (includes vsync wait)
	IntervalMs float64 // time since previous successful frame
}

func main() {
	nFrames := flag.Int("frames", 600, "frames to capture")
	csvPath := flag.String("csv", "", "CSV output path")
	adapterIdx := flag.Uint("adapter", 0, "adapter index")
	outputIdx := flag.Uint("output", 0, "output index")
	flag.Parse()

	fmt.Println("=== DXGI Desktop Duplication Benchmark v2 ===")
	fmt.Println()

	factory := createFactory()
	defer factory.release()

	// List all adapters
	fmt.Println("Adapters found:")
	for i := uint32(0); ; i++ {
		a := enumAdapter(factory, i)
		if a == nil {
			break
		}
		d := getDesc(a)
		name := windows.UTF16ToString(d.Description[:])
		hasOutput := enumOutput(a, 0) != nil
		outputStr := ""
		if !hasOutput {
			outputStr = " (no display connected)"
		}
		fmt.Printf("  [%d] %s — %d MB VRAM%s\n", i, name, d.DedicatedVideoMemory/(1024*1024), outputStr)
		a.release()
	}
	fmt.Println()

	adapter := enumAdapter(factory, uint32(*adapterIdx))
	if adapter == nil {
		fmt.Fprintf(os.Stderr, "FATAL: adapter %d not found\n", *adapterIdx)
		os.Exit(1)
	}
	defer adapter.release()

	desc := getDesc(adapter)
	adapterName := windows.UTF16ToString(desc.Description[:])

	output := enumOutput(adapter, uint32(*outputIdx))
	if output == nil {
		fmt.Fprintf(os.Stderr, "FATAL: adapter %d has no output %d (no display connected?)\n", *adapterIdx, *outputIdx)
		os.Exit(1)
	}
	defer output.release()

	output1 := qi(output, &IID_IDXGIOutput1)
	if output1 == nil {
		fmt.Fprintf(os.Stderr, "FATAL: IDXGIOutput1 not available\n")
		os.Exit(1)
	}
	defer output1.release()

	device := createDevice(adapter)
	defer device.release()

	dupl := dupOutput(output1, device)
	defer dupl.release()

	fmt.Printf("Benchmarking: %s, output %d\n", adapterName, *outputIdx)
	fmt.Printf("Frames: %d\n\n", *nFrames)

	// Warmup
	for i := 0; i < 10; i++ {
		res, _, _, _ := acquireFrame(dupl, 100)
		if res != nil {
			res.release()
			relFrame(dupl)
		}
	}

	// === Method 1: Blocking acquire (timeout=1000ms) — measures frame interval ===
	fmt.Println("[Test 1] Blocking AcquireNextFrame (timeout=1000ms)")
	fmt.Println("  Measures: vsync interval + acquire overhead combined")

	samples := make([]frameSample, 0, *nFrames)
	lastFrame := time.Now()
	errs := 0

	for i := 0; i < *nFrames; i++ {
		start := time.Now()
		res, _, isTimeout, err := acquireFrame(dupl, 1000)
		elapsed := time.Since(start)

		if err != nil {
			errs++
			continue
		}
		if isTimeout {
			continue
		}

		interval := start.Sub(lastFrame)
		lastFrame = start

		samples = append(samples, frameSample{
			TotalMs:    float64(elapsed.Microseconds()) / 1000.0,
			IntervalMs: float64(interval.Microseconds()) / 1000.0,
		})

		res.release()
		relFrame(dupl)
	}

	if len(samples) > 0 {
		totals := make([]float64, len(samples))
		for i, s := range samples {
			totals[i] = s.TotalMs
		}
		sort.Float64s(totals)
		n := len(totals)
		fmt.Printf("  Captured:  %d frames, %d errors\n", n, errs)
		fmt.Printf("  Acquire latency (includes vsync wait):\n")
		fmt.Printf("    Min: %.3f ms\n", totals[0])
		fmt.Printf("    P50: %.3f ms\n", totals[n*50/100])
		fmt.Printf("    P95: %.3f ms\n", totals[n*95/100])
		fmt.Printf("    P99: %.3f ms\n", totals[n*99/100])
		fmt.Printf("    Max: %.3f ms\n", totals[n-1])
		fmt.Printf("  Effective FPS: %.1f\n", float64(n)/float64(samples[n-1].IntervalMs/1000.0*float64(n))*float64(n))
	}
	fmt.Println()

	// === Method 2: Polling acquire (timeout=0) — measures raw acquire overhead ===
	fmt.Println("[Test 2] Polling AcquireNextFrame (timeout=0)")
	fmt.Println("  Measures: raw acquire overhead when frame is already available")

	pollLatencies := make([]float64, 0, *nFrames)
	pollTimeouts := 0
	pollTarget := *nFrames

	deadline := time.Now().Add(20 * time.Second) // hard deadline
	for len(pollLatencies) < pollTarget && time.Now().Before(deadline) {
		start := time.Now()
		res, _, isTimeout, err := acquireFrame(dupl, 0)
		elapsed := time.Since(start)

		if err != nil {
			errs++
			time.Sleep(time.Millisecond)
			continue
		}
		if isTimeout {
			pollTimeouts++
			// No frame ready — busy-spin briefly then retry
			time.Sleep(500 * time.Microsecond)
			continue
		}

		pollLatencies = append(pollLatencies, float64(elapsed.Microseconds())/1000.0)
		res.release()
		relFrame(dupl)
	}

	if len(pollLatencies) > 0 {
		sort.Float64s(pollLatencies)
		n := len(pollLatencies)
		fmt.Printf("  Captured:  %d frames (%d poll timeouts — no frame ready)\n", n, pollTimeouts)
		fmt.Printf("  Raw acquire latency (frame already available):\n")
		fmt.Printf("    Min: %.3f ms\n", pollLatencies[0])
		fmt.Printf("    P50: %.3f ms\n", pollLatencies[n*50/100])
		fmt.Printf("    P95: %.3f ms\n", pollLatencies[n*95/100])
		fmt.Printf("    P99: %.3f ms\n", pollLatencies[n*99/100])
		fmt.Printf("    Max: %.3f ms\n", pollLatencies[n-1])
	}
	fmt.Println()

	// CSV
	if *csvPath != "" {
		f, err := os.Create(*csvPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "CSV error: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		w := csv.NewWriter(f)
		w.Write([]string{"test", "frame", "latency_ms"})
		for i, s := range samples {
			w.Write([]string{"blocking", strconv.Itoa(i), fmt.Sprintf("%.3f", s.TotalMs)})
		}
		for i, l := range pollLatencies {
			w.Write([]string{"polling", strconv.Itoa(i), fmt.Sprintf("%.3f", l)})
		}
		w.Flush()
		fmt.Printf("CSV written: %s\n", *csvPath)
	}

	fmt.Println("--- Summary ---")
	fmt.Printf("Adapter: %s\n", adapterName)
	if len(samples) > 0 {
		totals := make([]float64, len(samples))
		for i, s := range samples {
			totals[i] = s.TotalMs
		}
		sort.Float64s(totals)
		n := len(totals)
		fmt.Printf("Blocking P50: %.3f ms (vsync + acquire)\n", totals[n*50/100])
	}
	if len(pollLatencies) > 0 {
		sort.Float64s(pollLatencies)
		n := len(pollLatencies)
		fmt.Printf("Polling  P50: %.3f ms (raw acquire, no wait)\n", pollLatencies[n*50/100])
	}
}
