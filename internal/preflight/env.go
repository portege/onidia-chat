// env.go - the environment requirements checks: what the host must provide
// for chat-app to be useful. All are SeverityWarn: a missing piece degrades
// speech/pet-forwarding/agents but never stops the window from opening.
package preflight

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Env is the resolved environment context, filled by the caller from the same
// values chat-app already computed (flag > config > default precedence).
type Env struct {
	Pipe        string // resolved pet say-FIFO ("" = forwarding disabled)
	AgentsDir   string // agent discovery directory
	AgentsOff   bool   // agent discovery disabled
	ImageSource string // "pixabay" | "wiki" | "gemini" | "off"
	PixabayKey  string // resolved key ("" = none configured)
	TTSOn       bool   // speech replies requested
}

// ttsPlayerCandidates mirrors chat-app's tts.go player preference list
// (findTTSPlayer: pw-play/paplay first on PipeWire, then aplay/paplay/ffplay).
var ttsPlayerCandidates = []string{"pw-play", "paplay", "aplay", "ffplay"}

// EnvChecks returns the environment requirement checks for the given context.
func EnvChecks(e Env) []Check {
	return []Check{
		{
			ID:       "env.display",
			Kind:     KindEnv,
			Severity: SeverityWarn,
			Run: func(context.Context) Outcome {
				d := os.Getenv("DISPLAY")
				if d == "" {
					return Fail("no DISPLAY - chat-app opens a plain X11 window",
						"run inside an X session, or export DISPLAY=:0")
				}
				return Pass("DISPLAY=" + d)
			},
		},
		{
			ID:       "env.audio-player",
			Kind:     KindEnv,
			Severity: SeverityWarn,
			Run: func(context.Context) Outcome {
				if !e.TTSOn {
					return Skip("tts off")
				}
				for _, p := range ttsPlayerCandidates {
					if path, err := exec.LookPath(p); err == nil {
						return Pass(fmt.Sprintf("%s (%s)", p, path))
					}
				}
				return Fail("no audio player on PATH (tried "+fmt.Sprint(ttsPlayerCandidates)+")",
					"install one: alsa-utils (aplay), pulseaudio-utils (paplay) or ffmpeg (ffplay) - or tts = off")
			},
		},
		{
			ID:       "env.pet-pipe",
			Kind:     KindEnv,
			Severity: SeverityWarn,
			Run: func(context.Context) Outcome {
				if e.Pipe == "" {
					return Pass("say-pipe forwarding disabled (replies stay in the chat window)")
				}
				st, err := os.Stat(e.Pipe)
				if err != nil {
					return Fail(fmt.Sprintf("pet pipe %s not found", e.Pipe),
						"start the pet (onidia) so replies reach its bubble, or set pet-pipe = off")
				}
				if st.Mode()&os.ModeNamedPipe == 0 {
					return Fail(e.Pipe+" exists but is not a FIFO",
						"remove it and let the pet recreate its say-pipe")
				}
				// Non-blocking write-open: exactly chat-app's petPipeReady
				// probe - ENXIO means the FIFO exists but nobody reads it.
				f, err := os.OpenFile(e.Pipe, os.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err != nil {
					return Fail(fmt.Sprintf("pet pipe %s has no reader (pet not running?)", e.Pipe),
						"start the pet (onidia), or set pet-pipe = off")
				}
				f.Close()
				return Pass("pet is listening on " + e.Pipe)
			},
		},
		{
			ID:       "env.agents-dir",
			Kind:     KindEnv,
			Severity: SeverityWarn,
			Run: func(context.Context) Outcome {
				if e.AgentsOff {
					return Skip("agent discovery disabled (agents-off)")
				}
				entries, err := os.ReadDir(e.AgentsDir)
				if err != nil {
					return Fail(fmt.Sprintf("agents dir %s unreadable: %s", e.AgentsDir, errDetail(err)),
						"create it (mkdir -p "+e.AgentsDir+"), install agents with agentctl, or set agents-off = true")
				}
				installed := 0
				for _, ent := range entries {
					if !ent.IsDir() {
						continue
					}
					if _, err := os.Stat(filepath.Join(e.AgentsDir, ent.Name(), "agent.json")); err == nil {
						installed++
					}
				}
				if len(entries) == 0 {
					return Pass(fmt.Sprintf("%s is empty (built-in agents still work)", e.AgentsDir))
				}
				return Pass(fmt.Sprintf("%d agent(s) in %s", installed, e.AgentsDir))
			},
		},
		{
			ID:       "env.images",
			Kind:     KindEnv,
			Severity: SeverityWarn,
			Run: func(context.Context) Outcome {
				switch e.ImageSource {
				case "off":
					return Skip("image replies off")
				case "wiki":
					return Pass("source=wiki (no key needed)")
				case "gemini":
					return Pass("source=gemini (uses the Gemini API)")
				}
				if e.PixabayKey == "" {
					return Fail("image-source=pixabay but no API key",
						"export PIXABAY_API_KEY or set pixabay-key in chat-app.ini (chat-app's built-in key also works)")
				}
				return Pass("pixabay key " + MaskKey(e.PixabayKey))
			},
		},
	}
}
