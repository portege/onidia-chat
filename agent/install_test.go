package agent

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallFromFolder(t *testing.T) {
	Reset()
	defer Reset()
	src := t.TempDir()
	makeFolder(t, src, "pkg", `{
		"id": "from_folder", "version": "2.0.0", "description": "d",
		"exec": ["./run.sh"]
	}`, "#!/bin/sh\necho OK hi\n")
	dest := t.TempDir()

	id, err := Install(filepath.Join(src, "pkg"), dest)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if id != "from_folder" {
		t.Fatalf("id = %q", id)
	}
	if _, err := os.Stat(filepath.Join(dest, "from_folder", "agent.json")); err != nil {
		t.Fatalf("installed agent.json: %v", err)
	}
	// Re-install replaces cleanly.
	if _, err := Install(filepath.Join(src, "pkg"), dest); err != nil {
		t.Fatalf("re-Install: %v", err)
	}
}

func TestInstallRejectsInvalidBeforeWriting(t *testing.T) {
	Reset()
	defer Reset()
	src := t.TempDir()
	makeFolder(t, src, "bad", `{not json`, "")
	dest := t.TempDir()
	if _, err := Install(filepath.Join(src, "bad"), dest); err == nil {
		t.Fatal("invalid manifest should fail install")
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Errorf("dest not empty after failed install: %v", entries)
	}
}

// zipUp packs the given name->content entries into a zip file in dir.
func zipUp(t *testing.T, dir string, entries map[string]string, modes map[string]os.FileMode) string {
	t.Helper()
	zipPath := filepath.Join(dir, "pkg.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	_ = modes // exec bits: zip.FileHeader.SetMode would go here; install chmods via resolve
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return zipPath
}

func TestInstallFromZipWrappedFolder(t *testing.T) {
	Reset()
	defer Reset()
	dir := t.TempDir()
	zipPath := zipUp(t, dir, map[string]string{
		// GitHub-style: everything under one top-level folder.
		"my-agent-main/agent.json": `{
			"id": "from_zip", "version": "1.0.0", "description": "d",
			"exec": ["./run.sh"]
		}`,
		"my-agent-main/run.sh": "#!/bin/sh\necho OK zipped\n",
	}, nil)
	dest := t.TempDir()

	id, err := Install(zipPath, dest)
	if err != nil {
		t.Fatalf("Install(zip): %v", err)
	}
	if id != "from_zip" {
		t.Fatalf("id = %q", id)
	}
	if _, err := os.Stat(filepath.Join(dest, "from_zip", "run.sh")); err != nil {
		t.Fatalf("script extracted: %v", err)
	}
	// The installed copy must be runnable even though the zip stored 0644.
	m, err := LoadManifest(filepath.Join(dest, "from_zip"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewExternal(m, filepath.Join(dest, "from_zip")); err != nil {
		t.Fatalf("installed copy not runnable: %v", err)
	}
}

func TestInstallZipSlipRejected(t *testing.T) {
	Reset()
	defer Reset()
	dir := t.TempDir()
	zipPath := zipUp(t, dir, map[string]string{
		"../evil.sh": "#!/bin/sh\n",
	}, nil)
	dest := t.TempDir()
	_, err := Install(zipPath, dest)
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("error = %v, want zip-slip rejection", err)
	}
}

func TestInstallTwoAgentsInOneZipFails(t *testing.T) {
	Reset()
	defer Reset()
	dir := t.TempDir()
	zipPath := zipUp(t, dir, map[string]string{
		"a/agent.json": `{"id":"a_one","description":"d","exec":["./x"]}`,
		"b/agent.json": `{"id":"b_one","description":"d","exec":["./x"]}`,
	}, nil)
	dest := t.TempDir()
	_, err := Install(zipPath, dest)
	if err == nil || !strings.Contains(err.Error(), "one agent per zip") {
		t.Fatalf("error = %v, want one-agent-per-zip", err)
	}
}
