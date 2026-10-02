// preflight - chat-app's requirements check, standalone.
//
// Answers "will chat-app actually work right now?" without opening the UI:
// the selected LLM backend reachable/credentialed (ollama server + model,
// Gemini key + API, Bedrock credentials, OpenRouter key + API) plus the host
// environment (display, audio player, pet pipe, agents dir, image source).
// It runs the same internal/preflight checks chat-app's startup gate runs,
// so what it reports is exactly what the app would launch with.
//
//	preflight                 # what ./chat-app would launch with
//	preflight -all            # ...also probe the other backends (info only)
//	preflight -quick          # config + environment only: no network
//	preflight -deep           # bedrock: also fire a real 1-token Converse
//	preflight -json           # machine-readable report
//	preflight -provider ollama -model qwen2:1.5b   # test an alternative setup
//
// Exit codes: 0 = ok, 1 = warnings only, 2 = blocked (a fatal check failed).
// chat-app's own gate (preflight = strict, the default) exits 2 the same way,
// before its X window opens.
//
// Config resolution mirrors chat-app (flag > env > chat-app.ini > built-in
// default, sections ignored) with keep-in-sync constants - same rule as
// cmd/geminitest.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/portege/chat-app/agent"
	"github.com/portege/chat-app/internal/mic"
	"github.com/portege/chat-app/internal/preflight"
)

