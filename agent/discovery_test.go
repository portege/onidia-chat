package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeFolder writes one agent folder (manifest + script) under root.
func makeFolder(t *testing.T, root, folder, manifest, script string) string {
	t.Helper()
	dir := filepath.Join(root, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDiscover(t *testing.T) {
	Reset()
	defer Reset()
	root := t.TempDir()

	makeFolder(t, root, "good", `{
		"id": "good_one", "version": "1.0.0", "description": "works",
		"exec": ["./run.sh"], "params": [{"name": "x"}]
	}`, "#!/bin/sh\necho OK done\n")
	makeFolder(t, root, "broken", `{not json`, "")
	makeFolder(t, root, "noexec", `{
		"id": "noexec", "description": "missing binary", "exec": ["./gone"]
	}`, "")
	makeFolder(t, root, "disabled", `{
		"id": "off_one", "description": "off", "exec": ["./run.sh"], "disabled": true
	}`, "#!/bin/sh\n")
	// Not an agent folder (no agent.json): skipped silently.
	os.MkdirAll(filepath.Join(root, "notes"), 0o755)
	// Hidden folder: skipped silently.
	makeFolder(t, root, ".cache", `{"id": "hidden", "description": "x", "exec": ["./run.sh"]}`, "")

	ids, problems := Discover(root)
	if len(ids) != 1 || ids[0] != "good_one" {
		t.Errorf("ids = %v, want [good_one]", ids)
	}
	if len(problems) != 2 {
		t.Errorf("problems = %d:\n%s\nwant 2 (broken, noexec)", len(problems), strings.Join(problems, "\n"))
	}
	// Registered and runnable from the registry.
	a, err := Get("good_one")
	if err != nil {
		t.Fatalf("Get(good_one): %v", err)
	}
	if a.Description() != "works" {
		t.Errorf("Description = %q", a.Description())
	}
}

func TestDiscoverMissingDir(t *testing.T) {
	Reset()
	defer Reset()
	ids, problems := Discover(filepath.Join(t.TempDir(), "does-not-exist"))
	if len(ids) != 0 || len(problems) != 0 {
		t.Errorf("missing dir: ids=%v problems=%v, want both empty", ids, problems)
	}
}

func TestDiscoverDuplicateAcrossRuns(t *testing.T) {
	Reset()
	defer Reset()
	root := t.TempDir()
	makeFolder(t, root, "a", `{
		"id": "twice", "description": "x", "exec": ["./run.sh"]
	}`, "#!/bin/sh\n")
	if ids, _ := Discover(root); len(ids) != 1 {
		t.Fatalf("first discover = %v", ids)
	}
	// Second run over the same dir: nothing NEW registers (first wins) and
	// the duplicate attempt is reported as a problem.
	ids, problems := Discover(root)
	if len(ids) != 0 {
		t.Errorf("second discover ids = %v, want [] (already registered)", ids)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "already registered") {
		t.Errorf("problems = %v, want duplicate registration notice", problems)
	}
}

func TestManifestValidate(t *testing.T) {
	cases := []struct {
		name string
		json string
		bad  string
	}{
		{"minimal ok", `{"id":"mm","description":"d","exec":["./x"]}`, ""},
		{"bad id", `{"id":"M-1","description":"d","exec":["./x"]}`, "bad id"},
		{"missing description", `{"id":"mm","exec":["./x"]}`, "description is required"},
		{"missing exec", `{"id":"mm","description":"d"}`, "exec is required"},
		{"bad protocol", `{"id":"mm","description":"d","exec":["./x"],"protocol":"rpc-v9"}`, "unsupported protocol"},
		{"dup param", `{"id":"mm","description":"d","exec":["./x"],"params":[{"name":"a"},{"name":"a"}]}`, "duplicate parameter"},
		{"bad param type", `{"id":"mm","description":"d","exec":["./x"],"params":[{"name":"a","type":"float"}]}`, "unsupported type"},
		{"enum on int", `{"id":"mm","description":"d","exec":["./x"],"params":[{"name":"a","type":"int","enum":["1"]}]}`, "enum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.json))
			if tc.bad == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.bad) {
				t.Fatalf("error = %v, want containing %q", err, tc.bad)
			}
		})
	}
}

// The user directory always comes first: first registration wins, so a
// person's own agent has to be able to shadow a bundled one of the same id.
func TestDefaultDirsUserFirst(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dirs := DefaultDirs()
	if len(dirs) == 0 {
		t.Fatal("DefaultDirs() is empty; the per-user directory must always be there")
	}
	if want := DefaultDir(); dirs[0] != want {
		t.Errorf("DefaultDirs()[0] = %q, want the user dir %q", dirs[0], want)
	}
}

// The packaged directory is added only when it really exists, and it goes
// LAST so a user's own agent still shadows a bundled one of the same id.
//
// SystemDir is redirected to a temp dir: the real /opt/onidia/share/agents
// needs root to create, which would turn this into a skip on every ordinary
// machine and leave the important case untested.
func TestDefaultDirsIncludesSystemDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Present: it must be included, and it must be last.
	sys := t.TempDir()
	swapSystemDir(t, sys)
	dirs := DefaultDirs()
	if !containsStr(dirs, sys) {
		t.Fatalf("DefaultDirs() = %v, want it to include the existing %s", dirs, sys)
	}
	if dirs[len(dirs)-1] != sys {
		t.Errorf("DefaultDirs() = %v, want the packaged dir last so the user's own agents win", dirs)
	}
	// Absent: it must not appear at all, or a from-source build reports a
	// directory that does not exist.
	swapSystemDir(t, filepath.Join(t.TempDir(), "not-installed"))
	if dirs := DefaultDirs(); containsStr(dirs, SystemDir) {
		t.Errorf("DefaultDirs() = %v, want no entry for a %s that does not exist", dirs, SystemDir)
	}
}

