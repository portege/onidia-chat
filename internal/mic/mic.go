// Package mic finds the microphones on a machine and measures whether one
// actually carries signal.
//
// "The mic is not recognised" is the most common speech-input problem, and it
// has three very different causes: no capture device at all, the recorder
// linked to a monitor or a null node instead of a real input, or a
// muted/quiet device. So chat-app can be pointed at a specific device
// (stt-device in chat-app.ini), and this package supplies the listing that
// makes choosing one possible plus a short level probe that separates "no
// device" from "device, but silence".
//
// It lives in its own package because both the app and the standalone
// preflight CLI need it, and the CLI is a separate binary.
package mic

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// sttErrf mirrors the app's user-facing error type: a plain message that the
// CLI can print without unwrapping.
type errString struct{ msg string }

func (e *errString) Error() string { return e.msg }

func errf(format string, args ...any) error {
	return &errString{msg: fmt.Sprintf(format, args...)}
}

// sttSampleRate matches chat-app's capture rate (stt.go). The probe has to
// record exactly what a real take records.
const sttSampleRate = 16000

// MicDevice is one capture endpoint, in the spelling the chosen recorder
// accepts for stt-device.
type MicDevice struct {
	Device  string // the value to put in stt-device
	Desc    string // human label ("Jabra Evolve2 40 SE Mono")
	Default bool   // the one the system picks with no stt-device set
}

// List enumerates capture devices for the given recorder, using the
// enumerator that matches it: PipeWire for pw-record/parecord, ALSA for
// arecord and ffmpeg. The returned Device values are directly usable as
// stt-device for that recorder.
func List(recorder string) ([]MicDevice, error) {
	switch filepath.Base(recorder) {
	case "pw-record", "parecord":
		if mics, err := listPipeWireMics(); err == nil && len(mics) > 0 {
			return mics, nil
		}
		// A PipeWire session without pw-cli still gets the ALSA list; those
		// values are what arecord wants anyway.
		return listALSAMics()
	case "arecord", "ffmpeg":
		return listALSAMics()
	default:
		if mics, err := listPipeWireMics(); err == nil && len(mics) > 0 {
			return mics, nil
		}
		return listALSAMics()
	}
}

// pwNullSources are PipeWire's virtual capture nodes. They are always
// present, they are never a microphone, and - crucially - they record pure
// silence, so letting one be picked is exactly how "the mic is not
// recognised" happens.
var pwNullSources = map[string]bool{
	"Dummy-Driver":     true,
	"Freewheel-Driver": true,
}

