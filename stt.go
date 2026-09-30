// stt.go - speech-to-text: record from the microphone, transcribe, and hand
// the text to the chat input bar.
//
// The microphone button in the input bar starts a recording; pressing it again
// (or pressing Escape) stops it, and the transcript is typed into the input bar
// rather than sent, so the user can read and edit it before it goes to the
// model. The UI never blocks on either step: recording and transcription both
// run off the UI goroutine and deliver their result on a channel.
//
// Backends are pluggable behind the STT interface so the transport can change
// without touching the UI:
//
//	transcribe - Amazon Transcribe streaming (default; needs the AWS profile
//	             to allow transcribe:StartStreamTranscription)
//	whisper    - local faster-whisper, run through a small python helper
//	off        - no microphone button at all
//
// Recording uses the first available system recorder, preferring the
// PipeWire-native pw-record over arecord for the same reason tts.go prefers
// pw-play: on a PipeWire desktop the raw ALSA device is held by the server.
// Everything is captured as 16 kHz mono s16 WAV, which is the native input
// format of both backends, so nothing ever needs resampling.

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// sttSampleRate is the capture rate both backends want. AWS Transcribe
	// requires exactly 16 kHz for PCM input, and faster-whisper resamples
	// anything else anyway.
	sttSampleRate = 16000
	// sttMaxRecord bounds a single take: long enough for a sentence, short
	// enough that a forgotten recording cannot run forever.
	sttMaxRecord = 120 * time.Second
	// sttTranscribeTimeout bounds the backend call once recording has stopped.
	sttTranscribeTimeout = 90 * time.Second
	// sttChunkSeconds is the audio chunk size the Transcribe docs recommend
	// for PCM: Duration x Rate x Channels x 2 bytes.
	sttChunkSeconds = 1
)

// sttRecorderCandidates is the order in which system recorders are tried.
var sttRecorderCandidates = []string{"pw-record", "parecord", "arecord", "ffmpeg"}

// sttResult is what a finished take produces. Err is non-nil when recording or
// transcription failed, and Text is meaningless then.
type sttResult struct {
	Text string
	Err  error
}

// STT is a speech-to-text backend. Transcribe is handed the path of a 16 kHz
// mono s16 WAV file and returns the spoken text.
type STT interface {
	Name() string
	Transcribe(ctx context.Context, wavPath string) (string, error)
}

// STTError is a user-facing failure (bad config, no recorder, denied
// credentials). The UI shows its message; anything else just gets logged.
type STTError struct{ msg string }

func (e *STTError) Error() string { return e.msg }

func sttErrf(format string, args ...any) error {
	return &STTError{msg: fmt.Sprintf(format, args...)}
}

// STTOptions carries every speech-input setting, so adding one does not grow
// this function's signature (it already has six knobs across two backends).
type STTOptions struct {
	Backend      string // "transcribe" | "whisper" | "off"
	Language     string // language hint
	Device       string // capture device ("" = system default)
	AWSProfile   string // transcribe: shared credentials profile
	AWSRegion    string // transcribe: region
	WhisperModel string // faster-whisper size
	WhisperCmd   string // python interpreter with faster-whisper
	WhisperVAD   bool   // voice-activity filter (off: it eats quiet takes)
	Debug        bool   // log the whole transcription trace
}

// NewSTT builds the configured backend. It returns (nil, nil) when speech
// input is switched off, so callers can treat "no mic" uniformly.
func NewSTT(o STTOptions) (STT, error) {
	switch strings.ToLower(strings.TrimSpace(o.Backend)) {
	case "", "transcribe":
		return newTranscribeSTT(o.Language, o.AWSProfile, o.AWSRegion, o.Debug)
	case "whisper":
		return newWhisperSTT(o.Language, o.WhisperModel, o.WhisperCmd, o.WhisperVAD, o.Debug)
	case "off", "none", "disabled":
		return nil, nil
	default:
		return nil, sttErrf("unknown stt backend %q (want transcribe, whisper or off)", o.Backend)
	}
}

// ---- recorder ---------------------------------------------------------------

