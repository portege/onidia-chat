package main

// media_test.go - the transport strip's data half: the session files the media
// agents write, the state the strip shows, and the click path into the
// media_control agent. The agent itself is covered end-to-end (with real
// processes) in agent/media_test.go.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/portege/chat-app/agent"
)

// stubControl is a native stand-in for the media_control agent: it records what
// the transport buttons asked for.
type stubControl struct {
	mu   sync.Mutex
	got  map[string]any
	once sync.Once
}

func (s *stubControl) ID() string          { return mediaControlID }
func (s *stubControl) Description() string { return "stub" }
func (s *stubControl) Params() []agent.Param {
	return []agent.Param{
		{Name: "cmd", Required: true},
		{Name: "target", Default: "any"},
	}
}
func (s *stubControl) Run(_ context.Context, args map[string]string) (agent.Result, error) {
	s.mu.Lock()
	s.got = map[string]any{"cmd": args["cmd"], "target": args["target"]}
	s.mu.Unlock()
	return agent.Result{Message: args["cmd"] + "ed"}, nil
}
func (s *stubControl) last() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got["cmd"].(string), s.got["target"].(string)
}

// writeSession drops a session file the way play_song.py:save_session does.
func writeSession(t *testing.T, dir, name string, pid int, started int64, paused bool) {
	t.Helper()
	body := fmt.Sprintf(`{"id":%q,"pid":%d,"title":"%s","player":"mpv","paused":%t,"started":%d}`,
		name, pid, name+" track", paused, started)
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMediaStateDirPrefersEnv(t *testing.T) {
	t.Setenv("CHAT_APP_STATE_DIR", "/tmp/chat-app-state-test")
	if got := mediaStateDir(); got != "/tmp/chat-app-state-test" {
		t.Errorf("mediaStateDir = %q, want the env value", got)
	}
	t.Setenv("CHAT_APP_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg")
	if got, want := mediaStateDir(), "/tmp/xdg/chat-app"; got != want {
		t.Errorf("mediaStateDir = %q, want %q", got, want)
	}
}

func TestMediaSessionsPicksLiveNewest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHAT_APP_STATE_DIR", dir)
	// This test process is alive, a pid that cannot be is not.
	writeSession(t, dir, "play_song", os.Getpid(), 100, false)
	writeSession(t, dir, "play_movie", 1<<30, 200, true) // dead: pruned
	if err := os.WriteFile(filepath.Join(dir, "junk.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions := mediaSessions()
	if len(sessions) != 1 || sessions[0].ID != "play_song" {
		t.Fatalf("mediaSessions = %+v, want only the live play_song", sessions)
	}
	// Newest wins when two players are live.
	writeSession(t, dir, "play_movie", os.Getpid(), 300, true)
	st := currentMedia()
	if !st.Active || !st.Paused || st.Title != "play_movie track" {
		t.Errorf("currentMedia = %+v, want the newer paused movie session", st)
	}
	// A dead session leaves the strip empty (and does not delete the file:
	// media_control owns the cleanup).
	os.Remove(filepath.Join(dir, "play_song.json"))
	os.Remove(filepath.Join(dir, "play_movie.json"))
	if st := currentMedia(); st.Active {
		t.Errorf("currentMedia = %+v, want inactive with no live player", st)
	}
	// Missing directory: no sessions, no panic.
	t.Setenv("CHAT_APP_STATE_DIR", filepath.Join(dir, "gone"))
	if got := mediaSessions(); got != nil {
		t.Errorf("mediaSessions = %+v, want nil for a missing dir", got)
	}
}

func TestApplyMediaControlCallsTheAgent(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	if mediaControlInstalled() {
		t.Error("no agent registered yet but mediaControlInstalled is true")
	}
	stub := &stubControl{}
	if err := agent.Register(stub); err != nil {
		t.Fatal(err)
	}
	if !mediaControlInstalled() {
		t.Fatal("mediaControlInstalled = false after registering the agent")
	}
	// Every transport button ends in the same agent, with the command the
	// button means and the newest player.
	for _, cmd := range []string{"pause", "resume", "stop"} {
		applyMediaControl(cmd)
		gotCmd, gotTarget := stub.last()
		if gotCmd != cmd || gotTarget != "any" {
			t.Errorf("applyMediaControl(%q) sent (%q, %q)", cmd, gotCmd, gotTarget)
		}
	}
	// Without the agent installed the click is logged and dropped - the
	// window keeps working, there is simply no transport.
	agent.Reset()
	applyMediaControl("pause")
}
