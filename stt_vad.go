package main

// stt_vad.go - end-of-speech detection, so a take can end without the user
// pressing the mic button a second time.
//
// The recorder already writes PCM into the temp WAV while it runs, so "is
// anyone still talking" is answerable by watching that file grow rather than by
// changing how anything is captured. This file owns that watcher and nothing
// else: it reports the end of speech, and STTSession closes the take exactly as
// the 120-second cap closes it.
//
// This is deliberately NOT the stt-whisper-vad option. That one is
// faster-whisper's vad_filter, applied to a finished recording to throw quiet
// takes away; by the time it runs the audio is already captured, so it cannot
// say when anyone stopped talking. It is also off by default because a fixed
// threshold can discard a whole take.
//
// The failure modes here are the same ones that put whisper's VAD off, and
// they are the reason for every guard below:
//
//   - Never end a take that has not heard anything. seenSpeech gates the whole
//     detector, so a take of pure room noise runs to the length cap instead of
//     stopping instantly and transcribing nothing.
//   - Judge on the frame PEAK, not its RMS. internal/mic spells out why: "a
//     working microphone in a quiet room sits at a very low RMS (well under
//     -50 dBFS) while still carrying speech, and gating on the RMS declares a
//     perfectly good mic dead." Peak is what a speech transient actually has.

import (
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// vadPoll is how often the growing WAV is sampled. Short enough that the
	// silence window is accurate to ~100ms, long enough that reading a few
	// kilobytes ten times a second is nothing.
	vadPoll = 100 * time.Millisecond
	// vadFrame is the analysis window: 20ms of 16 kHz mono is 320 samples,
	// long enough for a speech transient to show up in the peak.
	vadFrame = 320
	// vadSilence is how long the level must stay under the floor before the
	// take is considered over. It is a sentence pause, not a gap between
	// words: 1.2s is short enough to feel responsive and long enough to sit
	// through "what I mean is...". Raise this first if takes get cut.
	//
	// This is also the de facto minimum take length, which is why there is no
	// separate short-take guard: a take can only be ended by a full silence
	// window, so it cannot be ended before vadSilence of audio exists. A take
	// too short to matter produces an empty transcript instead, and the empty
	// case is handled where the transcript lands (nothing is armed, nothing is
	// sent). A second, smaller floor would be dead logic.
	vadSilence = 1200 * time.Millisecond
	// vadFloorAbs is a peak that always counts as speech, however quiet the
	// take overall. It sits well above the digital-silence peak of 64 that
	// internal/mic uses for "muted", and below ordinary speech.
	vadFloorAbs = 220
	// vadFloorRel is the fraction of the take's own loudest frame that also
	// counts as speech. This is what keeps a quiet microphone working: the
	// recogniser normalises levels anyway, so relative loudness is the more
	// honest signal.
	vadFloorRel = 0.18
	// vadFrameDur is how much audio one analysis frame covers. The silence
	// window is counted in these rather than in polls, so it means the same
	// thing whatever way the recorder chunks its writes: a burst of two
	// seconds arriving in one poll is two seconds of quiet, not one.
	vadFrameDur = vadFrame * time.Second / sttSampleRate
)

//   - Keep the floor relative as well as absolute, so a quiet headset still
//     counts as speech relative to its own best frame.
//   - Wait a real pause (vadSilence) rather than a gap between words, and never
//     end a take shorter than vadMinAudio.
//
// A noisy room still defeats this - the level never falls and the take runs to

// sttVAD watches one recording for the end of speech.
//
// It is started by STTSession.Record and stopped by STTSession.finish, so its
// lifetime is exactly the recording's. Level is readable from any goroutine
// (the UI reads it each frame to drive the mic meter) and is only ever written
// under mu.
type sttVAD struct {
	path string

	mu    sync.Mutex
	level int // 0..100, the newest frame's loudness, for the meter

	loudest  int    // the take's loudest frame so far, for the relative floor
	seen     bool   // a frame has ever counted as speech
	quietRun int    // frames under the floor in a row, carried across polls
	bytes    int    // how much of the WAV has been analysed
	onEnd    func() // ends the take; runs on this goroutine
	done     atomic.Bool
	stopped  chan struct{}
	stopOnce sync.Once
}

