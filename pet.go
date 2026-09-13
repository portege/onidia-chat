package main

// pet.go - bridge to the desktop-pet: bot replies are written to its say-FIFO
// so Onidia speaks them. The FIFO lives at /tmp/desktop-pet-<display>.say
// (the path is logged by the pet at startup). Writes are best-effort and
// never hang the chat: with no pet listening, the open fails with ENXIO and
// we quietly skip.

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// petSayClearToken is a reserved, lone control-character line written to the
// say-FIFO to dismiss the pet's current speech bubble. Real reply lines (which
// go through sanitise* and TrimSpace on the pet side) can never equal it, so
// it is a safe side-channel for closing the bubble exactly when text-to-speech
// playback ends.
const petSayClearToken = "\x04"

// petPipePath derives the say-FIFO path from $DISPLAY exactly the way the
// pet names it (see desktop-pet main.go instanceTag): every '/', ':' and '.'
// becomes '-'. Returns "" when no display is set.
func petPipePath() string {
	disp := os.Getenv("DISPLAY")
	if disp == "" {
		return ""
	}
	tag := strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == '.' {
			return '-'
		}
		return r
	}, disp)
	return "/tmp/desktop-pet-" + tag + ".say"
}

// petCmdPathFor derives the pet's command-FIFO path from its say-FIFO path.
// The pet names the two pipes identically apart from the extension (.say for
// speech, .cmd for action/event commands), so replacing the suffix yields the
// exact sibling pipe even for user-customized paths. Returns "" when the say
// pipe is disabled.
func petCmdPathFor(sayPath string) string {
	if sayPath == "" {
		return ""
	}
	return strings.TrimSuffix(sayPath, ".say") + ".cmd"
}

// petSay writes one reply to the pet say-FIFO: the image (if any) is encoded
// to a temp PNG and the assembled line is pushed via petSayLine. Best-effort -
// the FIFO write never hangs the chat: with no pet listening, the open fails
// with ENXIO and we quietly skip.
func petSay(path, mood, text string, img image.Image) {
	petSayLine(path, buildPetSayLine(path, mood, text, img))
}

// buildPetSayLine prepares the say-pipe line for a reply: the image is encoded
// to a temp PNG, then the mood tag + caption are assembled in the pet's parse
// order. Returns "" when there is nothing to show. The line is handed to
// petSayLine later (possibly by the TTS worker, synchronised with audio
// playback), so building and writing are kept separate.
func buildPetSayLine(path, mood, text string, img image.Image) string {
	if path == "" || (text == "" && img == nil) {
		return ""
	}
	imgPath := ""
	if img != nil {
		p, err := saveTempPNG(img)
		if err != nil {
			log.Printf("pet: encoding say-image: %v", err)
		} else {
			cleanupSayImages(p) // drop generations the pet has long consumed
			imgPath = p
		}
	}
	return buildSayLine(mood, text, imgPath)
}

// petSayLine writes one pre-built line to the say-FIFO. Best-effort and
// never blocking: O_NONBLOCK so a missing reader yields ENXIO instead of a
// hang, with a short retry that covers the pet reopening the pipe between
// messages. A line whose write fails cleans up any temp image it references.
func petSayLine(path, line string) {
	if path == "" || line == "" {
		return
	}
	// O_NONBLOCK so we get ENXIO instead of blocking forever when the pet
	// is not running (a FIFO write-open waits for a reader otherwise).
	var f *os.File
	var err error
	for tries := 0; tries < 3; tries++ {
		f, err = os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.ENXIO) {
			petTryRemoveSayImage(line) // no pipe at all -> pet not running
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		petTryRemoveSayImage(line)
		return
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, line); err != nil {
		log.Printf("pet: say write failed: %v", err)
		petTryRemoveSayImage(line)
	}
}

// petClear asks the pet to close its current speech bubble (text-to-speech
// playback just finished). It writes the reserved clear token as its own
// say-pipe line, so bubble open and close share one FIFO and keep their
// ordering even on a slow pet.
func petClear(path string) {
	if path == "" {
		return
	}
	petSayLine(path, petSayClearToken)
}

// petCmd sends one command line (e.g. "action dance" or "event love") to the
// pet's command FIFO so the LLM's [ACTION: ...] / [EVENT: ...] reply tags
// translate into the pet acting out the reply. Best-effort and identical to
// petSayLine in spirit: O_NONBLOCK, no hang when no pet is listening, retry
// once to cover a pipe reopen.
func petCmd(path, line string) {
	if path == "" || line == "" {
		return
	}
	for tries := 0; tries < 3; tries++ {
		f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			if _, werr := fmt.Fprintln(f, line); werr != nil {
				log.Printf("pet: cmd write failed: %v", werr)
			}
			f.Close()
			return
		}
		if !errors.Is(err, syscall.ENXIO) {
			return // no cmd pipe at all -> pet not running
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// petTryRemoveSayImage removes the temp image referenced by a say-line whose
// write failed, so an undelivered picture does not litter /tmp.
func petTryRemoveSayImage(line string) {
	if m := sayImagePath.FindStringSubmatch(line); m != nil {
		os.Remove(m[1])
	}
}

// buildSayLine assembles the line the pet expects, in its parse order: mood
// tag, then image tag, then the caption text.
func buildSayLine(mood, text, imgPath string) string {
	line := text
	if imgPath != "" {
		line = "[image " + imgPath + "] " + line
	}
	if mood != "" {
		line = "[" + mood + "] " + line
	}
	return line
}

// sayImagePath extracts the path from an "[image <path>]" say-tag.
var sayImagePath = regexp.MustCompile(`\[image ([^\]]+)\]`)

// sayImagePattern matches the temp PNGs written for pet say-lines.
const sayImagePattern = "chat-app-say-*.png"

// saveTempPNG encodes img as a PNG in a unique temp file and returns its
// path, ready to be referenced from a say-line.
func saveTempPNG(img image.Image) (string, error) {
	f, err := os.CreateTemp("", sayImagePattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := png.Encode(f, img); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// cleanupSayImages removes temp say-images from earlier replies, keeping
// keep (the file referenced by the newest line - the pet may still be about
// to read it). Keeps /tmp tidy during long chat sessions.
func cleanupSayImages(keep string) {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), sayImagePattern))
	if err != nil {
		return
	}
	for _, m := range matches {
		if m == keep {
			continue
		}
		os.Remove(m)
	}
}