// findSTTRecorder returns the absolute path of the first usable system
// recorder, preferring the PipeWire-native clients when that server is running.
// Tests override sttUserRuntimeDir to neutralise the preference branch.
func findSTTRecorder() string {
	cands := sttRecorderCandidates
	if pipewireRunning() {
		cands = append([]string{"pw-record", "parecord"}, cands...)
	}
	for _, c := range cands {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// sttRecorder runs one system recorder writing a WAV to a temp file.
type sttRecorder struct {
	cmd    *exec.Cmd
	path   string
	pgid   int          // the recorder's own process group; see startSTTRecorder
	stop   func() error // signals the recorder so it finalises the WAV header
	mu     sync.Mutex
	closed bool
	waited bool // Stop has reaped the process, so cleanup must not Wait again
}

// startSTTRecorder begins a recording into a fresh temp WAV. The caller must
// call Stop() to finish it, even on the error path.
func startSTTRecorder(recorder, device string) (*sttRecorder, error) {
	if recorder == "" {
		return nil, sttErrf("no audio recorder found (tried pw-record, parecord, arecord, ffmpeg) - " +
			"install pipewire-utils or alsa-utils, or set stt = off")
	}
	f, err := os.CreateTemp("", "chat-app-stt-*.wav")
	if err != nil {
		return nil, fmt.Errorf("create temp wav: %w", err)
	}
	path := f.Name()
	f.Close() // the recorder writes to the file by path

	cmd := exec.Command(recorder, sttArgs(recorder, device, path)...)
	// Its own process group. A recorder is a child process, so any path that
	// exits without stopping it - os.Exit, a panic, the app quitting mid-take -
	// orphans it, and an orphan keeps appending to the WAV at ~32 KB/s until
	// the tmpfs fills. In a group it can be signalled and killed as a unit, and
	// a leftover can always be reaped by group. Setpgid failing fails Start.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("start %s: %w", filepath.Base(recorder), err)
	}
	// Setpgid makes the child a group leader, so its pgid is its pid.
	pgid := cmd.Process.Pid
	r := &sttRecorder{
		cmd:  cmd,
		path: path,
		pgid: pgid,
		stop: func() error {
			// SIGINT makes pw-record and arecord write the RIFF sizes and
			// exit cleanly; SIGKILL would leave a truncated, unusable header.
			// Signalling the group also takes down anything they spawned.
			return signalGroup(pgid, syscall.SIGINT)
		},
	}
	r.register()
	return r, nil
}

// liveRecorders tracks every recorder this process has started and not yet
// cleaned up, so they can all be ended at once on the way out.
//
// "Somebody must remember to stop it" is not a contract a caller can be held
// to: the mic button's own path, a test that presses WMic and moves on, or a
// future caller all end up in the same place - a recorder still running after
// the thing that wanted it is gone, appending to a WAV at ~32 KB/s. This is
// the net for that, and it is cheap.
var liveRecorders = struct {
	mu sync.Mutex
	m  map[*sttRecorder]struct{}
}{m: make(map[*sttRecorder]struct{})}

func (r *sttRecorder) register() {
	liveRecorders.mu.Lock()
	liveRecorders.m[r] = struct{}{}
	liveRecorders.mu.Unlock()
}

func (r *sttRecorder) unregister() {
	liveRecorders.mu.Lock()
	delete(liveRecorders.m, r)
	liveRecorders.mu.Unlock()
}

// killAllRecorders ends every take still open in this process and unlinks their
// WAVs. Safe to call repeatedly and from a shutdown path.
func killAllRecorders() {
	liveRecorders.mu.Lock()
	recs := make([]*sttRecorder, 0, len(liveRecorders.m))
	for r := range liveRecorders.m {
		recs = append(recs, r)
	}
	liveRecorders.mu.Unlock()
	for _, r := range recs {
		r.cleanup() // unregisters itself
	}
}

// signalGroup delivers sig to the recorder's process group. A pgid of 0 or less
// means the recorder was never started, so there is nothing to signal.
func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return syscall.ESRCH
	}
	return syscall.Kill(-pgid, sig)
}

// Stop ends the recording and returns the WAV path. The recorder gets a short
// grace period to flush, so the file is complete before a backend reads it.
func (r *sttRecorder) Stop() (string, error) {
	if r == nil {
		return "", nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return r.path, nil
	}
	r.closed = true
	r.mu.Unlock()

	if r.stop != nil {
		_ = r.stop()
	}
	done := make(chan error, 1)
	go func() { done <- r.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// The polite signal did not take (a recorder wedged on a full
		// queue will sit there and keep growing the file), so the group
		// gets SIGKILL. A truncated header only matters if a backend is
		// about to read it, and the timeout means it never will be.
		r.kill()
		<-done
	}
	r.mu.Lock()
	r.waited = true
	r.mu.Unlock()
	return r.path, nil
}

