package main

// chat-app - a tiny desktop chat window with a textarea and a submit (SEND)
// button, written in pure Go on raw X11 - the same no-toolkit approach as its
// sibling ../desktop-pet.
//
// Phase 1 (done): the complete UI - message bubbles, textarea with
// placeholder + caret, SEND button, typing, scrolling, resizing - plus
// headless PNG previews (`-preview`).
// Phase 2 (done): the brain - Google Gemini via chat.go, with replies
// forwarded to the desktop-pet's say-FIFO (pet.go).
// Phase 3 (done): configurable system instruction via -system-prompt,
// -system-file, and -config INI file.

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// defaultConfigPath returns the conventional chat-app.ini to auto-load when
// -config is not given: first in the working directory, then next to the
// running binary. Returns "" when neither exists (config stays disabled).
func defaultConfigPath() string {
	if _, err := os.Stat("chat-app.ini"); err == nil {
		return "chat-app.ini"
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "chat-app.ini")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// pickModel resolves the effective model ID for a provider and returns an
// optional warning. Precedence: explicit -model flag > config-file model >
// provider default. A config-file/default model that clearly belongs to the
// other provider family (a leftover Gemini ID while provider=bedrock, or a
// Bedrock ID with provider=gemini) is replaced by the provider's default and
// flagged: sending it would fail with a confusing API error (e.g. Bedrock
// "ValidationException: the provided model identifier is invalid").
func pickModel(flagVal string, flagSet bool, cfgModel, provider string) (string, string) {
	model := strings.TrimSpace(flagVal)
	if !flagSet && cfgModel != "" {
		model = strings.TrimSpace(cfgModel)
	}
	if model == "" {
		switch provider {
		case "bedrock":
			return defaultBedrockModelID, ""
		case "openrouter":
			return defaultOpenRouterModel, ""
		}
		return defaultModel, ""
	}
	if !flagSet {
		switch {
		case provider == "bedrock" && isGeminiModel(model):
			return defaultBedrockModelID, fmt.Sprintf(
				"provider=bedrock but model %q is a Gemini ID (from config) - using %q; set model in chat-app.ini or -model to a Bedrock ID",
				model, defaultBedrockModelID)
		case provider == "gemini" && isBedrockModel(model):
			return defaultModel, fmt.Sprintf(
				"provider=gemini but model %q is a Bedrock ID (from config) - using %q; set -model to a Gemini model",
				model, defaultModel)
		case provider == "openrouter" && isGeminiModel(model):
			return defaultOpenRouterModel, fmt.Sprintf(
				"provider=openrouter but model %q is a Gemini ID (from config) - using %q; set model in chat-app.ini or -model to an OpenRouter ID (e.g. deepseek/deepseek-chat)",
				model, defaultOpenRouterModel)
		case provider == "openrouter" && isBedrockModel(model):
			return defaultOpenRouterModel, fmt.Sprintf(
				"provider=openrouter but model %q is a Bedrock ID (from config) - using %q; set model in chat-app.ini or -model to an OpenRouter ID (e.g. deepseek/deepseek-chat)",
				model, defaultOpenRouterModel)
		}
	}
	if provider == "bedrock" && !isBedrockModel(model) {
		return model, fmt.Sprintf(
			"model %q does not look like a Bedrock model ID (e.g. %s) - requests will likely be rejected",
			model, defaultBedrockModelID)
	}
	return model, ""
}

// resolvePetPipe combines the -pet-pipe flag and the config-file pet-pipe
// value (flag wins) into the FIFO path replies are forwarded to. "auto" or
// empty means derive the path from $DISPLAY exactly the way the pet names
// it; "off" (case-insensitive) disables forwarding; any other value is used
// as the literal path. Before config auto-loading, "auto" from chat-app.ini
// used to fall through as a literal (relative) path and every say write
// failed silently with ENOENT - the character never showed the reply.
func resolvePetPipe(flagVal, cfgVal string) string {
	pipe := strings.TrimSpace(flagVal)
	if pipe == "" {
		pipe = strings.TrimSpace(cfgVal)
	}
	switch strings.ToLower(pipe) {
	case "off":
		return ""
	case "", "auto":
		return petPipePath()
	}
	return pipe
}

func main() {
	log.SetPrefix("[chat] ")
	var (
		w       = flag.Int("w", defaultWinW, "initial window width")
		h       = flag.Int("h", defaultWinH, "initial window height")
		preview = flag.Bool("preview", false,
			"render chat_ui_*.png previews and exit (no display needed)")
		apiKey = flag.String("api-key", "",
			`LLM API key (default: env or built-in for the selected provider; "off" = stub mode)`)
		model  = flag.String("model", "", "model ID (Gemini, Bedrock, or OpenRouter; default depends on -provider)")
		apiURL = flag.String("api-url", "",
			"LLM endpoint base (default depends on -provider: Gemini, OpenRouter, or localhost for ollama)")
		provider = flag.String("provider", "",
			`LLM backend: "gemini" (default), "bedrock", "ollama", or "openrouter"`)
		awsProfile = flag.String("aws-profile", "",
			"AWS shared profile name for Bedrock (default: default, or $AWS_PROFILE)")
		awsRegion = flag.String("aws-region", "",
			"AWS region for Bedrock (default: from profile or $AWS_REGION)")
		petPipe = flag.String("pet-pipe", "",
			`desktop-pet say FIFO (default: auto /tmp/desktop-pet-<display>.say, "off" disables)`)
		systemPrompt = flag.String("system-prompt", "",
			"system instruction / persona (default: built-in Buddy persona)")
		systemFile = flag.String("system-file", "",
			"load system instruction from this file (overrides the built-in persona)")
		configFile = flag.String("config", "",
			"INI config file to load defaults from (default: auto-load ./chat-app.ini if present)")
		fetchImageFlag = flag.String("fetch-image", "",
			"test image fetch: download thumbnail for keyword and exit (no window)")
		genImageFlag = flag.String("gen-image", "",
			"test gemini image generation: generate a picture from this prompt and exit")
		imageSource = flag.String("image-source", "",
			`image replies: "pixabay" (Pixabay photo, default) | "wiki" (Wikipedia thumbnail) | "gemini" (AI generated) | "off"`)
		images     = flag.Bool("images", true, "legacy alias; use -image-source (false = off)")
		forceImage = flag.String("force-image", "", "always fetch/generate an image for this keyword (testing)")
		pixabayKey = flag.String("pixabay-key", "",
			"Pixabay API key for image replies (default: $PIXABAY_API_KEY, config pixabay-key, or built-in)")
		ttsFlag = flag.String("tts", "",
			`speech replies: "on" (default, needs Typecast + aplay/paplay/ffplay) | "off"`)
		ttsKey = flag.String("tts-key", "",
			"Typecast API key for spoken replies (default: $TYPECAST_API_KEY, config tts-key, or built-in)")
		ttsVoice = flag.String("tts-voice", "",
			"Typecast voice id for spoken replies (default: config tts-voice, or built-in)")
	)
	flag.Parse()

	// Track which flags were explicitly set on the command line so they
	// take priority over values from the config file.
	explicitFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		explicitFlags[f.Name] = true
	})

	// Load INI config file. `-config` wins; otherwise a conventional
	// chat-app.ini is auto-loaded from the working directory or the binary's
	// directory so edits to it take effect without extra flags. Precedence
	// stays the same: explicit flags and env vars always win over the file.
	var cfg *Config
	configPath := strings.TrimSpace(*configFile)
	if configPath == "" {
		configPath = defaultConfigPath()
	}
	if configPath != "" {
		var err error
		cfg, err = LoadConfig(configPath)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		log.Printf("config: loaded from %s", configPath)
	}

	// Resolve the brain configuration. Precedence (highest first):
	//  1. explicitly-set CLI flag
	//  2. environment variable (key only)
	//  3. config file value
	//  4. built-in default constant
	var cfgKey string
	if cfg != nil {
		cfgKey = cfg.APIKey
	}
	key := *apiKey
	if !explicitFlags["api-key"] {
		if envKey := os.Getenv("GEMINI_API_KEY"); envKey != "" {
			key = envKey
		} else if envKey := os.Getenv("GOOGLE_API_KEY"); envKey != "" {
			key = envKey
		} else if cfgKey != "" {
			key = cfgKey
		}
	}
	key = strings.TrimSpace(key)
	if key == "" {
		key = defaultAPIKey
	}
	switch strings.ToLower(key) {
	case "off", "none", "stub":
		key = ""
	}

	// Provider: explicit flag > config file > gemini default
	providerVal := strings.ToLower(strings.TrimSpace(*provider))
	if providerVal == "" && cfg != nil {
		providerVal = strings.ToLower(strings.TrimSpace(cfg.Provider))
	}
	if providerVal == "" {
		providerVal = "gemini"
	}
	switch providerVal {
	case "gemini", "bedrock", "ollama", "openrouter":
		// ok
	default:
		log.Printf("warning: unknown provider %q, using gemini", providerVal)
		providerVal = "gemini"
	}

	// Model: explicit -model flag > config-file model > provider default. A
	// leftover model from the other provider family (e.g. a Gemini ID in
	// chat-app.ini while provider=bedrock) is auto-swapped and warned about.
	var cfgModel string
	if cfg != nil {
		cfgModel = cfg.Model
	}
	modelVal, modelWarn := pickModel(*model, explicitFlags["model"], cfgModel, providerVal)
	if modelWarn != "" {
		log.Printf("warning: %s", modelWarn)
	}

	// API URL: explicit flag > config file > default
	urlVal := strings.TrimSpace(*apiURL)
	if urlVal == "" && cfg != nil {
		urlVal = strings.TrimSpace(cfg.APIURL)
	}
	if urlVal == "" {
		switch providerVal {
		case "openrouter":
			urlVal = defaultOpenRouterURL
		case "ollama":
			urlVal = defaultOllamaURL
		default:
			urlVal = defaultAPIURL
		}
	}

	// Pet pipe: explicit flag > config file > auto-detect > off
	var cfgPipe string
	if cfg != nil {
		cfgPipe = cfg.PetPipe
	}
	pipe := resolvePetPipe(*petPipe, cfgPipe)
	if pipe != "" && !filepath.IsAbs(pipe) {
		log.Printf("warning: pet-pipe %q is not an absolute path - say writes will fail silently; use auto, off, or an absolute FIFO path", pipe)
	}
	// The pet's command FIFO (actions/events, and "quit" for the Haiya!
	// button) sits next to the say-FIFO with a .cmd suffix.
	petCmdPath := petCmdPathFor(pipe)

	// System instruction: -system-prompt flag > -system-file flag > config file
	sysPrompt := *systemPrompt
	sysFile := *systemFile
	if cfg != nil {
		if sysPrompt == "" && sysFile == "" {
			sysPrompt = cfg.SystemPrompt
			if cfg.SystemFile != "" {
				sysFile = cfg.SystemFile
			}
		}
	}

	// Image source: explicit flag > config file > legacy images bool > pixabay default
	imgSource := strings.ToLower(strings.TrimSpace(*imageSource))
	if imgSource == "" && cfg != nil {
		imgSource = strings.ToLower(strings.TrimSpace(cfg.ImageSource))
	}
	if imgSource == "" {
		if explicitFlags["images"] && !*images {
			imgSource = "off"
		} else if cfg != nil && !cfg.Images && !explicitFlags["images"] {
			imgSource = "off"
		} else {
			imgSource = "pixabay"
		}
	}
	switch imgSource {
	case "pixabay", "wiki", "gemini", "off":
		// ok
	default:
		log.Printf("warning: unknown image-source %q, using pixabay", imgSource)
		imgSource = "pixabay"
	}

	// Pixabay key: explicit flag > $PIXABAY_API_KEY > config file > built-in
	pxKey := strings.TrimSpace(*pixabayKey)
	if pxKey == "" {
		if envKey := os.Getenv("PIXABAY_API_KEY"); envKey != "" {
			pxKey = strings.TrimSpace(envKey)
		} else if cfg != nil {
			pxKey = strings.TrimSpace(cfg.PixabayKey)
		}
	}
	if pxKey == "" {
		pxKey = defaultPixabayKey
	}

	forceImg := *forceImage
	if forceImg == "" && cfg != nil {
		forceImg = cfg.ForceImage
	}

	// TTS: explicit -tts flag > config file > on by default. Key/voice:
	// explicit flag > $TYPECAST_API_KEY > config file > built-in default.
	ttsVal := strings.ToLower(strings.TrimSpace(*ttsFlag))
	if ttsVal == "" && cfg != nil {
		ttsVal = strings.ToLower(strings.TrimSpace(cfg.TTS))
	}
	ttsOn := ttsVal != "off"
	ttsKeyVal := strings.TrimSpace(*ttsKey)
	if ttsKeyVal == "" {
		if envKey := os.Getenv("TYPECAST_API_KEY"); envKey != "" {
			ttsKeyVal = strings.TrimSpace(envKey)
		} else if cfg != nil {
			ttsKeyVal = strings.TrimSpace(cfg.TTSKey)
		}
	}
	ttsVoiceVal := strings.TrimSpace(*ttsVoice)
	if ttsVoiceVal == "" && cfg != nil {
		ttsVoiceVal = strings.TrimSpace(cfg.TTSVoice)
	}

	// -fetch-image tests the resolved source's fetch path (the gemini source
	// has its own -gen-image probe).
	if *fetchImageFlag != "" {
		kw := CleanKeyword(*fetchImageFlag)
		var img *ImageResult
		switch imgSource {
		case "gemini":
			log.Fatalf("image-source=gemini generates images; use -gen-image %q instead", kw)
		case "wiki":
			img = FetchImage(kw, nil)
		default: // pixabay
			img = FetchPixabayImage(kw, pxKey, nil)
		}
		if img.Err != nil {
			log.Fatalf("image fetch: %v", img.Err)
		}
		fmt.Printf("%s -> %dx%d\n", img.URL, img.Image.Bounds().Dx(), img.Image.Bounds().Dy())
		return
	}

	if *genImageFlag != "" {
		bot := NewBot()
		bot.APIKey = key
		bot.APIURL = urlVal
		bot.Model = modelVal
		img, err := bot.GenerateImage(*genImageFlag)
		if err != nil {
			log.Fatalf("image generation: %v", err)
		}
		fmt.Printf("generated %dx%d\n", img.Bounds().Dx(), img.Bounds().Dy())
		return
	}

	if *preview {
		dumpPreviews()
		return
	}

	expandedH := max(*h, 260)
	collapsedH := headerH + inputH

	win, err := Open(max(*w, 200), collapsedH) // prompt-only start (collapsed)
	if err != nil {
		log.Fatalf("%v", err)
	}
	defer win.Close()

	ui := NewUI(win.windowSize())
	ui.expandedH = expandedH // restore this height when the conversation expands
	ui.Bot = NewBot()
	ui.Bot.APIKey = key
	ui.Bot.Model = modelVal
	ui.Bot.APIURL = urlVal
	ui.Bot.PetPipe = pipe
	ui.Bot.SystemInstruction = resolveSystemPrompt(sysPrompt, sysFile)
	// Haiya! button lifecycle: pink whenever a pet is listening on the cmd
	// FIFO (launched by us or adopted), teal when none is. The click launches
	// (teal) or gracefully quits with the poof-out animation (pink).
	petRunning := func(path string) bool { return path != "" && petPipeReady(path) }
	if petRunning(petCmdPath) {
		log.Printf("pet: onidia already running - Haiya! button will quit it")
		ui.SetPetRunning(true)
	}
	petGoneCh := make(chan struct{}, 1)
	var petQuitting atomic.Bool
	var petRestartPending atomic.Bool
	petTick := time.NewTicker(2 * time.Second)
	defer petTick.Stop()
	// Haiya! click: launch when teal, gracefully quit (poof-out) when pink.
	onHaiya := func() {
		if ui.PetRunning() {
			if !petQuitting.CompareAndSwap(false, true) {
				return // a quit is already in flight
			}
			log.Printf("pet: quit requested via Haiya! button")
			QuitPet(petCmdPath, petGoneCh)
			return
		}
		if err := LaunchPet(ui.PetCharacter(), ui.PetDemo()); err != nil {
			log.Printf("pet: %v", err)
			return
		}
		petQuitting.Store(false)
		ui.SetPetRunning(true)
	}
	ui.Bot.ImageSource = imgSource
	ui.Bot.PixabayKey = pxKey
	ui.Bot.ForceImageKeyword = forceImg
	// Settings dialog wiring: saves go to the loaded INI file (or a
	// conventional ./chat-app.ini when none was loaded), and a character-age
	// from the config seeds the dialog and the persona.
	ui.savePath = configPath
	if ui.savePath == "" {
		ui.savePath = "chat-app.ini"
	}
	if cfg != nil && cfg.CharacterName != "" {
		ui.name = cfg.CharacterName
		ui.Bot.Name = cfg.CharacterName
		ui.Bot.CharacterName = cfg.CharacterName
	}
	if cfg != nil && cfg.CharacterAge > 0 {
		ui.age = cfg.CharacterAge
		ui.Bot.CharacterAge = cfg.CharacterAge
	}
	if cfg != nil && cfg.SleepSet {
		ui.sleepFrom, ui.sleepTo = cfg.SleepFrom, cfg.SleepTo
		ui.sleepFromMin, ui.sleepToMin = cfg.SleepFromM, cfg.SleepToM
		ui.Bot.SleepSet = true
		ui.Bot.SleepFromH, ui.Bot.SleepToH = cfg.SleepFromH, cfg.SleepToH
		ui.Bot.SleepFromM, ui.Bot.SleepToM = cfg.SleepFromM, cfg.SleepToM
		ui.Bot.SleepFrom, ui.Bot.SleepTo = cfg.SleepFrom, cfg.SleepTo
	}
	if cfg != nil && cfg.BusySet {
		ui.busyFrom, ui.busyTo = cfg.BusyFrom, cfg.BusyTo
		ui.busyFromMin, ui.busyToMin = cfg.BusyFromM, cfg.BusyToM
		ui.Bot.BusySet = true
		ui.Bot.BusyFromH, ui.Bot.BusyToH = cfg.BusyFromH, cfg.BusyToH
		ui.Bot.BusyFromM, ui.Bot.BusyToM = cfg.BusyFromM, cfg.BusyToM
		ui.Bot.BusyFrom, ui.Bot.BusyTo = cfg.BusyFrom, cfg.BusyTo
	}
	if cfg != nil && cfg.Mute {
		ui.mute = true // the dialog's MUTE SPEECH checkbox starts checked
	}
	// Demo mode defaults to OFF (planted pet): only an explicit
	// demo-mode = true in the INI turns autonomous roaming/chatter on.
	if cfg != nil {
		ui.demo = cfg.DemoMode
	}
	// OpenRouter (and other OpenAI-compatible gateways) auth: explicit
	// -api-key flag > $OPENROUTER_API_KEY > config api-key. There is no
	// built-in default key, so a missing key makes GenerateText fail loud.
	var openrouterKey string
	if providerVal == "openrouter" {
		openrouterKey = strings.TrimSpace(*apiKey)
		if !explicitFlags["api-key"] {
			if e := os.Getenv("OPENROUTER_API_KEY"); e != "" {
				openrouterKey = strings.TrimSpace(e)
			} else if cfg != nil && cfg.APIKey != "" {
				openrouterKey = cfg.APIKey
			}
		}
	}

	// SSE streaming for the openrouter provider: default on, the config key
	// `stream = false` switches back to single-shot replies.
	streamVal := true
	if cfg != nil && cfg.StreamSet {
		streamVal = cfg.Stream
	}

	// Build the selected provider.
	var botProvider Provider
	switch providerVal {
	case "bedrock":
		awsProfile := *awsProfile
		if awsProfile == "" && cfg != nil {
			awsProfile = cfg.AWSProfile
		}
		if awsProfile == "" {
			awsProfile = "default"
		}
		awsRegion := *awsRegion
		if awsRegion == "" && cfg != nil {
			awsRegion = cfg.AWSRegion
		}
		p, err := newBedrockProvider(awsProfile, awsRegion, modelVal)
		if err != nil {
			log.Fatalf("bedrock provider: %v", err)
		}
		botProvider = p
	case "ollama":
		botProvider = newOllamaProvider(urlVal, modelVal)
	case "openrouter":
		botProvider = newOpenRouterProvider(openrouterKey, urlVal, modelVal, streamVal)
	default: // gemini
		botProvider = &geminiProvider{apiKey: key, apiURL: urlVal, model: modelVal, http: &http.Client{Timeout: imageTimeout + geminiTimeout}}
	}

	ui.Bot.Provider = botProvider
	// SSE deltas fire on the reply goroutine; hand them to the main loop over
	// a channel so only the main loop ever touches UI state (mirrors Replies).
	ui.Bot.OnDelta = func(accumulated string) { ui.Stream <- accumulated }

	if sysPrompt != "" || (sysFile != "" && sysFile != "off") {
		log.Printf("system-instruction custom (from flag or config)")
	} else {
		log.Printf("system-instruction default persona")
	}
	if providerVal == "bedrock" {
		log.Printf("bedrock: model=%s", modelVal)
	} else if providerVal == "ollama" {
		log.Printf("ollama: model=%s, api-url=%s", modelVal, urlVal)
	} else if providerVal == "openrouter" {
		if openrouterKey == "" {
			log.Printf("openrouter: no API key (set $OPENROUTER_API_KEY or -api-key) - replies will fail")
		} else {
			log.Printf("openrouter: model=%s, api-url=%s, stream=%t", modelVal, urlVal, streamVal)
		}
	} else if key == "" {
		log.Printf("gemini: no API key (set GEMINI_API_KEY or -api-key) - running in stub mode")
	} else {
		log.Printf("gemini: model=%s", modelVal)
	}
	demoState := "off (planted)"
	if ui.PetDemo() {
		demoState = "on (roams + chatters)"
	}
	log.Printf("pet: demo mode %s", demoState)
	log.Printf("images: source=%s", imgSource)
	if pipe == "" {
		log.Printf("pet: say-pipe forwarding disabled")
	} else {
		log.Printf("pet: replies go to %s", pipe)
	}

	// Text-to-speech: the bubble shows immediately; the reply text is queued
	// and spoken (via Typecast -> aplay/paplay/ffplay) as soon as it's ready.
	tts := NewTTS(ttsOn, ttsKeyVal, ttsVoiceVal)
	tts.Start()
	defer tts.Close()
	if tts.Enabled() {
		log.Printf("tts: on (voice %s, player %s)", tts.voiceID, filepath.Base(tts.player))
	} else if ttsOn {
		log.Printf("tts: on but no audio player available - speech disabled")
	} else {
		log.Printf("tts: off")
	}

	// Apply the busy window to the bubble label right away so the name is
	// correct from the first frame.
	ui.updateBusyState()

	dirty := true
	caret := time.NewTicker(530 * time.Millisecond)
	defer caret.Stop()

	// busyTicker re-checks the busy window once a minute so the bubble
	// sender label flips to "Busy/Work" (and back) without a manual refresh.
	busyTicker := time.NewTicker(1 * time.Minute)
	defer busyTicker.Stop()

	// Header-drag state: pressing the frameless header and moving beyond a
	// small threshold hands the drag to the WM via _NET_WM_MOVERESIZE; a
	// plain click (no movement) still toggles collapse on release.
	const dragThreshold = 4 // px of movement that turns a click into a drag
	var (
		pressW                 Widget
		pressX, pressY         int // window-relative press point
		pressRootX, pressRootY int // root-relative press point
		dragging               bool
	)

	log.Printf("ui ready - type in the textarea, press enter or SEND")

	// Startup greeting: once the pet's entrance animation (skate / parachute /
	// poof-in, a few seconds) has finished, have the LLM open the day with a
	// warm hello plus one short did-you-know fact. The result arrives on the
	// regular Replies channel, so it gets the same bubble/TTS/pet pipeline as
	// a user-prompted answer. Skipped when the character should be asleep or
	// busy right now (checked again inside Bot.Greeting).
	//
	// It also waits for the pet's say-FIFO to actually be listening before
	// firing: a fixed timer can fire before the desktop-pet has even created
	// its pipe (or before its reader is ready), which would silently drop the
	// greeting's bubble. The grace period then covers the entrance animation.
	go func() {
		const (
			entranceGrace = 7 * time.Second // leave the entrance room to play
			petWait       = 90 * time.Second
			pollEvery     = 250 * time.Millisecond
		)
		if pipe != "" {
			deadline := time.Now().Add(petWait)
			for !petPipeReady(pipe) && time.Now().Before(deadline) {
				time.Sleep(pollEvery)
			}
		}
		time.Sleep(entranceGrace)
		ui.Thinking = true
		go func() {
			result := ui.Bot.Greeting()
			ui.Replies <- result
		}()
	}()

	for {
		select {
		case ev, ok := <-win.Events():
			if !ok {
				return
			}
			switch ev.Type {
			case EvQuit:
				return
			case EvKey:
				if ui.Key(ev.Key, ev.Sym) {
					dirty = true
					// Enter/Escape in the settings modal can restore the
					// pre-modal window size.
					if cw, ch := win.windowSize(); cw != ui.W || ch != ui.H {
						win.Resize(ui.W, ui.H)
					}
				}
			case EvMouse:
				if ev.Pressed {
					pressW = ui.HitTest(ev.X, ev.Y)
					pressX, pressY = ev.X, ev.Y
					pressRootX, pressRootY = ev.RootX, ev.RootY
					dragging = false
					ui.Press(pressW)
				} else if dragging {
					// Release that ended a WM drag: not a click, so no
					// collapse toggle. (The WM consumes the real release;
					// this is the synthetic one from our UngrabPointer.)
					ui.press = WNone
				} else {
					if ui.Release(ui.HitTest(ev.X, ev.Y)) {
						// Header clicked: collapse/expand and resize the
						// window to match the new UI size.
						win.Resize(ui.W, ui.H)
					}
					if ui.WantClose() { // header close button clicked
						return
					}
					if ui.WantPet() { // header "Haiya!" button clicked
						onHaiya() // launch when teal, poof-out quit when pink
					}
					// Settings SAVE flipped DEMO MODE while a pet is
					// running: quit it now and relaunch the new mode as
					// soon as the old process is confirmed gone (see the
					// petGoneCh case), so the toggle takes effect at once
					// instead of waiting for the next manual Haiya! click.
					if ui.WantPetRestart() {
						log.Printf("pet: demo mode changed - restarting the pet")
						petRestartPending.Store(true)
						petQuitting.Store(true)
						QuitPet(petCmdPath, petGoneCh)
					}
					if ui.WantCopy() { // a message's COPY pill was clicked
						if err := win.SetClipboard(ui.TakeCopiedText()); err != nil {
							log.Printf("clipboard: %v", err)
						}
					}
				}
				dirty = true
			case EvMotion:
				if pressW == WHeader && !dragging {
					dx, dy := ev.X-pressX, ev.Y-pressY
					if dx*dx+dy*dy >= dragThreshold*dragThreshold {
						dragging = true
						win.StartMove(pressRootX, pressRootY)
					}
				}
				if wd := ui.HitTest(ev.X, ev.Y); wd != ui.hover {
					ui.SetHover(wd)
					switch wd {
					case WInput, WName:
						win.SetCursor(win.cursorText)
					case WButton, WHeader, WClose, WHaiya, WSettings, WAbout, WToggle,
						WAboutOK, WCopy,
						WDrop, WDropFrom, WDropTo, WMute, WOption, WSave, WCancel:
						win.SetCursor(win.cursorHand)
					default:
						win.SetCursor(win.cursorDefault)
					}
					dirty = true
				}
			case EvScroll:
				if !ui.ScrollHourList(ev.N) && !ui.ScrollMinuteList(ev.N) {
					ui.ScrollBy(ev.N * 40)
				}
				dirty = true
			case EvResize:
				if ev.W > 0 && ev.H > 0 && (ev.W != ui.W || ev.H != ui.H) {
					win.winW, win.winH = ev.W, ev.H
					ui.Resize(ev.W, ev.H)
					dirty = true
				}
			case EvExpose:
				dirty = true
			}
		case accumulated := <-ui.Stream:
			ui.SetStreamText(accumulated)
			dirty = true
		case reply := <-ui.Replies:
			ui.Thinking = false
			ui.streamText = ""    // drop the preview; AddMsg shows the final text
			if reply.Text != "" { // empty = skipped greeting (quiet hours)
				if reply.Image != nil {
					ui.AddMsgWithImage(ui.Bot.Name, reply.Text, reply.Image)
				} else {
					ui.AddMsg(ui.Bot.Name, reply.Text)
				}
			}
			// Pet bubble + TTS, kept in sync: the bubble appears only once the
			// audio is ready to play and closes as soon as playback ends. With
			// no audio (muted, no engine, or no pet running) the bubble shows
			// immediately and the pet dismisses it by its normal reading-time
			// duration.
			audioOn := !ui.Muted() && tts.Enabled()
			if reply.petPipe != "" && reply.petLine != "" {
				if audioOn {
					tts.SpeakLine(reply.Text,
						func() { petSayLine(reply.petPipe, reply.petLine) },
						func() { petClear(reply.petPipe) })
				} else {
					petSayLine(reply.petPipe, reply.petLine)
				}
			} else if audioOn {
				tts.SpeakLine(reply.Text, nil, nil) // speak aloud even without a pet
			}
			// Pet action/event command from [ACTION: ...] / [EVENT: ...]: acted
			// out on the sibling cmd-FIFO, independent of TTS/bubble timing.
			if reply.petCmdPipe != "" && reply.petCmdLine != "" {
				petCmd(reply.petCmdPipe, reply.petCmdLine)
			}
			dirty = true
		case <-caret.C:
			ui.caret = !ui.caret
			if ui.focused || (ui.settingsOpen && ui.nameFocused) {
				dirty = true
			}
		case <-busyTicker.C:
			if ui.updateBusyState() {
				dirty = true
			}
		case <-petTick.C:
			// Keep the Haiya! button honest: pink only while a pet is really
			// listening, so a pet that died on its own flips the button back
			// to teal (click relaunches) instead of sending quit into the void.
			if r := petRunning(petCmdPath); r != ui.PetRunning() {
				ui.SetPetRunning(r)
				if !r {
					petQuitting.Store(false)
				}
				dirty = true
			}
		case petGone := <-petGoneCh:
			// The graceful quit was confirmed (or escalated): drop the
			// running state so the button turns teal for the next launch.
			_ = petGone
			petQuitting.Store(false)
			ui.SetPetRunning(false)
			dirty = true
			// A quit we made only to pick up a demo-mode flip: bring the
			// pet straight back with the new -demo setting.
			if petRestartPending.CompareAndSwap(true, false) {
				if err := LaunchPet(ui.PetCharacter(), ui.PetDemo()); err != nil {
					log.Printf("pet: restart: %v", err)
				} else {
					ui.SetPetRunning(true)
					dirty = true
				}
			}
		}

		if dirty {
			win.DrawFrame(ui.Render())
			dirty = false
		}
	}
}
