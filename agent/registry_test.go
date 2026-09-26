package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	makeFolder(t, dir, "a", `{"id":"a","description":"d","exec":["./run.sh"]}`, "#!/bin/sh\necho OK\n")
	agentDir := filepath.Join(dir, "a")

	// Missing signature -> not signed.
	if HasSignature(agentDir) {
		t.Fatal("clean dir should have no signature")
	}
	if err := VerifyManifest(agentDir, pub); err == nil {
		t.Fatal("verify without sig must error")
	}

	// Sign.
	if err := SignManifest(agentDir, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if !HasSignature(agentDir) {
		t.Fatal("agentDir should have signature after SignManifest")
	}

	// Verify succeeds with correct key.
	if err := VerifyManifest(agentDir, pub); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}

	// Verify fails with different key.
	otherPub, _, _ := GenerateKey()
	if err := VerifyManifest(agentDir, otherPub); err == nil {
		t.Fatal("verify with wrong key must fail")
	}

	// Tampered manifest fails.
	if err := os.WriteFile(filepath.Join(agentDir, "agent.json"), []byte(`{"id":"a","description":"tampered","exec":["./run.sh"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(agentDir, pub); err == nil {
		t.Fatal("verify on tampered manifest must fail")
	}
}

func TestSignerID(t *testing.T) {
	pub, _, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := SignerID(pub)
	if len(signer) != 64 {
		t.Fatalf("SignerID length = %d, want 64", len(signer))
	}
	if SignerID(pub) != signer {
		t.Fatal("SignerID not deterministic")
	}
}

func TestSignaturePolicy(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	makeFolder(t, dir, "a", `{"id":"a","description":"d","exec":["./run.sh"]}`, "#!/bin/sh\necho OK\n")
	agentDir := filepath.Join(dir, "a")

	// Default empty policy allows unsigned agent.
	pol := Policy{}
	if err := pol.Check(agentDir); err != nil {
		t.Fatalf("empty policy should allow unsigned: %v", err)
	}

	// RequireSignature on unsigned agent fails.
	polReq := Policy{RequireSignature: true, Key: pub}
	if err := polReq.Check(agentDir); err == nil {
		t.Fatal("RequireSignature on unsigned agent must fail")
	}

	// Sign the agent.
	if err := SignManifest(agentDir, priv); err != nil {
		t.Fatal(err)
	}

	// Now RequireSignature succeeds with matching key.
	if err := polReq.Check(agentDir); err != nil {
		t.Fatalf("RequireSignature with matching key failed: %v", err)
	}

	// Broken signature file always fails even when RequireSignature is false.
	if err := os.WriteFile(filepath.Join(agentDir, ManifestSigFile), []byte("garbage!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (Policy{}).Check(agentDir); err == nil {
		t.Fatal("corrupt sig file must fail even on empty policy")
	}
}

func TestRegistryParseAndSearch(t *testing.T) {
	raw := `{
		"version": 1,
		"agents": [
			{"id": "play_song", "version": "1.2.0", "description": "Play local music", "url": "https://example.com/song.zip", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{"id": "read_story", "version": "0.9.0", "description": "AI storytelling", "url": "https://example.com/story.zip"}
		]
	}`
	idx, err := ParseIndex([]byte(raw))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if len(idx.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(idx.Agents))
	}

	if e := idx.FindEntry("play_song"); e == nil || e.Version != "1.2.0" {
		t.Fatalf("FindEntry(play_song) = %+v", e)
	}
	if e := idx.FindEntry("nonexistent"); e != nil {
		t.Fatalf("expected nil for nonexistent")
	}

	results := idx.Search("music")
	if len(results) != 1 || results[0].ID != "play_song" {
		t.Fatalf("search(music) = %v", results)
	}
	all := idx.Search("")
	if len(all) != 2 {
		t.Fatalf("search() = %d, want 2", len(all))
	}

	bad := []string{
		`{"version": 0, "agents": []}`,
		`{"version": 1, "agents": [{"id": "bad id", "url": "x"}]}`,
		`{"version": 1, "agents": [{"id": "ok", "url": ""}]}`,
		`{"version": 1, "agents": [{"id": "ok", "url": "u", "sha256": "short"}]}`,
		`{"version": 1, "agents": [{"id": "a", "url": "u"}, {"id": "a", "url": "u"}]}`,
	}
	for i, b := range bad {
		if _, err := ParseIndex([]byte(b)); err == nil {
			t.Errorf("case %d: expected parse failure for %s", i, b)
		}
	}
}

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		base, cand string
		want       bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "1.1.0", true},
		{"1.0.0", "2.0.0", true},
		{"v1.0.0", "v1.0.1", true},
		{"1.0.1", "1.0.0", false},
		{"1.0.0", "1.0.0", false},
		{"1.0", "1.0.1", true},
		{"1.0.1", "1.0", false},
		{"1.0.0-rc1", "1.0.1", true},
	}
	for _, tc := range cases {
		got := VersionNewer(tc.base, tc.cand)
		if got != tc.want {
			t.Errorf("VersionNewer(%q, %q) = %v, want %v", tc.base, tc.cand, got, tc.want)
		}
	}
}

func TestPackAndInstallVerified(t *testing.T) {
	Reset()
	defer Reset()
	pub, priv, _ := GenerateKey()

	srcDir := t.TempDir()
	makeFolder(t, srcDir, "greeter", `{
		"id": "greeter", "version": "1.0.0", "description": "Greets user",
		"exec": ["./run.sh"]
	}`, "#!/bin/sh\necho OK hello\n")
	agentSrc := filepath.Join(srcDir, "greeter")
	if err := SignManifest(agentSrc, priv); err != nil {
		t.Fatal(err)
	}

	distZip := filepath.Join(t.TempDir(), "greeter-1.0.0.zip")
	sum, err := Pack(agentSrc, distZip)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if len(sum) != 64 {
		t.Fatalf("Pack sha256 = %q, want 64 hex chars", sum)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, distZip)
	}))
	defer srv.Close()

	destDir := t.TempDir()

	// Bad sha256 fails.
	badOpts := InstallOptions{SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
	if _, err := InstallWithOptions(srv.URL, destDir, badOpts); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("Install with bad sha256 should fail: %v", err)
	}

	// Wrong pubkey signature enforcement fails.
	wrongPub, _, _ := GenerateKey()
	wrongKeyOpts := InstallOptions{SHA256: sum, Policy: Policy{RequireSignature: true, Key: wrongPub}}
	if _, err := InstallWithOptions(srv.URL, destDir, wrongKeyOpts); err == nil {
		t.Fatal("Install with wrong key should fail")
	}

	// Install verified succeeds.
	goodOpts := InstallOptions{SHA256: sum, Policy: Policy{RequireSignature: true, Key: pub}}
	id, err := InstallWithOptions(srv.URL, destDir, goodOpts)
	if err != nil {
		t.Fatalf("InstallWithOptions: %v", err)
	}
	if id != "greeter" {
		t.Fatalf("installed id = %q, want greeter", id)
	}

	installedDir := filepath.Join(destDir, "greeter")
	if !fileExists(filepath.Join(installedDir, "agent.json")) {
		t.Fatal("agent.json missing")
	}
	if !HasSignature(installedDir) {
		t.Fatal("signature missing from install")
	}

	ids, problems := DiscoverWithPolicy(destDir, Policy{RequireSignature: true, Key: pub})
	if len(problems) != 0 {
		t.Fatalf("discover problems: %v", problems)
	}
	if len(ids) != 1 || ids[0] != "greeter" {
		t.Fatalf("discovered ids = %v, want [greeter]", ids)
	}

	if err := Remove("greeter", destDir); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if fileExists(filepath.Join(destDir, "greeter")) {
		t.Fatal("dir still exists after Remove")
	}
}

func TestScanInstalled(t *testing.T) {
	dir := t.TempDir()
	makeFolder(t, dir, "aa", `{"id":"aa","version":"1.0.0","description":"a","exec":["./run.sh"]}`, "")
	makeFolder(t, dir, "bb", `{"id":"bb","version":"2.0.0","description":"b","exec":["./run.sh"]}`, "")
	makeFolder(t, dir, "broken", `{"bad json`, "")

	list := ScanInstalled(dir)
	if len(list) != 3 {
		t.Fatalf("ScanInstalled = %d, want 3", len(list))
	}
	if list[0].ID != "aa" || list[0].Version != "1.0.0" {
		t.Errorf("list[0] = %+v", list[0])
	}
	if list[1].ID != "bb" || list[1].Version != "2.0.0" {
		t.Errorf("list[1] = %+v", list[1])
	}
	if list[2].BrokenErr == nil {
		t.Errorf("broken agent should record BrokenErr: %+v", list[2])
	}
}
