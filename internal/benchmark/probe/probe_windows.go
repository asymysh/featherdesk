//go:build windows

package probe

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	psapi    = windows.NewLazySystemDLL("psapi.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")

	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemMetrics     = user32.NewProc("GetSystemMetrics")
)

// memoryStatusEx mirrors MEMORYSTATUSEX from WinAPI.
type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// SystemInfo contains everything the benchmark tool needs to know about the
// host machine on Windows.
type SystemInfo struct {
	Platform       string
	OSName         string
	OSVersion      string
	OSArch         string
	CPUModel       string
	CPUCores       int
	CPUThreads     int
	CPUFreqMHz     int
	RAMTotalMB     int64
	RAMAvailableMB int64
	DisplayW       int
	DisplayH       int
	DisplayHz      int
	DisplayAdapter string
	GPUs           []GPUInfo
	AdditionalArgs string // JSON with any extra fields
}

// GPUInfo describes one display adapter found via WMIC.
type GPUInfo struct {
	DeviceIndex        int
	DeviceName         string
	DeviceType         string
	Vendor             string
	VRAMMb             *int64
	DriverVersion      string
	DXGIAdapterLUID    string
	D3DFeatureLevel    string
	DXGIDedicatedVRAMMb *int64
	DXGISharedMemMb    *int64
	// Encode capabilities — populated later by encode probing
	EncH264 *int
	EncH265 *int
	EncVP8  *int
	EncVP9  *int
	EncAV1  *int
	AdditionalArgs string // JSON
}

const (
	smCxScreen    = 0
	smCyScreen    = 1
	smCxFullscreen = 16
	smCyFullscreen = 17
)

// Gather probes the host and returns a populated SystemInfo.
func Gather() (*SystemInfo, error) {
	info := &SystemInfo{
		Platform: "windows",
		OSArch:   runtime.GOARCH,
	}

	// ── OS version ────────────────────────────────────────────────────────
	info.OSName, info.OSVersion = osVersion()

	// ── CPU ───────────────────────────────────────────────────────────────
	info.CPUModel = cpuModel()
	info.CPUCores = runtime.NumCPU() / 2 // approximation if HT is on
	info.CPUThreads = runtime.NumCPU()
	info.CPUFreqMHz = cpuFreqMHz()

	// ── Memory ────────────────────────────────────────────────────────────
	var ms memoryStatusEx
	ms.dwLength = uint32(unsafe.Sizeof(ms))
	ret, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if ret != 0 {
		info.RAMTotalMB = int64(ms.ullTotalPhys / 1024 / 1024)
		info.RAMAvailableMB = int64(ms.ullAvailPhys / 1024 / 1024)
	}

	// ── Display ───────────────────────────────────────────────────────────
	w, _, _ := procGetSystemMetrics.Call(smCxScreen)
	h, _, _ := procGetSystemMetrics.Call(smCyScreen)
	info.DisplayW = int(w)
	info.DisplayH = int(h)
	info.DisplayHz, info.DisplayAdapter = displayRefreshAndAdapter()

	// ── GPUs ──────────────────────────────────────────────────────────────
	info.GPUs = gatherGPUs()

	return info, nil
}

func osVersion() (name, version string) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`(Get-CimInstance Win32_OperatingSystem | Select-Object Caption,Version | ConvertTo-Json)`).Output()
	if err != nil {
		return "Windows", ""
	}
	var v struct {
		Caption string
		Version string
	}
	if err := json.Unmarshal(out, &v); err == nil {
		return strings.TrimSpace(v.Caption), strings.TrimSpace(v.Version)
	}
	return "Windows", ""
}

func cpuModel() string {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`(Get-CimInstance Win32_Processor).Name`).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func cpuFreqMHz() int {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`(Get-CimInstance Win32_Processor).MaxClockSpeed`).Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return v
}

func displayRefreshAndAdapter() (hz int, adapter string) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`Get-CimInstance Win32_VideoController | Select-Object Name,CurrentRefreshRate | ConvertTo-Json`).Output()
	if err != nil {
		return 0, ""
	}
	// Could be an array or a single object
	raw := strings.TrimSpace(string(out))

	// Try array first
	var arr []struct {
		Name               string
		CurrentRefreshRate int
	}
	if err := json.Unmarshal([]byte(raw), &arr); err == nil && len(arr) > 0 {
		return arr[0].CurrentRefreshRate, arr[0].Name
	}
	// Try single object
	var single struct {
		Name               string
		CurrentRefreshRate int
	}
	if err := json.Unmarshal([]byte(raw), &single); err == nil {
		return single.CurrentRefreshRate, single.Name
	}
	return 0, ""
}

