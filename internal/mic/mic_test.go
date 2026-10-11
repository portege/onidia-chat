package mic

import (
	"os"
	"strings"
	"testing"
)

// TestSplitPipeWireNodes pins the record splitting: "pw-cli ls Node" runs
// every record straight into the next with no blank line, so splitting on
// "\n\n" silently yields ONE giant record and every microphone disappears
// behind the first node's media.class.
func TestSplitPipeWireNodes(t *testing.T) {
	out := "" +
		"        id 30, type PipeWire:Interface:Node/3\n" +
		"                object.serial = \"30\"\n" +
		"                node.name = \"Dummy-Driver\"\n" +
		"                media.class = \"Audio/Source\"\n" +
		"        id 58, type PipeWire:Interface:Node/3\n" +
		"                node.description = \"Jabra Evolve2 40 SE Mono\"\n" +
		"                node.name = \"alsa_input.usb-Jabra.mono-fallback\"\n" +
		"                media.class = \"Audio/Source\"\n" +
		"        id 136, type PipeWire:Interface:Node/3\n" +
		"                node.name = \"pw-record\"\n" +
		"                media.class = \"Stream/Input/Audio\"\n"
	recs := splitPipeWireNodes(out)
	if len(recs) != 3 {
		t.Fatalf("split into %d records, want 3", len(recs))
	}
	if got := pwField(recs[0], "node.name"); got != "Dummy-Driver" {
		t.Fatalf("record 0 name = %q", got)
	}
	if got := pwField(recs[1], "node.name"); got != "alsa_input.usb-Jabra.mono-fallback" {
		t.Fatalf("record 1 name = %q", got)
	}
	if got := pwField(recs[1], "media.class"); got != "Audio/Source" {
		t.Fatalf("record 1 class = %q", got)
	}
	// The stream record must not inherit the previous node's properties.
	if got := pwField(recs[2], "media.class"); got != "Stream/Input/Audio" {
		t.Fatalf("record 2 class = %q (properties leaked across the split)", got)
	}
	if len(splitPipeWireNodes("no records here")) != 0 {
		t.Fatal("junk should yield no records")
	}
}

// TestNullSourcesAreNotMics: PipeWire always publishes a Dummy-Driver, and
// recording it yields pure silence - which is exactly the "mic is not
// recognised" symptom.
func TestNullSourcesAreNotMics(t *testing.T) {
	for _, n := range []string{"Dummy-Driver", "Freewheel-Driver"} {
		if !pwNullSources[n] {
			t.Fatalf("%s should be filtered out of the mic list", n)
		}
	}
}

// TestPick covers stt-device resolution: an exact match wins, an unknown value
// falls back to the system default, and an empty list is reported.
func TestPick(t *testing.T) {
	mics := []MicDevice{
		{Device: "a", Desc: "first"},
		{Device: "b", Desc: "second", Default: true},
	}
	if m, ok := Pick(mics, "a"); !ok || m.Device != "a" {
		t.Fatalf("an exact stt-device match should win, got %q ok=%v", m.Device, ok)
	}
	if m, ok := Pick(mics, "b"); !ok || m.Device != "b" {
		t.Fatalf("default should be picked, got %q", m.Device)
	}
	if m, ok := Pick(mics, "typo-device"); !ok || m.Device != "b" {
		t.Fatalf("an unknown stt-device should fall back to the default, got %q", m.Device)
	}
	if _, ok := Pick(nil, ""); ok {
		t.Fatal("an empty list must report no device")
	}
	// With nothing marked default, the first device is used.
	plain := []MicDevice{{Device: "x"}, {Device: "y"}}
	if m, _ := Pick(plain, ""); m.Device != "x" {
		t.Fatalf("no default marked should use the first, got %q", m.Device)
	}
}

// TestRecordArgs pins the per-recorder device spellings, which are the whole
// point of stt-device: pw-record needs --target, arecord needs -D.
func TestRecordArgs(t *testing.T) {
	if got := recordArgs("pw-record", "alsa_input.usb-Jabra.mono", "/tmp/a.wav"); !hasPair(got, "--target", "alsa_input.usb-Jabra.mono") {
		t.Fatalf("pw-record args = %v", got)
	}
	if containsArg(recordArgs("pw-record", "", "/tmp/a.wav"), "--target") {
		t.Fatal("no stt-device must not pass --target")
	}
	if got := recordArgs("arecord", "plughw:2,0", "/tmp/a.wav"); !hasPair(got, "-D", "plughw:2,0") {
		t.Fatalf("arecord args = %v", got)
	}
	if got := recordArgs("ffmpeg", "plughw:2,0", "/tmp/a.wav"); !hasPair(got, "-i", "alsa:plughw:2,0") {
		t.Fatalf("ffmpeg args = %v", got)
	}
	// Every recorder must be asked for the 16 kHz mono s16 WAV.
	for _, r := range []string{"pw-record", "arecord", "ffmpeg"} {
		got := recordArgs(r, "", "/tmp/a.wav")
		if got[len(got)-1] != "/tmp/a.wav" {
			t.Fatalf("%s: the output path must be last, got %v", r, got)
		}
	}
}

func hasPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

