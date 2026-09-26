package agent

// sign.go - Ed25519 detached signatures for agent.json manifests.
//
// A signed agent ships `agent.json.sig` next to `agent.json`. The signature
// covers the exact raw bytes of `agent.json` (canonical JSON, whitespace
// preserved).
//
// Third-party authors generate a keypair via `agentctl keygen`, sign with
// `agentctl sign <folder> -key <priv>`, and users configure `agents-key`
// in chat-app.ini (or `agentctl install -key <pub>`).

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ManifestSigFile = "agent.json.sig"

// GenerateKey creates a fresh Ed25519 keypair.
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// SignerID computes the canonical signer identifier (lowercase hex of the
// SHA-256 hash of the 32-byte Ed25519 public key). This matches the "signer"
// field in registry index entries.
func SignerID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// EncodePublicKey formats a public key as a 64-char lowercase hex string.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return hex.EncodeToString(pub)
}

// ParsePublicKey parses a 64-char hex Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid public key hex: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key length %d (want %d)", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// EncodePrivateKey formats a private key as a lowercase hex string (64 bytes = 128 chars).
func EncodePrivateKey(priv ed25519.PrivateKey) string {
	return hex.EncodeToString(priv)
}

// ParsePrivateKey parses a 128-char hex (or 64-char seed) Ed25519 private key.
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	s = strings.TrimSpace(s)
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid private key hex: %w", err)
	}
	switch len(b) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	default:
		return nil, fmt.Errorf("invalid private key length %d (want %d or %d)", len(b), ed25519.PrivateKeySize, ed25519.SeedSize)
	}
}

// SignManifest reads agent.json in dir, signs its exact bytes with priv,
// and writes agent.json.sig (standard base64 followed by newline).
func SignManifest(dir string, priv ed25519.PrivateKey) error {
	manifestPath := filepath.Join(dir, "agent.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	sig := ed25519.Sign(priv, raw)
	b64 := base64.StdEncoding.EncodeToString(sig) + "\n"
	sigPath := filepath.Join(dir, ManifestSigFile)
	if err := os.WriteFile(sigPath, []byte(b64), 0o644); err != nil {
		return fmt.Errorf("sign: write %s: %w", sigPath, err)
	}
	return nil
}

// HasSignature reports whether dir contains an agent.json.sig file.
func HasSignature(dir string) bool {
	return fileExists(filepath.Join(dir, ManifestSigFile))
}

// ReadSignature loads and decodes the signature from agent.json.sig in dir.
func ReadSignature(dir string) ([]byte, error) {
	sigPath := filepath.Join(dir, ManifestSigFile)
	raw, err := os.ReadFile(sigPath)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("corrupt %s: %w", ManifestSigFile, err)
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%s has bad length %d (want %d)", ManifestSigFile, len(sig), ed25519.SignatureSize)
	}
	return sig, nil
}

// VerifyManifest checks that agent.json in dir matches agent.json.sig using pub.
// If agent.json.sig is missing, returns os.ErrNotExist.
func VerifyManifest(dir string, pub ed25519.PublicKey) error {
	sig, err := ReadSignature(dir)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(dir, "agent.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if !ed25519.Verify(pub, raw, sig) {
		return errors.New("signature verification failed (manifest modified or wrong public key)")
	}
	return nil
}

// Policy governs signature verification during agent discovery and installation.
type Policy struct {
	// RequireSignature: if true, unsigned agents are rejected.
	RequireSignature bool
	// Key: trusted public key. If set, any agent that ships a signature
	// must verify against it. If empty, signature presence is noted but
	// not cryptographically verified unless RequireSignature demands it.
	Key ed25519.PublicKey
}

// Check enforces the policy against an agent folder.
// Rules:
//   - If RequireSignature is true: missing sig -> error; empty Key -> error.
//   - If a signature file is present AND a key is configured: must verify.
//   - If a signature file is corrupt: always an error.
//   - Unsigned agents are allowed when RequireSignature is false.
func (p Policy) Check(dir string) error {
	hasSig := HasSignature(dir)
	if p.RequireSignature {
		if len(p.Key) == 0 {
			return errors.New("require-signature enabled but no trusted key configured")
		}
		if !hasSig {
			return errors.New("unsigned agent rejected by signature policy")
		}
	}
	if !hasSig {
		return nil
	}
	if len(p.Key) == 0 {
		// Key not configured: make sure the sig isn't syntactically broken,
		// but don't fail verification since no key was pinned.
		_, err := ReadSignature(dir)
		return err
	}
	return VerifyManifest(dir, p.Key)
}