func gatherGPUs() []GPUInfo {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`Get-CimInstance Win32_VideoController | Select-Object Name,DriverVersion,AdapterRAM,VideoProcessor | ConvertTo-Json`).Output()
	if err != nil {
		return nil
	}
	raw := strings.TrimSpace(string(out))

	type wmicGPU struct {
		Name           string
		DriverVersion  string
		AdapterRAM     *int64
		VideoProcessor string
	}

	var gpus []wmicGPU
	// Try array
	if err := json.Unmarshal([]byte(raw), &gpus); err != nil {
		// Try single
		var g wmicGPU
		if err2 := json.Unmarshal([]byte(raw), &g); err2 == nil {
			gpus = []wmicGPU{g}
		}
	}

	result := make([]GPUInfo, 0, len(gpus))
	for i, g := range gpus {
		info := GPUInfo{
			DeviceIndex:   i,
			DeviceName:    g.Name,
			DriverVersion: g.DriverVersion,
			Vendor:        inferVendor(g.Name),
			DeviceType:    inferDeviceType(g.Name),
		}
		if g.AdapterRAM != nil && *g.AdapterRAM > 0 {
			vram := *g.AdapterRAM / 1024 / 1024
			info.VRAMMb = &vram
		}

		// Extra metadata as JSON
		extra := fmt.Sprintf(`{"video_processor":%q}`, g.VideoProcessor)
		info.AdditionalArgs = extra

		result = append(result, info)
	}
	return result
}

func inferVendor(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "nvidia") || strings.Contains(n, "geforce") || strings.Contains(n, "quadro") || strings.Contains(n, "rtx") || strings.Contains(n, "gtx"):
		return "nvidia"
	case strings.Contains(n, "amd") || strings.Contains(n, "radeon") || strings.Contains(n, "rx "):
		return "amd"
	case strings.Contains(n, "intel") || strings.Contains(n, "uhd") || strings.Contains(n, "iris") || strings.Contains(n, "arc"):
		return "intel"
	case strings.Contains(n, "parsec") || strings.Contains(n, "virtual") || strings.Contains(n, "microsoft basic"):
		return "microsoft"
	default:
		return "unknown"
	}
}

func inferDeviceType(name string) string {
	n := strings.ToLower(name)
	if strings.Contains(n, "virtual") || strings.Contains(n, "parsec") || strings.Contains(n, "remote") {
		return "virtual"
	}
	if strings.Contains(n, "intel") && (strings.Contains(n, "uhd") || strings.Contains(n, "hd") || strings.Contains(n, "iris")) {
		return "integrated"
	}
	if strings.Contains(n, "microsoft basic") || strings.Contains(n, "display adapter (microsoft)") {
		return "software"
	}
	return "discrete"
}

// ProbeFFmpegEncoders shells out to ffmpeg and returns the list of available
// encoder names. Returns empty slice if ffmpeg is not in PATH.
func ProbeFFmpegEncoders() []string {
	out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	if err != nil {
		return nil
	}
	var encoders []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 8 {
			continue
		}
		// Lines look like " V..... h264_nvenc         ..."
		if line[0] != ' ' || len(line) < 8 {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && strings.HasPrefix(parts[0], "V") {
			encoders = append(encoders, parts[1])
		}
	}
	return encoders
}

// HasEncoder returns true if ffmpeg reports the named encoder is available.
func HasEncoder(name string) bool {
	for _, e := range ProbeFFmpegEncoders() {
		if e == name {
			return true
		}
	}
	return false
}

// intPtr is a helper to create a *int from an int literal.
func intPtr(v int) *int { return &v }

// EncodeCapabilities probes a GPUInfo to fill in enc_* fields based on known
// ffmpeg encoders. Must be called after ProbeFFmpegEncoders.
func EncodeCapabilities(gpu *GPUInfo, ffmpegEncoders []string) {
	has := func(name string) bool {
		for _, e := range ffmpegEncoders {
			if e == name {
				return true
			}
		}
		return false
	}
	boolPtr := func(b bool) *int {
		v := 0
		if b {
			v = 1
		}
		return &v
	}

	switch gpu.Vendor {
	case "nvidia":
		gpu.EncH264 = boolPtr(has("h264_nvenc"))
		gpu.EncH265 = boolPtr(has("hevc_nvenc"))
		gpu.EncAV1 = boolPtr(has("av1_nvenc"))
	case "amd":
		gpu.EncH264 = boolPtr(has("h264_amf"))
		gpu.EncH265 = boolPtr(has("hevc_amf"))
		gpu.EncAV1 = boolPtr(has("av1_amf"))
	case "intel":
		gpu.EncH264 = boolPtr(has("h264_qsv"))
		gpu.EncH265 = boolPtr(has("hevc_qsv"))
		gpu.EncVP9 = boolPtr(has("vp9_qsv"))
		gpu.EncAV1 = boolPtr(has("av1_qsv"))
	default:
		// virtual / software / unknown — no hardware encoders
		zero := 0
		gpu.EncH264 = &zero
		gpu.EncH265 = &zero
		gpu.EncVP8 = &zero
		gpu.EncVP9 = &zero
		gpu.EncAV1 = &zero
	}
}

// dummyUse silences "unused import" for syscall if nothing else uses it.
var _ = syscall.SIGINT
