//go:build unix

package ytdlp

import (
	"errors"
	"io/fs"
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

// processExists reports whether pid names a process, including one owned by
// another user (EPERM) or a zombie; when unsure it says yes.
func processExists(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// ownedByCurrentUser reports whether info belongs to this process's user.
func ownedByCurrentUser(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
