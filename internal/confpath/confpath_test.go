package confpath

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultFallsBackToUserConfig is the package-install case: no
// ./chat-app.ini and no chat-app.ini beside the binary, so the user's config
// dir is the only thing that can answer. preflight once had its own copy of
// this list that stopped at the first path, and so reported keys as missing
// that were set all along - this is the check that keeps the copy from coming
// back.
func TestDefaultFallsBackToUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Chdir(t.TempDir())

	dir := filepath.Join(home, "cfg", "chat-app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "chat-app.ini")
	if err := os.WriteFile(want, []byte("provider = gemini\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := Default(); got != want {
		t.Fatalf("Default() = %q, want %q", got, want)
	}
}

// TestDefaultEmptyWhenNothingExists checks Default reports "" rather than a
// path it invented, so callers keep their config disabled instead of trying to
// read a file that is not there.
func TestDefaultEmptyWhenNothingExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Chdir(t.TempDir())

	if got := Default(); got != "" {
		t.Fatalf("Default() = %q, want empty", got)
	}
}

// TestDirHonoursXDG covers both halves of the directory rule: XDG wins when
// set, and ~/.config/chat-app is the fallback when it is not.
func TestDirHonoursXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	if got, want := Dir(), filepath.Join(home, "cfg", "chat-app"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	if got, want := Dir(), filepath.Join(home, ".config", "chat-app"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}
