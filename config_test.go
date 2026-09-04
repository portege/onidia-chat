package main

import (
	"os"
	"path/filepath"
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

// TestLoadConfigCharacterName covers the character-name key.
func TestLoadConfigCharacterName(t *testing.T) {
	c, err := LoadConfig(writeTempINI(t, "[character]\ncharacter-name = Onidia\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.CharacterName != "Onidia" {
		t.Errorf("character-name: got %q want \"Onidia\"", c.CharacterName)
	}
}

// TestLoadConfigMute covers the mute key (the settings dialog's MUTE SPEECH
// checkbox): truthy spellings parse as true, everything else (and a missing
// key) leaves speech unmuted.
func TestLoadConfigMute(t *testing.T) {
	c, err := LoadConfig(writeTempINI(t, "[character]\nmute = true\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !c.Mute {
		t.Error("mute = true parsed as false")
	}
	for _, v := range []string{"yes", "on", "1"} {
		c, err := LoadConfig(writeTempINI(t, "[character]\nmute = "+v+"\n"))
		if err != nil {
			t.Fatalf("LoadConfig(mute = %q): %v", v, err)
		}
		if !c.Mute {
			t.Errorf("mute = %q parsed as false", v)
		}
	}
	for _, v := range []string{"false", "0", "off", "junk"} {
		c, err := LoadConfig(writeTempINI(t, "[character]\nmute = "+v+"\n"))
		if err != nil {
			t.Fatalf("LoadConfig(mute = %q): %v", v, err)
		}
		if c.Mute {
			t.Errorf("mute = %q parsed as true", v)
		}
	}
	// A missing key leaves speech unmuted.
	c, err = LoadConfig(writeTempINI(t, "[character]\ncharacter-age = 9\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Mute {
		t.Error("missing mute parsed as true")
	}
}

// writeTempINI writes content to a fresh temp file and returns its path.
func writeTempINI(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat-app.ini")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadConfigCharacterAge covers the character-age key.
func TestLoadConfigCharacterAge(t *testing.T) {
	c, err := LoadConfig(writeTempINI(t, "[character]\ncharacter-age = 11\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.CharacterAge != 11 {
		t.Errorf("character-age: got %d want 11", c.CharacterAge)
	}

	// Missing, commented or junk values leave the field at zero (unset).
	c2, err := LoadConfig(writeTempINI(t, "# character-age = 3\n[character]\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c2.CharacterAge != 0 {
		t.Errorf("commented character-age parsed as %d, want 0", c2.CharacterAge)
	}
	c3, err := LoadConfig(writeTempINI(t, "character-age = soon\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c3.CharacterAge != 0 {
		t.Errorf("junk character-age parsed as %d, want 0", c3.CharacterAge)
	}
}

// TestSetConfigValueReplacesInPlace verifies an existing key is rewritten on
// its own line while every other byte of the file survives.
func TestSetConfigValueReplacesInPlace(t *testing.T) {
	orig := "# header comment\n" +
		"[gemini]\n" +
		"api-key = secret-key\n" +
		"model = gemini-3.6-flash\n" +
		"\n" +
		"[character]\n" +
		"character-age = 7\n" +
		"; trailing comment\n"
	path := writeTempINI(t, orig)

	if err := SetConfigValue(path, "character", "character-age", "13"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(orig, "character-age = 7", "character-age = 13", 1)
	if string(got) != want {
		t.Errorf("rewritten file:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(string(got), "api-key = secret-key") {
		t.Error("api-key was lost while saving the age")
	}
}

// TestSetConfigValueInsertsUnderSection verifies a new key lands right after
// its [section] header when the section exists.
func TestSetConfigValueInsertsUnderSection(t *testing.T) {
	path := writeTempINI(t, "[gemini]\napi-key = k\n\n[character]\n")
	if err := SetConfigValue(path, "character", "character-age", "9"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	b, _ := os.ReadFile(path)
	want := "[gemini]\napi-key = k\n\n[character]\ncharacter-age = 9\n"
	if string(b) != want {
		t.Errorf("file = %q, want %q", b, want)
	}
}

// TestSetConfigValueAppendsSection verifies a key with no matching section is
// appended as a fresh section at the end of the file.
func TestSetConfigValueAppendsSection(t *testing.T) {
	path := writeTempINI(t, "[gemini]\napi-key = k\n")
	if err := SetConfigValue(path, "character", "character-age", "8"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	b, _ := os.ReadFile(path)
	want := "[gemini]\napi-key = k\n\n[character]\ncharacter-age = 8\n"
	if string(b) != want {
		t.Errorf("file = %q, want %q", b, want)
	}
	// The appended value must parse back.
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.CharacterAge != 8 || c.APIKey != "k" {
		t.Errorf("round-trip: age=%d api-key=%q", c.CharacterAge, c.APIKey)
	}
}

// TestSetConfigValueCreatesMissingFile verifies saving into a path that does
// not exist yet creates the file (the no-config-installed case).
func TestSetConfigValueCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat-app.ini")
	if err := SetConfigValue(path, "character", "character-age", "10"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.CharacterAge != 10 {
		t.Errorf("created file parsed age %d, want 10", c.CharacterAge)
	}
}

// TestSetConfigValueSkipsFencedContent verifies keys inside ``` multi-line
// blocks (e.g. prompt text that happens to look like "key = value") are never
// touched, and the fence itself survives.
func TestSetConfigValueSkipsFencedContent(t *testing.T) {
	orig := "system-prompt-multi = ```\n" +
		"You are Buddy.\n" +
		"character-age = 99\n" +
		"```\n"
	path := writeTempINI(t, orig)
	if err := SetConfigValue(path, "character", "character-age", "12"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	b, _ := os.ReadFile(path)
	want := orig + "\n[character]\ncharacter-age = 12\n"
	if string(b) != want {
		t.Errorf("file = %q, want %q (fenced content must be untouched)", b, want)
	}
}

// TestSetConfigValueIdempotent verifies saving twice does not duplicate the
// key or the section.
func TestSetConfigValueIdempotent(t *testing.T) {
	path := writeTempINI(t, "[character]\ncharacter-age = 7\n")
	for i, v := range []string{"9", "11"} {
		if err := SetConfigValue(path, "character", "character-age", v); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	b, _ := os.ReadFile(path)
	if n := strings.Count(string(b), "character-age"); n != 1 {
		t.Errorf("character-age appears %d times after two saves:\n%s", n, b)
	}
	if n := strings.Count(string(b), "[character]"); n != 1 {
		t.Errorf("[character] appears %d times:\n%s", n, b)
	}
}

// TestLoadConfigSleepTime covers the sleep-time key ("HH:00-HH:00").
func TestLoadConfigSleepTime(t *testing.T) {
	c, err := LoadConfig(writeTempINI(t, "[character]\nsleep-time = 22:00-07:00\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !c.SleepSet || c.SleepFrom != 22 || c.SleepTo != 7 {
		t.Errorf("sleep-time: set=%v from=%d to=%d, want true/22/7",
			c.SleepSet, c.SleepFrom, c.SleepTo)
	}

	// Bare hours are accepted too.
	c2, err := LoadConfig(writeTempINI(t, "sleep-time = 7-22\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !c2.SleepSet || c2.SleepFrom != 7 || c2.SleepTo != 22 {
		t.Errorf("bare sleep-time: set=%v from=%d to=%d, want true/7/22",
			c2.SleepSet, c2.SleepFrom, c2.SleepTo)
	}

	// Junk values leave the window unset.
	for _, bad := range []string{"sleep-time = soon", "sleep-time = 24:00-07:00", "sleep-time = 22"} {
		c3, err := LoadConfig(writeTempINI(t, bad+"\n"))
		if err != nil {
			t.Fatalf("LoadConfig(%q): %v", bad, err)
		}
		if c3.SleepSet {
			t.Errorf("%q parsed as set (from=%d to=%d), want unset", bad, c3.SleepFrom, c3.SleepTo)
		}
	}

	// A missing key leaves the window unset.
	c4, err := LoadConfig(writeTempINI(t, "[character]\ncharacter-age = 9\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c4.SleepSet {
		t.Error("missing sleep-time parsed as set, want unset")
	}
}

// TestSetConfigValueMultiLine covers fenced multi-line values: a newline
// value is written as a ``` block and round-trips through LoadConfig, and
// rewriting an existing fenced value replaces it completely (no stray lines
// from the old value survive).
func TestSetConfigValueMultiLine(t *testing.T) {
	path := writeTempINI(t, "[character]\ncharacter-age = 7\n")
	const prompt = "You are Buddy.\nSecond line."
	if err := SetConfigValue(path, "", "system-prompt-multi", prompt); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.SystemPrompt != prompt {
		t.Errorf("round-trip prompt: got %q want %q", c.SystemPrompt, prompt)
	}
	if c.CharacterAge != 7 {
		t.Errorf("age lost while writing the prompt: %d", c.CharacterAge)
	}

	// Rewriting the fenced value swaps the whole block; a single-line
	// value needs no fence at all.
	if err := SetConfigValue(path, "", "system-prompt-multi", "New persona."); err != nil {
		t.Fatalf("SetConfigValue rewrite: %v", err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "Second line.") {
		t.Errorf("old fenced content survived the rewrite:\n%s", b)
	}
	if strings.Contains(string(b), "```") {
		t.Errorf("stale fence left behind:\n%s", b)
	}
	c, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.SystemPrompt != "New persona." {
		t.Errorf("rewritten prompt: got %q want %q", c.SystemPrompt, "New persona.")
	}
	if c.CharacterAge != 7 {
		t.Errorf("age lost on rewrite: %d", c.CharacterAge)
	}
}

// TestWithCharacterSettings covers the in-place identity rewrite: the
// "your name is ..." slot gets the dialog's name with the age right behind
// it, re-saving replaces the previous values instead of stacking, the rest
// of the sentence survives, and a persona without the sentence grows one.
func TestWithCharacterSettings(t *testing.T) {
	persona := "You are Buddy, a tiny chibi girl, your name is sung_jinwoo, a desktop-pet chibi girl."
	want := "You are Buddy, a tiny chibi girl, your name is Onidia, 12 years old, a desktop-pet chibi girl."
	if got := withCharacterSettings(persona, "Onidia", 12); got != want {
		t.Errorf("substitution:\ngot  %q\nwant %q", got, want)
	}

	// Re-saving is idempotent, and new values replace the old age clause.
	if got := withCharacterSettings(want, "Onidia", 12); got != want {
		t.Errorf("not idempotent:\ngot  %q\nwant %q", got, want)
	}
	got13 := withCharacterSettings(want, "Onidia", 13)
	want13 := "You are Buddy, a tiny chibi girl, your name is Onidia, 13 years old, a desktop-pet chibi girl."
	if got13 != want13 {
		t.Errorf("age not replaced:\ngot  %q\nwant %q", got13, want13)
	}

	// The phrase matches in any capitalisation, before a sentence end.
	if got := withCharacterSettings("Your name is Milo.", "Onidia", 7); got != "Your name is Onidia, 7 years old." {
		t.Errorf("capitalised sentence: got %q", got)
	}

	// Clearing the name keeps the persona's written one; the age still lands.
	if got := withCharacterSettings(persona, "", 9); !strings.Contains(got, "your name is sung_jinwoo, 9 years old") {
		t.Errorf("empty name should keep the written name: %q", got)
	}

	// No identity sentence: one is appended on its own line.
	if got := withCharacterSettings("You are Buddy.", "Onidia", 10); got != "You are Buddy.\nYour name is Onidia, 10 years old." {
		t.Errorf("append: got %q", got)
	}

	// A legacy [character-settings] block from an older version is dropped
	// so its stale values cannot contradict the rewritten sentence.
	legacy := persona + "\n\n[character-settings]\nYour name is Old. You are 99 years old; keep your replies age-appropriate.\n[/character-settings]"
	if got := withCharacterSettings(legacy, "Onidia", 12); strings.Contains(got, "character-settings") ||
		strings.Contains(got, "Old") || !strings.Contains(got, want) {
		t.Errorf("legacy block not stripped:\n%s", got)
	}

	// An empty prompt grows the built-in persona so a first save cannot
	// drop the default character definition.
	fresh := withCharacterSettings("", "Onidia", 10)
	if !strings.HasPrefix(fresh, botPersona) || !strings.Contains(fresh, "Your name is Onidia, 10 years old.") {
		t.Errorf("empty prompt should grow the default persona:\n%s", fresh)
	}
}

// TestBakeCharacterPrompt runs the settings dialog's persona rewrite
// against a copy of the shipped INI: the "your name is ..." sentence is
// updated in place, the API keys and the rest of the persona survive, and a
// second bake rewrites the same sentence instead of stacking.
func TestBakeCharacterPrompt(t *testing.T) {
	ship := defaultConfigPath()
	if ship == "" {
		t.Skip("no shipped chat-app.ini found")
	}
	raw, err := os.ReadFile(ship)
	if err != nil {
		t.Fatal(err)
	}
	path := writeTempINI(t, string(raw))
	before, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := bakeCharacterPrompt(path, "Onidia", 12); err != nil {
		t.Fatalf("bake: %v", err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SystemPrompt, "your name is Onidia, 12 years old") {
		t.Errorf("persona lacks the baked name/age:\n%s", c.SystemPrompt)
	}
	if !strings.Contains(c.SystemPrompt, "a tiny cheerful chat companion") {
		t.Errorf("original persona lost:\n%s", c.SystemPrompt)
	}
	if c.APIKey == "" || c.CharacterAge != before.CharacterAge {
		t.Errorf("other keys damaged by the bake: api-key=%q age=%d (was %d)",
			c.APIKey, c.CharacterAge, before.CharacterAge)
	}
	// A legacy [character-settings] block in the input must not survive.
	if strings.Contains(c.SystemPrompt, "character-settings") {
		t.Errorf("legacy block not stripped:\n%s", c.SystemPrompt)
	}
	if n := strings.Count(strings.ToLower(c.SystemPrompt), "your name is"); n != 1 {
		t.Errorf("identity sentence appears %d times:\n%s", n, c.SystemPrompt)
	}

	// Baking again with different values rewrites the same sentence.
	if err := bakeCharacterPrompt(path, "Buddy", 9); err != nil {
		t.Fatalf("second bake: %v", err)
	}
	c, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.SystemPrompt, "Onidia") ||
		!strings.Contains(c.SystemPrompt, "your name is Buddy, 9 years old") {
		t.Errorf("second bake did not replace the values:\n%s", c.SystemPrompt)
	}
	if n := strings.Count(strings.ToLower(c.SystemPrompt), "your name is"); n != 1 {
		t.Errorf("identity sentence stacked:\n%s", c.SystemPrompt)
	}
}

// TestSetConfigValueOnShippedINI runs a save against a copy of the real,
// shipped chat-app.ini: its API keys, the multi-line prompt fence and all
// comments must survive, and the age must land under [character].
func TestSetConfigValueOnShippedINI(t *testing.T) {
	ship := defaultConfigPath()
	if ship == "" {
		t.Skip("no shipped chat-app.ini found")
	}
	raw, err := os.ReadFile(ship)
	if err != nil {
		t.Fatal(err)
	}
	path := writeTempINI(t, string(raw))

	if err := SetConfigValue(path, "character", "character-age", "11"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "api-key = ") {
		t.Error("api-key lost while saving the age")
	}
	if !strings.Contains(got, "system-prompt-multi = ```") || !strings.Contains(got, "the tag is stripped before display.") {
		t.Error("multi-line system prompt fence damaged while saving the age")
	}
	if n := strings.Count(got, "character-age = 11"); n != 1 {
		t.Errorf("character-age = 11 appears %d times:\n%s", n, got)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.CharacterAge != 11 {
		t.Errorf("round-trip age: got %d want 11", c.CharacterAge)
	}
	if c.SystemPrompt == "" {
		t.Error("system prompt lost while saving the age")
	}

	// The sleep window saves into the same file just as cleanly.
	if err := SetConfigValue(path, "character", "sleep-time", "21:00-08:00"); err != nil {
		t.Fatalf("SetConfigValue sleep-time: %v", err)
	}
	b, _ = os.ReadFile(path)
	if n := strings.Count(string(b), "sleep-time = 21:00-08:00"); n != 1 {
		t.Errorf("sleep-time appears %d times:\n%s", n, b)
	}
	c, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.SleepSet || c.SleepFrom != 21 || c.SleepTo != 8 {
		t.Errorf("round-trip sleep: set=%v from=%d to=%d, want true/21/8",
			c.SleepSet, c.SleepFrom, c.SleepTo)
	}
	if c.CharacterAge != 11 {
		t.Errorf("age clobbered by the sleep save: %d, want 11", c.CharacterAge)
	}
}
