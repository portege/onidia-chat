package preflight

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadINIMirrorsChatAppParser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat-app.ini")
	content := `# a comment
; another comment
provider = bedrock

[gemini]
api-key = KEYFROMGEMINI
model = gemini-3.6-flash
api-url = https://generativelanguage.googleapis.com

[ollama]
api-url = http://localhost:8000
model = qwen2:1.5b

[ui]
system-prompt-multi = ` + "```" + `
multi line body
` + "```" + `
tts = on
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	kv, err := ReadINI(path)
	if err != nil {
		t.Fatal(err)
	}
	// Sections are ignored and keys are flat / last-wins - exactly like
	// onidia-chat's LoadConfig. (qwen2 beats gemini-3.6-flash: it is later.)
	if Get(kv, "provider") != "bedrock" {
		t.Errorf("provider = %q, want bedrock", Get(kv, "provider"))
	}
	if Get(kv, "model") != "qwen2:1.5b" {
		t.Errorf("model = %q, want the last occurrence qwen2:1.5b", Get(kv, "model"))
	}
	if Get(kv, "api-key") != "KEYFROMGEMINI" {
		t.Errorf("api-key = %q, want KEYFROMGEMINI", Get(kv, "api-key"))
	}
	if Get(kv, "api-url") != "http://localhost:8000" {
		t.Errorf("api-url = %q, want the last occurrence (ollama's)", Get(kv, "api-url"))
	}
	if Get(kv, "tts") != "on" {
		t.Errorf("tts = %q, want on", Get(kv, "tts"))
	}
	// Multi-line value captured; comments and section headers skipped.
	if got := Get(kv, "system-prompt-multi"); got == "" || got[0] == '[' {
		t.Errorf("system-prompt-multi = %q, want the fenced body", got)
	}
	if _, ok := kv["[gemini]"]; ok {
		t.Error("section headers must not become keys")
	}
	if Get(kv, "# a comment") != "" {
		t.Error("comments must be skipped")
	}
	if Get(kv, "absent") != "" {
		t.Error("Get on a missing key must be empty")
	}
}

func TestReadINIMissingFile(t *testing.T) {
	if _, err := ReadINI(filepath.Join(t.TempDir(), "nope.ini")); err == nil {
		t.Fatal("want an error for a missing file")
	}
}
