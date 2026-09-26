package agent

// media_control_test.go - the transport contract end to end, with real
// processes: play_song records what it started, media_control pauses, resumes
// and stops it, and the session files follow along. The fake player is named
// "mpv" so the mpv-only IPC path is exercised too - with no socket behind it,
// the agent must fall back to signals instead of failing.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// procState reads a process's single-letter state from /proc (Linux): "T" is
// stopped (what SIGSTOP does), "R"/"S" is running/sleeping. "" when there is
// no such process - or no /proc at all, which makes the test skip.
func procState(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	// The comm field may contain spaces and parens, so cut at the last ')':
	// what follows is "state ppid ...".
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return ""
	}
	return strings.TrimSpace(s[i+2 : i+3])
}

// mediaSession is the part of a session file these tests care about.
type mediaSession struct {
	ID      string `json:"id"`
	PID     int    `json:"pid"`
	Title   string `json:"title"`
	Player  string `json:"player"`
	IPC     string `json:"ipc"`
	Paused  bool   `json:"paused"`
	Started int64  `json:"started"`
}

func readSession(t *testing.T, path string) mediaSession {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session %s: %v", path, err)
	}
	var s mediaSession
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("session json: %v", err)
	}
	return s
}

func TestMediaControlAgent(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	if procState(os.Getpid()) == "" {
		t.Skip("no /proc (Linux only)")
	}
	Reset()
	defer Reset()
	defer SetExtraEnv(nil)

	music := t.TempDir()
	if err := os.WriteFile(filepath.Join(music, "Havana.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A player that stays alive: "sleep 30" is enough to pause, resume and stop.
	bin := t.TempDir()
	player := filepath.Join(bin, "mpv")
	if err := os.WriteFile(player, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	SetExtraEnv(map[string]string{
		"CHAT_APP_MUSIC_DIR": music,
		"CHAT_APP_PLAYER":    player,
		"CHAT_APP_STATE_DIR": state,
	})

	dest := t.TempDir()
	for _, a := range []string{"../agents/play_song", "../agents/media_control"} {
		if _, err := Install(a, dest); err != nil {
			t.Fatalf("install %s: %v", a, err)
		}
	}
	if ids, problems := Discover(dest); len(problems) != 0 || len(ids) != 2 {
		t.Fatalf("discover = %v, problems %v", ids, problems)
	}

	// Nothing playing yet: status is a clean OK, the other commands an error.
	if res, err := Run("media_control", map[string]string{"cmd": "status"}); err != nil ||
		!strings.Contains(res.Message, "nothing is playing") {
		t.Errorf("status with no session = (%q, %v)", res.Message, err)
	}
	if _, err := Run("media_control", map[string]string{"cmd": "pause"}); err == nil {
		t.Error("pause with nothing playing should fail")
	}
	// An undeclared command never reaches the script.
	if _, err := Run("media_control", map[string]string{"cmd": "explode"}); err == nil {
		t.Error("an undeclared cmd should be rejected before the agent runs")
	}

	// Start something to control.
	res, err := Run("play_song", map[string]string{"query": "havana"})
	if err != nil {
		t.Fatalf("play_song: %v", err)
	}
	if res.PetCmd != "action dance" {
		t.Errorf("PetCmd = %q, want action dance", res.PetCmd)
	}
	session := filepath.Join(state, "play_song.json")
	s := readSession(t, session)
	if s.PID <= 0 || s.Title != "Havana" || s.Started == 0 {
		t.Fatalf("session = %+v, want a live pid, the title and a start time", s)
	}
	if s.IPC == "" {
		t.Error("mpv launched without an --input-ipc-server path in the session")
	}
	if st := procState(s.PID); st != "S" && st != "R" {
		t.Fatalf("player process state = %q, want it running", st)
	}

	// status names the track.
	if res, err := Run("media_control", map[string]string{"cmd": "status"}); err != nil ||
		!strings.Contains(res.Message, "Havana") || !strings.Contains(res.Message, "playing") {
		t.Errorf("status = (%q, %v), want Havana playing", res.Message, err)
	}

	// pause -> the process is really stopped, and the session says so.
	res, err = Run("media_control", map[string]string{"cmd": "pause"})
	if err != nil || !strings.Contains(res.Message, "Paused") {
		t.Fatalf("pause = (%q, %v)", res.Message, err)
	}
	if got := procState(s.PID); got != "T" {
		t.Errorf("player state after pause = %q, want T (stopped)", got)
	}
	if !readSession(t, session).Paused {
		t.Error("session not marked paused after the pause command")
	}
	// Pausing twice is a no-op, not an error.
	if res, err := Run("media_control", map[string]string{"cmd": "pause"}); err != nil ||
		!strings.Contains(res.Message, "Already paused") {
		t.Errorf("double pause = (%q, %v)", res.Message, err)
	}

	// resume -> running again.
	if res, err := Run("media_control", map[string]string{"cmd": "resume"}); err != nil ||
		!strings.Contains(res.Message, "Resumed") {
		t.Fatalf("resume = (%q, %v)", res.Message, err)
	}
	if got := procState(s.PID); got == "T" {
		t.Error("player still stopped after resume")
	}
	if readSession(t, session).Paused {
		t.Error("session still marked paused after the resume command")
	}

	// target filter: a second (movie) session on the same pid, addressed by name.
	movie := filepath.Join(state, "play_movie.json")
	if err := os.WriteFile(movie, []byte(fmt.Sprintf(
		`{"id":"play_movie","pid":%d,"title":"Inception","player":"mpv","paused":false,"started":1}`,
		s.PID)), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := Run("media_control", map[string]string{
		"cmd": "status", "target": "song"}); err != nil || !strings.Contains(res.Message, "Havana") {
		t.Errorf("target=song status = (%q, %v), want Havana", res.Message, err)
	}
	if res, err := Run("media_control", map[string]string{
		"cmd": "status", "target": "movie"}); err != nil || !strings.Contains(res.Message, "Inception") {
		t.Errorf("target=movie status = (%q, %v), want Inception", res.Message, err)
	}
	// "any" takes the newest session, which is the song.
	if res, err := Run("media_control", map[string]string{"cmd": "status"}); err != nil ||
		!strings.Contains(res.Message, "Havana") {
		t.Errorf("any status = (%q, %v), want the newest session (Havana)", res.Message, err)
	}
	os.Remove(movie)

	// stop -> process gone, session pruned, the pet told the music stopped.
	res, err = Run("media_control", map[string]string{"cmd": "stop"})
	if err != nil || !strings.Contains(res.Message, "Stopped") {
		t.Fatalf("stop = (%q, %v)", res.Message, err)
	}
	if res.PetCmd != "action skip" {
		t.Errorf("stop PetCmd = %q, want action skip", res.PetCmd)
	}
	if procState(s.PID) != "" {
		t.Error("player still alive after stop")
	}
	if _, err := os.Stat(session); !os.IsNotExist(err) {
		t.Error("session file not removed after stop")
	}
	if res, err := Run("media_control", map[string]string{"cmd": "status"}); err != nil ||
		!strings.Contains(res.Message, "nothing is playing") {
		t.Errorf("status after stop = (%q, %v), want nothing playing", res.Message, err)
	}
}
