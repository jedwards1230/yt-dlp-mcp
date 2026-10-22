//go:build !unix

package ytdlp

import "os/exec"

// setProcessGroup is a no-op where process groups are unavailable; context
// cancellation falls back to killing yt-dlp itself.
func setProcessGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
