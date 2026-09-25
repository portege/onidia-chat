package agent

// env.go - extra environment passed to every spawned agent process.
// chat-app resolves user config (media folders etc.) into CHAT_APP_* vars
// ONCE at startup; agents inherit them on top of the normal environment.
// Empty values are skipped so an agent's own defaults (~/Music, ...) apply.

import (
	"os"
	"sort"
	"sync"
)

var (
	extraEnvMu sync.Mutex
	extraEnv   []string // "KEY=value" pairs, sorted for stable output
)

// SetExtraEnv replaces the extra environment handed to spawned agents.
// Nil/empty clears it. Call once at startup (and from tests).
func SetExtraEnv(m map[string]string) {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+m[k])
	}
	extraEnvMu.Lock()
	extraEnv = pairs
	extraEnvMu.Unlock()
}

// envFor builds cmd.Env: the full process environment plus the extras
// (extras win on duplicate keys - later entries override in execve).
func envFor() []string {
	extraEnvMu.Lock()
	defer extraEnvMu.Unlock()
	if len(extraEnv) == 0 {
		return nil // nil = inherit (exec default)
	}
	return append(os.Environ(), extraEnv...)
}
