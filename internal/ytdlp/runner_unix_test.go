//go:build unix

package ytdlp

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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
	requireGone(t, readPid(t, pidFile), 3*time.Second)
}

// TestRunKillsLeftoverChildren covers the exits a timeout never sees:
// yt-dlp ends on its own, successfully or not, but leaves a child running,
// either detached from its stdio (so Wait returns at once) or still holding
// the output pipes (so Wait gives up after waitDelay). The child must be
// gone when the call returns and the scratch directory removed, so nothing
// keeps running or writing after the call. The short grace only covers the
// kernel tearing down a SIGKILLed process; without the kill the child lives
// for 30s.
func TestRunKillsLeftoverChildren(t *testing.T) {
	tests := []struct {
		id      string
		wantErr string // "" = success
	}{
		{"fakeChildOk", ""},
		{"fakeChildEr", "yt-dlp failed: [youtube] fakeChildEr: Requested format is not available"},
		{"fakeChildPp", "yt-dlp failed: [youtube] fakeChildPp: Requested format is not available"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			r := newRunner(t)
			r.extraEnv = []string{"FAKE_YTDLP_PID_FILE=" + pidFile}

			_, _, err := r.Info(t.Context(), "https://www.youtube.com/watch?v="+tt.id)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Info: %v", err)
			case tt.wantErr != "":
				if e := wantKind(t, err, KindFailed); e.Msg != tt.wantErr {
					t.Fatalf("message = %q, want %q", e.Msg, tt.wantErr)
				}
			}
			requireGone(t, readPid(t, pidFile), 500*time.Millisecond)
			if left, err := os.ReadDir(r.cfg.TempDir); err != nil || len(left) != 0 {
				t.Fatalf("scratch directories left behind: %v (err %v)", left, err)
			}
		})
	}
}

// TestRemoveStaleScratch sweeps a temp dir holding leftovers of this
// process and of a dead one next to a live server's directory, legacy and
// unrelated names, a plain file and a symbolic link: only the two stale
// directories go, and the link's target is untouched.
func TestRemoveStaleScratch(t *testing.T) {
	tmp := t.TempDir()
	r := newRunner(t, func(c *Config) { c.TempDir = tmp })

	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	mkdir := func(name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(tmp, name, ".cache"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, name, "transcript.en.vtt"), []byte("WEBVTT\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stale := []string{
		fmt.Sprintf("yt-dlp-mcp-%d-111", self),             // this process, earlier run
		fmt.Sprintf("yt-dlp-mcp-%d-222", dead.Process.Pid), // a server that is gone
	}
	kept := []string{
		fmt.Sprintf("yt-dlp-mcp-%d-333", os.Getppid()), // a live server
		"yt-dlp-mcp-12345", // no pid tag
		"yt-dlp-mcp-x-444", // not a pid
		"other-tool-555",
	}
	for _, name := range append(slices.Clone(stale), kept...) {
		mkdir(name)
	}
	file := fmt.Sprintf("yt-dlp-mcp-%d-file", self)
	if err := os.WriteFile(filepath.Join(tmp, file), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := fmt.Sprintf("yt-dlp-mcp-%d-link", self)
	if err := os.Symlink(target, filepath.Join(tmp, link)); err != nil {
		t.Fatal(err)
	}

	n, err := r.RemoveStaleScratch()
	if err != nil || n != len(stale) {
		t.Fatalf("RemoveStaleScratch = %d, %v; want %d, nil", n, err, len(stale))
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := append(slices.Clone(kept), file, link)
	slices.Sort(left)
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Fatalf("left %q, want %q", left, want)
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Fatalf("symlink target was touched: %v", err)
	}

	// A call's own scratch directory carries this process's pid.
	envFile := filepath.Join(t.TempDir(), "env")
	r.extraEnv = []string{"FAKE_YTDLP_ENV_FILE=" + envFile}
	if _, _, err := r.Info(t.Context(), manualURL); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		if home, ok := strings.CutPrefix(l, "HOME="); ok {
			if pid, ok := scratchOwner(filepath.Base(home)); !ok || pid != self || filepath.Dir(home) != tmp {
				t.Fatalf("scratch dir %q is not %s/yt-dlp-mcp-%d-*", home, tmp, self)
			}
			return
		}
	}
	t.Fatal("child environment has no HOME")
}

// readPid reads the child pid the fake recorded.
func readPid(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("child pid not recorded: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// requireGone fails the test unless pid stops running within grace. A
// process that outlives it is killed so it cannot leak past the test.
func requireGone(t *testing.T, pid int, grace time.Duration) {
	t.Helper()
	deadline := time.Now().Add(grace)
	for !processGone(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child process %d is still running %s after the call returned", pid, grace)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// processGone reports whether pid has stopped running. A zombie (exited but
// not yet reaped by whichever process inherited it) counts as gone, although
// kill(pid, 0) still succeeds on one; on Linux the state letter in
// /proc/<pid>/stat tells the two apart. Other systems fall back to kill alone.
func processGone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	// "pid (comm) S ...": the state follows the last ')', since comm may
	// itself contain spaces and parentheses.
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 >= len(data) {
		return false
	}
	switch data[i+2] {
	case 'Z', 'X', 'x': // zombie, dead
		return true
	}
	return false
}

// TestProcessGone pins the zombie handling the process-group tests rely on:
// an exited child its parent has not reaped yet is a zombie, which kill(pid,
// 0) still reports as present.
func TestProcessGone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("zombie detection reads /proc")
	}
	var attr syscall.SysProcAttr
	pid, err := syscall.ForkExec("/bin/sh", []string{"sh", "-c", "exit 0"}, &syscall.ProcAttr{Sys: &attr})
	if err != nil {
		t.Fatal(err)
	}
	// Not reaped yet: wait for it to become a zombie.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("unreaped child %d: kill(pid, 0) = %v, want success on a zombie", pid, err)
		}
		if processGone(pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("exited child %d never reported gone", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil {
		t.Fatal(err)
	}
	if !processGone(pid) {
		t.Fatalf("reaped child %d reported running", pid)
	}
	if processGone(os.Getpid()) {
		t.Fatal("the test process itself reported gone")
	}
}
