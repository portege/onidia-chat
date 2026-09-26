package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeAgent creates an agent folder running the given sh script body and
// returns its manifest (id "test_agent", 2s timeout unless overridden).
func writeAgent(t *testing.T, dir, script string, mutate func(*Manifest)) *Manifest {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		ID:          "test_agent",
		Version:     "0.1.0",
		Description: "test agent",
		Protocol:    ProtocolLineV1,
		Exec:        []string{"./run.sh"},
		TimeoutMS:   2000,
	}
	if mutate != nil {
		mutate(m)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func TestExternalAgentOK(t *testing.T) {
	dir := t.TempDir()
	m := writeAgent(t, dir, `#!/bin/sh
read -r line
case "$line" in RUN\ *) ;; *) echo "ERR bad request: $line"; exit 1;; esac
echo "INFO working on it"
echo 'OK played Bohemian Rhapsody'
`, nil)
	ext, err := NewExternal(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ext.Run(context.Background(), map[string]string{"title": "Bohemian Rhapsody"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Message != "played Bohemian Rhapsody" {
		t.Errorf("Message = %q, want %q", res.Message, "played Bohemian Rhapsody")
	}
}

func TestExternalAgentERR(t *testing.T) {
	dir := t.TempDir()
	m := writeAgent(t, dir, `#!/bin/sh
read -r line
echo 'ERR no track matching "xyzzy"'
`, nil)
	ext, err := NewExternal(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ext.Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "no track matching") {
		t.Fatalf("error = %v, want the ERR payload", err)
	}
}

func TestExternalAgentTimeout(t *testing.T) {
	dir := t.TempDir()
	m := writeAgent(t, dir, "#!/bin/sh\nsleep 30\n", func(m *Manifest) { m.TimeoutMS = 300 })
	ext, err := NewExternal(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = ext.Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want timed out", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("timeout took %v, want ~300ms (process not killed?)", d)
	}
}

func TestExternalAgentCrashWithoutTerminal(t *testing.T) {
	dir := t.TempDir()
	m := writeAgent(t, dir, "#!/bin/sh\necho 'boom on stderr' >&2\nexit 3\n", nil)
	ext, err := NewExternal(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ext.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("crashing agent should fail")
	}
	if !strings.Contains(err.Error(), "without OK/ERR") || !strings.Contains(err.Error(), "boom on stderr") {
		t.Errorf("error = %v, want exit reason + stderr", err)
	}
}

// runScriptAgent builds an external agent whose script is body and runs it,
// returning the parsed Result / error.
func runScriptAgent(t *testing.T, body string) (Result, error) {
	t.Helper()
	dir := t.TempDir()
	m := writeAgent(t, dir, "#!/bin/sh\nread -r line\n"+body, nil)
	ext, err := NewExternal(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	return ext.Run(context.Background(), nil)
}

// TestExternalAgentPetCmd covers the optional character-control line: the
// canonical form is forwarded, a well-formed unknown name passes through (the
// brain, which owns the pet's tables, drops it), and a malformed or repeated
// directive fails the run loudly instead of reaching the cmd-FIFO.
func TestExternalAgentPetCmd(t *testing.T) {
	res, err := runScriptAgent(t, "echo 'PET action dance'\necho 'OK playing'\n")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PetCmd != "action dance" {
		t.Errorf("PetCmd = %q, want %q", res.PetCmd, "action dance")
	}
	if res.Message != "playing" {
		t.Errorf("Message = %q, want playing", res.Message)
	}

	// Casing/whitespace are normalized; an event works the same way.
	res, err = runScriptAgent(t, "echo 'PET  Event   LOVE '\necho 'OK x'\n")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PetCmd != "event love" {
		t.Errorf("PetCmd = %q, want %q", res.PetCmd, "event love")
	}

	// A PET line after OK is never read: the terminal line ends the run.
	res, err = runScriptAgent(t, "echo 'OK done'\necho 'PET action dance'\n")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PetCmd != "" {
		t.Errorf("PetCmd = %q, want empty (PET after OK ignored)", res.PetCmd)
	}

	// No PET line at all stays valid: it is optional.
	res, err = runScriptAgent(t, "echo 'OK plain'\n")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PetCmd != "" {
		t.Errorf("PetCmd = %q, want empty", res.PetCmd)
	}

	// Malformed / hostile directives fail the run (never smuggled through).
	for _, bad := range []struct{ line, want string }{
		{"PET action", "want"},
		{"PET dance", "want"}, // no verb at all
		{"PET twerk dance", "unknown command"},
		{"PET quit", "want"},
		{"PET action dance; rm -rf /", "bad action name"},
		{"PET action dance extra", "bad action name"},
		{"PET action ../../evil", "bad action name"},
		// A real newline can never arrive (the client splits on it), but a
		// literal "\r" or any other separator can - and must not survive.
		{"PET action dance\\rm -rf /", "bad action name"},
	} {
		_, err := runScriptAgent(t, "echo '"+bad.line+"'\necho 'OK x'\n")
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("PET %q: error = %v, want one containing %q", bad.line, err, bad.want)
		}
	}

	// A second directive cannot overwrite the first one.
	_, err = runScriptAgent(t, "echo 'PET action dance'\necho 'PET event love'\necho 'OK x'\n")
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate PET: error = %v, want duplicate-PET error", err)
	}
}

func TestNormalizePetCmd(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"action dance", "action dance"},
		{"ACTION DANCE", "action dance"},
		{"  event   celebration  ", "event celebration"},
	} {
		got, err := normalizePetCmd(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("normalizePetCmd(%q) = (%q, %v), want %q", tc.in, got, err, tc.want)
		}
	}
	// Surrounding whitespace is tolerated (a trailing newline can never reach
	// here: the client splits lines and trims \r), but anything else that could
	// smuggle a second command into the FIFO is rejected.
	for _, bad := range []string{
		"", "dance", "action", "event", "action a b", "action do it",
		"action dance\necho pwned", "action dance; rm -rf /", "action ..",
	} {
		if got, err := normalizePetCmd(bad); err == nil {
			t.Errorf("normalizePetCmd(%q) = %q, want error", bad, got)
		}
	}
}

func TestResolveExec(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Relative-to-folder executable.
	if _, err := resolveExec(dir, []string{"./run.sh"}); err != nil {
		t.Errorf("relative exec: %v", err)
	}
	// PATH lookup (sh always exists on a Unix box).
	if _, err := resolveExec(dir, []string{"sh"}); err != nil {
		t.Errorf("PATH exec: %v", err)
	}
	// Nothing anywhere -> error naming both lookup places.
	if _, err := resolveExec(dir, []string{"./nope"}); err == nil {
		t.Error("missing exec should fail")
	}
}
