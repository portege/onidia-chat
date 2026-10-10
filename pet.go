package main

// pet.go - bridge to the desktop-pet: bot replies are written to its say-FIFO
// so Onidia speaks them. The FIFO lives at /tmp/desktop-pet-<display>.say
// (the path is logged by the buddy at startup). Writes are best-effort and
// never hang the chat: with no buddy listening, the open fails with ENXIO and
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
// say-FIFO to dismiss the buddy's current speech bubble. Real reply lines (which
// go through sanitise* and TrimSpace on the buddy side) can never equal it, so
// it is a safe side-channel for closing the bubble exactly when text-to-speech
// playback ends.
const petSayClearToken = "\x04"

// petPipePath derives the say-FIFO path from $DISPLAY exactly the way the
// buddy names it: every '/', ':' and '.' becomes '-'. Returns "" when no
// display is set.
//
// The display is canonicalised first (see canonicalDisplay) because the buddy
// does the same, and the two have to agree: ":0", ":0.0" and "localhost:0.0"
// are one desktop, so all three must land on one path. If they did not, the
// buddy would listen on a pipe this app never writes to and every reply would
// vanish with no error anywhere.
func petPipePath() string {
	disp := canonicalDisplay(os.Getenv("DISPLAY"))
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

// canonicalDisplay reduces a DISPLAY value to one spelling of the display it
// names. It is a copy of Canonical in the buddy's own internal/single package -
// the two repos are separate Go modules and this is their only shared
// contract, so the rules are spelled out twice on purpose:
//
//	":0"  ":0.0"  "localhost:0.0"  "unix:0"  "unix/:0"  -> ":0"
//	":0.1"                                         -> ":0.1" (a different screen)
//	"MyHost:2.0"                                   -> "myhost:2"
//
// A missing display stays missing (""), which petPipePath reads as "forward
// nothing". Keep this and single.Canonical in step; the buddy's own table of
// examples is the reference.
func canonicalDisplay(display string) string {
	d := strings.TrimSpace(display)
	if d == "" {
		return ""
	}
	// Both socket spellings mean "this machine", and they are cut differently
	// on purpose: "unix:0" has no leading colon left, "unix/:0" does.
	if rest, ok := strings.CutPrefix(d, "unix:"); ok {
		d = ":" + rest
	} else if rest, ok := strings.CutPrefix(d, "unix/"); ok {
		d = rest
	}
	host, num, ok := strings.Cut(d, ":")
	if !ok {
		return strings.ToLower(d) // no display number: nothing to normalise
	}
	host = strings.ToLower(host)
	if host == "localhost" {
		host = "" // an empty host already means "this machine"
	}
	num = strings.TrimSuffix(num, ".0") // screen 0 is the default screen
	if host == "" {
		if num == "" {
			return ""
		}
		return ":" + num
	}
	return host + ":" + num
}

// petCmdPathFor derives the buddy's command-FIFO path from its say-FIFO path.
// The buddy names the two pipes identically apart from the extension (.say for
// speech, .cmd for action/event commands), so replacing the suffix yields the
// exact sibling pipe even for user-customized paths. Returns "" when the say
// pipe is disabled.
func petCmdPathFor(sayPath string) string {
	if sayPath == "" {
		return ""
	}
	return strings.TrimSuffix(sayPath, ".say") + ".cmd"
}

// The buddy lifecycle: onidia-chat launches the onidia binary detached and
// remembers the process, so quitting can be confirmed (and escalated to
// SIGTERM when the buddy ignores the quit command, e.g. a stale binary).

var petMu sync.Mutex
var petProc *os.Process // the onidia instance onidia-chat launched, if any

// onidiaPath resolves the buddy binary. The candidates, in order:
//
//	onidia/onidia                 run from the repo root (development checkout)
//	desktop-pet/desktop-pet       the same, older name for the buddy
//	<exedir>/onidia               the .deb ships both binaries in /opt/onidia/bin,
//	                              so the buddy is a sibling of onidia-chat
//	<exedir>/desktop-pet          the same, older name
//	<exedir>/../onidia/onidia     the checkout again, binary run in place
//	<exedir>/../desktop-pet/desktop-pet
//	../onidia/onidia              run from a directory beside onidia-chat/
//	../desktop-pet/desktop-pet    the same, older name
//	onidia on $PATH               the .deb also symlinks /usr/bin/onidia
//
// "Next to the binary" is what makes a package install work: the .deb puts
// onidia-chat at /opt/onidia/bin/onidia-chat with the buddy right beside it,
// and a desktop launcher runs the chat with any old working directory, so
// the working-directory candidates cannot be relied on there. The kernel
// resolves os.Executable() through the /usr/bin symlink (/proc/self/exe is
// always the real path), so the exe-relative candidates fire whether
// onidia-chat was started as /usr/bin/onidia-chat or as
// /opt/onidia/bin/onidia-chat.
//
// No developer home directory is probed: a missing buddy is an error, not a
// reason to launch someone's checkout. Returns "" when no candidate is
// executable.
func onidiaPath() string {
	exe, _ := os.Executable()
	for _, c := range onidiaCandidates(exe) {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return c
		}
	}
	return ""
}

