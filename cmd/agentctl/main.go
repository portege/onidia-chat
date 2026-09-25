package main

// agentctl - manage chat-app's pluggable agents without opening the chat
// window: list installed ones, install new ones (folder, .zip or URL),
// test-run an agent exactly as the reply pipeline would, and validate a
// folder before you package it.
//
//	go build -o agentctl ./cmd/agentctl     (or: make agentctl)
//	./agentctl list
//	./agentctl install agents/hello_world
//	./agentctl install https://example.com/my-agent.zip
//	./agentctl run hello_world name=Ada style=formal
//	./agentctl validate ./my-agent-folder
//
// Discovery and Run go through the same agent/ package chat-app uses, so
// "it works in agentctl" means "it works in the chat". The default
// directory matches chat-app's -agents-dir / config agents-dir.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"crypto/ed25519"

	"github.com/portege/chat-app/agent"
)

func main() {
	dir := flag.String("dir", "", "agents directory (default: chat-app's agents-dir)")
	registryURL := flag.String("registry", os.Getenv("CHAT_APP_AGENTS_REGISTRY"), "registry URL or file (default: $CHAT_APP_AGENTS_REGISTRY)")
	trustedKeyHex := flag.String("key", "", "trusted Ed25519 public key hex for verification")
	requireSig := flag.Bool("require-signature", false, "require a valid signature to install or run")
	sha256Hex := flag.String("sha256", "", "expected SHA-256 hex for install archive")
	outPath := flag.String("out", "", "output path for pack/keygen commands")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	agentsDir := strings.TrimSpace(*dir)
	if agentsDir == "" {
		agentsDir = agent.DefaultDir()
	}
	if agentsDir == "" {
		fatalf("no agents directory: set -dir or $HOME")
	}

	var pubKey ed25519.PublicKey
	cmdName := ""
	if len(args) > 0 {
		cmdName = args[0]
	}
	if *trustedKeyHex != "" && cmdName != "sign" {
		pk, err := agent.ParsePublicKey(*trustedKeyHex)
		if err != nil {
			fatalf("invalid -key: %v", err)
		}
		pubKey = pk
	}
	pol := agent.Policy{
		RequireSignature: *requireSig,
		Key:              pubKey,
	}

	switch args[0] {
	case "list":
		cmdList(agentsDir, pol)
	case "search":
		query := ""
		if len(args) >= 2 {
			query = args[1]
		}
		cmdSearch(*registryURL, agentsDir, query)
	case "install":
		if len(args) < 2 {
			fatalf("usage: agentctl install <name|folder|zip|url>")
		}
		cmdInstall(args[1], agentsDir, *registryURL, *sha256Hex, pol)
	case "update":
		target := ""
		if len(args) >= 2 {
			target = args[1]
		}
		cmdUpdate(*registryURL, agentsDir, target, pol)
	case "remove":
		if len(args) < 2 {
			fatalf("usage: agentctl remove <id>")
		}
		if err := agent.Remove(args[1], agentsDir); err != nil {
			fatalf("%v", err)
		}
		fmt.Printf("removed %s\n", args[1])
	case "run":
		if len(args) < 2 {
			fatalf("usage: agentctl run <id> [key=value ...]")
		}
		cmdRun(agentsDir, args[1], args[2:], pol)
	case "validate":
		if len(args) < 2 {
			fatalf("usage: agentctl validate <agent-folder>")
		}
		cmdValidate(args[1], pol)
	case "keygen":
		cmdKeygen(*outPath)
	case "sign":
		if len(args) < 2 {
			fatalf("usage: agentctl sign <folder> -key <privkey-hex-or-file>")
		}
		cmdSign(args[1], *trustedKeyHex)
	case "pack":
		if len(args) < 2 {
			fatalf("usage: agentctl pack <folder> [-out <path.zip>]")
		}
		cmdPack(args[1], *outPath)
	default:
		usage()
		os.Exit(2)
	}
}

// cmdList discovers agentsDir and prints the catalog the model sees.
func cmdList(agentsDir string, pol agent.Policy) {
	ids, problems := agent.DiscoverWithPolicy(agentsDir, pol)
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, p)
	}
	if len(ids) == 0 {
		fmt.Printf("no agents in %s\n", agentsDir)
		fmt.Println("install one: agentctl install <name|folder|zip|url>")
		return
	}
	fmt.Printf("agents in %s:\n", agentsDir)
	for _, id := range ids {
		a, err := agent.Get(id)
		if err != nil {
			continue
		}
		ver := ""
		if e, ok := a.(*agent.ExternalAgent); ok && e.Version() != "" {
			ver = " " + e.Version()
		}
		sigTag := ""
		sub := filepath.Join(agentsDir, id)
		if agent.HasSignature(sub) {
			sigTag = " [signed]"
		}
		fmt.Printf("  %s%s%s - %s\n", id, ver, sigTag, a.Description())
		for _, p := range a.Params() {
			fmt.Println(paramLine(p))
		}
	}
}

