// Creates a Parsec virtual display, then benchmarks DXGI DD capture on it.
// This tests the headless scenario: no physical monitor, virtual display only.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	setupapi          = windows.NewLazySystemDLL("setupapi.dll")
	procSetupDiGetClassDevsA = setupapi.NewProc("SetupDiGetClassDevsA")
	procSetupDiEnumDeviceInterfaces = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailA = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailA")
	procSetupDiDestroyDeviceInfoList = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

const (
	DIGCF_PRESENT         = 0x02
	DIGCF_DEVICEINTERFACE = 0x10

	VDD_IOCTL_ADD     = 0x0022e004
	VDD_IOCTL_REMOVE  = 0x0022a008
	VDD_IOCTL_UPDATE  = 0x0022a00c
	VDD_IOCTL_VERSION = 0x0022e010
)

var VDD_ADAPTER_GUID = windows.GUID{
	Data1: 0x00b41627, Data2: 0x04c4, Data3: 0x429e,
	Data4: [8]byte{0xa2, 0x6e, 0x02, 0x65, 0xcf, 0x50, 0xc3, 0x58},
}

type SP_DEVICE_INTERFACE_DATA struct {
	CbSize             uint32
	InterfaceClassGuid windows.GUID
	Flags              uint32
	Reserved           uintptr
}

func openVDD() (windows.Handle, error) {
	devInfo, _, _ := procSetupDiGetClassDevsA.Call(
		uintptr(unsafe.Pointer(&VDD_ADAPTER_GUID)),
		0, 0,
		DIGCF_PRESENT|DIGCF_DEVICEINTERFACE,
	)
	if devInfo == uintptr(windows.InvalidHandle) {
		return windows.InvalidHandle, fmt.Errorf("SetupDiGetClassDevs failed")
	}
	defer procSetupDiDestroyDeviceInfoList.Call(devInfo)

	var ifData SP_DEVICE_INTERFACE_DATA
	ifData.CbSize = uint32(unsafe.Sizeof(ifData))

	r, _, _ := procSetupDiEnumDeviceInterfaces.Call(
		devInfo, 0, uintptr(unsafe.Pointer(&VDD_ADAPTER_GUID)), 0,
		uintptr(unsafe.Pointer(&ifData)),
	)
	if r == 0 {
		return windows.InvalidHandle, fmt.Errorf("no Parsec VDD device found (driver not installed?)")
	}

	// Get required size
	var reqSize uint32
	procSetupDiGetDeviceInterfaceDetailA.Call(
		devInfo, uintptr(unsafe.Pointer(&ifData)), 0, 0,
		uintptr(unsafe.Pointer(&reqSize)), 0,
	)

	// Allocate detail buffer
	buf := make([]byte, reqSize)
	// cbSize for SP_DEVICE_INTERFACE_DETAIL_DATA_A on 64-bit = 8
	*(*uint32)(unsafe.Pointer(&buf[0])) = 8

	r, _, _ = procSetupDiGetDeviceInterfaceDetailA.Call(
		devInfo, uintptr(unsafe.Pointer(&ifData)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(reqSize),
		0, 0,
	)
	if r == 0 {
		return windows.InvalidHandle, fmt.Errorf("GetDeviceInterfaceDetail failed")
	}

	// Device path starts at offset 4
	devicePath := string(buf[4:])
	// Trim null
	for i, b := range devicePath {
		if b == 0 {
			devicePath = devicePath[:i]
			break
		}
	}

	fmt.Printf("VDD device path: %s\n", devicePath)

	pathPtr, _ := syscall.UTF16PtrFromString(devicePath)
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	return handle, err
}

func vddIoctl(h windows.Handle, code uint32, inBuf []byte) (int, error) {
	var returned uint32
	var inPtr *byte
	var inLen uint32
	if len(inBuf) > 0 {
		inPtr = &inBuf[0]
		inLen = uint32(len(inBuf))
	}
	var outBuf [4]byte
	err := windows.DeviceIoControl(h, code, inPtr, inLen, &outBuf[0], 4, &returned, nil)
	if err != nil {
		return -1, err
	}
	result := int(*(*int32)(unsafe.Pointer(&outBuf[0])))
	return result, nil
}

func main() {
	nFrames := flag.Int("frames", 300, "frames to capture")
	flag.Parse()

	fmt.Println("=== Headless Capture Benchmark: Parsec VDD + DXGI DD ===")
	fmt.Println()

	// Open Parsec VDD device
	h, err := openVDD()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
	defer windows.CloseHandle(h)
	fmt.Println("Parsec VDD device opened")

	// Get driver version
	ver, err := vddIoctl(h, VDD_IOCTL_VERSION, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: VDD_IOCTL_VERSION failed: %v\n", err)
	} else {
		fmt.Printf("Parsec VDD version: 0.%d\n", ver)
	}

	// Create a virtual display
	idx, err := vddIoctl(h, VDD_IOCTL_ADD, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: VDD_IOCTL_ADD failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Virtual display created at index %d\n", idx)

	// Keep-alive: send UPDATE periodically
	stopKeepAlive := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				vddIoctl(h, VDD_IOCTL_UPDATE, nil)
			case <-stopKeepAlive:
				return
			}
		}
	}()

	// Wait for Windows to create the display
	fmt.Println("Waiting 3s for Windows to initialize virtual display...")
	time.Sleep(3 * time.Second)

	// Now run the DXGI DD benchmark against the new virtual display
	// by shelling out to our existing benchmark
	fmt.Printf("\nRunning DXGI DD benchmark (%d frames) on all adapters with outputs...\n\n", *nFrames)

	cmd := exec.Command("./capture_dxgi.exe",
		"--frames", fmt.Sprintf("%d", *nFrames),
		"--csv", "bench_headless_vdd.csv")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	// Also try adapter index that might be the Quadro
	fmt.Println("\n--- Trying all adapter indices ---")
	for i := 0; i < 5; i++ {
		fmt.Printf("\nAdapter %d:\n", i)
		cmd := exec.Command("./capture_dxgi.exe",
			"--frames", "60",
			"--adapter", fmt.Sprintf("%d", i))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	}

	// Cleanup: remove virtual display
	close(stopKeepAlive)
	removeBuf := make([]byte, 4)
	*(*int32)(unsafe.Pointer(&removeBuf[0])) = int32(idx)
	vddIoctl(h, VDD_IOCTL_REMOVE, removeBuf)
	fmt.Println("\nVirtual display removed")
}