// Keep-in-sync with chat-app (package main): chat.go defaultAPIKey/
// defaultAPIURL, providers.go defaultOllamaURL/defaultOpenRouterURL,
// images.go defaultPixabayKey. They exist here because package main is not
// importable; the resolution LOGIC is shared (preflight.ResolveModel).
const (
	defaultGeminiAPIKey  = "AIzaSyB7YR3ypNW2A-raPItTfLir-B-vKuuzyR8"
	defaultGeminiAPIURL  = "https://generativelanguage.googleapis.com"
	defaultOllamaURL     = "http://localhost:8000"
	defaultOpenRouterURL = "https://openrouter.ai/api/v1"
	defaultPixabayKey    = "57324196-80f538aa87774c57d2e3c7b71"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

// run parses args, resolves chat-app's configuration, executes the checks and
// returns the exit code (0 ok / 1 warnings / 2 blocked).
func run(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		configFlag   = fs.String("config", "", "INI config file (default: ./chat-app.ini if present)")
		providerFlag = fs.String("provider", "", "LLM backend to test (default: chat-app.ini provider)")
		modelFlag    = fs.String("model", "", "model ID to test (default: chat-app.ini / provider default)")
		apiURLFlag   = fs.String("api-url", "", "LLM endpoint base (default: chat-app.ini / provider default)")
		apiKeyFlag   = fs.String("api-key", "", "API key for gemini/openrouter (default: env / chat-app.ini / built-in)")
		profileFlag  = fs.String("aws-profile", "", "AWS profile for bedrock (default: chat-app.ini / default)")
		regionFlag   = fs.String("aws-region", "", "AWS region for bedrock (default: chat-app.ini)")
		allFlag      = fs.Bool("all", false, "also probe the non-selected backends (informational, never affects the exit code)")
		quickFlag    = fs.Bool("quick", false, "config + environment checks only: no network probes")
		deepFlag     = fs.Bool("deep", false, "bedrock: also fire a real 1-token Converse call")
		jsonFlag     = fs.Bool("json", false, "write the machine-readable report to stdout")
		timeoutFlag  = fs.Duration("timeout", 3*time.Second, "budget for the whole run")
		micFlag      = fs.Bool("mic", false, "list the microphones you can point stt-device at, and exit")
		micTestFlag  = fs.Bool("mic-test", false, "record ~2s from the selected microphone and report its level")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	// Config file: -config wins, else the conventional ./chat-app.ini
	// (chat-app's defaultConfigPath without the next-to-binary fallback,
	// which only matters when the binary is relocated).
	path := strings.TrimSpace(*configFlag)
	if path == "" {
		path = preflight.DefaultINIPath()
	}
	kv := map[string]string{}
	if path != "" {
		var err error
		kv, err = preflight.ReadINI(path)
		if err != nil {
			fmt.Fprintf(out, "preflight: %v\n", err)
			return 2
		}
	}

	// -mic / -mic-test answer "why is my microphone not recognised?". They
	// short-circuit the requirement checks: the listing says what stt-device
	// can be set to, and the probe says whether that device carries signal.
	if *micFlag || *micTestFlag {
		return runMicReport(out, preflight.Get(kv, "stt-device"), *micFlag, *micTestFlag)
	}

	spec := resolveSpec(kv, explicit, *providerFlag, *modelFlag, *apiURLFlag, *apiKeyFlag,
		*profileFlag, *regionFlag, *deepFlag)
	env := resolveEnv(kv)

	checks := preflight.Checks(spec)
	if *allFlag {
		for _, other := range otherSpecs(spec, kv, explicit) {
			checks = append(checks, preflight.AsInfo(preflight.Checks(other))...)
		}
	}
	checks = append(checks, preflight.EnvChecks(env)...)
	if *quickFlag {
		checks = preflight.WithoutLive(checks)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()
	rep := preflight.Run(ctx, checks)
	rep.Context = preflight.SpecContext(spec)
	if *jsonFlag {
		_ = rep.WriteJSON(out)
	} else {
		_ = rep.WriteText(out)
	}
	return rep.ExitCode()
}

// resolveSpec reproduces chat-app main.go's flag > env > config > default
// precedence for the provider under test.
func resolveSpec(kv map[string]string, explicit map[string]bool,
	providerFlag, modelFlag, apiURLFlag, apiKeyFlag, profileFlag, regionFlag string, deep bool) preflight.Spec {

	// Provider: flag > config > gemini, unknown names normalized with a note
	// (main.go does the same and falls back to gemini).
	provider := strings.ToLower(strings.TrimSpace(providerFlag))
	if provider == "" {
		provider = strings.ToLower(preflight.Get(kv, "provider"))
	}
	if provider == "" {
		provider = "gemini"
	}
	switch provider {
	case "gemini", "bedrock", "ollama", "openrouter":
	default:
		fmt.Fprintf(os.Stderr, "preflight: warning: unknown provider %q, using gemini\n", provider)
		provider = "gemini"
	}

	// Model: shared resolution - the same call chat-app's pickModel makes.
	cfgModel := preflight.Get(kv, "model")
	model, warn := preflight.ResolveModel(provider, modelFlag, explicit["model"], cfgModel)
	if warn != "" {
		fmt.Fprintf(os.Stderr, "preflight: warning: %s\n", warn)
	}

	// Endpoint: flag > config > provider default.
	apiURL := strings.TrimSpace(apiURLFlag)
	if apiURL == "" {
		apiURL = preflight.Get(kv, "api-url")
	}
	if apiURL == "" {
		switch provider {
		case "openrouter":
			apiURL = defaultOpenRouterURL
		case "ollama":
			apiURL = defaultOllamaURL
		default:
			apiURL = defaultGeminiAPIURL
		}
	}

	// Keys: each backend resolves them exactly like main.go does (the
	// chat-app.ini api-key field is shared across providers).
	key := resolveGeminiKey(kv, explicit, apiKeyFlag)
	if provider == "openrouter" {
		key = resolveOpenRouterKey(kv, explicit, apiKeyFlag)
	}

	// AWS profile/region: flag > config > profile default ("default").
	profile := strings.TrimSpace(profileFlag)
	if profile == "" {
		profile = preflight.Get(kv, "aws-profile")
	}
	if profile == "" {
		profile = "default"
	}
	region := strings.TrimSpace(regionFlag)
	if region == "" {
		region = preflight.Get(kv, "aws-region")
	}

	return preflight.Spec{
		Provider:   provider,
		APIURL:     apiURL,
		Model:      model,
		APIKey:     key,
		AWSProfile: profile,
		AWSRegion:  region,
		Deep:       deep && provider == "bedrock",
	}
}

// resolveGeminiKey mirrors main.go: explicit -api-key > $GEMINI_API_KEY >
// $GOOGLE_API_KEY > chat-app.ini api-key > the built-in free-tier key;
// "off"/"none"/"stub" means stub mode (no key).
func resolveGeminiKey(kv map[string]string, explicit map[string]bool, flagVal string) string {
	key := strings.TrimSpace(flagVal)
	if !explicit["api-key"] {
		if e := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); e != "" {
			key = e
		} else if e := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")); e != "" {
			key = e
		} else if k := preflight.Get(kv, "api-key"); k != "" {
			key = k
		}
	}
	if key == "" {
		key = defaultGeminiAPIKey
	}
	switch strings.ToLower(key) {
	case "off", "none", "stub":
		return ""
	}
	return key
}

