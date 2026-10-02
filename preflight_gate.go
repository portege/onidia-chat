// preflight_gate.go - onidia-chat's startup requirements gate.
//
// Runs the shared internal/preflight checks for the resolved configuration
// BEFORE the X window opens, so a backend that cannot work is reported up
// front instead of on the first send. Modes (flag -preflight > chat-app.ini
// preflight key > default):
//
//	strict  a fatal check fails -> print the report and exit 2 pre-window
//	warn    failures are logged, the window opens anyway (the old behavior)
//	off     the gate does not run at all
//
// The standalone cmd/preflight CLI runs the same checks with the same exit
// codes for scripts and launchers.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/portege/onidia-chat/internal/preflight"
)

// startupGateTimeout bounds the whole startup run: live probes are one cheap
// GET each, run concurrently, so a healthy setup costs milliseconds and a
// dead backend fails within this budget instead of hanging the launch.
const startupGateTimeout = 2500 * time.Millisecond

// resolvePreflightMode picks the gate mode: explicit -preflight flag >
// chat-app.ini preflight key > strict. Unknown values warn and fall back to
// strict (fail safe, never silently off).
func resolvePreflightMode(flagVal string, cfg *Config) string {
	mode := strings.ToLower(strings.TrimSpace(flagVal))
	if mode == "" && cfg != nil {
		mode = strings.ToLower(strings.TrimSpace(cfg.Preflight))
	}
	switch mode {
	case "":
		return "strict"
	case "strict", "warn", "off":
		return mode
	default:
		log.Printf("warning: unknown preflight %q, using strict", mode)
		return "strict"
	}
}

// runStartupPreflight executes the gate. It either returns (continue opening
// the window) or exits 2 with the failed checks printed to stderr.
func runStartupPreflight(spec preflight.Spec, env preflight.Env, mode string) {
	if mode == "off" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), startupGateTimeout)
	defer cancel()
	checks := append(preflight.Checks(spec), preflight.EnvChecks(env)...)
	rep := preflight.Run(ctx, checks)
	rep.Context = preflight.SpecContext(spec)

	if mode == "strict" && rep.Blocked() {
		_ = rep.WriteText(os.Stderr)
		log.Printf("preflight: BLOCKED - exiting before the window opens (fix the failures above, or launch with -preflight warn / -preflight off)")
		os.Exit(2)
	}
	if rep.ExitCode() > 0 {
		log.Printf("preflight: %s", preflightContext(spec))
		for _, f := range rep.Failures() {
			log.Printf("preflight: %s: %s", f.ID, f.Detail)
			if f.Fix != "" {
				log.Printf("preflight:   fix: %s", f.Fix)
			}
		}
		log.Printf("preflight: %s (continuing: preflight = %s)", rep.Summary(), mode)
		return
	}
	log.Printf("preflight: %s - ok (%d checks in %s)",
		preflightContext(spec), len(rep.Items), rep.Took.Round(time.Millisecond))
}

// preflightContext renders the one-line "what is being checked" log prefix.
func preflightContext(spec preflight.Spec) string {
	m := preflight.SpecContext(spec)
	var sb strings.Builder
	fmt.Fprintf(&sb, "provider=%s", m["provider"])
	for _, k := range []string{"model", "url", "profile", "region"} {
		if v, ok := m[k]; ok && v != "" {
			fmt.Fprintf(&sb, " %s=%s", k, v)
		}
	}
	return sb.String()
}
