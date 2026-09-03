// config.go - a tiny INI parser for chat-app configuration.
//
// Keeps zero external dependencies: the app uses only the Go standard
// library. Supports:
//   - key = value pairs
//   - [section] headers (ignored for now, but accepted)
//   - # and ; line comments
//   - multi-line values with + continuation (optional, simple)

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds settings loaded from an INI file. All fields are optional;
// the app falls back to flag defaults / built-in constants for any missing key.
type Config struct {
	APIKey           string `ini:"api-key"`
	Model            string `ini:"model"`
	APIURL           string `ini:"api-url"`
	PetPipe          string `ini:"pet-pipe"`
	SystemPrompt     string `ini:"system-prompt"`
	SystemFile       string `ini:"system-file"`
	SystemPromptFull string `ini:"system-prompt-multi"` // multi-line alias
	Images           bool   `ini:"images"`              // enable image replies (legacy: use image-source)
	ImageSource      string `ini:"image-source"`        // "pixabay" | "wiki" | "gemini" | "off"
	PixabayKey       string `ini:"pixabay-key"`         // Pixabay API key (empty = env/built-in default)
	ForceImage       string `ini:"force-image"`         // always fetch image for this keyword
	TTS              string `ini:"tts"`                 // "on" (default) | "off"
	TTSKey           string `ini:"tts-key"`             // Typecast API key
	TTSVoice         string `ini:"tts-voice"`           // Typecast voice id
	Provider         string `ini:"provider"`            // "gemini" | "bedrock"
	AWSProfile       string `ini:"aws-profile"`         // AWS shared profile name
	AWSRegion        string `ini:"aws-region"`          // AWS region for Bedrock
	CharacterAge     int    `ini:"character-age"`       // chat character's age (settings dialog, 7-13)
	CharacterName    string `ini:"character-name"`      // chat character's name (settings dialog)
	SleepFrom        int    // sleep-window start hour (0-23), from sleep-time
	SleepTo          int    // sleep-window end hour (0-23), from sleep-time
	SleepSet         bool   // sleep-time was present in the config
}

// LoadConfig reads a simple INI file and returns populated Config.
// Unknown keys are silently ignored. No sections are enforced yet.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := &Config{}
	lines := strings.Split(string(raw), "\n")
	var curKey string
	var curVal strings.Builder
	var inMultiline bool

	flush := func() {
		if curKey != "" {
			applyConfigField(cfg, curKey, strings.TrimSpace(curVal.String()))
		}
		curKey = ""
		curVal.Reset()
	}

	for _, line := range lines {
		s := strings.TrimSpace(line)
		if inMultiline {
			if strings.HasPrefix(s, "```") {
				// end of multi-line block
				inMultiline = false
				flush()
				continue
			}
			curVal.WriteString(line + "\n")
			continue
		}

		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") {
			continue
		}
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			continue // section header - ignored
		}

		// Check for multi-line opener
		if idx := strings.Index(s, "="); idx > 0 {
			key := strings.TrimSpace(s[:idx])
			val := strings.TrimSpace(s[idx+1:])
			if strings.HasPrefix(val, "```") {
				// multi-line value starts here
				flush() // commit any previous key
				curKey = key
				curVal.Reset()
				inMultiline = true
				continue
			}
			flush()
			curKey = key
			curVal.WriteString(val)
			flush()
		}
	}
	flush()

	// If system-prompt-multi was used, prefer it
	if cfg.SystemPromptFull != "" {
		cfg.SystemPrompt = cfg.SystemPromptFull
	}

	return cfg, nil
}

