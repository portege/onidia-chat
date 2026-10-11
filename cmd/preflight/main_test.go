package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeINI drops a config file and returns its path.
func writeINI(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat-app.ini")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// hermetic keeps the host's environment out of the assertions: a display,
// no API keys, tts off (no PATH probing).
func hermetic(t *testing.T) {
	t.Setenv("DISPLAY", ":99")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("PIXABAY_API_KEY", "")
}

// quietINI is a config where every environment check passes or skips.
const quietINI = `provider = ollama
pet-pipe = off
agents-off = true
image-source = off
tts = off
`

func TestRunQuickPassingSetup(t *testing.T) {
	hermetic(t)
	path := writeINI(t, quietINI)
	var out bytes.Buffer
	if code := run([]string{"-config", path, "-quick"}, &out); code != 0 {
		t.Errorf("exit = %d, want 0:\n%s", code, out.String())
	}
	got := out.String()
	// Assertions on the happy path (no BLOCKED allowed).
	if strings.Contains(got, "BLOCKED") {
		t.Errorf("passing run must not be BLOCKED:\n%s", got)
	}
	for _, want := range []string{"preflight: provider=ollama", "ok   config.provider"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	hermetic(t)

	t.Run("warn failure exits 1", func(t *testing.T) {
		// A pet-pipe that points nowhere is a warning, never a block.
		path := writeINI(t, strings.Replace(quietINI, "pet-pipe = off",
			"pet-pipe = /nonexistent/pet.say", 1))
		var out bytes.Buffer
		if code := run([]string{"-config", path, "-quick"}, &out); code != 1 {
			t.Errorf("exit = %d, want 1:\n%s", code, out.String())
		}
		if !strings.Contains(out.String(), "warn env.pet-pipe") {
			t.Errorf("missing the pet-pipe warning:\n%s", out.String())
		}
	})

	t.Run("fatal failure exits 2", func(t *testing.T) {
		// openrouter has no built-in key: missing it blocks the launch.
		path := writeINI(t, "provider = openrouter\npet-pipe = off\nagents-off = true\nimage-source = off\ntts = off\n")
		var out bytes.Buffer
		if code := run([]string{"-config", path, "-quick"}, &out); code != 2 {
			t.Errorf("exit = %d, want 2:\n%s", code, out.String())
		}
		if !strings.Contains(out.String(), "FAIL openrouter.key") {
			t.Errorf("missing the openrouter key failure:\n%s", out.String())
		}
	})

	t.Run("missing config file exits 2", func(t *testing.T) {
		var out bytes.Buffer
		code := run([]string{"-config", filepath.Join(t.TempDir(), "absent.ini"), "-quick"}, &out)
		if code != 2 || !strings.Contains(out.String(), "read config") {
			t.Errorf("exit = %d, want 2 with a read error:\n%s", code, out.String())
		}
	})
}

func TestRunFlagsOverrideConfig(t *testing.T) {
	hermetic(t)
	path := writeINI(t, quietINI)
	var out bytes.Buffer
	// -provider overrides the ini's ollama; openrouter without a key blocks.
	code := run([]string{"-config", path, "-quick", "-provider", "openrouter"}, &out)
	if code != 2 {
		t.Errorf("exit = %d, want 2 (flag provider wins, its key check fails):\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "provider=openrouter") {
		t.Errorf("flag -provider not honoured:\n%s", out.String())
	}
}

func TestRunUnknownProviderNormalizesToGemini(t *testing.T) {
	hermetic(t)
	path := writeINI(t, "provider = banana\npet-pipe = off\nagents-off = true\nimage-source = off\ntts = off\n")
	var out bytes.Buffer
	// main.go normalizes unknown providers to gemini (with a note on stderr),
	// so preflight must predict that: gemini.key passes via the built-in key
	// and gemini.api is dropped by -quick -> exit 0.
	if code := run([]string{"-config", path, "-quick"}, &out); code != 0 {
		t.Errorf("exit = %d, want 0:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "provider=gemini") {
		t.Errorf("unknown provider must resolve to gemini:\n%s", out.String())
	}
}

func TestRunJSON(t *testing.T) {
	hermetic(t)
	path := writeINI(t, quietINI)
	var out bytes.Buffer
	if code := run([]string{"-config", path, "-quick", "-json"}, &out); code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, out.String())
	}
	var rep struct {
		OK     bool `json:"ok"`
		Exit   int  `json:"exit"`
		Checks []struct {
			ID string `json:"id"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if !rep.OK || rep.Exit != 0 || len(rep.Checks) == 0 {
		t.Errorf("report = %+v, want ok with checks", rep)
	}
}

func TestRunAllAddsInformationalBackends(t *testing.T) {
	hermetic(t)
	path := writeINI(t, quietINI)
	var plain, all bytes.Buffer
	if code := run([]string{"-config", path, "-quick"}, &plain); code != 0 {
		t.Fatalf("plain exit = %d:\n%s", code, plain.String())
	}
	// -all: the non-selected backends show up as info rows and -quick keeps
	// their fatal checks from blocking (info severity never raises the exit).
	code := run([]string{"-config", path, "-quick", "-all"}, &all)
	if code != 0 {
		t.Errorf("-all exit = %d, want 0 (info rows never block):\n%s", code, all.String())
	}
	if !strings.Contains(all.String(), "gemini.key") || !strings.Contains(all.String(), "bedrock.credentials") {
		t.Errorf("-all must list the other backends:\n%s", all.String())
	}
	if all.Len() <= plain.Len() {
		t.Errorf("-all output (%d bytes) must be longer than plain (%d)", all.Len(), plain.Len())
	}
}
