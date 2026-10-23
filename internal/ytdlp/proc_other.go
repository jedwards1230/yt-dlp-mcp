//go:build !unix

package ytdlp

import (
	"io/fs"
	"os/exec"
)

// setProcessGroup is a no-op where process groups are unavailable; context
// cancellation falls back to killing yt-dlp itself.
func setProcessGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// processExists cannot check other processes here, so it says yes: only this
// process's own leftovers are swept.
func processExists(int) bool { return true }

// ownedByCurrentUser cannot read file ownership here; the temp dir's own
// permissions decide what can be removed.
func ownedByCurrentUser(fs.FileInfo) bool { return true }