// onidiaCandidates lists the paths onidiaPath probes, in order (see there);
// exe is the resolved path of this process, "" when it is unavailable.
// Split out from onidiaPath so the order and the package layout can be
// asserted in a test without moving the binary under test.
func onidiaCandidates(exe string) []string {
	cands := []string{
		"onidia/onidia",
		"desktop-pet/desktop-pet",
	}
	if exe != "" {
		d := filepath.Dir(exe)
		cands = append(cands,
			filepath.Join(d, "onidia"),
			filepath.Join(d, "desktop-pet"),
			filepath.Join(d, "..", "onidia", "onidia"),
			filepath.Join(d, "..", "desktop-pet", "desktop-pet"),
		)
	}
	cands = append(cands,
		"../onidia/onidia",
		"../desktop-pet/desktop-pet",
	)
	if p, err := exec.LookPath("onidia"); err == nil {
		cands = append(cands, p)
	}
	return cands
}

// ensureOnidiaFresh rebuilds the onidia binary when any of its .go sources is
// newer, so freshly edited buddy behaviour (new commands such as quit, new
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

// petLaunchArgs builds the launch arguments for the onidia binary: the
// -character flag selects the sprite (Kama for a boy, Onidia for a girl) and
// -demo picks the autonomous-behavior mode. Both flags' defaults on the buddy
// side (onidia, demo off) match this app's defaults, so a default launch is
// flagless.
func petLaunchArgs(character string, demo bool) []string {
	var args []string
	if character != "" && character != "onidia" {
		args = append(args, "-character", character)
	}
	if demo {
		args = append(args, "-demo=true")
	}
	return args
}

// normalizeGender maps any configured gender spelling onto "boy" or "girl";
// anything unrecognised (including empty) is a girl, the historical default.
func normalizeGender(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "boy", "male", "m", "b":
		return "boy"
	default:
		return "girl"
	}
}

