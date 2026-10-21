package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDiagnosticSettingsPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat-app.ini")
	if err := os.WriteFile(path, []byte("provider = openrouter\napi-key = ini-key\nopenrouter-api-url = https://config.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "environment-key")

	settings := resolveDiagnosticSettings(path, "", "", "", "", "", "", "")
	if settings["provider"] != "openrouter" || settings["openrouter-api-key"] != "environment-key" {
		t.Fatalf("resolved settings = %#v, want config provider and environment key", settings)
	}
	if settings["tts"] != "" {
		t.Fatalf("unspecified TTS value = %q, want app default", settings["tts"])
	}

	settings = resolveDiagnosticSettings(path, "gemini", "flag-key", "https://flag.invalid", "", "", "", "")
	if settings["provider"] != "gemini" || settings["gemini-api-key"] != "flag-key" || settings["gemini-api-url"] != "https://flag.invalid" {
		t.Fatalf("flag overrides = %#v", settings)
	}
}
