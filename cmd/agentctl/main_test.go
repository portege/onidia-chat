package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portege/chat-app/agent"
)

func buildAgentctl(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agentctl")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/agentctl")
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build agentctl: %v\n%s", err, out)
	}
	return bin
}

func TestAgentctlKeygenAndSign(t *testing.T) {
	agentctl := buildAgentctl(t)

	keyDir := t.TempDir()
	out, err := exec.Command(agentctl, "-out", keyDir, "keygen").CombinedOutput()
	if err != nil {
		t.Fatalf("agentctl keygen failed: %v\n%s", err, out)
	}

	pubFile := filepath.Join(keyDir, "agent_ed25519.pub")
	privFile := filepath.Join(keyDir, "agent_ed25519.priv")
	if _, err := os.Stat(pubFile); err != nil {
		t.Fatalf("pub key file missing: %v", err)
	}
	if _, err := os.Stat(privFile); err != nil {
		t.Fatalf("priv key file missing: %v", err)
	}

	agentDir := filepath.Join(t.TempDir(), "dummy")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"dummy","version":"1.0.0","description":"test dummy","exec":["./dummy.sh"]}`
	if err := os.WriteFile(filepath.Join(agentDir, "agent.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "dummy.sh"), []byte("#!/bin/sh\necho OK\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Sign using key file
	signOut, err := exec.Command(agentctl, "-key", privFile, "sign", agentDir).CombinedOutput()
	if err != nil {
		t.Fatalf("sign failed: %v\n%s", err, signOut)
	}
	if !agent.HasSignature(agentDir) {
		t.Fatal("agent.json.sig not created")
	}

	// Validate with pubkey
	pubHexBytes, _ := os.ReadFile(pubFile)
	pubHex := strings.TrimSpace(string(pubHexBytes))
	valOut, err := exec.Command(agentctl, "-key", pubHex, "-require-signature", "validate", agentDir).CombinedOutput()
	if err != nil {
		t.Fatalf("validate failed: %v\n%s", err, valOut)
	}
	if !strings.Contains(string(valOut), "[signed]") {
		t.Errorf("validate output missing [signed]: %s", valOut)
	}
}

func TestAgentctlPackSearchInstallUpdate(t *testing.T) {
	agentctl := buildAgentctl(t)

	pub, priv, _ := agent.GenerateKey()
	pubHex := agent.EncodePublicKey(pub)
	privHex := agent.EncodePrivateKey(priv)

	srcDir := t.TempDir()
	agentFolder := filepath.Join(srcDir, "sample_tool")
	if err := os.MkdirAll(agentFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	m1 := `{"id":"sample_tool","version":"1.0.0","description":"Sample tool for tests","exec":["./run.sh"]}`
	os.WriteFile(filepath.Join(agentFolder, "agent.json"), []byte(m1), 0o644)
	os.WriteFile(filepath.Join(agentFolder, "run.sh"), []byte("#!/bin/sh\necho 'OK done'\n"), 0o755)

	// Sign v1.0.0
	signCmd := exec.Command(agentctl, "-key", privHex, "sign", agentFolder)
	if out, err := signCmd.CombinedOutput(); err != nil {
		t.Fatalf("sign v1: %v\n%s", err, out)
	}

	// Pack v1.0.0
	zipV1 := filepath.Join(t.TempDir(), "sample_tool-1.0.0.zip")
	packCmd := exec.Command(agentctl, "-out", zipV1, "pack", agentFolder)
	if out, err := packCmd.CombinedOutput(); err != nil {
		t.Fatalf("pack v1: %v\n%s", err, out)
	}
	shaV1, err := agent.HashFile(zipV1)
	if err != nil {
		t.Fatal(err)
	}

	// Update agent to v1.1.0
	m2 := `{"id":"sample_tool","version":"1.1.0","description":"Sample tool upgraded","exec":["./run.sh"]}`
	os.WriteFile(filepath.Join(agentFolder, "agent.json"), []byte(m2), 0o644)
	if out, err := exec.Command(agentctl, "-key", privHex, "sign", agentFolder).CombinedOutput(); err != nil {
		t.Fatalf("sign v2: %v\n%s", err, out)
	}
	zipV2 := filepath.Join(t.TempDir(), "sample_tool-1.1.0.zip")
	if out, err := exec.Command(agentctl, "-out", zipV2, "pack", agentFolder).CombinedOutput(); err != nil {
		t.Fatalf("pack v2: %v\n%s", err, out)
	}
	shaV2, err := agent.HashFile(zipV2)
	if err != nil {
		t.Fatal(err)
	}

	// HTTP Registry server
	var currentReg agent.RegistryIndex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/registry.json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(currentReg)
		case "/sample_tool-1.0.0.zip":
			http.ServeFile(w, r, zipV1)
		case "/sample_tool-1.1.0.zip":
			http.ServeFile(w, r, zipV2)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	currentReg = agent.RegistryIndex{
		Version: 1,
		Agents: []agent.RegistryEntry{
			{
				ID:          "sample_tool",
				Version:     "1.0.0",
				Description: "Sample tool for tests",
				URL:         srv.URL + "/sample_tool-1.0.0.zip",
				SHA256:      shaV1,
				Signer:      agent.SignerID(pub),
			},
		},
	}

	agentsDir := t.TempDir()

	// 1. Search registry
	regURL := srv.URL + "/registry.json"
	searchOut, err := exec.Command(agentctl, "-dir", agentsDir, "-registry", regURL, "search", "sample").CombinedOutput()
	if err != nil {
		t.Fatalf("search failed: %v\n%s", err, searchOut)
	}
	if !strings.Contains(string(searchOut), "sample_tool") {
		t.Errorf("search results missing sample_tool: %s", searchOut)
	}

	// 2. Install by name from registry
	installOut, err := exec.Command(agentctl, "-dir", agentsDir, "-registry", regURL, "-key", pubHex, "-require-signature", "install", "sample_tool").CombinedOutput()
	if err != nil {
		t.Fatalf("install by name failed: %v\n%s", err, installOut)
	}

	// Check installed version
	listOut, err := exec.Command(agentctl, "-dir", agentsDir, "list").CombinedOutput()
	if err != nil {
		t.Fatalf("list failed: %v\n%s", err, listOut)
	}
	if !strings.Contains(string(listOut), "sample_tool 1.0.0") {
		t.Errorf("list missing 1.0.0: %s", listOut)
	}

	// 3. Upgrade registry to v1.1.0 and run update
	currentReg.Agents[0].Version = "1.1.0"
	currentReg.Agents[0].URL = srv.URL + "/sample_tool-1.1.0.zip"
	currentReg.Agents[0].SHA256 = shaV2

	upOut, err := exec.Command(agentctl, "-dir", agentsDir, "-registry", regURL, "-key", pubHex, "-require-signature", "update").CombinedOutput()
	if err != nil {
		t.Fatalf("update failed: %v\n%s", err, upOut)
	}
	if !strings.Contains(string(upOut), "updated sample_tool to 1.1.0") {
		t.Errorf("update output unexpected: %s", upOut)
	}

	// Verify updated on disk
	listOut2, _ := exec.Command(agentctl, "-dir", agentsDir, "list").CombinedOutput()
	if !strings.Contains(string(listOut2), "sample_tool 1.1.0") {
		t.Errorf("list missing updated 1.1.0: %s", listOut2)
	}
}
