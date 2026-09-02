package main

import (
	"os"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	cfg := `[gemini]
api-key = test-key
model = gemini-test
api-url = https://example.com
system-prompt = hello world
image-source = gemini
pixabay-key = test-px-key

[ui]
pet-pipe = off
images = false
`
	tmp, err := os.CreateTemp("", "chat-app-config-*.ini")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(cfg); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	c, err := LoadConfig(tmp.Name())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.APIKey != "test-key" {
		t.Errorf("api-key: got %q want %q", c.APIKey, "test-key")
	}
	if c.Model != "gemini-test" {
		t.Errorf("model: got %q want %q", c.Model, "gemini-test")
	}
	if c.APIURL != "https://example.com" {
		t.Errorf("api-url: got %q want %q", c.APIURL, "https://example.com")
	}
	if c.SystemPrompt != "hello world" {
		t.Errorf("system-prompt: got %q want %q", c.SystemPrompt, "hello world")
	}
	if c.PetPipe != "off" {
		t.Errorf("pet-pipe: got %q want %q", c.PetPipe, "off")
	}
	if c.ImageSource != "gemini" {
		t.Errorf("image-source: got %q want %q", c.ImageSource, "gemini")
	}
	if c.PixabayKey != "test-px-key" {
		t.Errorf("pixabay-key: got %q want %q", c.PixabayKey, "test-px-key")
	}
	if c.Images != false {
		t.Errorf("images: got %v want false", c.Images)
	}
}

func TestLoadConfigMultiLine(t *testing.T) {
	cfg := "[gemini]\n" +
		"system-prompt-multi = ```\n" +
		"You are Buddy.\n" +
		"Keep replies short.\n" +
		"```\n"

	tmp, err := os.CreateTemp("", "chat-app-config-*.ini")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(cfg); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	c, err := LoadConfig(tmp.Name())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := "You are Buddy.\nKeep replies short."
	got := strings.TrimSpace(c.SystemPrompt)
	if got != want {
		t.Errorf("system-prompt-multi: got %q want %q", got, want)
	}
}

// TestDefaultConfigPath verifies the conventional chat-app.ini is auto-loaded
// when -config is not given. The repo ships chat-app.ini, so running from the
// package directory must find it (via the working-directory check).
func TestDefaultConfigPath(t *testing.T) {
	p := defaultConfigPath()
	if p == "" {
		t.Fatal("defaultConfigPath() = \"\", want the shipped chat-app.ini")
	}
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig(%q): %v", p, err)
	}
	if c.SystemPrompt == "" && c.SystemPromptFull == "" {
		t.Error("auto-loaded config has no system prompt; expect the multi-line persona")
	}
	if c.APIKey == "" {
		t.Error("auto-loaded config has no api-key; expect the shipped key")
	}
}