// A packaged install must actually find its agents this way: an empty
// per-user directory, the bundled ones in the packaged directory, nothing
// configured.
func TestDefaultDirsDiscoverPackagedAgents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	sys := t.TempDir()
	swapSystemDir(t, sys)
	makeFolder(t, sys, "pet_control",
		`{"id":"pc_packaged","description":"packaged","exec":["./run.sh"]}`, "#!/bin/sh\nexit 0\n")
	var ids []string
	for _, dir := range DefaultDirs() {
		got, problems := DiscoverWithPolicy(dir, Policy{})
		for _, p := range problems {
			t.Errorf("unexpected problem: %s", p)
		}
		ids = append(ids, got...)
	}
	if len(ids) != 1 || ids[0] != "pc_packaged" {
		t.Fatalf("discovered %v, want [pc_packaged] with an empty per-user directory", ids)
	}
}

// swapSystemDir points SystemDir at dir for the duration of the test.
func swapSystemDir(t *testing.T, dir string) {
	t.Helper()
	old := SystemDir
	SystemDir = dir
	t.Cleanup(func() { SystemDir = old })
}

func containsStr(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// A disabled agent is skipped by discovery: never registered, so the model is
// never offered it and no [AGENT: ...] tag can reach it.
func TestDisabledAgentIsSkipped(t *testing.T) {
	Reset()
	defer Reset()
	root := t.TempDir()
	makeFolder(t, root, "on", `{"id":"aa_on","description":"on","exec":["./run.sh"]}`, "#!/bin/sh\n")
	makeFolder(t, root, "off", `{"id":"bb_off","description":"off","exec":["./run.sh"]}`, "#!/bin/sh\n")

	SetDisabled([]string{"bb_off"})
	ids, problems := Discover(root)
	for _, p := range problems {
		t.Errorf("unexpected problem: %s", p)
	}
	if len(ids) != 1 || ids[0] != "aa_on" {
		t.Fatalf("discovered %v, want only aa_on", ids)
	}
	if _, err := Get("bb_off"); err == nil {
		t.Error("bb_off is registered but was disabled")
	}
	// The catalog must not advertise it either.
	if c := CatalogInstruction(); strings.Contains(c, "bb_off") {
		t.Errorf("catalog still advertises the disabled agent:\n%s", c)
	}
}

// Disabling is not deleting: the folder survives and -agents-list still shows
// it, marked disabled. Otherwise switching an agent off would look exactly like
// it had never been installed.
func TestDisabledAgentStaysListed(t *testing.T) {
	Reset()
	defer Reset()
	root := t.TempDir()
	makeFolder(t, root, "off", `{"id":"bb_off","description":"off","exec":["./run.sh"]}`, "#!/bin/sh\n")

	SetDisabled([]string{"bb_off"})
	Discover(root) // registers nothing

	items := ScanInstalledDirs([]string{root})
	if len(items) != 1 {
		t.Fatalf("listed %d agents, want the disabled one to still be listed", len(items))
	}
	if !items[0].Disabled {
		t.Error("listed agent is not marked disabled")
	}
	if got := DisabledIDs(); len(got) != 1 || got[0] != "bb_off" {
		t.Errorf("DisabledIDs() = %v, want [bb_off]", got)
	}
}

// A user's own agent shadows a packaged one of the same id, in the listing as
// well as in discovery.
func TestScanInstalledDirsPrecedence(t *testing.T) {
	Reset()
	defer Reset()
	user, packaged := t.TempDir(), t.TempDir()
	makeFolder(t, user, "play_song", `{"id":"play_song","version":"2.0.0","description":"mine","exec":["./run.sh"]}`, "#!/bin/sh\n")
	makeFolder(t, packaged, "play_song", `{"id":"play_song","version":"1.0.0","description":"theirs","exec":["./run.sh"]}`, "#!/bin/sh\n")
	makeFolder(t, packaged, "pet_control", `{"id":"pet_control","version":"1.0.0","description":"theirs","exec":["./run.sh"]}`, "#!/bin/sh\n")

	items := ScanInstalledDirs([]string{user, packaged})
	if len(items) != 2 {
		t.Fatalf("listed %d agents, want 2 (the duplicate id collapses)", len(items))
	}
	for _, it := range items {
		if it.ID != "play_song" {
			continue
		}
		if it.Version != "2.0.0" {
			t.Errorf("play_song version = %q, want 2.0.0 from the user's own directory", it.Version)
		}
		if it.Dir != filepath.Join(user, "play_song") {
			t.Errorf("play_song dir = %q, want the user's own", it.Dir)
		}
	}
}

// A missing directory is skipped, not an error: the per-user agents directory
// is absent for anyone who has never installed one.
func TestScanInstalledDirsMissingDir(t *testing.T) {
	Reset()
	defer Reset()
	if got := ScanInstalledDirs([]string{filepath.Join(t.TempDir(), "nope")}); len(got) != 0 {
		t.Errorf("listed %v, want nothing for a missing directory", got)
	}
}

// SetDisabled replaces rather than accumulates, and trims the whitespace a
// hand-edited "a, b" config inevitably has.
func TestSetDisabledReplaces(t *testing.T) {
	Reset()
	defer Reset()
	SetDisabled([]string{" one ", "", "two"})
	if !IsDisabled("one") || !IsDisabled("two") {
		t.Errorf("disabled = %v, want one and two", DisabledIDs())
	}
	SetDisabled([]string{"three"})
	if IsDisabled("one") || IsDisabled("two") {
		t.Error("SetDisabled appended instead of replacing")
	}
	if !IsDisabled("three") {
		t.Error("three was not disabled")
	}
}
