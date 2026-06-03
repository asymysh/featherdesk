package encode

import (
	"os"
	"os/exec"
	"strings"
)

func ProbeVAAPI() bool {
	render := "/dev/dri/renderD128"
	if _, err := os.Stat(render); err != nil {
		return false
	}

	out, err := exec.Command("ffmpeg", "-hide_banner", "-vaapi_device", render,
		"-encoders").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "h264_vaapi")
}