// cmdSearch searches the remote registry index for agents.
func cmdSearch(registrySrc, agentsDir, query string) {
	if registrySrc == "" {
		fatalf("no registry specified: pass -registry <url|file> or set $CHAT_APP_AGENTS_REGISTRY")
	}
	idx, err := agent.LoadIndex(registrySrc)
	if err != nil {
		fatalf("%v", err)
	}
	entries := idx.Search(query)
	if len(entries) == 0 {
		fmt.Printf("no matching agents found in registry %s\n", registrySrc)
		return
	}
	installed := map[string]string{}
	for _, inst := range agent.ScanInstalled(agentsDir) {
		installed[inst.ID] = inst.Version
	}
	fmt.Printf("registry results (%s):\n", registrySrc)
	for _, e := range entries {
		status := ""
		if curVer, ok := installed[e.ID]; ok {
			if agent.VersionNewer(curVer, e.Version) {
				status = fmt.Sprintf(" [installed: %s -> update available]", curVer)
			} else {
				status = fmt.Sprintf(" [installed: %s]", curVer)
			}
		}
		signed := ""
		if e.Signer != "" {
			signed = " [signed]"
		}
		fmt.Printf("  %-16s %-8s%s%s\n", e.ID, e.Version, signed, status)
		if e.Description != "" {
			fmt.Printf("      %s\n", e.Description)
		}
	}
}

// cmdInstall handles installing from a local path, zip, URL, or by registry name.
func cmdInstall(src, agentsDir, registrySrc, explicitSHA string, pol agent.Policy) {
	isLocal := strings.HasPrefix(src, "./") || strings.HasPrefix(src, "/") ||
		strings.HasPrefix(src, "../") || strings.HasPrefix(src, "~") ||
		strings.HasSuffix(src, ".zip") || strings.Contains(src, string(filepath.Separator))
	isURL := strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")

	downloadTarget := src
	sha := explicitSHA

	if !isLocal && !isURL && agent.ValidID(src) && registrySrc != "" {
		idx, err := agent.LoadIndex(registrySrc)
		if err != nil {
			fatalf("failed to load registry: %v", err)
		}
		entry := idx.FindEntry(src)
		if entry == nil {
			fatalf("agent %q not found in registry %s", src, registrySrc)
		}
		downloadTarget = entry.URL
		if sha == "" {
			sha = entry.SHA256
		}
		fmt.Printf("resolved %s %s from registry: %s\n", entry.ID, entry.Version, downloadTarget)
	}

	opts := agent.InstallOptions{
		SHA256: sha,
		Policy: pol,
	}
	id, err := agent.InstallWithOptions(downloadTarget, agentsDir, opts)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("installed %s -> %s\n", id, filepath.Join(agentsDir, id))
	fmt.Println("restart chat-app to pick it up (agentctl list shows it now)")
}

// cmdUpdate queries registry and upgrades installed agents when newer versions exist.
func cmdUpdate(registrySrc, agentsDir, target string, pol agent.Policy) {
	if registrySrc == "" {
		fatalf("no registry specified: pass -registry <url|file> or set $CHAT_APP_AGENTS_REGISTRY")
	}
	idx, err := agent.LoadIndex(registrySrc)
	if err != nil {
		fatalf("failed to load registry: %v", err)
	}
	installed := agent.ScanInstalled(agentsDir)
	if len(installed) == 0 {
		fmt.Printf("no agents installed in %s\n", agentsDir)
		return
	}
	updated := 0
	for _, inst := range installed {
		if target != "" && inst.ID != target {
			continue
		}
		entry := idx.FindEntry(inst.ID)
		if entry == nil {
			if target != "" {
				fmt.Printf("%s: not found in registry\n", inst.ID)
			}
			continue
		}
		if !agent.VersionNewer(inst.Version, entry.Version) {
			if target != "" {
				fmt.Printf("%s: up to date (%s)\n", inst.ID, inst.Version)
			}
			continue
		}
		fmt.Printf("updating %s (%s -> %s)...\n", inst.ID, inst.Version, entry.Version)
		opts := agent.InstallOptions{
			SHA256: entry.SHA256,
			Policy: pol,
		}
		_, err := agent.InstallWithOptions(entry.URL, agentsDir, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to update %s: %v\n", inst.ID, err)
			continue
		}
		fmt.Printf("updated %s to %s\n", inst.ID, entry.Version)
		updated++
	}
	if updated == 0 && target == "" {
		fmt.Println("all agents are up to date.")
	}
}

// cmdRun runs one agent exactly like the chat reply pipeline does.
func cmdRun(agentsDir, id string, kv []string, pol agent.Policy) {
	for _, p := range discoverProblems(agentsDir, pol) {
		fmt.Fprintln(os.Stderr, p)
	}
	args := make(map[string]string, len(kv))
	for _, pair := range kv {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			fatalf("bad argument %q (want key=value)", pair)
		}
		args[strings.TrimSpace(k)] = v
	}
	res, err := agent.Run(id, args)
	if err != nil {
		fatalf("%v", err)
	}
	if res.Message != "" {
		fmt.Println(res.Message)
	}
	if res.PetCmd != "" {
		fmt.Printf("(pet command: %s)\n", res.PetCmd)
	}
}