// resolveOpenRouterKey mirrors main.go: explicit -api-key >
// $OPENROUTER_API_KEY > chat-app.ini api-key; there is no built-in key.
func resolveOpenRouterKey(kv map[string]string, explicit map[string]bool, flagVal string) string {
	if explicit["api-key"] {
		return strings.TrimSpace(flagVal)
	}
	if e := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); e != "" {
		return e
	}
	return preflight.Get(kv, "api-key")
}

// otherSpecs builds one Spec per non-selected backend for -all: model/url
// fall back to each backend's own defaults inside Checks(), keys follow the
// app's shared api-key field, -deep only ever applies to the selection.
func otherSpecs(sel preflight.Spec, kv map[string]string, explicit map[string]bool) []preflight.Spec {
	var out []preflight.Spec
	geminiKey := resolveGeminiKey(kv, explicit, "")
	openrouterKey := resolveOpenRouterKey(kv, explicit, "")
	for _, p := range []string{"gemini", "bedrock", "ollama", "openrouter"} {
		if p == sel.Provider {
			continue
		}
		other := preflight.Spec{
			Provider:   p,
			AWSProfile: sel.AWSProfile,
			AWSRegion:  sel.AWSRegion,
		}
		switch p {
		case "gemini":
			other.APIKey = geminiKey
		case "openrouter":
			other.APIKey = openrouterKey
		}
		out = append(out, other)
	}
	return out
}

// resolveEnv assembles the environment context from chat-app.ini, mirroring
// main.go's resolution (pet-pipe auto-detect, agents dir default, image
// source fallbacks, tts default on).
func resolveEnv(kv map[string]string) preflight.Env {
	// Pet pipe: config "off" disables; "auto"/empty derives the path from
	// $DISPLAY exactly like pet.go's petPipePath.
	pipe := ""
	switch strings.ToLower(preflight.Get(kv, "pet-pipe")) {
	case "off":
		pipe = ""
	case "", "auto":
		pipe = petPipePath()
	default:
		pipe = preflight.Get(kv, "pet-pipe")
	}

	// Search directories, in precedence order. An agents-dir from the config
	// REPLACES the defaults; otherwise the per-user dir is scanned first and
	// the packaged /opt/onidia/share/agents second, matching main.go.
	agentsDir := preflight.Get(kv, "agents-dir")
	var searchDirs []string
	if agentsDir != "" {
		searchDirs = []string{agentsDir}
	} else {
		searchDirs = agent.DefaultDirs()
	}
	agentsOff, _ := strconv.ParseBool(preflight.Get(kv, "agents-off"))

	// Image source: config image-source > legacy images bool > pixabay.
	imageSource := strings.ToLower(preflight.Get(kv, "image-source"))
	if imageSource == "" {
		if images := strings.ToLower(preflight.Get(kv, "images")); images == "false" || images == "off" {
			imageSource = "off"
		} else {
			imageSource = "pixabay"
		}
	}
	// Pixabay key: $PIXABAY_API_KEY > config > built-in (same fallback as
	// main.go, so an unset key still counts as present).
	pxKey := strings.TrimSpace(os.Getenv("PIXABAY_API_KEY"))
	if pxKey == "" {
		pxKey = preflight.Get(kv, "pixabay-key")
	}
	if pxKey == "" {
		pxKey = defaultPixabayKey
	}

	ttsOn := strings.ToLower(preflight.Get(kv, "tts")) != "off"
	// "off" hides the mic button, exactly as NewSTT does in the app.
	stt := strings.ToLower(strings.TrimSpace(preflight.Get(kv, "stt")))
	sttOn := stt != "off" && stt != "none" && stt != "disabled"

	return preflight.Env{
		Pipe:        pipe,
		AgentsDirs:  searchDirs,
		AgentsOff:   agentsOff,
		ImageSource: imageSource,
		PixabayKey:  pxKey,
		TTSOn:       ttsOn,
		STTOn:       sttOn,
	}
}

