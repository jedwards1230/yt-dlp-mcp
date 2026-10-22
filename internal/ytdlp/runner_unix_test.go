//go:build unix

package ytdlp

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTimeoutKillsProcessGroup runs a fake that spawns a long-lived child
// (standing in for the JavaScript runtime) and checks that the timeout kills
// that child too, not just yt-dlp.
func TestTimeoutKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	r := newRunner(t, func(c *Config) { c.Timeout = 700 * time.Millisecond })
	r.extraEnv = []string{"FAKE_YTDLP_PID_FILE=" + pidFile}

	start := time.Now()
	_, _, err := r.Info(t.Context(), "https://www.youtube.com/watch?v=fakeOrphan0")
	e := wantKind(t, err, KindTimeout)
	if e.Msg != "yt-dlp timed out after 700ms (YTDLP_MCP_TIMEOUT); the site may be slow or blocking requests — retry later" {
		t.Fatalf("message = %q", e.Msg)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("child pid not recorded: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child process %d survived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
