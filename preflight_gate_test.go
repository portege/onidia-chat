package main

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/portege/chat-app/internal/preflight"
)

func TestResolvePreflightMode(t *testing.T) {
	cfg := func(v string) *Config { return &Config{Preflight: v} }
	for _, tc := range []struct {
		flag string
		cfg  *Config
		want string
	}{
		{"", nil, "strict"},               // nothing set: block by default
		{"", cfg(""), "strict"},           // empty key: same
		{"", cfg("warn"), "warn"},         // config key alone
		{"", cfg("off"), "off"},           // config disables the gate
		{"strict", cfg("warn"), "strict"}, // flag beats config
		{"off", cfg("warn"), "off"},       // flag beats config
		{"WARN", nil, "warn"},             // case-insensitive
		{"  off  ", nil, "off"},           // trimmed
		{"bogus", nil, "strict"},          // unknown: fail safe to strict
		{"", cfg("bogus"), "strict"},      // unknown config value too
	} {
		if got := resolvePreflightMode(tc.flag, tc.cfg); got != tc.want {
			t.Errorf("resolvePreflightMode(%q, %+v) = %q, want %q", tc.flag, tc.cfg, got, tc.want)
		}
	}
}

// TestRunStartupPreflightNonBlockingModes: in warn/off mode a dead backend
// must only be logged - the function returns and the window would open.
func TestRunStartupPreflightNonBlockingModes(t *testing.T) {
	spec := preflight.Spec{
		Provider: "ollama",
		APIURL:   "http://127.0.0.1:9", // discard port: nothing listens
		Model:    "qwen2:1.5b",
	}
	env := preflight.Env{}
	for _, mode := range []string{"warn", "off"} {
		runStartupPreflight(spec, env, mode) // must return; os.Exit would fail the run
	}
}

// TestRunStartupPreflightStrictBlocks is the subprocess form: strict mode on
// a dead backend must exit 2 (which is what stops chat-app before its window).
func TestRunStartupPreflightStrictBlocks(t *testing.T) {
	if os.Getenv("PREFLIGHT_GATE_CHILD") == "1" {
		runStartupPreflight(preflight.Spec{
			Provider: "ollama",
			APIURL:   "http://127.0.0.1:9",
			Model:    "qwen2:1.5b",
		}, preflight.Env{}, "strict")
		return // reached only if the gate failed to block
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestRunStartupPreflightStrictBlocks")
	cmd.Env = append(os.Environ(), "PREFLIGHT_GATE_CHILD=1")
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("child exit = %v, want exit code 2", err)
	}
}