// cmdValidate checks a folder's manifest + exec without installing it.
func cmdValidate(folder string, pol agent.Policy) {
	if err := pol.Check(folder); err != nil {
		fatalf("validation policy failed: %v", err)
	}
	m, err := agent.LoadManifest(folder)
	if err != nil {
		fatalf("%v", err)
	}
	if _, err := agent.NewExternal(m, folder); err != nil {
		fatalf("%v", err)
	}
	sigTag := ""
	if agent.HasSignature(folder) {
		sigTag = " [signed]"
	}
	fmt.Printf("ok: %s %s%s - %s\n", m.ID, m.Version, sigTag, m.Description)
	for _, p := range m.Params {
		fmt.Println(paramLine(p))
	}
}

// cmdKeygen generates an Ed25519 signing keypair.
func cmdKeygen(outDir string) {
	pub, priv, err := agent.GenerateKey()
	if err != nil {
		fatalf("keygen: %v", err)
	}
	pubHex := agent.EncodePublicKey(pub)
	privHex := agent.EncodePrivateKey(priv)
	signerID := agent.SignerID(pub)

	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o700); err != nil {
			fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "agent_ed25519.pub"), []byte(pubHex+"\n"), 0o644); err != nil {
			fatalf("write pubkey: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "agent_ed25519.priv"), []byte(privHex+"\n"), 0o600); err != nil {
			fatalf("write privkey: %v", err)
		}
		fmt.Printf("wrote keys to %s\n", outDir)
	}
	fmt.Printf("Public Key:  %s\n", pubHex)
	fmt.Printf("Private Key: %s\n", privHex)
	fmt.Printf("Signer ID:   %s\n", signerID)
}

// cmdSign signs an agent folder with an Ed25519 private key.
func cmdSign(folder, privKeyInput string) {
	if privKeyInput == "" {
		fatalf("missing -key: provide private key hex or path to private key file")
	}
	privKeyHex := privKeyInput
	if agent.HasSignature(folder) {
		// ok
	}
	fi, err := os.Stat(privKeyInput)
	if err == nil && !fi.IsDir() {
		b, err := os.ReadFile(privKeyInput)
		if err != nil {
			fatalf("read key file: %v", err)
		}
		privKeyHex = strings.TrimSpace(string(b))
	}
	priv, err := agent.ParsePrivateKey(privKeyHex)
	if err != nil {
		fatalf("parse private key: %v", err)
	}
	if err := agent.SignManifest(folder, priv); err != nil {
		fatalf("sign: %v", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	fmt.Printf("signed %s/agent.json -> %s\n", folder, agent.ManifestSigFile)
	fmt.Printf("signer: %s\n", agent.SignerID(pub))
}

// cmdPack packages an agent folder into a redistributable .zip.
func cmdPack(folder, outZip string) {
	m, err := agent.LoadManifest(folder)
	if err != nil {
		fatalf("manifest: %v", err)
	}
	if outZip == "" {
		outZip = fmt.Sprintf("%s-%s.zip", m.ID, m.Version)
		if m.Version == "" {
			outZip = fmt.Sprintf("%s.zip", m.ID)
		}
	}
	sha, err := agent.Pack(folder, outZip)
	if err != nil {
		fatalf("pack: %v", err)
	}
	fmt.Printf("packed %s -> %s\n", folder, outZip)
	fmt.Printf("sha256: %s\n", sha)
}

// discoverProblems runs discovery for its side effect (registering) and
// returns only the problem lines - run/list both want them on stderr.
func discoverProblems(dir string, pol agent.Policy) []string {
	_, problems := agent.DiscoverWithPolicy(dir, pol)
	return problems
}

// paramLine renders one parameter the way list/validate show it.
func paramLine(p agent.Param) string {
	t := p.Type
	if t == "" {
		t = "string"
	}
	s := "    " + p.Name + " (" + t
	if p.Required {
		s += ", required"
	} else {
		s += ", optional"
	}
	if len(p.Enum) > 0 {
		s += ", one of: " + strings.Join(p.Enum, "|")
	}
	if p.Default != "" {
		s += ", default " + p.Default
	}
	s += ")"
	if p.Description != "" {
		s += " - " + p.Description
	}
	return s
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "agentctl: "+format+"\n", a...)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: agentctl [flags] <command> [args...]

commands:
  list                 show installed agents
  search [query]       search registry index (-registry <url|file>)
  install <src>        install a folder, .zip, URL, or registry agent name
  update [id]          update installed agents from registry
  remove <id>          remove an installed agent
  run <id> [k=v ...]   run one agent once, print its output
  validate <folder>    check agent.json + exec + signature without installing
  pack <folder>        package an agent folder into a redistributable .zip
  keygen               generate an Ed25519 signing keypair
  sign <folder>        sign agent.json with an Ed25519 private key

flags:
  -dir <path>          agents directory
  -registry <url|path> registry index URL or local file path
  -key <hex>           trusted public key (or private key for sign)
  -require-signature   require cryptographic signature for install/run
  -sha256 <hex>        expected archive SHA-256 for install
  -out <path>          output destination for pack or keygen`)
}
