// Package confpath answers one question for every onidia-chat binary: which
// chat-app.ini should I read?
//
// The answer used to be written down in three places - main.defaultConfigPath,
// preflight.DefaultINIPath and the pet's own loader in the sibling onidia
// module - and the copies drifted. preflight's copy checked only
// ./chat-app.ini while its
// comment claimed it also checked next to the binary and the user's config
// dir, so on a package install - where neither of the first two exists - it
// read no config at all and cheerfully reported keys as missing that were
// perfectly well set. One list, one order, one package.
package confpath

import (
	"os"
	"path/filepath"
)

// Name is the config file's name. It is the app's former name, kept for the
// same reason the config directory keeps "chat-app": a rename would orphan
// every existing install's api-key.
const Name = "chat-app.ini"

// Dir returns the directory onidia-chat treats as its config home:
// $XDG_CONFIG_HOME/chat-app, else ~/.config/chat-app, and "" when neither can
// be determined - so callers get a path that does not exist rather than a bare
// relative one.
//
// The component is deliberately "chat-app" and not "onidia-chat". The app was
// renamed, but this path holds the user's api-key, their downloaded agents and
// the whisper helper, and a rename would orphan all three on every existing
// machine. The old name stays until a release ships a real migration.
func Dir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "chat-app")
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return ""
	}
	return filepath.Join(h, ".config", "chat-app")
}

// Candidates returns every chat-app.ini path, in the order it is tried:
//
//  1. ./chat-app.ini             - running from a checkout or a config directory
//  2. next to the running binary - a portable or relocated install
//  3. Dir()/chat-app.ini         - what a package install uses
//
// (3) is the only one that can fire on a .deb install: the binary lives in
// /opt/onidia/bin and the shipped template in /opt/onidia/share, so neither of
// the first two can see it. The template sits in share/ and not bin/ on
// purpose - in bin/, (2) would auto-load a root-owned file and shadow the
// user's own config.
//
// The pet walks the same list; see configCandidates in the sibling onidia
// module's sleep.go. The two are separate Go modules and cannot share this
// function, so the order is what has to be kept in step - if it is not, the
// window and the pet will disagree about when she goes to sleep. The order is
// pinned here by TestDefaultFallsBackToUserConfig, from the pet's side by
// onidia's TestConfigCandidatesMatchChatWindow, and written down for humans in
// web/docs/setup-gemini.html under "Where she looks for it". Three places, one
// order, and none of them can import the others.
func Candidates() []string {
	out := []string{Name}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), Name))
	}
	if d := Dir(); d != "" {
		out = append(out, filepath.Join(d, Name))
	}
	return out
}

// Default returns the first candidate that exists, or "" when none does - in
// which case the caller keeps its config disabled rather than inventing one.
func Default() string {
	for _, p := range Candidates() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
