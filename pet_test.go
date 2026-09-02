package main

// pet_test.go - the pet say-pipe bridge: path auto-detection, flag/config
// resolution precedence (including the "auto" keyword), and the say-line
// composition for text + image replies.

import (
	"bufio"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResolvePetPipe(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	const want = "/tmp/desktop-pet--0.say" // ":0" maps to "-0" like the pet

	cases := []struct {
		name            string
		flagVal, cfgVal string
		want            string
	}{
		{"config auto keyword", "", "auto", want},
		{"config auto case-insensitive", "", "AUTO", want},
		{"both empty auto-detects", "", "", want},
		{"whitespace trimmed", "  ", " auto ", want},
		{"flag off wins over config", "off", "auto", ""},
		{"config off", "", "off", ""},
		{"config off case-insensitive", "", "Off", ""},
		{"flag path wins over config auto", "/tmp/custom.say", "auto", "/tmp/custom.say"},
		{"config absolute path", "", "/tmp/from-ini.say", "/tmp/from-ini.say"},
		{"flag beats config path", "/tmp/custom.say", "/tmp/from-ini.say", "/tmp/custom.say"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvePetPipe(tc.flagVal, tc.cfgVal); got != tc.want {
				t.Errorf("resolvePetPipe(%q, %q) = %q, want %q", tc.flagVal, tc.cfgVal, got, tc.want)
			}
		})
	}
}

func TestPetPipePath(t *testing.T) {
	cases := []struct{ display, want string }{
		{":0", "/tmp/desktop-pet--0.say"},
		{":1", "/tmp/desktop-pet--1.say"},
		{"", ""}, // no DISPLAY -> forwarding disabled
	}
	for _, tc := range cases {
		t.Setenv("DISPLAY", tc.display)
		if got := petPipePath(); got != tc.want {
			t.Errorf("petPipePath(DISPLAY=%q) = %q, want %q", tc.display, got, tc.want)
		}
	}
}

func TestBuildSayLine(t *testing.T) {
	cases := []struct {
		name, mood, text, imgPath, want string
	}{
		{"text only", "", "hello", "", "hello"},
		{"mood first", "happy", "hello", "", "[happy] hello"},
		{"image then caption", "wink", "look!", "/tmp/p.png", "[wink] [image /tmp/p.png] look!"},
		{"image with empty caption", "", "", "/tmp/p.png", "[image /tmp/p.png] "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildSayLine(tc.mood, tc.text, tc.imgPath); got != tc.want {
				t.Errorf("buildSayLine(%q, %q, %q) = %q, want %q", tc.mood, tc.text, tc.imgPath, got, tc.want)
			}
		})
	}
}

func TestSaveTempPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	path, err := saveTempPNG(img)
	if err != nil {
		t.Fatalf("saveTempPNG: %v", err)
	}
	defer os.Remove(path)

	if ok, _ := filepath.Match("chat-app-say-*.png", filepath.Base(path)); !ok {
		t.Errorf("temp file %q does not match pattern %q", path, sayImagePattern)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open saved png: %v", err)
	}
	defer f.Close()
	got, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode saved png: %v", err)
	}
	if b := got.Bounds(); b.Dx() != 4 || b.Dy() != 3 {
		t.Errorf("decoded bounds %v, want 4x3", b)
	}
}

func TestPetSayFIFODeliversImageLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir) // say-image temp files land in the test dir
	fifo := filepath.Join(dir, "test.say")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	lines := make(chan string, 1)
	go func() {
		// Blocking open parks until petSay's writer opens the other end.
		f, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			lines <- ""
			return
		}
		defer f.Close()
		l, err := bufio.NewReader(f).ReadString('\n')
		if err != nil {
			lines <- ""
			return
		}
		lines <- l
	}()
	time.Sleep(50 * time.Millisecond) // let the reader open first (petSay retries ENXIO anyway)

	petSay(fifo, "happy", "look at this", image.NewRGBA(image.Rect(0, 0, 3, 2)))

	select {
	case line := <-lines:
		if line == "" {
			t.Fatal("reader goroutine failed")
		}
		if !strings.Contains(line, "[happy] ") ||
			!strings.Contains(line, "[image ") ||
			!strings.Contains(line, "look at this") {
			t.Errorf("say-line %q missing mood/image/caption", line)
		}
		m := sayImagePath.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("say-line %q has no [image path] tag", line)
		}
		f, err := os.Open(m[1])
		if err != nil {
			t.Fatalf("referenced image %s unreadable: %v", m[1], err)
		}
		defer f.Close()
		if _, err := png.Decode(f); err != nil {
			t.Errorf("referenced image %s is not a decodable png: %v", m[1], err)
		}
		os.Remove(m[1])
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for say-line")
	}
}

func TestPetSayFailureCleansTempImage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	count := func() int {
		m, _ := filepath.Glob(filepath.Join(dir, sayImagePattern))
		return len(m)
	}
	before := count()
	// No FIFO at that path -> open fails -> the temp image must not linger.
	petSay(filepath.Join(dir, "missing.say"), "happy", "hi", image.NewRGBA(image.Rect(0, 0, 2, 2)))
	if after := count(); after > before {
		t.Errorf("failed say write leaked %d temp image(s)", after-before)
	}
}

// sayImagePath extracts the path from an "[image <path>]" say-tag.
var sayImagePath = regexp.MustCompile(`\[image ([^\]]+)\]`)