// newSTTVAD starts watching path. onEnd is called once, on the watcher's own
// goroutine, when the end of speech is detected; the session passes its finish
// there so there is a single place a take can end. The caller must still call
// stop when the recording is over; stop is idempotent.
func newSTTVAD(path string, onEnd func()) *sttVAD {
	v := &sttVAD{path: path, onEnd: onEnd, stopped: make(chan struct{})}
	go v.run()
	return v
}

// stop ends the watcher. Safe to call more than once, from any goroutine, and
// after run has already returned - both happen in practice, because finish
// stops the watcher whether the take ended by watcher, by click or by the cap.
func (v *sttVAD) stop() {
	v.stopOnce.Do(func() { close(v.stopped) })
}

// ended reports whether the end of speech was detected.
func (v *sttVAD) ended() bool { return v.done.Load() }

// Level is the current loudness as 0..100, for the mic meter. The scale is
// logarithmic, because speech loudness is: a linear fraction of peak amplitude
// would sit near zero for any normal speaking voice.
func (v *sttVAD) Level() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.level
}

func (v *sttVAD) run() {
	// stop, not close: run returning (end of speech) and finish calling stop
	// are two separate events, and either can arrive first.
	defer v.stop()
	t := time.NewTicker(vadPoll)
	defer t.Stop()
	for {
		select {
		case <-v.stopped:
			return
		case <-t.C:
			if v.step() {
				v.done.Store(true)
				// finish is closeOnce-guarded and touches no UI state, so it is
				// safe to call from here - the same way the length cap does.
				v.onEnd()
				return
			}
		}
	}
}

// step samples the file once and reports whether the take is over.
func (v *sttVAD) step() bool {
	raw, err := os.ReadFile(v.path)
	if err != nil {
		return false // the recorder has not created the file yet
	}
	off, err := wavDataOffset(raw)
	if err != nil {
		return false // partial or absent header; try again next tick
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	// Only whole samples past what has already been analysed, so a poll that
	// lands mid-sample neither double-counts nor drops audio.
	pcm := raw[off+min(v.bytes, len(raw)-off):]
	if pcm == nil {
		return false
	}
	whole := len(pcm) - len(pcm)%2
	pcm, v.bytes = pcm[:whole], v.bytes+whole
	if len(pcm) < vadFrame*2 {
		return false // not even one frame yet
	}

	peak, frames := 0, 0
	for i := 0; i+vadFrame*2 <= len(pcm); i += vadFrame * 2 {
		fp := 0
		for j := 0; j < vadFrame*2; j += 2 {
			s := int(int16(uint16(pcm[i+j]) | uint16(pcm[i+j+1])<<8)) // LE s16
			if s < 0 {
				s = -s
			}
			if s > fp {
				fp = s
			}
		}
		frames++
		if fp > v.loudest {
			v.loudest = fp
		}
		// The silence run is counted per frame and carried across polls, so a
		// recorder that delivers a second of audio at a time is judged the same
		// as one that trickles it out. A single loud frame resets it.
		if fp >= v.floorLocked() {
			v.seen, v.quietRun = true, 0
		} else {
			v.quietRun++
		}
		if fp > peak {
			peak = fp
		}
	}
	if frames == 0 {
		return false
	}
	v.setLevelLocked(peak)

	// Nothing has been heard, so there is nothing to end: room noise runs to
	// the length cap rather than stopping at once.
	if !v.seen {
		return false
	}
	// quietRun is frames, not polls, so the window means the same length of
	// quiet however the recorder chunks its writes - and reaching it at all is
	// why a take shorter than vadSilence cannot be ended.
	return time.Duration(v.quietRun)*vadFrameDur >= vadSilence
}

// floorLocked is the peak a frame must reach to count as speech: the louder of
// a fixed floor and a fraction of the take's best frame. Callers hold mu.
func (v *sttVAD) floorLocked() int {
	if f := int(float64(v.loudest) * vadFloorRel); f > vadFloorAbs {
		return f
	}
	return vadFloorAbs
}

// setLevelLocked maps a peak onto 0..100 for the meter. Callers hold mu.
func (v *sttVAD) setLevelLocked(peak int) {
	const full = 8000 // a peak this high is shouting; treat it as 100%
	if peak <= 0 {
		v.level = 0
		return
	}
	// log10(peak/full + 1) maps 0..full onto 0..1, so the meter spends most of
	// its travel in the range a quiet voice actually occupies.
	pct := int(100 * (math.Log10(float64(peak)/full) + 1))
	v.level = max(0, min(100, pct))
}
