// stt_whisper.go - the local faster-whisper backend.
//
// No cloud, no API key, no AWS permission: it runs a tiny python helper that
// loads a faster-whisper model and prints the transcript on stdout. That keeps
// the heavy dependency (the ctranslate2 / torch wheels) in a virtualenv the
// user owns, instead of vendoring a python runtime into this repo.
//
// The helper is written into the user config dir on first use, so all the
// setup there is, is a `pip install faster-whisper` into a venv. The model
// download (a few hundred MB, once) is faster-whisper's own doing, cached
// under ~/.cache/huggingface.

package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/portege/onidia-chat/internal/mic"
)

const (
	// sttDefaultWhisperModel is the size/speed trade-off that suits a Pi:
	// "tiny" is fast enough to feel live, "base" is noticeably more accurate.
	sttDefaultWhisperModel = "base"
	// sttWhisperHelperName is the generated python helper.
	sttWhisperHelperName = "stt-whisper.py"
)

// sttWhisperHelper is the python program the backend runs. It is a constant
// rather than a shipped file so the binary stays self-contained; the backend
// materialises it on first use.
const sttWhisperHelper = `#!/usr/bin/env python3
"""Transcribe a WAV with faster-whisper. Prints the text on stdout.

argv[1] = model size, argv[2] = WAV path, argv[3] = language (may be ""),
argv[4] = "vad" to enable the voice-activity filter, argv[5] = "debug" to
print diagnostics on stderr.

VAD is OFF by default and that is deliberate: it classifies speech vs
silence with a fixed threshold, and on a quiet headset microphone it can
discard the whole take (faster-whisper reports duration_after_vad = 0.0),
which surfaces as "nothing recognised" no matter how healthy the level probe
said the microphone was.
"""
import sys

try:
    from faster_whisper import WhisperModel
except ImportError:
    sys.stderr.write("faster-whisper is not installed for this interpreter\n")
    sys.exit(3)

size = sys.argv[1] if len(sys.argv) > 1 else "base"
path = sys.argv[2] if len(sys.argv) > 2 else None
language = sys.argv[3] if len(sys.argv) > 3 else ""
use_vad = len(sys.argv) > 4 and sys.argv[4] == "vad"
debug = len(sys.argv) > 5 and sys.argv[5] == "debug"

if not path:
    sys.stderr.write("usage: stt-whisper.py <model> <wav> [lang] [vad] [debug]\n")
    sys.exit(2)

# cpu + int8 is the combination that runs on a Pi without a GPU.
model = WhisperModel(size, device="cpu", compute_type="int8")
kwargs = {"beam_size": 1, "vad_filter": use_vad}
if language:
    kwargs["language"] = language

if debug:
    sys.stderr.write("model=%s vad=%s language=%s\n" % (size, use_vad, language or "auto"))

segments, info = model.transcribe(path, **kwargs)

if debug:
    sys.stderr.write(
        "duration=%.2fs after_vad=%.2fs detected_language=%s prob=%.2f\n"
        % (info.duration, info.duration_after_vad, info.language, info.language_probability)
    )

text = ""
count = 0
for segment in segments:
    count += 1
    if debug:
        sys.stderr.write("  segment %d [%5.2fs-%5.2fs] %r\n" % (count, segment.start, segment.end, segment.text))
    text += segment.text

text = text.strip()
if debug:
    sys.stderr.write("segments=%d chars=%d\n" % (count, len(text)))
sys.stdout.write(text + "\n")
`

type whisperSTT struct {
	language string
	model    string
	cmd      string // the python interpreter that runs the helper
	helper   string // absolute path of the generated helper
	vad      bool   // voice-activity filter (off by default; see the helper)
	debug    bool   // echo the backend's diagnostics into our own log
}

// newWhisperSTT validates the settings. Nothing is written to disk or spawned
// here - the helper is materialised on the first take.
func newWhisperSTT(language, model, pythonCmd string, vad, debug bool) (STT, error) {
	if model = strings.TrimSpace(model); model == "" {
		model = sttDefaultWhisperModel
	}
	// A configured command wins (it can carry a venv python path); otherwise
	// look for an interpreter, preferring one inside a venv the user set up.
	cmd := strings.TrimSpace(pythonCmd)
	if cmd == "" {
		cmd = findWhisperPython()
	}
	if cmd == "" {
		return nil, sttErrf("stt = whisper needs a python with faster-whisper: pip install faster-whisper, " +
			"or point stt-whisper-cmd at that interpreter")
	}
	return &whisperSTT{
		language: strings.TrimSpace(language),
		model:    model,
		cmd:      cmd,
		helper:   filepath.Join(sttUserDir(), sttWhisperHelperName),
		vad:      vad,
		debug:    debug,
	}, nil
}