// kill SIGKILLs the recorder's process group. The last line of defence behind
// Stop: a take that is still running when we are done with it must not keep
// writing to disk.
func (r *sttRecorder) kill() {
	if r == nil || r.pgid <= 0 {
		return
	}
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill() // keeps Wait able to reap it
	}
	_ = signalGroup(r.pgid, syscall.SIGKILL)
}

// cleanup ends the take for good: the process is killed if it is somehow still
// alive, and only then is the WAV unlinked. Order matters - removing the path
// first leaves a process writing to a deleted inode, which is invisible in the
// directory listing but still takes the disk, and that is how a 28-hour-old
// take came to be filling /tmp. Safe to call after Stop, and safe to call
// without it, which is the whole point: every exit path can call this.
func (r *sttRecorder) cleanup() {
	if r == nil {
		return
	}
	r.kill()
	if r.cmd != nil && r.cmd.Process != nil {
		r.mu.Lock()
		waited := r.waited
		r.mu.Unlock()
		if !waited {
			// Nobody reaped it (the app is exiting mid-take); reap it here so
			// a kill on the shutdown path does not leave a zombie behind.
			go func() { _ = r.cmd.Wait() }()
		}
	}
	if r.path != "" {
		os.Remove(r.path)
	}
	r.unregister()
}

// sttArgs builds the command line for a given recorder. Split out from the
// spawn so tests can assert the flags without running a process.
//
// The device spelling differs per backend, which is why mic.List reports a
// value in the form each one actually accepts:
//
//	pw-record / parecord - a PipeWire node name (pw-record --target)
//	arecord               - an ALSA device ("hw:2,0" or "plughw:2,0")
//	ffmpeg                - an ALSA device, passed as the input
func sttArgs(recorder, device, path string) []string {
	switch filepath.Base(recorder) {
	case "pw-record", "parecord":
		args := []string{"--rate", fmt.Sprint(sttSampleRate), "--channels", "1", "--format", "s16"}
		if device != "" {
			// --target takes a node serial or name; without it pw-record
			// links to the default source, which on some desktops is a
			// monitor rather than the microphone.
			args = append(args, "--target", device)
		}
		return append(args, path)
	case "arecord":
		args := []string{"-q", "-f", "S16_LE", "-r", fmt.Sprint(sttSampleRate), "-c", "1", "-t", "wav", path}
		if device != "" {
			args = append(args, "-D", device)
		}
		return args
	default: // ffmpeg, and anything else that can read ALSA and write WAV
		src := "default"
		if device != "" {
			src = "alsa:" + device
		}
		return []string{"-hide_banner", "-loglevel", "error", "-f", "alsa",
			"-i", src, "-ac", "1", "-ar", fmt.Sprint(sttSampleRate), "-y", path}
	}
}

// ---- WAV parsing ------------------------------------------------------------

// wavPCM strips the WAV container and returns the raw samples plus the format
// the header described. Transcribe wants headerless PCM, so the header is
// parsed rather than assumed: a fixed 44-byte skip would hand the backend the
// recorder's own padding on files carrying extra chunks.
func wavPCM(path string) (data []byte, rate, channels int, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, 0, sttErrf("recorder produced no valid WAV header (was anything captured?)")
	}
	rate, channels, bits, data, err := parseWavChunks(raw)
	if err != nil {
		return nil, 0, 0, err
	}
	if bits != 16 {
		return nil, 0, 0, sttErrf("unsupported WAV sample size %d bits (want 16)", bits)
	}
	if channels != 1 {
		return nil, 0, 0, sttErrf("unsupported WAV channel count %d (want mono)", channels)
	}
	return data, rate, channels, nil
}

