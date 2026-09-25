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