func applyConfigField(cfg *Config, key, val string) {
	switch key {
	case "api-key":
		cfg.APIKey = val
	case "model":
		cfg.Model = val
	case "api-url":
		cfg.APIURL = val
	case "pet-pipe":
		cfg.PetPipe = val
	case "system-prompt":
		cfg.SystemPrompt = val
	case "system-file":
		cfg.SystemFile = val
	case "system-prompt-multi":
		cfg.SystemPromptFull = val
	case "images":
		cfg.Images = parseBool(val)
	case "image-source":
		cfg.ImageSource = val
	case "pixabay-key":
		cfg.PixabayKey = val
	case "force-image":
		cfg.ForceImage = val
	case "tts":
		cfg.TTS = val
	case "tts-key":
		cfg.TTSKey = val
	case "tts-voice":
		cfg.TTSVoice = val
	case "provider":
		cfg.Provider = val
	case "aws-profile":
		cfg.AWSProfile = val
	case "aws-region":
		cfg.AWSRegion = val
	case "character-age":
		cfg.CharacterAge = parseIntVal(val)
	case "character-name":
		cfg.CharacterName = val
	case "sleep-time":
		if from, to, ok := parseSleepTime(val); ok {
			cfg.SleepFrom, cfg.SleepTo, cfg.SleepSet = from, to, true
		}
	}
}

// parseIntVal parses a plain integer config value; junk parses as 0.
func parseIntVal(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// parseSleepTime parses a sleep window "22:00-07:00" (the settings dialog's
// format; bare hours like "22-7" are accepted too) into start/end hours.
func parseSleepTime(s string) (from, to int, ok bool) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	from, ok1 := parseHour(parts[0])
	to, ok2 := parseHour(parts[1])
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	return from, to, true
}

// parseHour parses one "HH", "H" or "HH:MM" value into the hour 0..23.
func parseHour(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i] // drop the minutes: the dropdown steps by whole hours
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 23 {
		return 0, false
	}
	return n, true
}

func parseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "yes" || s == "1" || s == "on"
}

// SetConfigValue sets a single key = value pair in an INI file while
// preserving everything else: comments, sections and unknown keys stay
// byte-for-byte identical (the settings dialog must not destroy API keys or
// hand-tuned prompts). The key is matched on uncommented lines outside ```
// multi-line fences only. When the key already exists its line is rewritten
// in place; otherwise it is inserted right after the [section] header, or
// appended as a fresh section at the end of the file. A missing file is
// created. The write is atomic (temp file + rename).
func SetConfigValue(path, section, key, val string) error {
	var lines []string
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		lines = strings.Split(string(raw), "\n")
	case os.IsNotExist(err):
		lines = []string{
			"# chat-app configuration (INI format)",
			"# Partially maintained by the in-app settings dialog.",
			"",
		}
	default:
		return fmt.Errorf("read config %s: %w", path, err)
	}

	// Pass 1: find an existing uncommented "key = ..." line (outside fences).
	inFence := false
	for i, line := range lines {
		s := strings.TrimSpace(line)
		if inFence {
			if strings.HasPrefix(s, "```") {
				inFence = false // end of the multi-line block
			}
			continue
		}
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") {
			continue
		}
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			continue // section header
		}
		idx := strings.Index(s, "=")
		if idx <= 0 {
			continue
		}
		k := strings.TrimSpace(s[:idx])
		if strings.HasPrefix(strings.TrimSpace(s[idx+1:]), "```") {
			inFence = true // multi-line value starts on this line
		}
		if k == key {
			lines[i] = key + " = " + val
			return writeConfigLines(path, lines)
		}
	}

	// Pass 2: the key is new - insert it right after its section header.
	if section != "" {
		header := "[" + section + "]"
		for i, line := range lines {
			if strings.TrimSpace(line) == header {
				out := make([]string, 0, len(lines)+1)
				out = append(out, lines[:i+1]...)
				out = append(out, key+" = "+val)
				out = append(out, lines[i+1:]...)
				return writeConfigLines(path, out)
			}
		}
	}

	// Pass 3: no section either - append one at the end of the file.
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	lines = append(lines[:end:end], "")
	if section != "" {
		lines = append(lines, "["+section+"]")
	}
	lines = append(lines, key+" = "+val)
	return writeConfigLines(path, lines)
}

// writeConfigLines joins and atomically replaces the INI file.
func writeConfigLines(path string, lines []string) error {
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n" // INI files always end with a newline
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace config %s: %w", path, err)
	}
	return nil
}