// parseWavChunks walks the RIFF chunk list and pulls out the fmt and data
// values. Chunks are word-aligned, so an odd-sized one is followed by a pad
// byte that has to be stepped over.
func parseWavChunks(raw []byte) (rate, channels, bits int, data []byte, err error) {
	// The RIFF header fields are 16- or 32-bit little-endian, so both widths
	// are needed: reading a 16-bit field as 32-bit would run off the end.
	le16 := func(b []byte) int { return int(b[0]) | int(b[1])<<8 }
	le32 := func(b []byte) int {
		return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24
	}
	for pos := 12; pos+8 <= len(raw); { // skip "RIFF" <size> "WAVE"
		id := string(raw[pos : pos+4])
		size := le32(raw[pos+4 : pos+8])
		body := pos + 8
		if size < 0 || body+size > len(raw) {
			size = len(raw) - body // tolerate a truncated final chunk
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return 0, 0, 0, nil, sttErrf("short WAV fmt chunk")
			}
			channels = le16(raw[body+2 : body+4])
			rate = le32(raw[body+4 : body+8])
			bits = le16(raw[body+14 : body+16])
		case "data":
			data = raw[body : body+size]
		}
		pos = body + size
		if size%2 == 1 {
			pos++
		}
	}
	if data == nil {
		return 0, 0, 0, nil, sttErrf("WAV has no data chunk")
	}
	return rate, channels, bits, data, nil
}

// ---- session ----------------------------------------------------------------

// STTSession drives one press-to-record cycle: it owns the recorder, then
// hands the WAV to the backend. The result lands on Done so the UI can pick it
// up without polling.
type STTSession struct {
	backend   STT
	recorder  string // absolute path of the system recorder
	device    string
	rec       *sttRecorder
	stopTimer *time.Timer
	Done      chan sttResult
	closeOnce sync.Once
}

// StartSTTSession returns a session ready to record, or an error if no backend
// is enabled or no recorder exists. Call Record to begin the take.
func StartSTTSession(backend STT, recorder, device string) (*STTSession, error) {
	if backend == nil {
		return nil, sttErrf("speech input is off")
	}
	if recorder == "" {
		return nil, sttErrf("no audio recorder found (tried pw-record, parecord, arecord, ffmpeg) - " +
			"install pipewire-utils or alsa-utils, or set stt = off")
	}
	return &STTSession{
		backend:  backend,
		recorder: recorder,
		device:   device,
		Done:     make(chan sttResult, 1),
	}, nil
}

// Record begins a take. A session records once; make a new session per take.
func (s *STTSession) Record() error {
	rec, err := startSTTRecorder(s.recorder, s.device)
	if err != nil {
		return err
	}
	s.rec = rec
	// A forgotten recording must not run forever: the timer finishes the take
	// exactly as an explicit stop would.
	s.stopTimer = time.AfterFunc(sttMaxRecord, func() { s.finish(nil) })
	return nil
}

// Recording reports whether a take is in progress.
func (s *STTSession) Recording() bool { return s.rec != nil && !s.rec.closed }

// Stop ends the take and starts transcription in the background.
func (s *STTSession) Stop() { s.finish(nil) }

// Cancel discards the take without transcribing (Escape, or the app quitting).
func (s *STTSession) Cancel() { s.finish(errSTTCanceled) }

// errSTTCanceled is delivered when the user abandons a take; the UI treats it
// as "nothing happened" rather than as an error worth showing.
var errSTTCanceled = &STTError{msg: "recording cancelled"}

// finish stops the recorder exactly once and then either reports an error or
// transcribes. userErr is non-nil when the caller supplied a reason.
func (s *STTSession) finish(userErr error) {
	s.closeOnce.Do(func() {
		if s.stopTimer != nil {
			s.stopTimer.Stop()
		}
		if s.rec == nil {
			s.deliver(sttResult{Err: userErr})
			return
		}
		path, err := s.rec.Stop()
		rec := s.rec
		if userErr != nil {
			rec.cleanup()
			s.deliver(sttResult{Err: userErr})
			return
		}
		if err != nil {
			rec.cleanup()
			s.deliver(sttResult{Err: fmt.Errorf("recorder failed: %w", err)})
			return
		}
		go func() {
			defer rec.cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), sttTranscribeTimeout)
			defer cancel()
			text, err := s.backend.Transcribe(ctx, path)
			s.deliver(sttResult{Text: strings.TrimSpace(text), Err: err})
		}()
	})
}

// deliver posts one result, never blocking and never panicking on a closed
// channel (the UI may have gone away).
func (s *STTSession) deliver(r sttResult) {
	defer func() { _ = recover() }() // sending on a closed Done must not crash
	select {
	case s.Done <- r:
	default:
		log.Printf("stt: result dropped, nobody is listening")
	}
}
