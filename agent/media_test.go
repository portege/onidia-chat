package agent

// media_test.go - end-to-end tests for the SHIPPED play_song / play_movie
// agents: install from the repo folders, discover, run the real python
// scripts against a fixture library with a fake player, and assert the
// player invocations (flags included).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pollFile waits for a detached player to append to path and returns it.
func pollFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("player log %s never written (agent returned before spawning?)", path)
	return ""
}

func TestShippedMediaAgents(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	Reset()
	defer Reset()
	defer SetExtraEnv(nil)

	// Fixture library.
	music := t.TempDir()
	for _, f := range []string{
		"Havana - Camila Cabello.mp3",
		"Sandstorm - Darude.flac",
		filepath.Join("rock", "Bohemian Rhapsody - Queen.mp3"),
	} {
		p := filepath.Join(music, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	videos := t.TempDir()
	if err := os.WriteFile(filepath.Join(videos, "Inception (2010).mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake player named "mpv" so the mode-flag maps apply; it appends its
	// argv to a log the test polls (the agent detaches it).
	logFile := filepath.Join(t.TempDir(), "player.log")
	bin := t.TempDir()
	player := filepath.Join(bin, "mpv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logFile + "'\n"
	if err := os.WriteFile(player, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	SetExtraEnv(map[string]string{
		"CHAT_APP_MUSIC_DIR": music,
		"CHAT_APP_VIDEO_DIR": videos,
		"CHAT_APP_PLAYER":    player,
	})

	// Install the shipped folders exactly like `agentctl install` would.
	dest := t.TempDir()
	if _, err := Install("../agents/play_song", dest); err != nil {
		t.Fatalf("install play_song: %v", err)
	}
	if _, err := Install("../agents/play_movie", dest); err != nil {
		t.Fatalf("install play_movie: %v", err)
	}
	ids, problems := Discover(dest)
	if len(problems) != 0 {
		t.Fatalf("discover problems: %v", problems)
	}
	if len(ids) != 2 || ids[0] != "play_movie" || ids[1] != "play_song" {
		t.Fatalf("ids = %v, want [play_movie play_song]", ids)
	}

	// Fuzzy match with song-mode flags.
	res, err := Run("play_song", map[string]string{"query": "havana"})
	if err != nil {
		t.Fatalf("play_song: %v", err)
	}
	if res.Message != "Playing Havana - Camila Cabello" {
		t.Errorf("Message = %q", res.Message)
	}
	wantPath := filepath.Join(music, "Havana - Camila Cabello.mp3")
	got := pollFile(t, logFile)
	if !strings.Contains(got, wantPath) {
		t.Errorf("player log = %q, want path %q", got, wantPath)
	}
	if !strings.Contains(got, "--no-video") {
		t.Errorf("player log = %q, want mpv song flags", got)
	}

	// Empty query -> random pick from the index.
	if res, err := Run("play_song", nil); err != nil ||
		!strings.HasPrefix(res.Message, "Playing ") {
		t.Errorf("random pick = (%q, %v), want Playing ...", res.Message, err)
	}

	// No match -> visible error with suggestions.
	if _, err := Run("play_song", map[string]string{"query": "zzzz-nonexistent"}); err == nil ||
		!strings.Contains(err.Error(), "no match") {
		t.Errorf("no-match error = %v", err)
	}

	// Movie, fullscreen=false: no flag; default (true): --fullscreen.
	os.Remove(logFile)
	if _, err := Run("play_movie", map[string]string{
		"query": "inception", "fullscreen": "false",
	}); err != nil {
		t.Fatalf("play_movie windowed: %v", err)
	}
	got = pollFile(t, logFile)
	if !strings.Contains(got, "Inception") || strings.Contains(got, "--fullscreen") {
		t.Errorf("windowed run log = %q, want Inception without --fullscreen", got)
	}
	os.Remove(logFile)
	if _, err := Run("play_movie", map[string]string{"query": "inception"}); err != nil {
		t.Fatalf("play_movie fullscreen: %v", err)
	}
	got = pollFile(t, logFile)
	if !strings.Contains(got, "--fullscreen") {
		t.Errorf("fullscreen run log = %q, want --fullscreen", got)
	}

	// Missing video folder -> actionable error naming the config keys.
	SetExtraEnv(map[string]string{
		"CHAT_APP_MUSIC_DIR": music,
		"CHAT_APP_VIDEO_DIR": filepath.Join(t.TempDir(), "missing"),
		"CHAT_APP_PLAYER":    player,
	})
	if _, err := Run("play_movie", nil); err == nil ||
		!strings.Contains(err.Error(), "not found") ||
		!strings.Contains(err.Error(), "video-dir") {
		t.Errorf("missing folder error = %v", err)
	}
}
