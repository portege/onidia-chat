package main

// Tests for the shutdown goodbye (farewells.go): the pool's renderability,
// the ASCII folding that keeps unsupported scripts out of the 5x7 bitmap
// fonts, the random pick, and one real end-to-end signOff through two FIFOs.
// TTS playback is not exercised (no network, no sound device in CI), so every
// signer here takes the silent path.

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFarewellPoolIsRenderable(t *testing.T) {
	if len(farewellLines) < 50 {
		t.Errorf("farewell pool is thin: %d lines", len(farewellLines))
	}
	seen := map[string]bool{}
	for _, line := range farewellLines {
		if line == "" {
			t.Fatal("empty farewell in pool")
		}
		if seen[line] {
			t.Errorf("duplicate farewell: %q", line)
		}
		seen[line] = true
		// The pool is authored romanized: folding must be a no-op, which is
		// what proves the bubble and the chat log can draw every entry.
		if got := asciiFarewell(line); got != line {
			t.Errorf("farewell %q is not renderable as-is (folds to %q)", line, got)
		}
	}
}

func TestAsciiFarewell(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Au revoir !", "Au revoir !"},
		{"Grüezi, zäme!", "Gruezi, zame!"},         // accents fold to plain cells
		{"Do widzenia — już", "Do widzenia - juz"}, // em dash becomes a hyphen
		{"“Bye”", `"Bye"`},                         // curly quotes become ASCII
		{"さよなら, Пока!", ""},                        // no 5x7 glyphs: nothing left
		{"Adiós\n  amigo\t!", "Adios amigo !"},     // one line, collapsed spaces
		{"\x07\x1b", ""},                           // control characters go
		{"", ""},
	}
	for _, c := range cases {
		if got := asciiFarewell(c.in); got != c.want {
			t.Errorf("asciiFarewell(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestRandomFarewell(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		line := randomFarewell()
		if line == "" {
			t.Fatal("randomFarewell returned an empty line")
		}
		if line != farewellFallback && !slices.Contains(farewellLines, line) {
			t.Errorf("randomFarewell invented %q", line)
		}
		if strings.TrimRight(line, "\t ") != line {
			t.Errorf("farewell %q ends in whitespace", line)
		}
		for _, r := range line {
			if r > 0x7f {
				t.Errorf("farewell %q carries an undrawable rune %q", line, r)
			}
		}
		seen[line] = true
	}
	if len(seen) < 2 {
		t.Errorf("randomFarewell picked the same line %d times", 200)
	}
}

// TestSignOffThroughFIFOs is the graceful-shutdown contract over real pipes:
// the wave goes to the cmd FIFO, the mood-prefixed farewell to the say FIFO,
// and the silent path leaves the bubble standing (the pet closes it by reading
// time) without ever holding the shutdown as long as the spoken grace allows.
func TestSignOffThroughFIFOs(t *testing.T) {
	dir := t.TempDir()
	say, cmd := filepath.Join(dir, "say"), filepath.Join(dir, "cmd")
	for _, p := range []string{say, cmd} {
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Skipf("FIFOs unavailable: %v", err)
		}
	}
	// O_RDWR on a FIFO never blocks and never sees EOF, so the reader is
	// already listening when signOff opens its write ends (petPipeReady is
	// part of what's under test) and a short-lived writer is never answered
	// with EPIPE because the reader hopped away between two messages.
	drain := func(path string) chan string {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		t.Cleanup(func() { f.Close() })
		ch := make(chan string, 64)
		go func() {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				ch <- sc.Text()
			}
		}()
		return ch
	}
	got := func(ch chan string) []string {
		var out []string
		for {
			select {
			case s := <-ch:
				out = append(out, s)
			case <-time.After(200 * time.Millisecond):
				return out
			}
		}
	}
	cmdCh, sayCh := drain(cmd), drain(say)

	start := time.Now()
	line := (&farewellSigner{SayPipe: say, CmdPipe: cmd}).signOff("test")
	if line == "" {
		t.Fatal("signOff said nothing")
	}
	// The silent path holds for one reading pause, never for the spoken
	// grace period: a dead or silent pet must not stall a shutdown.
	if elapsed := time.Since(start); elapsed > farewellSpokenGrace {
		t.Errorf("signOff held the shutdown for %v", elapsed)
	}

	if cmds := got(cmdCh); !slices.Contains(cmds, farewellWaveCmd) {
		t.Errorf("cmd FIFO got %v, want %q", cmds, farewellWaveCmd)
	}
	// Mood tag and text travel as one say-line, exactly like a spoken reply.
	want := "[" + farewellMood + "] " + line
	sayLines := got(sayCh)
	if !slices.Contains(sayLines, want) {
		t.Errorf("say FIFO got %v, want %q", sayLines, want)
	}
	if slices.Contains(sayLines, petSayClearToken) {
		t.Error("say FIFO got the clear token: a silent goodbye should stand a read, not be wiped")
	}
}
