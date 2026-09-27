package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A second acquire must fail while this test holds the lock, and the failure
// must name us - the pid is what tells a confused user which window to close.
func TestInstanceLockIsExclusive(t *testing.T) {
	releaseInstanceLock()
	defer releaseInstanceLock()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // isolate from a live chat-app

	first, err := acquireInstanceLock()
	if err != nil || first == nil {
		t.Fatalf("first acquire: lock=%v err=%v", first, err)
	}
	_, err = acquireInstanceLock()
	if err == nil {
		t.Fatal("second acquire succeeded while the lock was held")
	}
	if got := err.Error(); !strings.Contains(got, strconv.Itoa(os.Getpid())) {
		t.Errorf("error does not name the holder's pid %d: %s", os.Getpid(), got)
	}
}

// Releasing must hand the lock over, or a normal quit would block every
// relaunch for the rest of the session.
func TestInstanceLockReleasedOnRelease(t *testing.T) {
	releaseInstanceLock()
	defer releaseInstanceLock()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	if _, err := acquireInstanceLock(); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	releaseInstanceLock()
	if _, err := acquireInstanceLock(); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

// The point of flock over a PID file: a crashed holder must not lock the app
// out for the rest of the session. The holder here is a real child process
// that dies with SIGKILL - the case a PID file handles worst - and the lock
// must come back with no cleanup step.
func TestInstanceLockSurvivesHolderDeath(t *testing.T) {
	releaseInstanceLock()
	defer releaseInstanceLock()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	// Hold the lock from a child process, the way a second chat-app would.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	// CHAT_APP_TEST_LOCK_HOLDER makes the child acquire the lock and then block. The
	// parent sets the env AFTER starting it, so a normal suite run is
	// unaffected.
	cmd := exec.Command(self, "-test.run=TestLockHolderProcess", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "CHAT_APP_TEST_LOCK_HOLDER=1", "XDG_RUNTIME_DIR="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	// Wait for the child to record its pid, so the kill is not a race with
	// the write and the final acquire is not a race with the child's exit.
	pid := waitForLockFile(t, dir)
	cmd.Process.Kill()
	cmd.Wait()

	// The child is gone; the lock file still exists but must not be held.
	// Re-acquiring in a fresh process is what proves the kernel released it.
	verifyLockIsFree(t, dir)
	t.Logf("lock taken over after pid %s was killed", pid)
}

// waitForLockFile blocks until the holder has written its pid.
func waitForLockFile(t *testing.T, dir string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(filepath.Join(dir, "chat-app.lock"))
		if len(b) > 0 {
			return string(b)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("lock holder never wrote its pid")
	return ""
}

// verifyLockIsFree re-runs a helper process that must succeed in taking the
// lock. A same-process retry would be inconclusive: this process never held
// the lock, so it proves nothing about the dead one.
func verifyLockIsFree(t *testing.T, dir string) {
	t.Helper()
	self, _ := os.Executable()
	cmd := exec.Command(self, "-test.run=TestLockHolderProcess", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "CHAT_APP_TEST_LOCK_PROBE=1", "XDG_RUNTIME_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lock was still held after the holder died: %v\n%s", err, out)
	}
}

// The child half of the two tests above: it either holds the lock until it is
// killed, or tries to take it and reports the outcome on stdout.
func TestLockHolderProcess(t *testing.T) {
	switch {
	case os.Getenv("CHAT_APP_TEST_LOCK_HOLDER") == "1":
		if _, err := acquireInstanceLock(); err != nil {
			t.Fatalf("child could not take the lock: %v", err)
		}
		select {} // hold it until the parent kills us
	case os.Getenv("CHAT_APP_TEST_LOCK_PROBE") == "1":
		if _, err := acquireInstanceLock(); err != nil {
			t.Fatalf("lock still held: %v", err)
		}
	default:
		t.Skip("not running as the lock stand-in")
	}
}

// An empty or garbage lock file must not be reported as a live pid.
func TestReadLockHolderIgnoresGarbage(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/chat-app.lock"
	for _, content := range []string{"", "   ", "not-a-pid"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := readLockHolder(f); got != "" {
			t.Errorf("content %q: got %q, want \"\"", content, got)
		}
		f.Close()
	}
}

// The lock lives in the per-user runtime dir, never a shared path, so two
// users on one machine do not lock each other out.
func TestLockPathIsPerUser(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got, want := lockPath(), "/run/user/1000/chat-app.lock"; got != want {
		t.Errorf("lockPath() = %q, want %q", got, want)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := lockPath(); got == "" || !strings.Contains(got, "chat-app") {
		t.Errorf("fallback lockPath() = %q, want a chat-app path", got)
	}
}
