package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ProtocolLineV1 is the only protocol understood so far: the brain writes
// "RUN {json}" to the agent's stdin and reads "OK ..."/"ERR ..." lines back
// from stdout (see external.go and docs/AGENT-PROTOCOL.md).
const ProtocolLineV1 = "agent-line-v1"

// Manifest is the agent.json file sitting at the root of every downloaded
// agent folder. It is the whole discovery contract: id, what it does, what
// arguments it takes, and how to start it.
type Manifest struct {
	ID          string   `json:"id"`
	Version     string   `json:"version,omitempty"`    // informational, shown by `agentctl list`
	Description string   `json:"description"`          // ends up in the model's catalog
	Protocol    string   `json:"protocol,omitempty"`   // defaults to ProtocolLineV1
	Exec        []string `json:"exec"`                 // argv to start the agent (see resolveExec)
	TimeoutMS   int      `json:"timeout_ms,omitempty"` // per-run timeout, default 15s
	Disabled    bool     `json:"disabled,omitempty"`   // skipped by discovery
	Params      []Param  `json:"params,omitempty"`     // declared arguments
}

// ParseManifest decodes and validates one agent.json.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid agent.json: %w", err)
	}
	if m.Protocol == "" {
		m.Protocol = ProtocolLineV1
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// LoadManifest reads agent.json from an agent folder.
func LoadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "agent.json"))
	if err != nil {
		return nil, err
	}
	return ParseManifest(data)
}

// Validate checks every manifest invariant the brain relies on. Broken
// agents are reported at discovery time and never registered.
func (m *Manifest) Validate() error {
	if !idRe.MatchString(m.ID) {
		return fmt.Errorf("bad id %q (want [a-z][a-z0-9_]{1,31})", m.ID)
	}
	if m.Description == "" {
		return fmt.Errorf("agent %s: description is required (the model reads it)", m.ID)
	}
	if m.Protocol != ProtocolLineV1 {
		return fmt.Errorf("agent %s: unsupported protocol %q (want %q)", m.ID, m.Protocol, ProtocolLineV1)
	}
	if len(m.Exec) == 0 || m.Exec[0] == "" {
		return fmt.Errorf("agent %s: exec is required", m.ID)
	}
	if m.TimeoutMS < 0 || time.Duration(m.TimeoutMS)*time.Millisecond > maxTimeout {
		return fmt.Errorf("agent %s: timeout_ms must be between 0 and %d", m.ID, int(maxTimeout/time.Millisecond))
	}
	seen := map[string]bool{}
	for _, p := range m.Params {
		if !paramNameRe.MatchString(p.Name) {
			return fmt.Errorf("agent %s: bad parameter name %q", m.ID, p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("agent %s: duplicate parameter %q", m.ID, p.Name)
		}
		seen[p.Name] = true
		switch p.Type {
		case "", "string", "int", "bool":
			// ok
		default:
			return fmt.Errorf("agent %s: parameter %s has unsupported type %q", m.ID, p.Name, p.Type)
		}
		if len(p.Enum) > 0 && !(p.Type == "" || p.Type == "string") {
			return fmt.Errorf("agent %s: parameter %s has an enum but type %q", m.ID, p.Name, p.Type)
		}
		if p.Default != "" {
			if _, err := coerceParam(p, p.Default); err != nil {
				return fmt.Errorf("agent %s: default for %s: %w", m.ID, p.Name, err)
			}
		}
	}
	return nil
}

// Timeout is the per-run deadline: the manifest value, or 15s when unset,
// capped at 10 minutes.
func (m *Manifest) Timeout() time.Duration {
	if m.TimeoutMS <= 0 {
		return defaultTimeout
	}
	d := time.Duration(m.TimeoutMS) * time.Millisecond
	if d > maxTimeout {
		return maxTimeout
	}
	return d
}
