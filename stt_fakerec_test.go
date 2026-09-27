package main

// The recorder stand-in is this same test binary re-executed with a magic
// environment variable - the standard Go trick for exercising an os/exec
// pipeline without shipping a second script. fakeRecorderPath() points
// os.Args[0] at that mode; when the suite runs normally the test skips.

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
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
