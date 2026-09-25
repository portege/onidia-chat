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
