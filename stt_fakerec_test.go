package main

// The recorder stand-in is this same test binary re-executed with a magic
// environment variable - the standard Go trick for exercising an os/exec
// pipeline without shipping a second script. fakeRecorderPath() points
// os.Args[0] at that mode; when the suite runs normally the test skips.

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fakeRecorderPath returns a "recorder" command that creates the file named
// by its last argument, then waits for SIGINT and exits 0 - exactly what
// pw-record and arecord do when a take is finalised.
func fakeRecorderPath(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	t.Setenv("STT_FAKE_RECORDER", "1")
	return self
}

func TestSTTFakeRecorderProcess(t *testing.T) {
	if os.Getenv("STT_FAKE_RECORDER") != "1" {
		t.Skip("not running as the recorder stand-in")
	}
	path := os.Args[len(os.Args)-1]
	_ = os.WriteFile(path, nil, 0o644)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	os.Exit(0)
}

// TestMain ends any take a test opened and forgot. The mic-button tests press
// WMic, which goes through startMic and therefore the REAL system recorder -
// so without this the suite leaves a live pw-record writing to /tmp behind it,
// once per run.
func TestMain(m *testing.M) {
	code := m.Run()
	killAllRecorders()
	os.Exit(code)
}

// recorderScript writes a fake recorder to a temp dir and returns its path.
//
// A script, not a re-exec of this test binary: sttArgs builds ffmpeg-style
// flags (--rate, -ac, -y...) that the test binary's flag parser rejects, so
// such a child dies at startup and a test built on it passes without ever
// recording anything - which is how a regression test for this bug ships
// broken. A shell script ignores the flags and records into the file named
// by its last argument, exactly as the real recorders do.
//
// "polite"   - exits on SIGINT, like pw-record finalising its RIFF header.
// "stubborn" - ignores SIGINT and SIGTERM and grows the file forever, like a
// recorder wedged on a full queue. Only SIGKILL ends it.
func recorderScript(t *testing.T, mode string) string {
	t.Helper()
	var body string
	switch mode {
	case "polite":
		body = "trap 'exit 0' INT TERM\nwhile :; do sleep 0.05; done\n"
	case "stubborn":
		body = "trap '' INT TERM\n" +
			"while :; do dd if=/dev/zero bs=4096 count=1 >>\"$out\" 2>/dev/null; done\n"
	default:
		t.Fatalf("unknown recorder mode %q", mode)
	}
	// The last argument is the output path, whatever the flags in between say.
	script := "#!/bin/sh\nout=\"\"\nfor a in \"$@\"; do out=\"$a\"; done\n: > \"$out\"\n" + body
	path := filepath.Join(t.TempDir(), "fake-recorder")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake recorder: %v", err)
	}
	return path
}

// processGone reports whether a pid is gone. It is only meaningful after the
// child has been reaped: kill(pid, 0) succeeds on a zombie, so an unreaped
// process looks alive here and the assertion would be a false failure.
func processGone(pid int) bool {
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

// waitProcessGone polls for the process to disappear.
func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if processGone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("recorder pid %d is still alive after cleanup", pid)
}

// TestRecorderCleanupKillsStubbornRecorder is the disk-fill regression test.
// The bug: cleanup unlinked the WAV without ever stopping the recorder, so a
// recorder that ignored SIGINT went on writing to a deleted inode - invisible
// in ls, still eating the tmpfs. cleanup must kill first, then unlink, and it
// must do that with no Stop() beforehand, because "nobody stopped it" is the
// whole failure mode.
func TestRecorderCleanupKillsStubbornRecorder(t *testing.T) {
	rec, err := startSTTRecorder(recorderScript(t, "stubborn"), "")
	if err != nil {
		t.Fatalf("startSTTRecorder: %v", err)
	}
	pid := rec.cmd.Process.Pid

	// Let it actually start writing, so "it stopped growing" means something.
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(rec.path); err != nil {
		t.Fatalf("stubborn recorder made no file: %v", err)
	}

	rec.cleanup() // no Stop(): the app-quitting-mid-take case

	waitProcessGone(t, pid)
	if _, err := os.Stat(rec.path); !os.IsNotExist(err) {
		t.Errorf("cleanup left the WAV behind: %v", err)
	}
}

// TestRecorderIsOwnProcessGroup pins the mechanism the kill depends on: the
// recorder leads its own process group, so signalGroup reaches it without
// touching onidia-chat itself. The polite stand-in is the one to use here - it
// exits on SIGINT, so this also proves the group signal is actually wired to
// the recorder and not swallowed.
func TestRecorderIsOwnProcessGroup(t *testing.T) {
	rec, err := startSTTRecorder(recorderScript(t, "polite"), "")
	if err != nil {
		t.Fatalf("startSTTRecorder: %v", err)
	}
	defer rec.cleanup()

	pid := rec.cmd.Process.Pid
	if rec.pgid != pid {
		t.Errorf("recorder pgid = %d, want its own pid %d", rec.pgid, pid)
	}
	if err := signalGroup(rec.pgid, syscall.SIGINT); err != nil {
		t.Errorf("signalGroup: %v", err)
	}
	_ = rec.cmd.Wait()      // reap, so processGone can mean what it says
	waitProcessGone(t, pid) // the group signal reached it - and only it
}

// TestSelfTestRecorderLeavesNothingBehind is the invariant the self test's
// deferred cleanup depends on: starting a recorder and immediately cleaning it
// up must leave neither a process nor a file. The self test runs under
// os.Exit, so this is the only thing standing between a take and a full disk.
func TestSelfTestRecorderLeavesNothingBehind(t *testing.T) {
	rec, err := startSTTRecorder(recorderScript(t, "stubborn"), "")
	if err != nil {
		t.Fatalf("startSTTRecorder: %v", err)
	}
	path := rec.path
	pid := rec.cmd.Process.Pid
	rec.cleanup()

	waitProcessGone(t, pid)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("self-test cleanup left %s behind", path)
	}
}

// TestKillAllRecordersEndsEveryOpenTake covers the shutdown net: several takes
// open at once, with nobody holding their sessions, and killAllRecorders ends
// all of them. This is the path that stops a take surviving the process that
// wanted it - the app calls it on every exit, and TestMain after the suite.
func TestKillAllRecordersEndsEveryOpenTake(t *testing.T) {
	const takes = 3
	type take struct {
		rec *sttRecorder
		pid int
	}
	var open []take
	for i := 0; i < takes; i++ {
		rec, err := startSTTRecorder(recorderScript(t, "stubborn"), "")
		if err != nil {
			t.Fatalf("take %d: startSTTRecorder: %v", i, err)
		}
		open = append(open, take{rec, rec.cmd.Process.Pid})
	}
	// Let them all get going, so "stopped" cannot be confused with "never ran".
	time.Sleep(150 * time.Millisecond)

	killAllRecorders()

	for _, tk := range open {
		waitProcessGone(t, tk.pid)
		if _, err := os.Stat(tk.rec.path); !os.IsNotExist(err) {
			t.Errorf("take left its WAV behind: %v", err)
		}
	}
	// Idempotent: a second sweep must not panic or hang.
	killAllRecorders()
}
