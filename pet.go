package main

// pet.go - bridge to the desktop-pet: bot replies are written to its say-FIFO
// so Onidia speaks them. The FIFO lives at /tmp/desktop-pet-<display>.say
// (the path is logged by the pet at startup). Writes are best-effort and
// never hang the chat: with no pet listening, the open fails with ENXIO and
// we quietly skip.

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
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

// The pet lifecycle: chat-app launches the onidia binary detached and
// remembers the process, so quitting can be confirmed (and escalated to
// SIGTERM when the pet ignores the quit command, e.g. a stale binary).

var petMu sync.Mutex
var petProc *os.Process // the onidia instance chat-app launched, if any

// onidiaPath resolves the onidia/desktop-pet binary, in the same spirit as
// the pet's own chat-app.ini lookup: a sibling onidia/ directory, a sibling
// desktop-pet/ directory (older layout), or the conventional absolute repo
// path. Returns "" when no candidate is executable.
func onidiaPath() string {
	if p, err := os.Stat("onidia/onidia"); err == nil && !p.IsDir() && p.Mode()&0o111 != 0 {
		return "onidia/onidia"
	}
	if p, err := os.Stat("desktop-pet/desktop-pet"); err == nil && !p.IsDir() && p.Mode()&0o111 != 0 {
		return "desktop-pet/desktop-pet"
	}
	cands := []string{
		"../onidia/onidia", // relative to chat-app/ working dir
		"../desktop-pet/desktop-pet",
		"/home/boi/repo/ai-helper/onidia/onidia",
		"/home/boi/repo/ai-helper/desktop-pet/desktop-pet",
	}
	if exe, err := os.Executable(); err == nil {
		cands = append([]string{
			filepath.Join(filepath.Dir(exe), "..", "onidia", "onidia"),
			filepath.Join(filepath.Dir(exe), "..", "desktop-pet", "desktop-pet"),
		}, cands...)
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return c
		}
	}
	return ""
}

// ensureOnidiaFresh rebuilds the onidia binary when any of its .go sources is
// newer, so freshly edited pet behaviour (new commands such as quit, new
// animations) is what actually launches. Best-effort: returns an error when
// the build is skipped or fails, and the caller still launches the stale
// binary.
func ensureOnidiaFresh(bin string) error {
	src := filepath.Dir(bin)
	if _, err := os.Stat(filepath.Join(src, "go.mod")); err != nil {
		return fmt.Errorf("%s has no go.mod - not a module dir", src)
	}
	binTime := time.Time{}
	if fi, err := os.Stat(bin); err == nil {
		binTime = fi.ModTime()
	}
	newest := binTime
	found := false
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		found = true
		if fi, e := d.Info(); e == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no .go sources under %s", src)
	}
	if !newest.After(binTime) {
		return nil // binary is already up to date
	}
	log.Printf("pet: rebuilding %s (sources changed since the last build)", bin)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Base(bin), ".")
	build.Dir = src
	if out, berr := build.CombinedOutput(); berr != nil {
		return fmt.Errorf("go build: %v: %s", berr, strings.TrimSpace(string(out)))
	}
	return nil
}

// LaunchPet (re)builds the onidia binary if its sources changed, then starts
// it detached from the chat-app process (new session, no shared stdout) so it
// keeps running when chat-app exits. The started process is remembered for
// QuitPet's SIGTERM fallback. Returns an error when nothing was started.
func LaunchPet() error {
	bin := onidiaPath()
	if bin == "" {
		return errors.New("onidia binary not found - looked for onidia/onidia, desktop-pet/desktop-pet")
	}
	if err := ensureOnidiaFresh(bin); err != nil {
		log.Printf("pet: refresh build skipped (%v) - launching the existing binary", err)
	}
	cmd := exec.Command(bin)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from chat-app
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching %s: %w", bin, err)
	}
	log.Printf("pet: launched %s (pid %d)", bin, cmd.Process.Pid)
	go cmd.Wait() // release the child's resources when it exits
	petMu.Lock()
	petProc = cmd.Process
	petMu.Unlock()
	return nil
}

// QuitPet asks the running pet to exit gracefully: a bare "quit" line on its
// command FIFO makes it play the poof-out (disappear) animation and then
// close. A watcher confirms the exit and escalates to SIGTERM when the pet
// ignores the command (a stale binary without the quit handler, or a missed
// FIFO write). Signals `gone` exactly once once the pet is (believed) gone.
func QuitPet(cmdPath string, gone chan<- struct{}) {
	if cmdPath != "" {
		petCmd(cmdPath, "quit")
	}
	go watchPetQuit(cmdPath, gone)
}

// watchPetQuit polls the pet's cmd FIFO until its reader disappears (the pet
// process exited) or the deadline passes; two consecutive readerless probes
// count, so a single poll landing in the listener's reopen gap is not
// mistaken for an exit. On the deadline it SIGTERMs the pet we launched, or -
// for an adopted instance we have no handle on - the pet process by exact
// name, then signals `gone` either way.
func watchPetQuit(cmdPath string, gone chan<- struct{}) {
	deadline := time.Now().Add(5 * time.Second)
	misses := 0
	for cmdPath != "" && time.Now().Before(deadline) {
		if petPipeReady(cmdPath) {
			misses = 0
		} else if misses++; misses >= 2 {
			signalGone(gone)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	petMu.Lock()
	proc := petProc
	petMu.Unlock()
	if proc != nil {
		log.Printf("pet: quit command had no effect - stopping pid %d", proc.Pid)
		proc.Signal(syscall.SIGTERM)
		time.Sleep(time.Second) // let the signal land before reporting gone
	} else if cmdPath != "" {
		log.Printf("pet: quit command had no effect - stopping the pet by process name")
		stopPetByName()
	}
	signalGone(gone)
}

// stopPetByName SIGTERMs any running pet process (exact name match: onidia,
// or its older desktop-pet name) - used when chat-app adopted a pet it did
// not launch and therefore has no process handle for.
func stopPetByName() {
	for _, name := range []string{"onidia", "desktop-pet"} {
		out, err := exec.Command("pgrep", "-x", name).Output()
		if err != nil {
			continue // no match, or pgrep unavailable
		}
		for _, f := range strings.Fields(string(out)) {
			pid, err := strconv.Atoi(f)
			if err != nil {
				continue
			}
			if p, err := os.FindProcess(pid); err == nil {
				p.Signal(syscall.SIGTERM)
			}
		}
	}
}

// signalGone delivers the pet-has-exited notification without ever blocking.
func signalGone(gone chan<- struct{}) {
	select {
	case gone <- struct{}{}:
	default:
	}
}

// petPipeReady reports whether the pet's say-FIFO currently has a reader
// attached, i.e. the desktop-pet process is running and listening. It probes
// with a non-blocking write-open and closes immediately without writing a
// line. False for an empty path, a pipe that does not exist yet (ENOENT), or
// one with no reader attached (ENXIO).
func petPipeReady(path string) bool {
	if path == "" {
		return false
	}
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
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
