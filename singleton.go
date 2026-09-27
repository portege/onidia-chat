// singleton.go - one chat-app per user session.
//
// Two instances fight over the same resources: the onidia say-FIFO (one
// writer, and a second instance would fight the first over the pet), the
// settings in chat-app.ini, and the user's attention - the second window
// would just sit behind the first, accepting keystrokes nobody can see.
//
// The lock is a flock on a file in the runtime directory rather than a PID
// file, because the kernel drops a flock when the holder dies. A crash, a
// kill -9 or a stale PID file therefore cannot leave the app permanently
// unlaunchable, which is the failure mode of every hand-rolled PID check.

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// instanceLock is a held flock. The file stays open for the process lifetime;
// closing it (or the process exiting) releases the lock.
type instanceLock struct {
	file *os.File
}

// lockPath is where the lock lives: $XDG_RUNTIME_DIR is the right place for a
// per-user, per-session runtime file (it is removed on logout, and is not
// world-writable like /tmp). /tmp is the fallback for the odd desktop that
// does not set it; the uid in the name keeps two users apart.
func lockPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "chat-app.lock")
	}
	return fmt.Sprintf("/tmp/chat-app-%d.lock", os.Getuid())
}

// acquireInstanceLock takes the exclusive lock, or reports which process is
// already holding it. It returns a nil lock (and no error) when locking is
// impossible - a read-only runtime dir must not stop the app - but says so in
// the log, because a silently missing guard is worse than a missing one the
// user knows about.
func acquireInstanceLock() (*instanceLock, error) {
	path := lockPath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil && os.IsNotExist(err) {
		// A hand-made XDG_RUNTIME_DIR (the documented "run a second copy"
		// escape hatch) does not exist yet. Create it rather than silently
		// running with no lock at all.
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr == nil {
			f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		}
	}
	if err != nil {
		log.Printf("single-instance: lock unavailable (%v); another chat-app may be started", err)
		return nil, nil
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readLockHolder(f)
		f.Close()
		if holder != "" {
			return nil, fmt.Errorf("another chat-app is already running (pid %s); "+
				"close it first, or run one of the read-only modes "+
				"(-stt-test, -preview) which do not take the lock", holder)
		}
		return nil, fmt.Errorf("another chat-app is already running (pid unknown); close it first")
	}
	// Record who holds it, so the losing instance can name it. The file is
	// truncated first because the previous holder's pid is still in it.
	if err := f.Truncate(0); err == nil {
		f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	}
	// The fd is kept in a package-level var for the whole run: closing it
	// (or the process exiting) is what releases the lock, so nothing may
	// drop it early.
	instanceLockHeld = &instanceLock{file: f}
	return instanceLockHeld, nil
}

// instanceLockHeld keeps the fd reachable for the whole run. A nil value means
// locking was skipped or unavailable, which callers treat as "no guard".
var instanceLockHeld *instanceLock

// readLockHolder returns the pid recorded in the lock file, if it is there.
func readLockHolder(f *os.File) string {
	buf := make([]byte, 32)
	// A short read is normal here: the file holds a few digits, so ReadAt
	// returns io.EOF alongside the bytes it did read. Only n == 0 means the
	// file is empty and the holder really is unknown.
	n, _ := f.ReadAt(buf, 0)
	if n == 0 {
		return ""
	}
	pid := strings.TrimSpace(string(buf[:n]))
	if _, err := strconv.Atoi(pid); err != nil {
		return "" // not a pid: report the holder as unknown
	}
	return pid
}

// releaseInstanceLock drops the lock early. Not strictly needed (the kernel
// does it on exit) but it keeps the recorded pid from outliving us, which
// matters when the app exits and a second launch happens in the same moment.
func releaseInstanceLock() {
	if instanceLockHeld == nil {
		return
	}
	syscall.Flock(int(instanceLockHeld.file.Fd()), syscall.LOCK_UN)
	instanceLockHeld.file.Close()
	instanceLockHeld = nil
}
