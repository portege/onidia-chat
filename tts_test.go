package main

// Tests for the Typecast text-to-speech layer (tts.go): request encoding,
// fetch round-trip against a fake server, temp-WAV writing, and the
// Speak() queue semantics. Audio playback itself is not exercised (no sound
// device in CI).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewTTSDefaults(t *testing.T) {
	tr := NewTTS(true, "", "")
	if tr.apiKey != defaultTTSKey {
		t.Errorf("apiKey: got %q want bundled default", tr.apiKey)
	}
	if tr.voiceID != defaultTTSVoice {
		t.Errorf("voiceID: got %q want bundled default", tr.voiceID)
	}
	if !tr.Enabled() {
		// Fine on machines without aplay/paplay/ffplay.
		t.Skip("no system audio player available - Enabled() false expected here")
	}
}

func TestNewTTSDisabledWhenOff(t *testing.T) {
	tr := NewTTS(false, "k", "v")
	if tr.Enabled() {
		t.Error("NewTTS(false) must be disabled")
	}
}

func TestNewTTSNoPlayerDisables(t *testing.T) {
	old := ttsPlayerCandidates
	oldDir := ttsUserRuntimeDir
	ttsPlayerCandidates = []string{"definitely-not-a-real-player-xyz"}
	ttsUserRuntimeDir = "/nonexistent-xyz-runtime"
	defer func() {
		ttsPlayerCandidates = old
		ttsUserRuntimeDir = oldDir
	}()
	tr := NewTTS(true, "k", "v")
	if tr.Enabled() {
		t.Error("expected engine to disable when no audio player exists")
	}
	if tr.player != "" {
		t.Errorf("player: got %q want \"\"", tr.player)
	}
}

func TestPipewireDetection(t *testing.T) {
	oldDir := ttsUserRuntimeDir
	defer func() { ttsUserRuntimeDir = oldDir }()
	ttsUserRuntimeDir = "/nonexistent-xyz-runtime"
	if pipewireRunning() {
		t.Error("pipewireRunning should be false for a missing runtime dir")
	}
	// Positive: a pipewire-0 socket in the dir means the server runs.
	dir := t.TempDir()
	ttsUserRuntimeDir = dir
	if pipewireRunning() {
		t.Error("empty runtime dir must not report a pipewire server")
	}
	if err := os.WriteFile(filepath.Join(dir, "pipewire-0"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !pipewireRunning() {
		t.Error("pipewireRunning must be true when a pipewire-0 socket exists")
	}
}

func TestTTSSpeakDropsEmpties(t *testing.T) {
	tr := NewTTS(true, defaultTTSKey, defaultTTSVoice)
	if len(tr.ch) != 0 {
		t.Fatal("queue should start empty")
	}
	tr.Speak("   ") // blank
	if len(tr.ch) != 0 {
		t.Error("blank text must be dropped")
	}
	tr.Speak("hello there") // one line queued
	if len(tr.ch) != 1 {
		t.Errorf("queue: got %d lines want 1", len(tr.ch))
	}
	tr.Close()
}

func TestFetchTTSAudioRoundTrip(t *testing.T) {
	fakeWav := []byte{0x52, 0x49, 0x46, 0x46, 1, 2, 3, 4} // "RIFF..." stub
	var gotKey, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-KEY")
		gotCT = r.Header.Get("Content-Type")
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		gotBody = string(b)
		w.Write(fakeWav)
	}))
	defer srv.Close()

	data, err := fetchTTSAudio(srv.Client(), srv.URL, "secret-key", "voice-1", "hi there")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(data) != string(fakeWav) {
		t.Errorf("audio body: got %v want %v", data, fakeWav)
	}
	if gotKey != "secret-key" {
		t.Errorf("X-API-KEY: got %q", gotKey)
	}
	if !strings.Contains(gotCT, "application/json") {
		t.Errorf("Content-Type: got %q", gotCT)
	}
	var req ttsWireRequest
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if req.VoiceID != "voice-1" || req.Text != "hi there" || req.Model != ttsModel ||
		req.Language != ttsLanguage || req.Output.AudioFormat != "wav" || req.Seed != 42 {
		t.Errorf("request body mismatch: %+v", req)
	}
}

func TestFetchTTSAudioHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no credits", http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := fetchTTSAudio(srv.Client(), srv.URL, "k", "v", "hello")
	if err == nil || !strings.Contains(err.Error(), "http 400") {
		t.Errorf("want http 400 error, got: %v", err)
	}
}

func TestFetchTTSAudioEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, err := fetchTTSAudio(srv.Client(), srv.URL, "k", "v", "hello")
	if err == nil || !strings.Contains(err.Error(), "empty audio response") {
		t.Errorf("want empty-audio error, got: %v", err)
	}
}

func TestWriteWav(t *testing.T) {
	data := []byte{1, 2, 3, 4}
	path, err := writeWav(data)
	if err != nil {
		t.Fatalf("writeWav: %v", err)
	}
	defer os.Remove(path)
	if !strings.HasPrefix(path, "/tmp/") || !strings.Contains(path, "chat-app-tts-") {
		t.Errorf("unexpected temp path: %q", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if string(b) != string(data) {
		t.Errorf("file content: got %v want %v", b, data)
	}
}

func TestPlayAudioMissingPlayer(t *testing.T) {
	if err := playAudio("/nonexistent/player-xyz", "/tmp/whatever.wav"); err == nil {
		t.Error("expected error running a nonexistent player")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc..." {
		t.Errorf("truncate: got %q", got)
	}
	if got := truncate("abc", 5); got != "abc" {
		t.Errorf("truncate short: got %q", got)
	}
	if got := truncate("a\nb", 10); got != "a b" {
		t.Errorf("truncate newline: got %q", got)
	}
}

func TestLoadConfigTTSCases(t *testing.T) {
	tmp, err := os.CreateTemp("", "chat-app-config-tts-*.ini")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString("tts = off\ntts-key = tk-123\ntts-voice = vc-42\n"); err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	c, err := LoadConfig(tmp.Name())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.TTS != "off" || c.TTSKey != "tk-123" || c.TTSVoice != "vc-42" {
		t.Errorf("tts config mismatch: %+v", c)
	}
}