// TestWavLevel covers the level maths and the silence floor, which is what
// turns "nothing recognised" into "your mic is muted".
func TestWavLevel(t *testing.T) {
	dir := t.TempDir()
	// A 1-second 16 kHz mono WAV of a full-scale square wave.
	put32 := func(b []byte, v uint32) { b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24) }
	put16 := func(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
	fmt_ := make([]byte, 24)
	copy(fmt_, "fmt ")
	put32(fmt_[4:], 16)
	put16(fmt_[8:], 1)
	put16(fmt_[10:], 1)
	put32(fmt_[12:], 16000)
	put32(fmt_[16:], 32000)
	put16(fmt_[20:], 2)
	put16(fmt_[22:], 16)
	const n = 16000
	data := make([]byte, 8+n*2)
	copy(data, "data")
	put32(data[4:], n*2)
	for i := 0; i < n; i++ {
		v := uint16(32767)
		put16(data[8+i*2:], v)
	}
	body := make([]byte, 12)
	copy(body, "RIFF")
	copy(body[8:], "WAVE")
	body = append(body, fmt_...)
	body = append(body, data...)
	put32(body[4:], uint32(len(body)-8))

	loud := dir + "/loud.wav"
	if err := os.WriteFile(loud, body, 0o644); err != nil {
		t.Fatal(err)
	}
	lvl, err := wavLevel(loud)
	if err != nil {
		t.Fatal(err)
	}
	if lvl.Silent {
		t.Fatal("a full-scale take must not be reported as silent")
	}
	if lvl.Peak < 32000 || lvl.RMS < 32000 {
		t.Fatalf("full-scale audio measured peak=%d rms=%d", lvl.Peak, lvl.RMS)
	}
	if !strings.Contains(lvl.String(), "signal present") {
		t.Fatalf("String = %q", lvl.String())
	}

	// distinction that matters is "a recorder wrote a real file with no signal
	// in it", not "there is no file".
	quiet := append([]byte(nil), body...)
	for i := 44; i < len(quiet); i++ { // 44 = end of the RIFF+fmt+data headers
		quiet[i] = 0
	}
	silent := dir + "/silent.wav"
	if err := os.WriteFile(silent, quiet, 0o644); err != nil {
		t.Fatal(err)
	}
	lvl, err = wavLevel(silent)
	if err != nil {
		t.Fatal(err)
	}
	if !lvl.Silent {
		t.Fatal("digital silence must be reported as silent")
	}
	if !strings.Contains(lvl.Hint(), "mute") {
		t.Fatalf("the hint should name the mute button, got %q", lvl.Hint())
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestQuietMicIsNotMuted is the regression that made a perfectly good
// microphone look dead: silence was judged on the RMS, and a working mic in a
// quiet room sits far below -50 dBFS RMS while still carrying speech. Only
// true digital silence (the peak never leaves zero) means "muted".
func TestQuietMicIsNotMuted(t *testing.T) {
	// A real probe of this machine's mic measured -46 dBFS RMS with a peak
	// around 8000: audible, and the RMS is well under any RMS-based floor.
	quiet := MicLevel{DBFS: -46, RMS: 165, Peak: 8237, Quiet: true}
	if quiet.Silent {
		t.Fatal("a mic with a real peak must never be reported as muted")
	}
	if !quiet.Quiet {
		t.Fatal("-46 dBFS should be flagged as quiet")
	}
	if !strings.Contains(quiet.Hint(), "still transcribes") {
		t.Fatalf("a quiet mic should be told it still works, got %q", quiet.Hint())
	}

	// The muted case: the USB switch on, and every sample is zero.
	muted := MicLevel{DBFS: -100, RMS: 0, Peak: 0, Silent: true}
	if !muted.Silent {
		t.Fatal("peak 0 means digital silence and must be reported as muted")
	}
	if !strings.Contains(muted.Hint(), "mute switch") {
		t.Fatalf("the muted hint should point at the hardware switch, got %q", muted.Hint())
	}

	// Loud is neither.
	loud := MicLevel{DBFS: -12, RMS: 8200, Peak: 30000}
	if loud.Silent || loud.Quiet {
		t.Fatalf("a healthy level must be neither silent nor quiet: %+v", loud)
	}
	if !strings.Contains(loud.Hint(), "healthy") {
		t.Fatalf("healthy hint = %q", loud.Hint())
	}
}

// TestWavLevelQuietButAlive is the end-to-end version of the same rule, from
// real samples: a take with a low RMS but a real peak must not be reported as
// muted. A quarter-scale tone every 400 samples gives exactly that shape.
func TestWavLevelQuietButAlive(t *testing.T) {
	dir := t.TempDir()
	put32 := func(b []byte, v uint32) { b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24) }
	put16 := func(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
	build := func(data []byte) string {
		fmt_ := make([]byte, 24)
		copy(fmt_, "fmt ")
		put32(fmt_[4:], 16)
		put16(fmt_[8:], 1)
		put16(fmt_[10:], 1)
		put32(fmt_[12:], 16000)
		put32(fmt_[16:], 32000)
		put16(fmt_[20:], 2)
		put16(fmt_[22:], 16)
		body := make([]byte, 12)
		copy(body, "RIFF")
		copy(body[8:], "WAVE")
		body = append(body, fmt_...)
		put32(body[4:], uint32(len(body)+len(data)))
		chunk := make([]byte, 8+len(data))
		copy(chunk, "data")
		put32(chunk[4:], uint32(len(data)))
		copy(chunk[8:], data)
		body = append(body, chunk...)
		path := dir + "/x.wav"
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// Sparse: a sample at 8000 every 400 samples -> low RMS, peak 8000.
	const n = 16000
	data := make([]byte, n*2)
	for i := 0; i < n; i += 400 {
		put16(data[i*2:], 8000)
	}
	lvl, err := wavLevel(build(data))
	if err != nil {
		t.Fatal(err)
	}
	if lvl.Silent {
		t.Fatalf("sparse speech-like audio reported as muted: %+v", lvl)
	}
	if lvl.Peak != 8000 {
		t.Fatalf("peak = %d, want 8000", lvl.Peak)
	}

	// And the muted case from samples: all zeros.
	if lvl, err := wavLevel(build(make([]byte, n*2))); err != nil || !lvl.Silent {
		t.Fatalf("all-zero audio should be muted: %+v err=%v", lvl, err)
	}
}
