package agent

import (
	"fmt"
	"errors"

	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// install.go - `agentctl install <dir|zip|url>`: get a third-party agent
// into the agents directory so the next Discover picks it up.
//
// Trust model (v1): agents run with your privileges, straight from a
// user-writable folder, no sandbox and no signature check. Only install
// agents you trust - the same rule as a curl | sh script. Hardening
// (checksums, permissions lockdown) is deliberately deferred.

// maxInstallBytes caps downloads and unpacked zips (64 MiB) so a bad URL
// cannot fill the disk.
const maxInstallBytes = 64 << 20

// InstallOptions configures verification and trust checks during installation.
type InstallOptions struct {
	// SHA256 is the expected 64-char hex SHA-256 of the source zip archive.
	// Empty string means no checksum check.
	SHA256 string
	// Policy enforces signature requirements on the installed manifest.
	Policy Policy
}

// Install places an agent into destDir from one of:
//   - a local folder containing agent.json
//   - a local .zip containing agent.json (at root or one level down)
//   - an http(s) URL to such a .zip
//
// The source is validated BEFORE anything is written; the agent folder is
// named after its manifest id. Returns the installed id.
func Install(src, destDir string) (string, error) {
	return InstallWithOptions(src, destDir, InstallOptions{})
}

// InstallWithOptions installs an agent with checksum and signature verification.
func InstallWithOptions(src, destDir string, opts InstallOptions) (string, error) {
	staging, cleanup, err := stageVerified(src, opts.SHA256)
	if err != nil {
		return "", err
	}
	defer cleanup()

	// Check policy (signatures) against the staged folder.
	if err := opts.Policy.Check(staging); err != nil {
		return "", fmt.Errorf("install: %w", err)
	}

	m, err := LoadManifest(staging)
	if err != nil {
		return "", fmt.Errorf("install: %w", err)
	}
	// Validate exec resolves too, before touching destDir.
	if _, err := NewExternal(m, staging); err != nil {
		return "", fmt.Errorf("install %s: %w", m.ID, err)
	}

	dest := filepath.Join(destDir, m.ID)
	if err := os.RemoveAll(dest); err != nil { // replace cleanly on re-install
		return "", fmt.Errorf("install: replace %s: %w", dest, err)
	}
	if err := copyTree(staging, dest); err != nil {
		os.RemoveAll(dest)
		return "", fmt.Errorf("install: %w", err)
	}
	// The copy must itself be runnable (source may not be +x, e.g. a zip).
	if _, err := NewExternal(m, dest); err != nil {
		os.RemoveAll(dest)
		return "", fmt.Errorf("install %s: installed copy not runnable: %w", m.ID, err)
	}
	return m.ID, nil
}

// stageVerified is stage() with an optional SHA-256 check on the archive payload.
func stageVerified(src, wantSHA string) (root string, cleanup func(), err error) {
	wantSHA = strings.ToLower(strings.TrimSpace(wantSHA))
	noOp := func() {}
	switch {
	case strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://"):
		zipPath, err := download(src)
		if err != nil {
			return "", noOp, err
		}
		defer os.Remove(zipPath)
		if wantSHA != "" {
			if err := verifyFileSHA(zipPath, wantSHA); err != nil {
				return "", noOp, err
			}
		}
		root, cleanup, err = unzipToTemp(zipPath)
		if err != nil {
			return "", noOp, err
		}
		return locateManifest(root)
	default:
		fi, err := os.Stat(src)
		if err != nil {
			return "", noOp, fmt.Errorf("install: %w", err)
		}
		if fi.IsDir() {
			if wantSHA != "" {
				return "", noOp, errors.New("install: checksum verification applies to .zip archives or URLs, not bare folders")
			}
			if !fileExists(filepath.Join(src, "agent.json")) {
				return "", noOp, fmt.Errorf("install: %s has no agent.json", src)
			}
			return filepath.Clean(src), noOp, nil
		}
		if wantSHA != "" {
			if err := verifyFileSHA(src, wantSHA); err != nil {
				return "", noOp, err
			}
		}
		root, cleanup, err = unzipToTemp(src)
		if err != nil {
			return "", noOp, fmt.Errorf("install: %s: %v (expected a folder or .zip)", src, err)
		}
		return locateManifest(root)
	}
}

func verifyFileSHA(path, want string) error {
	got, err := HashFile(path)
	if err != nil {
		return fmt.Errorf("install: hash %s: %w", path, err)
	}
	if got != want {
		return fmt.Errorf("install: sha256 mismatch: got %s, want %s", got, want)
	}
	return nil
}

// Remove deletes an installed agent folder from destDir.
func Remove(id, destDir string) error {
	if !ValidID(id) {
		return fmt.Errorf("remove: invalid id %q", id)
	}
	target := filepath.Join(destDir, id)
	if !fileExists(filepath.Join(target, "agent.json")) {
		return fmt.Errorf("agent %s not found in %s", id, destDir)
	}
	return os.RemoveAll(target)
}


// stage materializes src (folder / zip file / http(s) zip URL) into a
// temp folder containing agent.json, plus a cleanup func. For a plain
// folder the staging IS the folder (cleanup is a no-op).
func stage(src string) (root string, cleanup func(), err error) {
	noOp := func() {}
	switch {
	case strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://"):
		zipPath, err := download(src)
		if err != nil {
			return "", noOp, err
		}
		root, cleanup, err = unzipToTemp(zipPath)
		os.Remove(zipPath)
		if err != nil {
			return "", noOp, err
		}
		return locateManifest(root)
	default:
		fi, err := os.Stat(src)
		if err != nil {
			return "", noOp, fmt.Errorf("install: %w", err)
		}
		if fi.IsDir() {
			if !fileExists(filepath.Join(src, "agent.json")) {
				return "", noOp, fmt.Errorf("install: %s has no agent.json", src)
			}
			return filepath.Clean(src), noOp, nil
		}
		root, cleanup, err = unzipToTemp(src)
		if err != nil {
			return "", noOp, fmt.Errorf("install: %s: %v (expected a folder or .zip)", src, err)
		}
		return locateManifest(root)
	}
}

// locateManifest finds agent.json in root or exactly one level down
// (zips often wrap everything in a top-level folder).
func locateManifest(root string) (string, func(), error) {
	kill := func() { os.RemoveAll(root) }
	if fileExists(filepath.Join(root, "agent.json")) {
		return root, kill, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", kill, fmt.Errorf("install: %w", err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(root, e.Name(), "agent.json")) {
			found = append(found, filepath.Join(root, e.Name()))
		}
	}
	switch len(found) {
	case 1:
		return found[0], kill, nil
	case 0:
		return "", kill, fmt.Errorf("install: no agent.json in the archive")
	default:
		return "", kill, fmt.Errorf("install: %d agents in one archive - package one agent per zip", len(found))
	}
}

// download fetches a zip into a temp file, size-capped.
func download(url string) (string, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("install: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("install: download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.CreateTemp("", "chat-app-agent-*.zip")
	if err != nil {
		return "", fmt.Errorf("install: %w", err)
	}
	defer f.Close()
	n, err := ioCopy(f, resp.Body)
	if err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("install: download: %w", err)
	}
	if n > maxInstallBytes {
		os.Remove(f.Name())
		return "", fmt.Errorf("install: %s larger than %d bytes", url, maxInstallBytes)
	}
	return f.Name(), nil
}
