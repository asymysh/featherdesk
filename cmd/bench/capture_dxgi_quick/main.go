package main

import (
    "fmt"
    "os"
    "sort"
    "syscall"
    "time"
    "unsafe"
    "golang.org/x/sys/windows"
)

var (
    IID_IDXGIFactory1 = windows.GUID{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
    IID_IDXGIOutput1  = windows.GUID{0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
    d3d11dll = windows.NewLazySystemDLL("d3d11.dll")
    dxgiDLL  = windows.NewLazySystemDLL("dxgi.dll")
    user32   = windows.NewLazySystemDLL("user32.dll")
    procD3D11CreateDevice  = d3d11dll.NewProc("D3D11CreateDevice")
    procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
    procSetCursorPos       = user32.NewProc("SetCursorPos")
)

type com struct{ vtbl *[1024]uintptr }
func (c *com) call(m int, a ...uintptr) uintptr {
    r, _, _ := syscall.SyscallN(c.vtbl[m], append([]uintptr{uintptr(unsafe.Pointer(c))}, a...)...)
    return r
}
func (c *com) release() { if c != nil { syscall.SyscallN(c.vtbl[2], uintptr(unsafe.Pointer(c))) } }

type fi struct{ _pad [48]byte }

func main() {
    adapterIdx := 1 // RX 6800 XT
    if len(os.Args) > 1 && os.Args[1] == "0" { adapterIdx = 0 }

    var factory *com
    procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&IID_IDXGIFactory1)), uintptr(unsafe.Pointer(&factory)))
    var adapter *com
    factory.call(12, uintptr(adapterIdx), uintptr(unsafe.Pointer(&adapter)))
    var output *com
    if r := adapter.call(7, 0, uintptr(unsafe.Pointer(&output))); int32(r) < 0 {
        fmt.Println("No output"); os.Exit(1)
    }
    var output1 *com
    output.call(0, uintptr(unsafe.Pointer(&IID_IDXGIOutput1)), uintptr(unsafe.Pointer(&output1)))
    var dev, ctx *com; var fl uint32
    procD3D11CreateDevice.Call(uintptr(unsafe.Pointer(adapter)),0,0,0x20,0,0,7,
        uintptr(unsafe.Pointer(&dev)),uintptr(unsafe.Pointer(&fl)),uintptr(unsafe.Pointer(&ctx)))
    if ctx != nil { ctx.release() }
    var dupl *com
    if r := output1.call(22, uintptr(unsafe.Pointer(dev)), uintptr(unsafe.Pointer(&dupl))); int32(r)<0 {
        fmt.Printf("DuplicateOutput failed: 0x%08x\n", uint32(r)); os.Exit(1)
    }
    fmt.Printf("Adapter %d ready. Capturing with timeout=16ms, forcing cursor movement...\n", adapterIdx)

    latencies := make([]float64, 0, 300)
    timeouts := 0
    for i := 0; i < 300; i++ {
        // Force desktop update by jiggling cursor
        procSetCursorPos.Call(uintptr(100+(i%200)), uintptr(100+(i%150)))
        time.Sleep(time.Millisecond)

        var info fi; var res *com
        start := time.Now()
        r, _, _ := syscall.SyscallN(dupl.vtbl[8], uintptr(unsafe.Pointer(dupl)), 16,
            uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&res)))
        elapsed := time.Since(start)
        if uint32(r) == 0x88890006 { timeouts++; continue }
        if int32(r) < 0 { continue }
        latencies = append(latencies, float64(elapsed.Microseconds())/1000.0)
        if res != nil { res.release() }
        syscall.SyscallN(dupl.vtbl[14], uintptr(unsafe.Pointer(dupl)))
    }

    if len(latencies) == 0 { fmt.Println("No frames captured"); os.Exit(1) }
    sort.Float64s(latencies)
    n := len(latencies)
    fmt.Printf("Frames: %d captured, %d timeouts\n", n, timeouts)
    fmt.Printf("P50: %.3f ms\n", latencies[n*50/100])
    fmt.Printf("P95: %.3f ms\n", latencies[n*95/100])
    fmt.Printf("P99: %.3f ms\n", latencies[n*99/100])
    fmt.Printf("Min: %.3f ms  Max: %.3f ms\n", latencies[0], latencies[n-1])
}
