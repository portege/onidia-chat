package main

// Tests for the agent management modes that replace the un-shipped agentctl.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portege/chat-app/agent"
)

// makeAgentFolder writes a minimal, valid agent folder under root.
func makeAgentFolder(t *testing.T, root, folder, id string) string {
	t.Helper()
	dir := filepath.Join(root, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"` + id + `","version":"1.0.0","description":"d","exec":["./run.sh"]}`
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// useSystemDir points the packaged-agents location at a temp dir for the test.
func useSystemDir(t *testing.T, dir string) {
	t.Helper()
	old := agent.SystemDir
	agent.SystemDir = dir
	t.Cleanup(func() { agent.SystemDir = old })
}

// The whole point of agents-disabled: switch a packaged agent off without
// owning it. On a real install the agents sit in root-owned /opt, so editing
// the manifest is not an option and this config key is the only way to do it.
func TestAgentsDisabledConfigEndToEnd(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	useSystemDir(t, "") // ignore any real /opt on this machine

	packaged := t.TempDir()
	useSystemDir(t, packaged)
	makeAgentFolder(t, packaged, "pet_control", "pet_control")
	makeAgentFolder(t, packaged, "play_song", "play_song")

	cfgPath := filepath.Join(t.TempDir(), "chat-app.ini")
	if err := os.WriteFile(cfgPath, []byte("agents-disabled = play_song\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	agent.SetDisabled(strings.Split(cfg.AgentsDisabled, ","))

	ids, _ := agent.DiscoverAll(agent.DefaultDirs())
	if len(ids) != 1 || ids[0] != "pet_control" {
		t.Fatalf("discovered %v, want only pet_control", ids)
	}
	// It is still on disk, just off - so -agents-list can report that
	// distinction instead of the agent looking uninstalled.
	items := agent.ScanInstalledDirs(agent.DefaultDirs())
	if len(items) != 2 {
		t.Fatalf("listed %d agents, want both, disabled or not", len(items))
	}
	off := 0
	for _, it := range items {
		if it.Disabled {
			off++
		}
	}
	if off != 1 {
		t.Errorf("%d agents marked disabled, want 1", off)
	}
}

// A packaged agent must not be removable: dpkg owns it. Removing it would
// leave the package out of step with its file list.
func TestAgentRemoveRefusesPackagedAgent(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	useSystemDir(t, "")
	packaged := t.TempDir()
	useSystemDir(t, packaged)
	makeAgentFolder(t, packaged, "pet_control", "pet_control")

	code := agentRemove("pet_control", agent.DefaultDirs())
	if code != 2 {
		t.Errorf("agentRemove on a packaged agent = %d, want 2 (refused)", code)
	}
	// And the folder is untouched.
	if _, err := os.Stat(filepath.Join(packaged, "pet_control", "agent.json")); err != nil {
		t.Errorf("the packaged agent was disturbed: %v", err)
	}
}

// Removing one of the user's own agents works and takes the folder with it.
func TestAgentRemoveOwnAgent(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	userHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", userHome)
	useSystemDir(t, "")
	makeAgentFolder(t, filepath.Join(userHome, "chat-app", "agents"), "my_agent", "my_agent")

	if code := agentRemove("my_agent", agent.DefaultDirs()); code != 0 {
		t.Fatalf("agentRemove = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(userHome, "chat-app", "agents", "my_agent")); !os.IsNotExist(err) {
		t.Errorf("the agent folder still exists: %v", err)
	}
}

// Installing never targets the packaged directory - /opt is root-owned, and
// needing sudo to add a pet behaviour would be a bad surprise.
func TestAgentInstallTargetsUserDir(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	userHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", userHome)
	useSystemDir(t, "")

	// Install takes the folder that holds agent.json, so hand it that folder
	// rather than its parent.
	src := makeAgentFolder(t, t.TempDir(), "fresh", "fresh_agent")

	if code := agentInstall(src, nil); code != 0 {
		t.Fatalf("agentInstall = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(userHome, "chat-app", "agents", "fresh_agent", "agent.json")); err != nil {
		t.Errorf("agent was not installed into the user directory: %v", err)
	}
}

// A relative path is never mistaken for a registry id, even though
// "fresh_agent" is a legal agent id.
func TestLooksLikePath(t *testing.T) {
	for _, src := range []string{"./a", "/a", "../a", "~/a", "a.zip", "dir/a"} {
		if !looksLikePath(src) {
			t.Errorf("looksLikePath(%q) = false, want true", src)
		}
	}
	for _, src := range []string{"play_song", "my_agent"} {
		if looksLikePath(src) {
			t.Errorf("looksLikePath(%q) = true, want false (it is a registry id)", src)
		}
	}
}
