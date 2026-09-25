package agent

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"path/filepath"
	"strings"
)

// installio.go - the zip/copy helpers install.go builds on: extraction
// (with the zip-slip guard) and mode-preserving copies.

// ioCopy is io.Copy with the download size cap applied.
func ioCopy(dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, io.LimitReader(src, maxInstallBytes+1))
}

// unzipToTemp extracts a zip into a temp folder. Entries that would
// escape the destination (../, absolute paths) are rejected - the classic
// zip-slip guard.
func unzipToTemp(zipPath string) (string, func(), error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", func() {}, fmt.Errorf("open zip: %w", err)
	}
	tmp, err := os.MkdirTemp("", "chat-app-agent-*")
	if err != nil {
		r.Close()
		return "", func() {}, err
	}
	cleanup := func() { r.Close(); os.RemoveAll(tmp) }

	var total int64
	for _, f := range r.File {
		name := filepath.Clean(f.Name)
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			cleanup()
			return "", func() {}, fmt.Errorf("zip entry escapes destination: %q", f.Name)
		}
		target := filepath.Join(tmp, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				cleanup()
				return "", func() {}, err
			}
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > maxInstallBytes {
			cleanup()
			return "", func() {}, fmt.Errorf("unpacked size exceeds %d bytes", maxInstallBytes)
		}
		if err := extractFile(f, target); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return tmp, cleanup, nil
}

// extractFile writes one zip entry, preserving the executable bit.
func extractFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := f.Mode() & 0o777
	if mode&0o111 == 0 {
		mode |= 0o644 // plain file default
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, io.LimitReader(rc, maxInstallBytes))
	return err
}

// copyTree recursively copies src into dest (mode-preserving).
func copyTree(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil // skip symlinks etc. - agents are plain files
		}
		return copyFile(path, target, info.Mode())
	})
}

// copyFile copies one file with its permission bits.
func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// Pack compresses folder into a .zip file at zipPath, prefixing all files
// with the folder name (or explicit prefix if provided). Computes and returns
// the hex-encoded SHA-256 of the created zip archive.
func Pack(folder, zipPath string) (string, error) {
	manifestPath := filepath.Join(folder, "agent.json")
	if !fileExists(manifestPath) {
		return "", fmt.Errorf("pack: %s has no agent.json", folder)
	}
	m, err := LoadManifest(folder)
	if err != nil {
		return "", fmt.Errorf("pack: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(zipPath)
	if err != nil {
		return "", fmt.Errorf("pack: create %s: %w", zipPath, err)
	}
	zw := zip.NewWriter(f)

	topDir := m.ID
	err = filepath.Walk(folder, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(folder, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip dotfiles (.git, .DS_Store)
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		nameInZip := filepath.ToSlash(filepath.Join(topDir, rel))
		if info.IsDir() {
			_, err := zw.CreateHeader(&zip.FileHeader{
				Name:   nameInZip + "/",
				Method: zip.Deflate,
			})
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = nameInZip
		header.Method = zip.Deflate
		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()
		_, err = io.Copy(w, srcFile)
		return err
	})
	if err != nil {
		zw.Close()
		f.Close()
		os.Remove(zipPath)
		return "", fmt.Errorf("pack %s: %w", folder, err)
	}
	if err := zw.Close(); err != nil {
		f.Close()
		os.Remove(zipPath)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(zipPath)
		return "", err
	}
	return HashFile(zipPath)
}

// HashFile computes the hex-encoded SHA-256 of the file at path.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
