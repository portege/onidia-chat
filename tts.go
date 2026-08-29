package main

// tts.go - text-to-speech for chat replies via the Typecast API.
//
// When a reply lands, the bubble is shown immediately (main.go) and the text
// is handed to Speak(), which queues it. A single worker posts it to
// https://api.typecast.ai/v1/text-to-speech, saves the returned WAV to a temp
// file and plays it with the first available system player (aplay -> paplay
// -> ffplay). Playback is serialised so replies never talk over each other,
// and a quiet failure (no sound card, no player, API hiccup) never blocks or
// crashes the UI.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	typecastURL     = "https://api.typecast.ai/v1/text-to-speech"
	defaultTTSKey   = "__plt9BasJSFiJWxLhkbqn3YzBxExsZqjqkeZWBRNzu2s" // bundled demo key
	defaultTTSVoice = "tc_6359e7f6467f9e240b68292c"
	ttsModel        = "ssfm-v30"
	ttsLanguage     = "eng"
	ttsTimeout      = 30 * time.Second
	ttsPlayTimeout  = 30 * time.Second
)

// ttsPlayerCandidates is the order in which system audio players are tried on
// a classic ALSA setup. On a PipeWire desktop ttspCandidates() reorders to
// prefer the server-native client (see pipewireRunning).
var ttsPlayerCandidates = []string{"aplay", "paplay", "ffplay"}

// ttsUserRuntimeDir overrides the runtime dir used to detect a running
// PipeWire server ("" = auto /run/user/<uid>). Tests use it to neutralise
// the PipeWire preference branch.
var ttsUserRuntimeDir string

// pipewireRunning reports whether a PipeWire server socket exists. When it
// does, the raw ALSA "default" device is usually held by the server, so aplay
// would fail with "Device or resource busy"; the native pw-play must win.
func pipewireRunning() bool {
	dir := ttsUserRuntimeDir
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	matches, err := filepath.Glob(filepath.Join(dir, "pipewire*"))
	if err != nil {
		return false
	}
	return len(matches) > 0
}

// TTS turns reply text into spoken audio. Speak is non-blocking: text lands
// in a small queue and a single worker fetches + plays each item in order.
type TTS struct {
	enabled bool
	apiKey  string
	voiceID string
	player  string // absolute path of the audio player ("" = nothing plays)
	ch      chan string
}

// NewTTS prepares a TTS engine. The API key and voice fall back to the
// bundled Typecast defaults (overridable via flag/env/config). If no system
// player is found the engine logs a warning and stays disabled, so the chat
// never depends on sound hardware being present.
func NewTTS(enabled bool, apiKey, voiceID string) *TTS {
	t := &TTS{
		enabled: enabled,
		apiKey:  strings.TrimSpace(apiKey),
		voiceID: strings.TrimSpace(voiceID),
		ch:      make(chan string, 32),
	}
	if t.apiKey == "" {
		t.apiKey = defaultTTSKey
	}
	if t.voiceID == "" {
		t.voiceID = defaultTTSVoice
	}
	if p := findTTSPlayer(); p == "" {
		if t.enabled {
			log.Printf("tts: no system audio player found (tried aplay, paplay, ffplay) - speech disabled")
		}
		t.enabled = false
	} else {
		t.player = p
	}
	return t
}

// Enabled reports whether speech will actually play.
func (t *TTS) Enabled() bool { return t.enabled }

// Start launches the playback worker goroutine.
func (t *TTS) Start() {
	if t.enabled {
		go t.worker()
	}
}

// Close stops the worker once queued speech has drained.
func (t *TTS) Close() {
	if t.enabled {
		close(t.ch)
	}
}

// Speak queues reply text for playback. Empty text (e.g. image-only replies)
// is dropped, and a full queue drops the newest line with a log message - it
// never blocks the caller.
func (t *TTS) Speak(text string) {
	if !t.enabled {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	select {
	case t.ch <- text:
	default:
		log.Printf("tts: queue full, skipping speech for %q", truncate(text, 40))
	}
}

func (t *TTS) worker() {
	for text := range t.ch {
		data, err := fetchTTSAudio(&http.Client{Timeout: ttsTimeout}, typecastURL, t.apiKey, t.voiceID, text)
		if err != nil {
			log.Printf("tts: %v", err)
			continue
		}
		path, err := writeWav(data)
		if err != nil {
			log.Printf("tts: cannot save audio: %v", err)
			continue
		}
		start := time.Now()
		if err := playAudio(t.player, path); err != nil {
			log.Printf("tts: play failed for %q: %v", truncate(text, 40), err)
		} else {
			log.Printf("tts: spoke %q (%.2fs, %d bytes)", truncate(text, 40), time.Since(start).Seconds(), len(data))
		}
		os.Remove(path)
	}
}

// fetchTTSAudio posts text to Typecast and returns the raw WAV bytes.
// Extracted as a package function so tests can point it at a fake server.
func fetchTTSAudio(client *http.Client, url, apiKey, voiceID, text string) ([]byte, error) {
	body, err := json.Marshal(ttsWireRequest{
		VoiceID:  voiceID,
		Text:     text,
		Model:    ttsModel,
		Language: ttsLanguage,
		Output: ttsOutputSpec{
			Volume:      100,
			AudioPitch:  0,
			AudioTempo:  1,
			AudioFormat: "wav",
		},
		Seed: 42,
	})
	if err != nil {
		return nil, fmt.Errorf("tts: encode request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("tts: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts: typecast request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("tts: typecast http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tts: read audio: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("tts: empty audio response")
	}
	return data, nil
}

// ttsWireRequest mirrors the Typecast /v1/text-to-speech body (sample from
// the user's spec). Only the fields we use are modelled.
type ttsWireRequest struct {
	VoiceID  string        `json:"voice_id"`
	Text     string        `json:"text"`
	Model    string        `json:"model"`
	Language string        `json:"language"`
	Output   ttsOutputSpec `json:"output"`
	Seed     int           `json:"seed"`
}

type ttsOutputSpec struct {
	Volume      int    `json:"volume"`
	AudioPitch  int    `json:"audio_pitch"`
	AudioTempo  int    `json:"audio_tempo"`
	AudioFormat string `json:"audio_format"`
}

// writeWav stores raw WAV bytes in a fresh /tmp/chat-app-tts-*.wav file.
func writeWav(data []byte) (string, error) {
	f, err := os.CreateTemp("", "chat-app-tts-*.wav")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// findTTSPlayer returns the first available system audio player (absolute
// path), or "" if none exists. On PipeWire desktops the native pw-play (or
// paplay) is preferred over aplay, which maps to the server-held ALSA device.
func findTTSPlayer() string {
	cands := ttsPlayerCandidates
	if pipewireRunning() {
		cands = append([]string{"pw-play", "paplay"}, cands...)
	}
	for _, p := range cands {
		if path, err := exec.LookPath(p); err == nil {
			return path
		}
	}
	return ""
}

// playAudio runs the chosen player on the WAV file with a safety timeout so a
// stuck player (or missing sound device) can never hang the worker.
func playAudio(player, path string) error {
	var args []string
	switch filepath.Base(player) {
	case "aplay":
		args = []string{"-q", path}
	case "ffplay":
		args = []string{"-nodisp", "-autoexit", "-loglevel", "quiet", path}
	default: // paplay and anything else takes the path as-is
		args = []string{path}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ttsPlayTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, player, args...)
	return cmd.Run()
}

// truncate shortens long text for log lines.
func truncate(s string, n int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "..."
}
