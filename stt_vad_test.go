package main

// Tests for end-of-speech detection. The watcher reads a WAV that is still
// growing, so these build audio incrementally rather than writing a finished
// file: the point is that a header can be short, a poll can land mid-sample,
// and a take is a stream of frames whose level crosses a threshold in the
// middle - not that a complete recording can be measured after the fact.

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// wavBuilder writes a 16 kHz mono s16 WAV the way a recorder would: a complete
// header up front, then samples appended as they are captured.
type wavBuilder struct {
	t    *testing.T
	path string
	f    *os.File
	n    int // samples written
}

func newWavBuilder(t *testing.T) *wavBuilder {
	t.Helper()
	path := filepath.Join(t.TempDir(), "growing.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	b := &wavBuilder{t: t, path: path, f: f}
	// A 44-byte canonical header with placeholder sizes, as a recorder does.
	hdr := make([]byte, 44)
	copy(hdr, "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], 36)
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(hdr[20:], 1)  // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1)  // mono
	binary.LittleEndian.PutUint32(hdr[24:], sttSampleRate)
	binary.LittleEndian.PutUint32(hdr[28:], sttSampleRate*2)
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	if _, err := f.Write(hdr); err != nil {
		t.Fatal(err)
	}
	return b
}

// tone appends ms of a sine at the given peak amplitude.
func (b *wavBuilder) tone(ms int, peak int) {
	b.t.Helper()
	for i := 0; i < sttSampleRate*ms/1000; i++ {
		s := int16(float64(peak) * math.Sin(2*math.Pi*220*float64(i)/sttSampleRate))
		var raw [2]byte
		binary.LittleEndian.PutUint16(raw[:], uint16(s))
		if _, err := b.f.Write(raw[:]); err != nil {
			b.t.Fatal(err)
		}
		b.n++
	}
}

// close patches the RIFF and data sizes the way a recorder does when stopped,
// and rewinds so the watcher can read the finished file.
func (b *wavBuilder) close() {
	b.t.Helper()
	if _, err := b.f.Write(nil); err != nil {
		b.t.Fatal(err)
	}
	dataBytes := uint32(b.n * 2)
	hdr := make([]byte, 44)
	copy(hdr, "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], 36+dataBytes)
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], 1)
	binary.LittleEndian.PutUint32(hdr[24:], sttSampleRate)
	binary.LittleEndian.PutUint32(hdr[28:], sttSampleRate*2)
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], dataBytes)
	if _, err := b.f.WriteAt(hdr, 0); err != nil {
		b.t.Fatal(err)
	}
	if err := b.f.Close(); err != nil {
		b.t.Fatal(err)
	}
}

// stepN runs n watcher polls synchronously, so a test does not depend on the
// real ticker. It returns whether the take was declared over.
func stepN(v *sttVAD, n int) bool {
	for i := 0; i < n; i++ {
		if v.step() {
			return true
		}
	}
	return false
}

// newTestVAD builds a watcher over path. The minimum-audio guard is on audio
// time, so there is nothing to fast-forward here: a test reaches it by writing
// enough audio.
func newTestVAD(path string) *sttVAD { return &sttVAD{path: path} }

// TestVADEndsOnSilence is the case the setting exists for: a burst of speech
// followed by a real pause must end the take, and only after the whole pause.
func TestVADEndsOnSilence(t *testing.T) {
	b := newWavBuilder(t)
	b.tone(1500, 4000) // a second and a half of speech
	v := newTestVAD(b.path)
	if stepN(v, 6) {
		t.Fatal("the take ended while the user was still talking")
	}
	if !v.seen {
		t.Fatal("speech at peak 4000 should have registered")
	}
	b.tone(400, 30) // a short gap
	if stepN(v, 3) {
		t.Fatal("the take ended on a gap far shorter than vadSilence")
	}
	b.tone(int(vadSilence/time.Millisecond)+300, 20) // then a real pause
	if !stepN(v, 8) {
		t.Fatal("the take should have ended after a full silence window")
	}
}

// TestVADIgnoresSilenceBeforeSpeech is the guard that makes a noisy room safe:
// a take that has never heard anything must never be declared over, however
// long the quiet goes on, or the app would transcribe nothing and look broken.
func TestVADIgnoresSilenceBeforeSpeech(t *testing.T) {
	b := newWavBuilder(t)
	b.tone(3000, 20) // three seconds of almost nothing
	v := newTestVAD(b.path)
	if stepN(v, 20) {
		t.Fatal("a take that has heard nothing was ended anyway")
	}
	if v.seen {
		t.Error("a peak of 20 should not count as speech")
	}
}

// TestVADKeepsListeningWhileSpeechReturns is the mid-sentence case: a pause
// shorter than the window must not end the take, and speech afterwards must
// reset the silence run completely.
func TestVADKeepsListeningWhileSpeechReturns(t *testing.T) {
	b := newWavBuilder(t)
	b.tone(1200, 3000)
	v := newTestVAD(b.path)
	stepN(v, 4)
	// Nearly the whole window of quiet...
	b.tone(int(vadSilence/time.Millisecond)-350, 15)
	stepN(v, 6)
	if v.ended() {
		t.Fatal("ended before the window was up")
	}
	// ...then the speaker comes back.
	b.tone(300, 3500)
	stepN(v, 3)
	if v.quietRun != 0 {
		t.Errorf("quietRun = %d after speech returned, want it reset to 0", v.quietRun)
	}
	if v.ended() {
		t.Fatal("ended although the speaker came back")
	}
}