func (w *whisperSTT) Name() string { return "whisper" }

// findWhisperPython looks for an interpreter to run the helper with. A local
// ./venv or ~/.venv is preferred over the system python, so a project-scoped
// install is picked up without extra configuration.
func findWhisperPython() string {
	cands := []string{
		"venv/bin/python3", ".venv/bin/python3",
		filepath.Join(os.Getenv("HOME"), ".venv", "bin", "python3"),
		"python3", "python",
	}
	for _, c := range cands {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// sttUserDir is the config dir the helper is written to, mirroring where the
// app already keeps its own files. Falls back to the temp dir when HOME is
// not usable.
//
// Same frozen "chat-app" directory as userConfigDir() - this is where an
// already-installed stt-whisper.py lives, and pointing somewhere new would
// download the helper again and leave the old one behind.
func sttUserDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "chat-app")
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "chat-app")
	}
	return os.TempDir()
}

// writeHelper materialises the helper script if it is missing or stale.
func (w *whisperSTT) writeHelper() error {
	if existing, err := os.ReadFile(w.helper); err == nil && string(existing) == sttWhisperHelper {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(w.helper), 0o755); err != nil {
		return sttErrf("cannot create %s: %v", filepath.Dir(w.helper), err)
	}
	if err := os.WriteFile(w.helper, []byte(sttWhisperHelper), 0o755); err != nil {
		return sttErrf("cannot write %s: %v", w.helper, err)
	}
	return nil
}

func (w *whisperSTT) Transcribe(ctx context.Context, wavPath string) (string, error) {
	if err := w.writeHelper(); err != nil {
		return "", err
	}
	// Everything about a take that explains "nothing recognised" lives in
	// these few lines, so with -stt-debug every one of them is reported.
	args := []string{w.helper, w.model, wavPath, w.language}
	if w.vad {
		args = append(args, "vad")
	} else {
		args = append(args, "novad")
	}
	if w.debug {
		args = append(args, "debug")
	}
	level, _ := mic.Level(wavPath)
	log.Printf("stt: whisper %s file=%s %.2fs %s", w.model, filepath.Base(wavPath), level.Seconds, level)
	log.Printf("stt: whisper running %s %s (vad=%v lang=%q debug=%v)",
		w.cmd, strings.Join(args[1:], " "), w.vad, w.language, w.debug)

	var stderr bytes.Buffer
	start := time.Now()
	cmd := exec.CommandContext(ctx, w.cmd, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	took := time.Since(start)

	// The helper's diagnostics are the whole point of the debug mode: the
	// duration_after_vad line is what proves a VAD-filtered take was
	// emptied before it ever reached the model.
	if diag := strings.TrimSpace(stderr.String()); diag != "" {
		if w.debug {
			log.Printf("stt: whisper diagnostics (%s):\n%s", took.Round(time.Millisecond), diag)
		} else if err != nil {
			log.Printf("stt: whisper stderr: %s", diag)
		}
	}
	if err != nil {
		// Exit 3 is our own "no faster-whisper" marker; anything else is the
		// interpreter complaining, and stderr is the actionable part.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(msg, "faster-whisper is not installed") {
			return "", sttErrf("faster-whisper is missing from %s - run: %s -m pip install faster-whisper",
				w.cmd, w.cmd)
		}
		return "", sttErrf("whisper failed: %s", firstLine(msg))
	}
	if text := strings.TrimSpace(string(out)); text != "" {
		log.Printf("stt: whisper ok (%s): %q", took.Round(time.Millisecond), truncate(text, 120))
		return text, nil
	}
	// An empty result is the confusing one, so say what was actually fed in.
	log.Printf("stt: whisper returned NO TEXT (%s) for %.2fs of audio at %s",
		took.Round(time.Millisecond), level.Seconds, level)
	if w.vad {
		return "", sttErrf("whisper heard nothing - the voice-activity filter is on and may have " +
			"discarded the take; try stt-whisper-vad = false")
	}
	return "", sttErrf("whisper heard nothing from %.1fs at %s - move closer to the mic, "+
		"raise its input gain, or run the app with -stt-debug for the full trace",
		level.Seconds, level)
}

// firstLine trims a multi-line error to its first line, which is the one that
// names the problem; the rest is usually traceback noise.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