// listPipeWireMics reads the node list and keeps the real Audio/Source
// nodes, which are exactly the capture endpoints pw-record --target accepts.
func listPipeWireMics() ([]MicDevice, error) {
	if _, err := exec.LookPath("pw-cli"); err != nil {
		return nil, errf("pw-cli not found (install pipewire-tools to list PipeWire microphones)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pw-cli", "ls", "Node").Output()
	if err != nil {
		return nil, errf("pw-cli ls Node: %v", err)
	}
	// The source WirePlumber marks with "*" is the one the recorder would
	// link to on its own, so the app defaults to the same one.
	want := pipeWireDefaultSourceLabel()

	var mics []MicDevice
	for _, rec := range splitPipeWireNodes(string(out)) {
		if !strings.Contains(rec, `media.class = "Audio/Source"`) {
			continue
		}
		name := pwField(rec, "node.name")
		if name == "" || pwNullSources[name] {
			continue
		}
		nick := pwField(rec, "node.nick")
		full := pwField(rec, "node.description")
		desc := nick
		if desc == "" {
			desc = full
		}
		if desc == "" {
			desc = name
		}
		// wpctl names the source with the description ("... Mono") while
		// node.nick is the shorter device label, so match against both.
		isDefault := want != "" && (want == nick || want == full)
		mics = append(mics, MicDevice{Device: name, Desc: desc, Default: isDefault})
	}
	if len(mics) == 0 {
		return nil, errf("PipeWire reports no real capture device - is a microphone plugged in? " +
			"(only the null Dummy-Driver is present)")
	}
	// With no active marker, the first real source is what gets recorded.
	if want == "" {
		mics[0].Default = true
	}
	return mics, nil
}

var (
	pwFieldRe   = regexp.MustCompile(`(?m)^\s*([\w.]+)\s*=\s*"([^"]*)"`)
	pwDefaultRe = regexp.MustCompile(`\*\s*(\d+)\.\s*([^\[]+)`)
	pwSrcRe     = regexp.MustCompile(`(?s)Sources:(.*?)(?:Sinks:|Streams:|\z)`)
)

// pwNodeStartRe marks the beginning of a record in "pw-cli ls Node" output.
// The records are NOT separated by blank lines - every one runs straight into
// the next - so they have to be split on their "id N, type ..." header.
var pwNodeStartRe = regexp.MustCompile(`(?m)^\s*id \d+, type `)

// splitPipeWireNodes cuts pw-cli output into one string per node.
func splitPipeWireNodes(out string) []string {
	locs := pwNodeStartRe.FindAllStringIndex(out, -1)
	recs := make([]string, 0, len(locs))
	for i, loc := range locs {
		end := len(out)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		recs = append(recs, out[loc[0]:end])
	}
	return recs
}

// pwField pulls one property value out of a pw-cli node record.
func pwField(rec, key string) string {
	for _, m := range pwFieldRe.FindAllStringSubmatch(rec, -1) {
		if m[1] == key {
			return m[2]
		}
	}
	return ""
}

// pipeWireDefaultSourceLabel returns the human name of the source
// WirePlumber marks active with "*" in "wpctl status", or "" when wpctl is
// unavailable. The label (not the node id) is what gets matched: the ids
// wpctl prints are not always the pw-cli node ids, but the names are the
// same string in both listings.
func pipeWireDefaultSourceLabel() string {
	if _, err := exec.LookPath("wpctl"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wpctl", "status").Output()
	if err != nil {
		return ""
	}
	block := pwSrcRe.FindStringSubmatch(string(out))
	if len(block) < 2 {
		return ""
	}
	m := pwDefaultRe.FindStringSubmatch(block[1])
	if len(m) < 3 {
		return ""
	}
	return strings.TrimSpace(m[2])
}

// alsaCardRe pulls "card 2: SE [Jabra...]" and "device 0: USB Audio" out of
// arecord -l, which is the only place ALSA names its capture hardware.
var alsaCardRe = regexp.MustCompile(`(?m)^card (\d+): (.+?)\[(.+?)\], device (\d+): (.+?) \[`)

// listALSAMics turns arecord -l output into devices arecord/ffmpeg accept.
func listALSAMics() ([]MicDevice, error) {
	if _, err := exec.LookPath("arecord"); err != nil {
		return nil, errf("arecord not found (install alsa-utils to list microphones)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "arecord", "-l").Output()
	if err != nil {
		return nil, errf("arecord -l: %v", err)
	}
	var mics []MicDevice
	for _, m := range alsaCardRe.FindAllStringSubmatch(string(out), -1) {
		mics = append(mics, MicDevice{
			Device: fmt.Sprintf("plughw:%s,%s", m[1], m[4]),
			Desc:   fmt.Sprintf("%s %s (%s)", strings.TrimSpace(m[2]), strings.TrimSpace(m[3]), strings.TrimSpace(m[5])),
		})
	}
	if len(mics) == 0 {
		return nil, errf("ALSA reports no capture devices - is a microphone plugged in?")
	}
	mics[0].Default = true
	return mics, nil
}

// sttMicProbeSeconds is how long the level probe records: long enough to see
// speech, short enough to feel instant.
const ProbeSeconds = 2.0

// Muted thresholds. A USB headset with its mute switch on, or a firmware
// level mute, emits EXACT zeros - not merely quiet audio. That is the only
// thing worth reporting as "muted", and it has to be judged on the PEAK
// rather than the RMS: a working microphone in a quiet room sits at a very
// low RMS (well under -50 dBFS) while still carrying speech, and gating on
// the RMS declares a perfectly good mic dead.
const (
	// MutePeak is the peak sample below which a take counts as digital
	// silence (about -54 dBFS on a 16-bit scale).
	MutePeak = 64
	// QuietRMSDBFS is the RMS below which the signal exists but is low; the
	// recognisers normalise this themselves, so it is advice, not a fault.
	QuietRMSDBFS = -35.0
)

// MicLevel is the measured loudness of a short probe recording. dBFS is the
// number that matters.
type MicLevel struct {
	Seconds float64
	Peak    int     // 0..32767
	RMS     int     // 0..32767
	DBFS    float64 // 20*log10(rms/32768)
	Silent  bool    // digital silence: the device recorded, but no signal at all
	Quiet   bool    // signal is present but low
}

// Probe records for about ProbeSeconds and reports the level. It is what
// separates "no microphone" from "microphone, but nothing is reaching it" -
// two failures that need completely different fixes.
func Probe(recorder, device string) (MicLevel, error) {
	path, stop, cleanup, err := startRecorder(recorder, device)
	if err != nil {
		return MicLevel{}, err
	}
	defer cleanup()
	time.Sleep(time.Duration(ProbeSeconds * float64(time.Second)))
	stop()
	return wavLevel(path)
}

// wavLevel measures the samples of a WAV, ignoring the header.
func wavLevel(path string) (MicLevel, error) {
	pcm, rate, err := readWavPCM(path)
	if err != nil {
		return MicLevel{}, err
	}
	if rate <= 0 {
		rate = sttSampleRate
	}
	var sum, peak float64
	n := len(pcm) / 2
	for i := 0; i+1 < len(pcm); i += 2 {
		s := float64(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8)) // LE s16
		sum += s * s
		if a := math.Abs(s); a > peak {
			peak = a
		}
	}
	if n == 0 {
		return MicLevel{}, errf("the recording is empty")
	}
	rms := math.Sqrt(sum / float64(n))
	db := -100.0
	if rms > 0 {
		db = 20 * math.Log10(rms/32768)
	}
	return MicLevel{
		Seconds: float64(n) / float64(rate),
		Peak:    int(peak),
		RMS:     int(rms),
		DBFS:    db,
		Silent:  int(peak) < MutePeak,
		Quiet:   db < QuietRMSDBFS,
	}, nil
}

// String renders a level for the preflight report and the CLI.
func (l MicLevel) String() string {
	if l.Silent {
		return fmt.Sprintf("%.0f dBFS (peak %d) - effectively SILENT, check the mute button and input gain",
			l.DBFS, l.Peak)
	}
	return fmt.Sprintf("%.0f dBFS (peak %d) - signal present", l.DBFS, l.Peak)
}

// Hint is the actionable half of a probe: what the level means and what to
// do about it.
func (l MicLevel) Hint() string {
	switch {
	case l.Silent:
		return "the recording is all zeros, so nothing is reaching the device - check the mic's own " +
			"mute switch, then the input gain, then pick a different stt-device"
	case l.Quiet:
		return "signal is present but quiet - move closer to the mic or raise its input gain " +
			"(this still transcribes)"
	default:
		return "signal level is healthy"
	}
}

// Describe renders a device list for the CLI and the preflight report.
// The device in use is marked with "*".
func Describe(mics []MicDevice) string {
	var b strings.Builder
	for _, m := range mics {
		mark := "  "
		if m.Default {
			mark = "* "
		}
		fmt.Fprintf(&b, "\n     %s%-44s %s", mark, m.Device, m.Desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Pick resolves stt-device against the listed devices: the named one when
// it matches, otherwise the system default.
func Pick(mics []MicDevice, want string) (MicDevice, bool) {
	if want != "" {
		for _, m := range mics {
			if m.Device == want {
				return m, true
			}
		}
	}
	for _, m := range mics {
		if m.Default {
			return m, true
		}
	}
	if len(mics) > 0 {
		return mics[0], true
	}
	return MicDevice{}, false
}

// ---- recording (the probe's own, self-contained) ---------------------------

// startRecorder runs the given recorder for ~2s into a temp WAV. The probe
// needs its own copy rather than chat-app's so this package stays independent
// of the app: it only has to produce a WAV it can measure, not a real take.
func startRecorder(recorder, device string) (path string, stop func(), cleanup func(), err error) {
	f, err := os.CreateTemp("", "mic-probe-*.wav")
	if err != nil {
		return "", nil, nil, err
	}
	path = f.Name()
	f.Close()
	cmd := exec.Command(recorder, recordArgs(recorder, device, path)...)
	if err := cmd.Start(); err != nil {
		os.Remove(path)
		return "", nil, nil, errf("start %s: %v", filepath.Base(recorder), err)
	}
	cleanup = func() { os.Remove(path) }
	stop = func() {
		// SIGINT makes pw-record and arecord finalise the RIFF header; SIGKILL
		// would leave a truncated file.
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-done
		}
	}
	return path, stop, cleanup, nil
}

// recordArgs mirrors chat-app's sttArgs: 16 kHz mono s16 WAV, with the
// device spelled the way this recorder expects it.
func recordArgs(recorder, device, path string) []string {
	switch filepath.Base(recorder) {
	case "pw-record", "parecord":
		args := []string{"--rate", fmt.Sprint(sttSampleRate), "--channels", "1", "--format", "s16"}
		if device != "" {
			args = append(args, "--target", device)
		}
		return append(args, path)
	case "arecord":
		args := []string{"-q", "-f", "S16_LE", "-r", fmt.Sprint(sttSampleRate), "-c", "1", "-t", "wav", path}
		if device != "" {
			args = append(args, "-D", device)
		}
		return args
	default: // ffmpeg
		src := "default"
		if device != "" {
			src = "alsa:" + device
		}
		return []string{"-hide_banner", "-loglevel", "error", "-f", "alsa",
			"-i", src, "-ac", "1", "-ar", fmt.Sprint(sttSampleRate), "-y", path}
	}
}

// readWavPCM strips a WAV container and returns the samples plus the rate.
// The header is parsed rather than skipped at a fixed 44 bytes, because
// recorders pad their chunk lists differently.
func readWavPCM(path string) (data []byte, rate int, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, errf("recorder produced no valid WAV header (was anything captured?)")
	}
	le16 := func(b []byte) int { return int(b[0]) | int(b[1])<<8 }
	le32 := func(b []byte) int {
		return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24
	}
	var bits, channels int
	for pos := 12; pos+8 <= len(raw); {
		id := string(raw[pos : pos+4])
		size := le32(raw[pos+4 : pos+8])
		body := pos + 8
		if size < 0 || body+size > len(raw) {
			size = len(raw) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, errf("short WAV fmt chunk")
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
		return nil, 0, errf("WAV has no data chunk")
	}
	if bits != 16 {
		return nil, 0, errf("unsupported WAV sample size %d bits (want 16)", bits)
	}
	if channels != 1 {
		return nil, 0, errf("unsupported WAV channel count %d (want mono)", channels)
	}
	return data, rate, nil
}

// Level is wavLevel exported for the app's own backends, which log the input
// level alongside a transcription so an empty result can be told apart from a
// healthy recording the model did not understand.
func Level(path string) (MicLevel, error) { return wavLevel(path) }
