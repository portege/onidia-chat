package main

// Agent management without agentctl.
//
// agentctl is built but never shipped (see DISTRIBUTING.md), so a packaged
// user has no way to list, install or remove an agent - and chat-app's own log
// used to point them at a binary that was not there. These three modes close
// that gap using the same agent-package functions agentctl calls, so the
// behaviour is identical and there is exactly one implementation of install.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portege/chat-app/agent"
)

// runAgentMode prints the result of -agents-list / -agents-install /
// -agents-remove and exits. Called only once the config and agent directories
// have been resolved, so all three honour agents-dir and agents-disabled.
func runAgentMode(searchDirs []string, install, remove string, cfg *Config) {
	switch {
	case install != "":
		os.Exit(agentInstall(install, cfg))
	case remove != "":
		os.Exit(agentRemove(remove, searchDirs))
	default:
		os.Exit(agentList(searchDirs))
	}
}

// agentList prints every agent found on disk, including the disabled ones.
//
// Disabled agents are listed on purpose. Discovery skips them, so a listing
// built from the registry would be exactly the list that makes a disabled
// agent look uninstalled - which is the confusion this mode exists to end.
func agentList(searchDirs []string) int {
	items := agent.ScanInstalledDirs(searchDirs)
	if len(items) == 0 {
		fmt.Println("No agents installed.")
		fmt.Printf("Searched: %s\n", strings.Join(searchDirs, ", "))
		fmt.Println("Install one with:  chat-app -agents-install <folder|zip|url|registry-id>")
		return 0
	}
	fmt.Printf("%-16s %-8s %-9s %s\n", "ID", "VERSION", "STATE", "LOCATION")
	for _, it := range items {
		state := "enabled"
		if it.Disabled {
			state = "disabled"
		}
		if it.BrokenErr != nil {
			state = "broken"
		}
		loc := it.Dir
		if it.BrokenErr != nil {
			loc = fmt.Sprintf("%s (%v)", it.Dir, it.BrokenErr)
		}
		fmt.Printf("%-16s %-8s %-9s %s\n", it.ID, dashIfEmpty(it.Version), state, loc)
	}
	if d := agent.DisabledIDs(); len(d) > 0 {
		fmt.Printf("\nDisabled via agents-disabled: %s\n", strings.Join(d, ", "))
		fmt.Println("Re-enable by removing the id from agents-disabled in chat-app.ini.")
	}
	return 0
}

// agentInstall installs from a folder, .zip, URL or registry id, then exits.
//
// It always targets the per-user directory, never the packaged one: /opt is
// root-owned, and silently failing there - or worse, needing sudo - would be a
// bad surprise for someone who just wanted to add a pet behaviour.
func agentInstall(src string, cfg *Config) int {
	dest := agent.DefaultDir()
	if dest == "" {
		fmt.Fprintln(os.Stderr, "chat-app: cannot determine your agents directory (no $HOME)")
		return 2
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "chat-app: cannot create %s: %v\n", dest, err)
		return 2
	}

	// A bare id resolves through the registry, the same way agentctl does.
	target, sha := src, ""
	registry := ""
	if cfg != nil {
		registry = strings.TrimSpace(cfg.AgentsRegistry)
	}
	if registry == "" {
		registry = strings.TrimSpace(os.Getenv("CHAT_APP_AGENTS_REGISTRY"))
	}
	if agent.ValidID(src) && registry != "" && !looksLikePath(src) {
		idx, err := agent.LoadIndex(registry)
		if err != nil {
			fmt.Fprintf(os.Stderr, "chat-app: cannot read registry %s: %v\n", registry, err)
			return 2
		}
		entry := idx.FindEntry(src)
		if entry == nil {
			fmt.Fprintf(os.Stderr, "chat-app: %q is not in the registry (%s)\n", src, registry)
			return 2
		}
		target, sha = entry.URL, entry.SHA256
		fmt.Printf("resolved %s %s from registry: %s\n", entry.ID, entry.Version, target)
	}

	var pol agent.Policy
	if cfg != nil {
		pol.RequireSignature = cfg.AgentsRequireSig
		if cfg.AgentsKey != "" {
			if pk, err := agent.ParsePublicKey(cfg.AgentsKey); err == nil {
				pol.Key = pk
			} else {
				fmt.Fprintf(os.Stderr, "chat-app: invalid agents-key in config: %v\n", err)
				return 2
			}
		}
	}

	id, err := agent.InstallWithOptions(target, dest, agent.InstallOptions{SHA256: sha, Policy: pol})
	if err != nil {
		fmt.Fprintf(os.Stderr, "chat-app: install failed: %v\n", err)
		return 2
	}
	fmt.Printf("installed %s -> %s\n", id, filepath.Join(dest, id))
	if agent.IsDisabled(id) {
		fmt.Printf("note: %s is listed in agents-disabled, so it stays off until you remove it there\n", id)
	}
	fmt.Println("restart chat-app to load it (chat-app -agents-list shows it now)")
	return 0
}

// agentRemove deletes an installed agent, then exits. Refuses to touch the
// packaged directory: those belong to the package, and dpkg should be the only
// thing that removes them.
func agentRemove(id string, searchDirs []string) int {
	userDir := agent.DefaultDir()
	for _, dir := range searchDirs {
		if dir == userDir {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, id)); err == nil {
			fmt.Fprintf(os.Stderr,
				"chat-app: %s belongs to the package (%s), not to you.\n"+
					"Switch it off instead - it keeps its files: chat-app -agents-disabled %s\n",
				id, dir, id)
			return 2
		}
	}
	if err := agent.Remove(id, userDir); err != nil {
		fmt.Fprintf(os.Stderr, "chat-app: cannot remove %s: %v\n", id, err)
		return 2
	}
	fmt.Printf("removed %s from %s\n", id, userDir)
	fmt.Println("restart chat-app to drop it from the catalog")
	return 0
}

// looksLikePath keeps a registry lookup from eating a relative path such as
// ./my_agent or ~/agents/foo. agentctl uses the same test.
func looksLikePath(src string) bool {
	return strings.HasPrefix(src, "./") || strings.HasPrefix(src, "/") ||
		strings.HasPrefix(src, "../") || strings.HasPrefix(src, "~") ||
		strings.HasSuffix(src, ".zip") || strings.ContainsRune(src, filepath.Separator)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
