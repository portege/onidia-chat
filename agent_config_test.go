package main

// agent_config_test.go - the agents/media INI keys and path helpers
// added with the Phase 1 agents (music-dir, video-dir, agents-*).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentConfigKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "chat-app.ini")
	ini := "music-dir = ~/tunes\n" +
		"video-dir = /data/movies\n" +
		"agents-off = true\n" +
		"agents-disabled = play_movie, media_control\n" +
		"agents-dir = /opt/agents\n"
	if err := os.WriteFile(p, []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MusicDir != "~/tunes" || cfg.VideoDir != "/data/movies" {
		t.Errorf("media dirs = %q / %q", cfg.MusicDir, cfg.VideoDir)
	}
	if !cfg.AgentsOff || cfg.AgentsDir != "/opt/agents" {
		t.Errorf("agents keys = off=%v dir=%q", cfg.AgentsOff, cfg.AgentsDir)
	}
	// The per-agent switch, kept as a raw string so main.go can split it the
	// same way the flag is split.
	if got := strings.Split(cfg.AgentsDisabled, ","); len(got) != 2 ||
		strings.TrimSpace(got[0]) != "play_movie" || strings.TrimSpace(got[1]) != "media_control" {
		t.Errorf("agents-disabled = %q, want play_movie and media_control", cfg.AgentsDisabled)
	}
	// Phase 4 signature and registry keys
	p2 := filepath.Join(t.TempDir(), "onidia-chat-sec.ini")
	ini2 := "agents-key = deadbeef\n" +
		"agents-require-sig = true\n" +
		"agents-registry = https://example.com/registry.json\n"
	if err := os.WriteFile(p2, []byte(ini2), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := LoadConfig(p2)
	if err != nil {
		t.Fatalf("LoadConfig sec: %v", err)
	}
	if cfg2.AgentsKey != "deadbeef" || !cfg2.AgentsRequireSig || cfg2.AgentsRegistry != "https://example.com/registry.json" {
		t.Errorf("Phase 4 config mismatch: %+v", cfg2)
	}

}

func TestFirstNonEmptyAndExpandHome(t *testing.T) {
	if got := firstNonEmpty("", "  ", "cfg"); got != "cfg" {
		t.Errorf("firstNonEmpty = %q, want cfg", got)
	}
	if got := firstNonEmpty("flag", "cfg"); got != "flag" {
		t.Errorf("firstNonEmpty = %q, want flag (flag beats config)", got)
	}
	home, _ := os.UserHomeDir()
	if got := expandHome("~/tunes"); got != filepath.Join(home, "tunes") {
		t.Errorf("expandHome(~/tunes) = %q, want %q", got, filepath.Join(home, "tunes"))
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("expandHome absolute = %q", got)
	}
	if got := expandHome(""); got != "" {
		t.Errorf("expandHome empty = %q", got)
	}
	if !strings.HasPrefix(expandHome("~"), home) && expandHome("~") != home {
		t.Errorf("expandHome(~) = %q, want home", expandHome("~"))
	}
}