// LaunchPet (re)builds the onidia binary if its sources changed, then starts
// it detached from the onidia-chat process (new session, no shared stdout) so it
// keeps running when onidia-chat exits. character picks the sprite: "kama" for
// the settings dialog's BOY gender, "onidia" (the binary's own default, so
// no flag is passed) for GIRL. demo enables the buddy's autonomous mode
// (random roaming + unsolicited chatter); demo=false keeps it planted — it
// still enters with the launch flourish and walks to its parking spot, but
// then stays there instead of wandering, still reactive and still idly
// blinking. The started process is remembered for QuitPet's SIGTERM fallback.
// Returns an error when nothing was started.
func LaunchPet(character string, demo bool) error {
	bin := onidiaPath()
	if bin == "" {
		return errors.New("onidia binary not found - looked in the working dir, next to onidia-chat, and on PATH (install the onidia .deb, or run from the repo root)")
	}
	if err := ensureOnidiaFresh(bin); err != nil {
		log.Printf("pet: refresh build skipped (%v) - launching the existing binary", err)
	}
	cmd := exec.Command(bin, petLaunchArgs(character, demo)...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from onidia-chat
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

// QuitPet asks the running buddy to exit gracefully: a bare "quit" line on its
// command FIFO makes it play the poof-out (disappear) animation and then
// close. A watcher confirms the exit and escalates to SIGTERM when the buddy
// ignores the command (a stale binary without the quit handler, or a missed
// FIFO write). Signals `gone` exactly once once the buddy is (believed) gone.
func QuitPet(cmdPath string, gone chan<- struct{}) {
	if cmdPath != "" {
		petCmd(cmdPath, "quit")
	}
	go watchPetQuit(cmdPath, gone)
}

// watchPetQuit polls the buddy's cmd FIFO until its reader disappears (the buddy
// process exited) or the deadline passes; two consecutive readerless probes
// count, so a single poll landing in the listener's reopen gap is not
// mistaken for an exit. On the deadline it SIGTERMs the buddy we launched, or -
// for an adopted instance we have no handle on - the buddy process by exact
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

// stopPetByName SIGTERMs any running buddy process (exact name match: onidia,
// or its older desktop-pet name) - used when onidia-chat adopted a buddy it did
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

// petPipeReady reports whether the buddy's say-FIFO currently has a reader
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

// petSay writes one reply to the buddy say-FIFO: the image (if any) is encoded
// to a temp PNG and the assembled line is pushed via petSayLine. Best-effort -
// the FIFO write never hangs the chat: with no buddy listening, the open fails
// with ENXIO and we quietly skip.
func petSay(path, mood, text, thinking string, img image.Image) {
	petSayLine(path, buildPetSayLine(path, mood, text, thinking, img))
}

// buildPetSayLine prepares the say-pipe line for a reply: the image is encoded
// to a temp PNG, then the mood tag + caption are assembled in the buddy's parse
// order. Returns "" when there is nothing to show. The line is handed to
// petSayLine later (possibly by the TTS worker, synchronised with audio
// playback), so building and writing are kept separate.
func buildPetSayLine(path, mood, text, thinking string, img image.Image) string {
	if path == "" || (text == "" && img == nil) {
		return ""
	}
	imgPath := ""
	if img != nil {
		p, err := saveTempPNG(img)
		if err != nil {
			log.Printf("pet: encoding say-image: %v", err)
		} else {
			cleanupSayImages(p) // drop generations the buddy has long consumed
			imgPath = p
		}
	}
	return buildSayLine(mood, text, thinking, imgPath)
}

// petSayLine writes one pre-built line to the say-FIFO. Best-effort and
// never blocking: O_NONBLOCK so a missing reader yields ENXIO instead of a
// hang, with a short retry that covers the buddy reopening the pipe between
// messages. A line whose write fails cleans up any temp image it references.
func petSayLine(path, line string) {
	if path == "" || line == "" {
		return
	}
	// O_NONBLOCK so we get ENXIO instead of blocking forever when the buddy
	// is not running (a FIFO write-open waits for a reader otherwise).
	var f *os.File
	var err error
	for tries := 0; tries < 3; tries++ {
		f, err = os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.ENXIO) {
			petTryRemoveSayImage(line) // no pipe at all -> buddy not running
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

// petClear asks the buddy to close its current speech bubble (text-to-speech
// playback just finished). It writes the reserved clear token as its own
// say-pipe line, so bubble open and close share one FIFO and keep their
// ordering even on a slow buddy.
func petClear(path string) {
	if path == "" {
		return
	}
	petSayLine(path, petSayClearToken)
}

// petCmd sends one command line (e.g. "action dance" or "event love") to the
// buddy's command FIFO so the LLM's [ACTION: ...] / [EVENT: ...] reply tags
// translate into the buddy acting out the reply. Best-effort and identical to
// petSayLine in spirit: O_NONBLOCK, no hang when no buddy is listening, retry
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
			return // no cmd pipe at all -> buddy not running
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

// buildSayLine assembles the line the buddy expects, in its parse order: mood
// tag, then the thinking block, then image tag, then the caption text.
//
// The thinking block is FORWARDED to the buddy so she can show it in her own
// small thought cloud (onidia parses <THINKING>...</THINKING> out of the
// say-line and renders it above the speech bubble). It is deliberately not
// part of the caption: the caption is what she speaks, and the reasoning is
// not said out loud.
func buildSayLine(mood, text, thinking, imgPath string) string {
	line := text
	if imgPath != "" {
		line = "[image " + imgPath + "] " + line
	}
	if t := sanitizeThinking(thinking); t != "" {
		line = thinkTagOpen + t + thinkTagClose + " " + line
	}
	if mood != "" {
		line = "[" + mood + "] " + line
	}
	return line
}

// sanitizeThinking keeps the block on ONE line: the say-pipe is line-oriented,
// so a newline here would be read by the buddy as a second message.
func sanitizeThinking(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// sayImagePath extracts the path from an "[image <path>]" say-tag.
var sayImagePath = regexp.MustCompile(`\[image ([^\]]+)\]`)

// sayImagePattern matches the temp PNGs written for buddy say-lines.
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
// keep (the file referenced by the newest line - the buddy may still be about
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
