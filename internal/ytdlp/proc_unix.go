//go:build unix

package ytdlp

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup starts cmd in its own process group and makes context
// cancellation SIGKILL the whole group, so a JavaScript runtime spawned by
// yt-dlp dies with it instead of outliving the call.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
}

// killGroup sends SIGKILL to cmd's process group. The group id equals the
// leader's pid because of Setpgid.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