// petPipePath mirrors pet.go: the say-FIFO name is derived from $DISPLAY
// with every '/', ':' and '.' replaced by '-'. Empty without a display.
func petPipePath() string {
	disp := os.Getenv("DISPLAY")
	if disp == "" {
		return ""
	}
	tag := strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == '.' {
			return '-'
		}
		return r
	}, disp)
	return "/tmp/desktop-pet-" + tag + ".say"
}

// runMicReport prints the microphone listing and/or the level probe for the
// configured stt-device. It is the diagnostic for the "mic is not
// recognised" case: the listing shows what there is to choose from, and the
// probe separates "no device" from "a device that records only silence"
// (a mute button, a wrong input, or a gain turned down).
func runMicReport(out io.Writer, want string, list, probe bool) int {
	recorder := micRecorder()
	if recorder == "" {
		fmt.Fprintln(out, "no audio recorder found (tried pw-record, parecord, arecord, ffmpeg)")
		fmt.Fprintln(out, "  install pipewire-utils (pw-record) or alsa-utils (arecord), or set stt = off")
		return 2
	}

	mics, err := mic.List(recorder)
	if err != nil {
		fmt.Fprintf(out, "microphones: %v\n", err)
		if list {
			return 2
		}
		mics = nil
	}
	selected, found := mic.Pick(mics, want)
	if list {
		fmt.Fprintf(out, "recorder: %s\n", recorder)
		if len(mics) == 0 {
			fmt.Fprintln(out, "no microphones found")
			return 2
		}
		fmt.Fprintf(out, "microphones (* = in use):%s\n", mic.Describe(mics))
		fmt.Fprintf(out, "  set stt-device = <value above> in chat-app.ini to pin one\n")
		if want != "" && !found {
			fmt.Fprintf(out, "  note: stt-device = %q does not match any device; falling back to the default\n", want)
		}
	}
	if !probe {
		return 0
	}

	fmt.Fprintf(out, "recording %.0fs from %s - say something now...\n", mic.ProbeSeconds, selected.Device)
	lvl, err := mic.Probe(recorder, selected.Device)
	if err != nil {
		fmt.Fprintf(out, "probe failed: %v\n", err)
		return 2
	}
	fmt.Fprintf(out, "level: %s\n", lvl)
	fmt.Fprintf(out, "  -> %s\n", lvl.Hint())
	if lvl.Silent {
		return 1
	}
	return 0
}

// micRecorder is the recorder chat-app would pick (see stt.go findSTTRecorder).
func micRecorder() string {
	if r, err := exec.LookPath("pw-record"); err == nil {
		if pipewireUp() {
			return r
		}
	}
	for _, c := range []string{"pw-record", "parecord", "arecord", "ffmpeg"} {
		if r, err := exec.LookPath(c); err == nil {
			return r
		}
	}
	return ""
}

// pipewireUp reports whether a PipeWire server socket exists, mirroring
// chat-app's tts.go: on a PipeWire desktop the raw ALSA device is held by the
// server, so the native pw-record must win.
func pipewireUp() bool {
	dir := fmt.Sprintf("/run/user/%d", os.Getuid())
	m, err := filepath.Glob(filepath.Join(dir, "pipewire*"))
	return err == nil && len(m) > 0
}
