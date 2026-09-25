// media.go - the "now playing" strip under the chat window.
//
// The play_song / play_movie agents record the player they started in a small
// JSON session file (play_song.py:save_session). This file is the chat
// window's half of that contract: it reads the session, decides what the
// transport strip shows, and turns a button click into a transport command.
//
// The buttons do NOT talk to the player themselves - they call the very same
// media_control agent the model would ("pause the music"), so "pause" means one
// thing in the whole system, and an ability that drives a different player
// keeps working without a chat-app change. With that agent not installed there
// is simply no strip (mediaControlInstalled), so nothing breaks.

package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/portege/chat-app/agent"
)

const (
	// mediaControlID is the agent that owns transport commands.
	mediaControlID = "media_control"
	// mediaControlTimeout bounds one button press. The agent is a fast local
	// script, so this only exists so a wedged one cannot freeze the window.
	mediaControlTimeout = 5 * time.Second
)

// MediaSession is what a media agent recorded right after it started a player.
// The chat window only reads it; the agent owns every change.
type MediaSession struct {
	ID      string `json:"id"`
	PID     int    `json:"pid"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Player  string `json:"player"`
	IPC     string `json:"ipc"`
	Paused  bool   `json:"paused"`
	Started int64  `json:"started"`
}

// MediaState is everything the transport strip draws itself from.
type MediaState struct {
	Active bool // a player an agent started is still running
	Paused bool
	Title  string
}

// mediaStateDir is where session files live. chat-app exports it to its agents
// (see main.go) so both halves always agree; the fallbacks only matter when
// media_control is run by hand through agentctl.
func mediaStateDir() string {
	if d := strings.TrimSpace(os.Getenv("CHAT_APP_STATE_DIR")); d != "" {
		return d
	}
	if d := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); d != "" {
		return filepath.Join(d, "chat-app")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "chat-app")
	}
	return filepath.Join(os.TempDir(), "chat-app-"+strconv.Itoa(os.Getuid()))
}

// processAlive reports whether pid is still running. Signal 0 does the liveness
// check without touching the process; EPERM means it exists but belongs to
// someone else, which still counts as alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// mediaSessions returns the sessions whose player is still running, newest
// first. Files it cannot parse are skipped (the state dir is shared), and a
// session whose process is gone is dropped from the strip without being
// deleted - media_control owns the cleanup.
func mediaSessions() []MediaSession {
	dir := mediaStateDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []MediaSession
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s MediaSession
		if err := json.Unmarshal(b, &s); err != nil || s.PID <= 0 {
			continue
		}
		if s.ID == "" {
			s.ID = strings.TrimSuffix(e.Name(), ".json")
		}
		if !processAlive(s.PID) {
			continue
		}
		out = append(out, s)
	}
	// Newest first: the newest session is the one the user just asked for.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Started > out[j-1].Started; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// currentMedia is the state of the transport strip right now: nothing playing,
// or the newest live player and whether it is paused.
func currentMedia() MediaState {
	sessions := mediaSessions()
	if len(sessions) == 0 {
		return MediaState{}
	}
	s := sessions[0]
	return MediaState{
		Active: true,
		Paused: s.Paused,
		Title:  firstNonEmpty(s.Title, s.ID),
	}
}

// mediaControlInstalled reports whether the transport agent is available. No
// agent, no strip: the play_* agents still work, the user just controls the
// player the usual way.
func mediaControlInstalled() bool {
	_, err := agent.Get(mediaControlID)
	return err == nil
}

// applyMediaControl runs one transport command through the media_control
// agent, in-process (no extra binary, no subprocess) and with a hard timeout so
// a wedged agent cannot freeze the window. Called from the click handler in a
// goroutine; the UI repaints from the next state poll, not from here.
func applyMediaControl(cmd string) {
	if !mediaControlInstalled() {
		log.Printf("media: %q ignored - install the %s agent to use the transport buttons",
			cmd, mediaControlID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaControlTimeout)
	defer cancel()
	res, err := agent.RunArgsContext(ctx, mediaControlID, map[string]any{
		"cmd":    cmd,
		"target": "any",
	})
	if err != nil {
		log.Printf("media: %s: %v", cmd, err)
		return
	}
	log.Printf("media: %s: %s", cmd, strings.TrimSpace(res.Message))
	if res.PetCmd != "" {
		log.Printf("media: pet command %q", res.PetCmd)
	}
}
