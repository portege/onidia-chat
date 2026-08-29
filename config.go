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
	Provider         string `ini:"provider"`            // "gemini" | "bedrock"
	AWSProfile       string `ini:"aws-profile"`         // AWS shared profile name
	AWSRegion        string `ini:"aws-region"`          // AWS region for Bedrock
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
	case "provider":
		cfg.Provider = val
	case "aws-profile":
		cfg.AWSProfile = val
	case "aws-region":
		cfg.AWSRegion = val
	}
}

func parseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "yes" || s == "1" || s == "on"
}
