package main

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSTT records what it was asked to transcribe and replies with a canned
// result, so the session/UI plumbing can be tested without a microphone or a
// cloud call.
type fakeSTT struct {
	name    string
	text    string
	err     error
	gotPath string
	delay   time.Duration
	calls   int
}

func (f *fakeSTT) Name() string { return f.name }

func (f *fakeSTT) Transcribe(ctx context.Context, wavPath string) (string, error) {
	f.calls++
	f.gotPath = wavPath
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.err != nil {
		return "", f.err
	}
	return f.text, nil
}

// writeTestWav builds a 16 kHz mono s16 WAV holding 100 ms of silence. With
// extraChunk it also inserts a LIST chunk before the data, the padding style
// a fixed 44-byte header skip would get wrong.
func writeTestWav(t *testing.T, path string, extraChunk bool) {
	t.Helper()
	put32 := func(b []byte, v uint32) {
		b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	put16 := func(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }

	fmtChunk := make([]byte, 24)
	copy(fmtChunk, "fmt ")
	put32(fmtChunk[4:], 16)     // chunk size
	put16(fmtChunk[8:], 1)      // audioFormat: PCM
	put16(fmtChunk[10:], 1)     // channels: mono
	put32(fmtChunk[12:], 16000) // sample rate
	put32(fmtChunk[16:], 32000) // byte rate
	put16(fmtChunk[20:], 2)     // block align
	put16(fmtChunk[22:], 16)    // bits per sample

	const samples = 3200
	data := make([]byte, 8+samples)
	copy(data, "data")
	put32(data[4:], samples)

	// "RIFF" <size> "WAVE" - the size is patched in once the body is known.
	body := make([]byte, 12)
	copy(body, "RIFF")
	copy(body[8:], "WAVE")
	if extraChunk {
		list := make([]byte, 12)
		copy(list, "LIST")
		put32(list[4:], 4) // a 4-byte INFO payload
		body = append(body, list...)
	}
	body = append(body, fmtChunk...)
	body = append(body, data...)
	put32(body[4:], uint32(len(body)-8))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSTTBackendSelection covers the config switch: the default is
// transcribe, "off" yields no backend at all, and a typo is a user-facing
// error rather than a silent fallback.
func TestSTTBackendSelection(t *testing.T) {
	if b, err := NewSTT(STTOptions{Backend: "", AWSProfile: "default", AWSRegion: "us-east-1"}); err != nil || b == nil {
		t.Fatalf("default backend should be transcribe: %v", err)
	} else if b.Name() != "transcribe" {
		t.Fatalf("default backend = %q, want transcribe", b.Name())
	}
	for _, off := range []string{"off", "OFF", "none", "disabled"} {
		b, err := NewSTT(STTOptions{Backend: off, AWSProfile: "default", AWSRegion: "us-east-1"})
		if err != nil || b != nil {
			t.Fatalf("stt = %q should disable speech input, got %v / %v", off, b, err)
		}
	}
	_, err := NewSTT(STTOptions{Backend: "nope", AWSProfile: "default", AWSRegion: "us-east-1"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown backend should be reported, got %v", err)
	}
	var se *STTError
	if !errors.As(err, &se) {
		t.Fatalf("unknown backend error should be an *STTError, got %T", err)
	}
}

// TestSTTDefaults pins the values the transcribe backend falls back to.
func TestSTTDefaults(t *testing.T) {
	b, err := NewSTT(STTOptions{Backend: "transcribe"})
	if err != nil {
		t.Fatal(err)
	}
	tr := b.(*transcribeSTT)
	if tr.language != sttDefaultLanguage {
		t.Fatalf("language = %q, want %q", tr.language, sttDefaultLanguage)
	}
	if tr.region != sttDefaultRegion {
		t.Fatalf("region = %q, want %q", tr.region, sttDefaultRegion)
	}
	// An explicit language/region/profile must survive.
	b, _ = NewSTT(STTOptions{Backend: "transcribe", Language: "de-DE", AWSProfile: "prof", AWSRegion: "eu-west-1"})
	tr = b.(*transcribeSTT)
	if tr.language != "de-DE" || tr.region != "eu-west-1" || tr.profile != "prof" {
		t.Fatalf("explicit settings lost: %+v", tr)
	}
}

// TestWavPCMParsing covers the header handling, including the padded-chunk
// case a fixed 44-byte skip would get wrong.
func TestWavPCMParsing(t *testing.T) {
	dir := t.TempDir()

	plain := filepath.Join(dir, "plain.wav")
	writeTestWav(t, plain, false)
	pcm, rate, ch, err := wavPCM(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 3200 {
		t.Fatalf("pcm = %d bytes, want 3200", len(pcm))
	}
	if rate != sttSampleRate || ch != 1 {
		t.Fatalf("rate/ch = %d/%d, want %d/1", rate, ch, sttSampleRate)
	}

	padded := filepath.Join(dir, "padded.wav")
	writeTestWav(t, padded, true)
	pcm, rate, _, err = wavPCM(padded)
	if err != nil {
		t.Fatalf("padded chunk list should still parse: %v", err)
	}
	if len(pcm) != 3200 {
		t.Fatalf("padded: pcm = %d bytes, want 3200 (header bytes leaked in)", len(pcm))
	}
	if rate != sttSampleRate {
		t.Fatalf("padded: rate = %d, want %d", rate, sttSampleRate)
	}

	// Garbage and empty files are user-facing errors, not panics.
	junk := filepath.Join(dir, "junk.wav")
	os.WriteFile(junk, []byte("not a wav at all"), 0o644)
	if _, _, _, err := wavPCM(junk); err == nil {
		t.Fatal("junk file should be rejected")
	}
	empty := filepath.Join(dir, "empty.wav")
	os.WriteFile(empty, nil, 0o644)
	if _, _, _, err := wavPCM(empty); err == nil {
		t.Fatal("empty file should be rejected")
	}
}

// TestSTTArgs checks each recorder is asked for the 16 kHz mono s16 WAV both
// backends need, and that a device is threaded through where supported.
func TestSTTArgs(t *testing.T) {
	pw := sttArgs("pw-record", "", "/tmp/a.wav")
	if !hasPair(pw, "--rate", "16000") || !hasPair(pw, "--channels", "1") || !hasPair(pw, "--format", "s16") {
		t.Fatalf("pw-record args = %v", pw)
	}
	ar := sttArgs("arecord", "hw:1,0", "/tmp/a.wav")
	if !hasPair(ar, "-f", "S16_LE") || !hasPair(ar, "-r", "16000") || !hasPair(ar, "-D", "hw:1,0") {
		t.Fatalf("arecord args = %v", ar)
	}
	// No device configured means no -D: the system default is what we want.
	if containsArg(sttArgs("arecord", "", "/tmp/a.wav"), "-D") {
		t.Fatal("arecord should not force a device when none is configured")
	}
	ff := sttArgs("ffmpeg", "", "/tmp/a.wav")
	if !hasPair(ff, "-ar", "16000") || !hasPair(ff, "-ac", "1") {
		t.Fatalf("ffmpeg args = %v", ff)
	}
}

// containsArg reports whether a single flag appears anywhere in args.
func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// hasPair reports whether two adjacent args appear in order anywhere in args.
func hasPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

// TestFindSTTRecorder makes sure one of the recorders this feature depends on
// is present; on a machine with none, the UI hides the button instead.
func TestFindSTTRecorder(t *testing.T) {
	if findSTTRecorder() == "" {
		t.Skip("no audio recorder installed (pw-record / arecord / ffmpeg)")
	}
}

// TestSTTSessionLifecycle drives a whole take against a fake backend: record,
// stop, and get the transcript back on Done. It uses /bin/sh as a stand-in
// recorder, since the session only needs *some* process that ignores SIGINT.
func TestSTTSessionLifecycle(t *testing.T) {
	fake := &fakeSTT{name: "fake", text: "  hello there  "}
	sess, err := StartSTTSession(fake, fakeRecorderPath(t), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Record(); err != nil {
		t.Fatal(err)
	}
	if !sess.Recording() {
		t.Fatal("a fresh session should be recording")
	}
	sess.Stop()
	if sess.Recording() {
		t.Fatal("Stop should end the take")
	}
	select {
	case res := <-sess.Done:
		if res.Err != nil {
			t.Fatalf("transcribe failed: %v", res.Err)
		}
		// The session trims the backend's whitespace.
		if res.Text != "hello there" {
			t.Fatalf("text = %q, want %q", res.Text, "hello there")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no result on Done")
	}
	if fake.calls != 1 {
		t.Fatalf("backend called %d times, want 1", fake.calls)
	}
	// The temp WAV must be cleaned up once the take is done.
	if fake.gotPath == "" {
		t.Fatal("backend was not given a path")
	}
	if _, err := os.Stat(fake.gotPath); !os.IsNotExist(err) {
		t.Fatalf("temp wav should be removed, stat err = %v", err)
	}
}

// TestSTTSessionCancel covers Escape: the take is dropped and no transcription
// is attempted.
func TestSTTSessionCancel(t *testing.T) {
	fake := &fakeSTT{name: "fake", text: "should not appear"}
	sess, err := StartSTTSession(fake, fakeRecorderPath(t), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Record(); err != nil {
		t.Fatal(err)
	}
	sess.Cancel()
	select {
	case res := <-sess.Done:
		if !errors.Is(res.Err, errSTTCanceled) {
			t.Fatalf("cancel should report errSTTCanceled, got %v", res.Err)
		}
		if res.Text != "" {
			t.Fatalf("cancelled take produced text %q", res.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no result on Done")
	}
	if fake.calls != 0 {
		t.Fatalf("cancelled take called the backend %d times", fake.calls)
	}
}

// TestSTTSessionStopIsIdempotent: a double stop (button press plus the
// max-record timer) must not transcribe twice.
func TestSTTSessionStopIsIdempotent(t *testing.T) {
	fake := &fakeSTT{name: "fake", text: "once"}
	sess, _ := StartSTTSession(fake, fakeRecorderPath(t), "", false)
	if err := sess.Record(); err != nil {
		t.Fatal(err)
	}
	sess.Stop()
	sess.Stop()
	sess.Cancel() // a late Cancel must not undo a finished take
	select {
	case <-sess.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("no result on Done")
	}
	time.Sleep(50 * time.Millisecond) // let any stray goroutine land
	if fake.calls != 1 {
		t.Fatalf("backend called %d times, want exactly 1", fake.calls)
	}
}

// TestStartSTTSessionGuards the two ways a session can be refused.
func TestStartSTTSessionGuards(t *testing.T) {
	fake := &fakeSTT{name: "fake"}
	if _, err := StartSTTSession(nil, fakeRecorderPath(t), "", false); err == nil {
		t.Fatal("a nil backend should be refused")
	}
	_, err := StartSTTSession(fake, "", "", false)
	if err == nil || !strings.Contains(err.Error(), "recorder") {
		t.Fatalf("no recorder should be reported clearly, got %v", err)
	}
}

// drainUntilSettled polls DrainSTT the way the main loop does, until the take
// is finished. The transcription runs on its own goroutine, so a single
// non-blocking drain can legitimately find nothing yet.
func drainUntilSettled(t *testing.T, u *UI) {
	t.Helper()
	for i := 0; i < 200; i++ {
		u.DrainSTT()
		if u.STTSess == nil && !u.sttBusy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("transcription never settled")
}

// newSTTTestUI builds a UI sized like the real window with a fake backend
// attached, so the mic button layout and the transcript plumbing can be
// exercised without a display.
func newSTTTestUI(t *testing.T, backend STT) *UI {
	t.Helper()
	u := NewUI(320, 480)
	u.STT = backend
	return u
}

// TestMicButtonHiddenWhenOff is the important default: with no backend the
// input bar must be laid out exactly as it was before this feature existed,
// so an existing install sees no change at all.
func TestMicButtonHiddenWhenOff(t *testing.T) {
	u := newSTTTestUI(t, nil)
	if u.micEnabled() {
		t.Fatal("no backend means no mic button")
	}
	if got := u.micSpace(); got != 0 {
		t.Fatalf("micSpace = %d, want 0 when speech input is off", got)
	}
	if u.HitTest(u.micRect().Min.X+2, u.micRect().Min.Y+2) == WMic {
		t.Fatal("the mic must not be hit-testable when speech input is off")
	}
	// The textarea keeps its old right edge.
	if want := u.W - btnW - btnGap - padX; u.inputRect().Max.X != want {
		t.Fatalf("input rect right edge = %d, want %d", u.inputRect().Max.X, want)
	}
}

// TestMicButtonTakesSpaceWhenOn: with a backend the textarea must yield room
// to the mic without overlapping it.
func TestMicButtonTakesSpaceWhenOn(t *testing.T) {
	u := newSTTTestUI(t, &fakeSTT{name: "fake"})
	if !u.micEnabled() {
		t.Fatal("a backend should show the mic button")
	}
	if got := u.micSpace(); got != micW+btnGap {
		t.Fatalf("micSpace = %d, want %d", got, micW+btnGap)
	}
	// The textarea must not run into the mic button.
	if u.inputRect().Max.X > u.micRect().Min.X {
		t.Fatalf("input rect (%v) overlaps the mic (%v)", u.inputRect(), u.micRect())
	}
	// And the mic must not run into SEND.
	if u.micRect().Max.X > u.buttonRect().Min.X {
		t.Fatalf("mic rect (%v) overlaps SEND (%v)", u.micRect(), u.buttonRect())
	}
	// A click on the mic hits WMic, not the textarea.
	p := image.Pt(u.micRect().Min.X+u.micRect().Dx()/2, u.micRect().Min.Y+u.micRect().Dy()/2)
	if got := u.HitTest(p.X, p.Y); got != WMic {
		t.Fatalf("click on the mic = %v, want WMic", got)
	}
	// A press+release on the mic starts a take (or reports the failure); the
	// important part is that it is not swallowed by the input field.
	u.Press(WMic)
	if u.Release(WMic) {
		t.Fatal("a mic click should not resize the window")
	}
}

// TestMicDrainLandsTranscript covers the payoff: a finished take drops its
// words into the textarea instead of sending them, and appends to whatever
// was already typed.
func TestMicDrainLandsTranscript(t *testing.T) {
	fake := &fakeSTT{name: "fake", text: "hello"}
	u := newSTTTestUI(t, fake)
	u.input = []rune("already typed")

	sess, err := StartSTTSession(fake, fakeRecorderPath(t), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Record(); err != nil {
		t.Fatal(err)
	}
	sess.Stop()
	u.STTSess, u.sttBusy = sess, true
	drainUntilSettled(t, u)

	if got := string(u.input); got != "already typed hello" {
		t.Fatalf("input = %q, want %q", got, "already typed hello")
	}
	if u.sttErr != "" {
		t.Fatalf("unexpected mic error %q", u.sttErr)
	}
	// No "transcript ready" banner: the words landing in the textarea IS
	// the confirmation, and a status line would only cover them.
	if u.sttNote != "" {
		t.Fatalf("a finished transcript should leave no status line, got %q", u.sttNote)
	}
	if u.STTSess != nil || u.Busy() {
		t.Fatal("the take should be finished and cleared")
	}
	if len(u.msgs) != 1 { // only the NewUI greeting: nothing was sent
		t.Fatalf("the transcript must not be sent automatically (msgs = %d)", len(u.msgs))
	}
}

// TestMicDrainReportsError puts a backend failure in the input bar without
// touching the textarea.
func TestMicDrainReportsError(t *testing.T) {
	fake := &fakeSTT{name: "fake", err: errors.New("mic: device busy")}
	u := newSTTTestUI(t, fake)
	sess, _ := StartSTTSession(fake, fakeRecorderPath(t), "", false)
	if err := sess.Record(); err != nil {
		t.Fatal(err)
	}
	sess.Stop()
	u.STTSess, u.sttBusy = sess, true
	drainUntilSettled(t, u)

	if !strings.Contains(u.sttErr, "device busy") {
		t.Fatalf("sttErr = %q, want the backend message", u.sttErr)
	}
	if len(u.input) != 0 {
		t.Fatalf("a failed take must not add text, got %q", string(u.input))
	}
}

// TestMicCancelLeavesNoTrace: Escape during a take clears the status line and
// adds nothing.
func TestMicCancelLeavesNoTrace(t *testing.T) {
	fake := &fakeSTT{name: "fake", text: "nope"}
	u := newSTTTestUI(t, fake)
	sess, _ := StartSTTSession(fake, fakeRecorderPath(t), "", false)
	if err := sess.Record(); err != nil {
		t.Fatal(err)
	}
	u.STTSess = sess
	u.sttNote = "Recording…"
	u.CancelMic()
	if u.STTSess != nil {
		t.Fatal("the take should be gone")
	}
	if u.sttNote != "" {
		t.Fatalf("a cancelled take should clear the status line, got %q", u.sttNote)
	}
	// Drain is a no-op with no session, and must not panic.
	u.DrainSTT()
	if len(u.input) != 0 {
		t.Fatalf("cancel produced text %q", string(u.input))
	}
}

// TestEscapeDuringRecording: Escape stops a live take before it would clear
// the textarea, so the user's words survive a mistaken Escape.
func TestEscapeDuringRecording(t *testing.T) {
	u := newSTTTestUI(t, &fakeSTT{name: "fake"})
	sess, _ := StartSTTSession(u.STT, fakeRecorderPath(t), "", false)
	if err := sess.Record(); err != nil {
		t.Fatal(err)
	}
	u.STTSess = sess
	u.input = []rune("keep me")

	if !u.Key(0, ksEscape) {
		t.Fatal("Escape during a take should be consumed")
	}
	if u.STTSess != nil {
		t.Fatal("Escape should have cancelled the take")
	}
	if string(u.input) != "keep me" {
		t.Fatalf("Escape ate the textarea too: %q", string(u.input))
	}
}

// TestFitCols covers the status-line clipping: a long mic status must never
// spill past the textarea border, and a short one must be left alone.
func TestFitCols(t *testing.T) {
	if got := fitCols("Recording… (press again to stop)", 12); got != "Recording… …" {
		t.Fatalf("fitCols = %q", got)
	}
	if got := fitCols("Recording… (press again to stop)", 40); got != "Recording… (press again to stop)" {
		t.Fatalf("a line that fits must be untouched, got %q", got)
	}
	if got := fitCols("hi", 10); got != "hi" {
		t.Fatalf("short text should be untouched, got %q", got)
	}
	// Exact width and a 1-column floor.
	if got := fitCols("abcd", 4); got != "abcd" {
		t.Fatalf("exact width should be untouched, got %q", got)
	}
	if got := fitCols("abcd", 1); got != "a" {
		t.Fatalf("one column = %q", got)
	}
}
