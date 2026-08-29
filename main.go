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
		if provider == "bedrock" {
			return defaultBedrockModelID, ""
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
		w       = flag.Int("w", 380, "initial window width")
		h       = flag.Int("h", 520, "initial window height")
		preview = flag.Bool("preview", false,
			"render chat_ui_*.png previews and exit (no display needed)")
		apiKey = flag.String("api-key", "",
			`Google Gemini API key (default: $GEMINI_API_KEY, $GOOGLE_API_KEY, or the built-in key; "off" = stub mode)`)
		model  = flag.String("model", "", "model ID (Gemini or Bedrock; default depends on -provider)")
		apiURL = flag.String("api-url", "",
			"Gemini endpoint base for relays/mirrors (default: "+defaultAPIURL+")")
		provider = flag.String("provider", "",
			`LLM backend: "gemini" (default) or "bedrock"`)
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
	case "gemini", "bedrock":
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
		urlVal = defaultAPIURL
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
	ui.Bot.ImageSource = imgSource
	ui.Bot.PixabayKey = pxKey
	ui.Bot.ForceImageKeyword = forceImg
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
	default: // gemini
		botProvider = &geminiProvider{apiKey: key, apiURL: urlVal, model: modelVal, http: &http.Client{Timeout: imageTimeout + geminiTimeout}}
	}

	ui.Bot.Provider = botProvider

	if sysPrompt != "" || (sysFile != "" && sysFile != "off") {
		log.Printf("system-instruction custom (from flag or config)")
	} else {
		log.Printf("system-instruction default persona")
	}
	if providerVal == "bedrock" {
		log.Printf("bedrock: model=%s", modelVal)
	} else if key == "" {
		log.Printf("gemini: no API key (set GEMINI_API_KEY or -api-key) - running in stub mode")
	} else {
		log.Printf("gemini: model=%s", modelVal)
	}
	log.Printf("images: source=%s", imgSource)
	if pipe == "" {
		log.Printf("pet: say-pipe forwarding disabled")
	} else {
		log.Printf("pet: replies go to %s", pipe)
	}

	dirty := true
	caret := time.NewTicker(530 * time.Millisecond)
	defer caret.Stop()

	log.Printf("ui ready - type in the textarea, press enter or SEND")
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
				}
			case EvMouse:
				if ev.Pressed {
					ui.Press(ui.HitTest(ev.X, ev.Y))
				} else {
					if ui.Release(ui.HitTest(ev.X, ev.Y)) {
						// Header clicked: collapse/expand and resize the
						// window to match the new UI size.
						win.Resize(ui.W, ui.H)
					}
					if ui.WantClose() { // header close button clicked
						return
					}
				}
				dirty = true
			case EvMotion:
				if wd := ui.HitTest(ev.X, ev.Y); wd != ui.hover {
					ui.SetHover(wd)
					switch wd {
					case WInput:
						win.SetCursor(win.cursorText)
					case WButton, WHeader, WClose:
						win.SetCursor(win.cursorHand)
					default:
						win.SetCursor(win.cursorDefault)
					}
					dirty = true
				}
			case EvScroll:
				ui.ScrollBy(ev.N * 40)
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
		case reply := <-ui.Replies:
			ui.Thinking = false
			if reply.Image != nil {
				ui.AddMsgWithImage(ui.Bot.Name, reply.Text, reply.Image)
			} else {
				ui.AddMsg(ui.Bot.Name, reply.Text)
			}
			dirty = true
		case <-caret.C:
			ui.caret = !ui.caret
			if ui.focused {
				dirty = true
			}
		}

		if dirty {
			win.DrawFrame(ui.Render())
			dirty = false
		}
	}
}
