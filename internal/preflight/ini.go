// ini.go - a minimal chat-app.ini reader for the standalone preflight CLI.
//
// It intentionally mirrors chat-app's own parser (config.go LoadConfig): the
// file is read flat - section headers are accepted but IGNORED and keys are
// last-wins - with # / ; comments and ``` multi-line values. preflight must
// see exactly the values the app will act on, quirks included.
package preflight

import (
	"fmt"
	"os"
	"strings"
)

// ReadINI parses path into a flat key -> value map (sections ignored,
// last occurrence wins), the way chat-app's LoadConfig resolves keys.
func ReadINI(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	out := map[string]string{}
	var curKey string
	var curVal strings.Builder
	var inMultiline bool

	flush := func() {
		if curKey != "" {
			out[curKey] = strings.TrimSpace(curVal.String())
		}
		curKey = ""
		curVal.Reset()
	}

	for _, line := range strings.Split(string(raw), "\n") {
		s := strings.TrimSpace(line)
		if inMultiline {
			if strings.HasPrefix(s, "```") {
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
			continue // section header - ignored, exactly like config.go
		}
		idx := strings.Index(s, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(s[:idx])
		val := strings.TrimSpace(s[idx+1:])
		if strings.HasPrefix(val, "```") {
			flush()
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
	flush() // commit a multi-line value cut short by EOF
	return out, nil
}

// Get returns the trimmed value for key, or "" when absent.
func Get(kv map[string]string, key string) string {
	return strings.TrimSpace(kv[key])
}

// DefaultINIPath mirrors chat-app's defaultConfigPath: ./chat-app.ini first,
// then one next to the running binary. Returns "" when neither exists.
func DefaultINIPath() string {
	if _, err := os.Stat("chat-app.ini"); err == nil {
		return "chat-app.ini"
	}
	return ""
}
