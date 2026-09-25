package agent

// registry.go - agent registry index format, remote fetch, and version resolution.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxRegistryBytes = 4 << 20 // 4 MiB cap for registry index JSON

// RegistryIndex holds the catalog of available agents in a registry.
type RegistryIndex struct {
	Version int             `json:"version"`
	Agents  []RegistryEntry `json:"agents"`
}

// RegistryEntry describes one downloadable agent in the registry.
type RegistryEntry struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	SHA256      string   `json:"sha256"`           // hex-encoded SHA-256 of the zip payload
	Signer      string   `json:"signer,omitempty"` // canonical SignerID (hex sha256 of pubkey)
	Homepage    string   `json:"homepage,omitempty"`
	Author      string   `json:"author,omitempty"`
	Params      []Param  `json:"params,omitempty"`
}

// ParseIndex unmarshals and validates a registry index JSON document.
func ParseIndex(data []byte) (*RegistryIndex, error) {
	var idx RegistryIndex
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&idx); err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	if idx.Version < 1 {
		return nil, errors.New("registry: missing or invalid version (want >= 1)")
	}
	seen := map[string]bool{}
	for i, a := range idx.Agents {
		if !ValidID(a.ID) {
			return nil, fmt.Errorf("registry agent[%d]: bad id %q", i, a.ID)
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("registry: duplicate agent id %q", a.ID)
		}
		seen[a.ID] = true
		if strings.TrimSpace(a.URL) == "" {
			return nil, fmt.Errorf("registry agent %s: url is required", a.ID)
		}
		if a.SHA256 != "" {
			a.SHA256 = strings.ToLower(strings.TrimSpace(a.SHA256))
			if len(a.SHA256) != 64 {
				return nil, fmt.Errorf("registry agent %s: sha256 must be 64 hex chars", a.ID)
			}
			if _, err := hex.DecodeString(a.SHA256); err != nil {
				return nil, fmt.Errorf("registry agent %s: bad sha256 hex: %w", a.ID, err)
			}
		}
		if a.Signer != "" {
			a.Signer = strings.ToLower(strings.TrimSpace(a.Signer))
			if len(a.Signer) != 64 {
				return nil, fmt.Errorf("registry agent %s: signer must be 64 hex chars", a.ID)
			}
			if _, err := hex.DecodeString(a.Signer); err != nil {
				return nil, fmt.Errorf("registry agent %s: bad signer hex: %w", a.ID, err)
			}
		}
	}
	return &idx, nil
}

// LoadIndex reads a registry index from an http(s) URL or a local file path.
func LoadIndex(src string) (*RegistryIndex, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, errors.New("registry source is empty")
	}
	var raw []byte
	var err error
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		raw, err = fetchURL(src, maxRegistryBytes)
	} else {
		raw, err = os.ReadFile(src)
		if err == nil && int64(len(raw)) > maxRegistryBytes {
			err = fmt.Errorf("file exceeds size limit (%d bytes)", maxRegistryBytes)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("load registry %s: %w", src, err)
	}
	return ParseIndex(raw)
}

func fetchURL(url string, maxBytes int64) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
}

// FindEntry returns the entry for id, or nil if absent.
func (idx *RegistryIndex) FindEntry(id string) *RegistryEntry {
	if idx == nil {
		return nil
	}
	for i := range idx.Agents {
		if idx.Agents[i].ID == id {
			return &idx.Agents[i]
		}
	}
	return nil
}

// Search returns entries matching query across ID and Description (case-insensitive).
// An empty query returns all entries sorted by ID.
func (idx *RegistryIndex) Search(query string) []RegistryEntry {
	if idx == nil {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []RegistryEntry
	for _, a := range idx.Agents {
		if q == "" || strings.Contains(strings.ToLower(a.ID), q) ||
			strings.Contains(strings.ToLower(a.Description), q) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// VersionNewer reports whether candidate version `cand` is strictly newer than `base`.
// Implements loose semver comparison (vX.Y.Z or X.Y.Z, numeric parts compared left-to-right).
func VersionNewer(base, cand string) bool {
	bp := parseVersionParts(base)
	cp := parseVersionParts(cand)
	for i := 0; i < len(bp) || i < len(cp); i++ {
		bVal := 0
		if i < len(bp) {
			bVal = bp[i]
		}
		cVal := 0
		if i < len(cp) {
			cVal = cp[i]
		}
		if cVal > bVal {
			return true
		}
		if cVal < bVal {
			return false
		}
	}
	return false
}

func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			n = 0
		}
		nums = append(nums, n)
	}
	return nums
}

// InstalledAgent describes an agent folder found on disk in the agents dir.
type InstalledAgent struct {
	ID        string
	Version   string
	Dir       string
	HasSig    bool
	Disabled  bool
	BrokenErr error
}

// ScanInstalled inspects agentsDir and returns an inventory of installed agents
// without registering them into the process-global registry.
func ScanInstalled(dir string) []InstalledAgent {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var list []InstalledAgent
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if !fileExists(filepath.Join(sub, "agent.json")) {
			continue
		}
		item := InstalledAgent{
			Dir:    sub,
			HasSig: HasSignature(sub),
		}
		m, err := LoadManifest(sub)
		if err != nil {
			item.BrokenErr = err
			item.ID = e.Name()
		} else {
			item.ID = m.ID
			item.Version = m.Version
			item.Disabled = m.Disabled
		}
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}