// TestVADShortTakesCannotBeAutoStopped pins why there is no separate
// minimum-length guard: a take can only be ended by a full silence window, so
// a take shorter than that cannot be auto-stopped at all. If this test ever
// fails, a guard has been removed that was not in fact dead.
func TestVADShortTakesCannotBeAutoStopped(t *testing.T) {
	for _, tc := range []struct{ speech, quiet int }{
		{200, 400},  // a cough
		{300, 700},  // a door
		{500, 1000}, // a short word
	} {
		b := newWavBuilder(t)
		b.tone(tc.speech, 5000)
		b.tone(tc.quiet, 10)
		v := newTestVAD(b.path)
		if stepN(v, 30) {
			t.Errorf("a %dms take was ended after %dms of silence: the whole take is "+
				"shorter than vadSilence, so it should be untouchable", tc.speech, tc.quiet)
		}
	}
}

// TestVADAdaptsToAQuietMic: the absolute floor is low, but so is a quiet
// headset. The relative floor is what makes it work - once the take has heard
// something, quiet-but-clearly-louder-than-nothing still counts as speech.
func TestVADAdaptsToAQuietMic(t *testing.T) {
	b := newWavBuilder(t)
	b.tone(1000, 400) // quiet but real: about -38 dBFS peak
	v := newTestVAD(b.path)
	stepN(v, 4)
	if !v.seen {
		t.Fatalf("a peak of 400 should clear the absolute floor of %d", vadFloorAbs)
	}
	// Now the relative floor has taken over; 100 is 25% of the take's best.
	if got := v.floorLocked(); got <= 100 {
		t.Errorf("floor = %d, want it raised above the quiet level by vadFloorRel", got)
	}
	b.tone(int(vadSilence/time.Millisecond)+300, 90) // 22% of 400: above 72
	if !stepN(v, 8) {
		t.Error("a pause after a quiet-but-present voice should still end the take")
	}
}

// TestVADIgnoresAnUnusableFile: the watcher must not decide anything from a
// file that is empty, missing, or still short of a data chunk.
func TestVADIgnoresAnUnusableFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"empty.wav", "header-only.wav", "junk.wav"} {
		if stepN(newTestVAD(filepath.Join(dir, name)), 3) {
			t.Errorf("%s: the watcher decided something from an unusable file", name)
		}
	}
	junk := filepath.Join(dir, "zeros.wav")
	if err := os.WriteFile(junk, make([]byte, 2048), 0o644); err != nil { // all zeros
		t.Fatal(err)
	}
	if stepN(newTestVAD(junk), 3) {
		t.Error("a file of zeros was treated as audio")
	}
}

// TestVADLevelRisesWithSpeech: the meter is the only way a user can tell the
// floor is wrong, so it has to actually track the input.
func TestVADLevelRisesWithSpeech(t *testing.T) {
	b := newWavBuilder(t)
	b.tone(300, 6000)
	v := newTestVAD(b.path)
	stepN(v, 3)
	loud := v.Level()
	if loud < 40 {
		t.Errorf("level = %d for a peak of 6000, want a clearly high reading", loud)
	}
	b2 := newWavBuilder(t)
	b2.tone(300, 300)
	v2 := newTestVAD(b2.path)
	stepN(v2, 3)
	if q := v2.Level(); q >= loud {
		t.Errorf("a quiet take reads %d, not below the loud take's %d", q, loud)
	}
	if l := v2.Level(); l < 0 || l > 100 {
		t.Errorf("level = %d, want 0..100", l)
	}
}

// TestVADStopIsIdempotent: stop is called from finish on every path, and a
// watcher and a click can race to end the same take.
func TestVADStopIsIdempotent(t *testing.T) {
	b := newWavBuilder(t)
	b.tone(200, 1000)
	v := newSTTVAD(b.path, func() {})
	v.stop()
	v.stop()
	b.close()
}

// TestVADEndOfSpeechEndsTheTake: the watcher's callback is what actually closes
// a hands-free take, so it has to fire exactly once and stop the watcher.
func TestVADEndOfSpeechEndsTheTake(t *testing.T) {
	b := newWavBuilder(t)
	b.tone(1000, 4000)
	fired := make(chan struct{}, 4)
	v := newSTTVAD(b.path, func() { fired <- struct{}{} })
	defer v.stop()
	b.tone(int(vadSilence/time.Millisecond)+600, 10)
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the end-of-speech callback never fired")
	}
	if !v.ended() {
		t.Error("ended() is false after the callback fired")
	}
	// And it fires once, not once per poll.
	select {
	case <-fired:
		t.Error("the callback fired more than once")
	case <-time.After(300 * time.Millisecond):
	}
}
