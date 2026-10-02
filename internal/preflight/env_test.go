package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// envOutcome runs one check by ID out of EnvChecks(e).
func envOutcome(t *testing.T, e Env, id string) Outcome {
	t.Helper()
	for _, c := range EnvChecks(e) {
		if c.ID == id {
			return safeRun(context.Background(), c.Run)
		}
	}
	t.Fatalf("no environment check with id %q", id)
	return Outcome{}
}

func TestEnvDisplay(t *testing.T) {
	t.Setenv("DISPLAY", "")
	if out := envOutcome(t, Env{}, "env.display"); out.Status != StatusFail {
		t.Errorf("no DISPLAY: status = %v, want fail", out.Status)
	}
	t.Setenv("DISPLAY", ":0")
	if out := envOutcome(t, Env{}, "env.display"); out.Status != StatusPass {
		t.Errorf("DISPLAY=:0: status = %v detail %q, want pass", out.Status, out.Detail)
	}
}

func TestEnvAudioPlayer(t *testing.T) {
	// tts off: the check does not apply.
	if out := envOutcome(t, Env{TTSOn: false}, "env.audio-player"); out.Status != StatusSkip {
		t.Errorf("tts off: status = %v, want skip", out.Status)
	}
	// Empty PATH: no player anywhere.
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	if out := envOutcome(t, Env{TTSOn: true}, "env.audio-player"); out.Status != StatusFail {
		t.Errorf("no player: status = %v detail %q, want fail", out.Status, out.Detail)
	}
	// A player on PATH: pass and name it.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "aplay"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	out := envOutcome(t, Env{TTSOn: true}, "env.audio-player")
	if out.Status != StatusPass || !strings.Contains(out.Detail, "aplay") {
		t.Errorf("player present: status = %v detail %q, want pass naming aplay", out.Status, out.Detail)
	}
}

func TestEnvPetPipe(t *testing.T) {
	t.Run("disabled passes", func(t *testing.T) {
		if out := envOutcome(t, Env{Pipe: ""}, "env.pet-pipe"); out.Status != StatusPass {
			t.Errorf("status = %v, want pass (disabled is fine)", out.Status)
		}
	})
	t.Run("missing pipe fails", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.say")
		if out := envOutcome(t, Env{Pipe: missing}, "env.pet-pipe"); out.Status != StatusFail {
			t.Errorf("status = %v, want fail", out.Status)
		}
	})
	t.Run("regular file fails", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "not-a-fifo")
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		out := envOutcome(t, Env{Pipe: p}, "env.pet-pipe")
		if out.Status != StatusFail || !strings.Contains(out.Detail, "not a FIFO") {
			t.Errorf("status = %v detail %q, want the not-a-FIFO failure", out.Status, out.Detail)
		}
	})
	t.Run("fifo without reader fails", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "pet.say")
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
		out := envOutcome(t, Env{Pipe: p}, "env.pet-pipe")
		if out.Status != StatusFail || !strings.Contains(out.Detail, "no reader") {
			t.Errorf("status = %v detail %q, want the no-reader failure", out.Status, out.Detail)
		}
	})
	t.Run("listening pet passes", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "pet.say")
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
		// The reader side (the pet) stays open for the probe.
		reader, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		out := envOutcome(t, Env{Pipe: p}, "env.pet-pipe")
		if out.Status != StatusPass {
			t.Errorf("status = %v detail %q, want pass", out.Status, out.Detail)
		}
	})
}

// A directory that does not exist is not a failure any more: the per-user
// one is absent for anyone who has never installed an agent, and the search
// now also covers the packaged /opt/onidia/share/agents.
func TestEnvAgentsDir(t *testing.T) {
	if out := envOutcome(t, Env{AgentsOff: true}, "env.agents-dir"); out.Status != StatusSkip {
		t.Errorf("agents off: status = %v, want skip", out.Status)
	}
	missing := filepath.Join(t.TempDir(), "agents")
	out := envOutcome(t, Env{AgentsDirs: []string{missing}}, "env.agents-dir")
	if out.Status != StatusPass || !strings.Contains(out.Detail, "no installed agents") {
		t.Errorf("missing dir: status = %v detail %q, want pass with no installed agents", out.Status, out.Detail)
	}
	empty := t.TempDir()
	out = envOutcome(t, Env{AgentsDirs: []string{empty}}, "env.agents-dir")
	if out.Status != StatusPass || !strings.Contains(out.Detail, "no installed agents") {
		t.Errorf("empty dir: status = %v detail %q, want pass with no installed agents", out.Status, out.Detail)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "play_song"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "play_song", "agent.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = envOutcome(t, Env{AgentsDirs: []string{dir}}, "env.agents-dir")
	if out.Status != StatusPass || !strings.Contains(out.Detail, "1 agent(s)") {
		t.Errorf("one agent: status = %v detail %q, want pass with 1 agent(s)", out.Status, out.Detail)
	}
}

// The real packaged case: nothing in the user's own directory, but the
// bundled agents are present. That has to count, otherwise a fresh .deb
// install reports "no agents" while pet_control is right there.
func TestEnvAgentsDirTwoDirectories(t *testing.T) {
	perUser := t.TempDir() // exists but empty - the normal state
	system := t.TempDir()
	for _, name := range []string{"pet_control", "play_song"} {
		if err := os.MkdirAll(filepath.Join(system, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(system, name, "agent.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := envOutcome(t, Env{AgentsDirs: []string{perUser, system}}, "env.agents-dir")
	if out.Status != StatusPass {
		t.Fatalf("status = %v detail %q, want pass", out.Status, out.Detail)
	}
	if !strings.Contains(out.Detail, "2 agent(s)") || !strings.Contains(out.Detail, system) {
		t.Errorf("detail = %q, want 2 agent(s) naming %s", out.Detail, system)
	}
}

func TestEnvImages(t *testing.T) {
	if out := envOutcome(t, Env{ImageSource: "off"}, "env.images"); out.Status != StatusSkip {
		t.Errorf("images off: status = %v, want skip", out.Status)
	}
	if out := envOutcome(t, Env{ImageSource: "wiki"}, "env.images"); out.Status != StatusPass {
		t.Errorf("wiki: status = %v, want pass", out.Status)
	}
	if out := envOutcome(t, Env{ImageSource: "gemini"}, "env.images"); out.Status != StatusPass {
		t.Errorf("gemini source: status = %v, want pass", out.Status)
	}
	if out := envOutcome(t, Env{ImageSource: "pixabay", PixabayKey: ""}, "env.images"); out.Status != StatusFail {
		t.Errorf("pixabay without key: status = %v, want fail", out.Status)
	}
	out := envOutcome(t, Env{ImageSource: "pixabay", PixabayKey: "px-key-12345"}, "env.images")
	if out.Status != StatusPass {
		t.Errorf("pixabay with key: status = %v detail %q, want pass", out.Status, out.Detail)
	}
	if strings.Contains(out.Detail, "px-key-12345") {
		t.Errorf("detail leaked the key: %q", out.Detail)
	}
}
