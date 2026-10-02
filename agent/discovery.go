package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discovery.go - auto-detection of downloaded agents.
//
// The agents directory holds one subfolder per agent, each with an
// agent.json at its root:
//
//	~/.config/chat-app/agents/
//	  hello_world/
//	    agent.json
//	    hello_world.sh
//
// Discover scans every subfolder, validates its manifest, resolves the
// executable and registers it. Broken folders are reported and skipped -
// one bad download never brings the app down. Discovery is startup-only;
// drop a new folder in and restart (or run `agentctl run` to test it
// standalone).

// DefaultDir returns the conventional per-user agents directory
// ($XDG_CONFIG_HOME/chat-app/agents or ~/.config/chat-app/agents).
//
// The "chat-app" component is the app's former name and is kept on purpose:
// this directory is where every already-installed agent lives, alongside the
// ini holding the user's key. Renaming it to "onidia-chat" would make a fresh
// install look as though no agents were installed at all.
func DefaultDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "chat-app", "agents")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "chat-app", "agents")
}

// SystemDir is where a package install keeps the bundled agents. The combined
// onidia package puts them in /opt/onidia/share/agents, and onidia-chat used to
// miss them entirely: discovery only ever looked in the per-user directory,
// so on a fresh install every bundled agent - including pet_control, the one
// that makes the pet act - was simply absent.
//
// It is a var rather than a const for one reason: the packaged path needs root
// to create, so as a const the one behaviour that matters could only be tested
// on a machine where the test would skip. Overridable, it is tested for real.
var SystemDir = "/opt/onidia/share/agents"

// DefaultDirs returns the directories to scan, in precedence order: the
// per-user one first, then the system one if it exists. First registration
// wins, so a user's own agent shadows a bundled one of the same id rather than
// the other way round.
//
// A caller that was given an explicit directory (the -agents-dir flag or the
// agents-dir key) must NOT use this - that setting replaces the defaults, so
// an explicit choice is never quietly widened to include /opt.
func DefaultDirs() []string {
	var dirs []string
	if d := DefaultDir(); d != "" {
		dirs = append(dirs, d)
	}
	if st, err := os.Stat(SystemDir); err == nil && st.IsDir() {
		dirs = append(dirs, SystemDir)
	}
	return dirs
}

// Discover scans dir for agent folders and registers every valid one.
// It returns the ids registered by THIS call and one problem line per
// broken/skipped folder (already-registered ids are reported, not fatal).
// A missing directory is not a problem - it just means nothing installed.
func Discover(dir string) (ids []string, problems []string) {
	return DiscoverWithPolicy(dir, Policy{})
}

// DiscoverWithPolicy scans dir and enforces the signature policy on every folder.
func DiscoverWithPolicy(dir string, pol Policy) (ids []string, problems []string) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("agents: cannot read %s: %v", dir, err)}
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") {
			continue // skip hidden folders (.git etc.) and templates (_template)
		}
		sub := filepath.Join(dir, e.Name())
		if !fileExists(filepath.Join(sub, "agent.json")) {
			continue // not an agent folder - ignore quietly (README, zips...)
		}
		id, skip, err := discoverOne(sub, pol)
		if err != nil {
			problems = append(problems, fmt.Sprintf("agents: %s: %v", sub, err))
			continue
		}
		if skip {
			continue // disabled by manifest: intentional, not a problem
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, problems
}

// discoverOne loads, validates and registers the agent in folder root.
// skip=true means the manifest is valid but "disabled": not registered,
// not an error. Either its own manifest says so, or the user switched it off
// with agents-disabled - which is the only option for a packaged agent, since
// those live in root-owned /opt.
func discoverOne(root string, pol Policy) (id string, skip bool, err error) {
	if err := pol.Check(root); err != nil {
		return "", false, err
	}
	m, err := LoadManifest(root)
	if err != nil {
		return "", false, err
	}
	if m.Disabled || IsDisabled(m.ID) {
		return "", true, nil
	}
	ext, err := NewExternal(m, root)
	if err != nil {
		return "", false, err
	}
	if err := Register(ext); err != nil {
		return "", false, err
	}
	log.Printf("agents: registered %s %s (%s)", m.ID, m.Version, root)
	return m.ID, false, nil
}

// DiscoverAll scans several directories in order (first registration of an
// id wins) and merges problems. Convenience wrapper for startup wiring.
func DiscoverAll(dirs []string) (ids []string, problems []string) {
	return DiscoverAllWithPolicy(dirs, Policy{})
}

// DiscoverAllWithPolicy scans several directories with the given policy.
func DiscoverAllWithPolicy(dirs []string, pol Policy) (ids []string, problems []string) {
	seen := map[string]bool{}
	for _, d := range dirs {
		got, probs := DiscoverWithPolicy(d, pol)
		problems = append(problems, probs...)
		for _, id := range got {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids, problems
}
