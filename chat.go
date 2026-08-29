package main

// chat.go - the chatbot brain: Google Gemini or Amazon Bedrock.
//
// Bot.Reply sends the conversation to the configured LLM provider and returns
// the model's answer. Answers are also forwarded to the desktop-pet's say-pipe
// (see pet.go) so Onidia speaks them, using a leading [mood] tag the model is
// asked to emit. When images are enabled, the model can also emit an [IMG: ...]
// tag; the app fetches or generates the picture, shows it above the bot's
// bubble, and hands it to the pet bridge so the same picture shows up in
// Onidia's speech bubble. Without a provider configured the bot falls back to
// a stub so the UI keeps working offline.

import (
	"fmt"
	"image"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	// defaultAPIKey is baked in so the app works out of the box on this
	// machine. It is a free-tier key: prefer $GEMINI_API_KEY for anything
	// shared, and rotate it if it ever leaks.
	defaultAPIKey = "AIzaSyB7YR3ypNW2A-raPItTfLir-B-vKuuzyR8"

	// defaultAPIURL is the official Gemini endpoint. If your ISP or region
	// blocks it (TLS connects but responses stall - see README), override
	// with -api-url pointing at any compatible relay.
	defaultAPIURL = "https://generativelanguage.googleapis.com"

	geminiTimeout = 45 * time.Second
	maxHistTurns  = 20 // conversation turns sent as context
)

// botPersona shapes every answer: short (the pet bubble is small), playful,
// no markdown/emoji (the bitmap font cannot draw them), and an optional
// leading [mood] tag that drives the pet's facial expression.
const botPersona = `You are Buddy, a tiny cheerful chat companion living in a chat window on a ` +
	`Linux desktop, friends with Onidia, a desktop-pet chibi girl. ` +
	`Keep every reply SHORT and playful: 1-3 plain sentences, no markdown, no emoji, no lists ` +
	`(the app draws text with a tiny bitmap font). ` +
	`If an emotion fits the answer, START it with exactly one mood tag from ` +
	`[happy] [wink] [sad] [thinking] [anxious] [angry] [surprised] [sleepy]; ` +
	`the tag is stripped before display.`

// defaultModel: 3.7-flash currently answers with 503 "high demand" on this
// key, and pre-3.6 models are deprecated on generateContent - 3.6 flash
// answers reliably. Override with -model (see `geminitest -models`).
const defaultModel = "gemini-3.6-flash"

// imageTagInstruction is appended to the system prompt whenever image replies
// are enabled. It asks the model to emit an [IMG: ...] tag when a picture would
// help the answer; the tag is parsed out and either fetched from Pixabay
// (pixabay, default) or Wikipedia (wiki), or generated via Gemini (gemini).
const imageTagInstruction = ` When your answer would benefit from an image (place, landmark, animal, famous person, object, food, etc.), start your reply with "[IMG: <short visual description>]" on its own line. If you also want to express an emotion, put the image tag first, then the mood tag. All tags are stripped before display.`

// wikiImageTagInstruction is used when image-source=wiki, so the model emits a
// concise Wikipedia article title that survives URL encoding and is likely to
// match a real page. Descriptive phrases ("sunny beach with palm trees") 404.
const wikiImageTagInstruction = ` When your answer would benefit from an image (place, landmark, animal, famous person, object, food, etc.), start your reply with "[IMG: <concise Wikipedia article title>]" on its own line. Pick a single, well-known article name (e.g. "Bali", "Eiffel Tower", "Capybara"), not a descriptive sentence. If you also want to express an emotion, put the image tag first, then the mood tag. All tags are stripped before display.`

// Bot answers user messages via the configured LLM provider.
type Bot struct {
	Name              string
	APIKey            string   // Gemini API key (used for Gemini text + image generation)
	PixabayKey        string   // Pixabay API key (used when ImageSource is "pixabay")
	Model             string   // provider-specific model ID
	APIURL            string   // Gemini endpoint base
	PetPipe           string   // desktop-pet say FIFO; empty disables forwarding
	SystemInstruction string   // system prompt sent to every model
	ImageSource       string   // "pixabay" | "wiki" | "gemini" | "off"
	ForceImageKeyword string   // if set, always fetch/generate an image for this keyword
	Provider          Provider // the active LLM backend (nil = offline stub)
	HTTP              *http.Client
}

func NewBot() *Bot {
	return &Bot{
		Name:   "bot",
		Model:  defaultModel,
		APIURL: defaultAPIURL,
		HTTP:   &http.Client{Timeout: imageTimeout + geminiTimeout},
	}
}

// ReplyResult is what Bot.Reply returns: text plus an optional image.
type ReplyResult struct {
	Text  string
	Image image.Image
}

// resolveSystemPrompt merges a -system-prompt override and a -system-file
// (loaded from disk). Precedence: explicit -system-prompt wins; otherwise
// -system-file is read; otherwise the built-in botPersona is used.
func resolveSystemPrompt(override, file string) string {
	override = strings.TrimSpace(override)
	if override != "" {
		return override
	}
	file = strings.TrimSpace(file)
	if file != "" && file != "off" {
		b, err := os.ReadFile(file)
		if err != nil {
			log.Printf("system-instruction: failed to read %s: %v - using default persona", file, err)
		} else if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	return botPersona
}

// httpStatusError marks a completed HTTP exchange with a non-200 code; the
// retry logic uses it to avoid retrying deterministic 4xx answers.
type httpStatusError struct {
	code int
	msg  string
}

func (e *httpStatusError) Error() string {
	if e.msg != "" {
		return fmt.Sprintf("http %d: %s", e.code, e.msg)
	}
	return fmt.Sprintf("http %d", e.code)
}

// Gemini wire types (only the fields we use).
type geminiPart struct {
	Text       string `json:"text,omitempty"`
	Thought    bool   `json:"thought,omitempty"` // reasoning parts: never display
	InlineData *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"` // "user" | "model"
	Parts []geminiPart `json:"parts"`
}

type generationConfig struct {
	ResponseModalities []string `json:"responseModalities,omitempty"`
}

type geminiRequest struct {
	Contents          []geminiContent   `json:"contents"`
	SystemInstruction *geminiContent    `json:"systemInstruction,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

// moodTag matches a leading "[happy] " style tag.
var moodTag = regexp.MustCompile(`^\[([a-z]+)\]\s*`)

// petMoods are the tags the pet understands (see desktop-pet docs).
var petMoods = map[string]bool{
	"happy": true, "wink": true, "sad": true, "thinking": true,
	"anxious": true, "angry": true, "surprised": true, "sleepy": true,
}

// imgTag matches a leading "[IMG: ...] " tag emitted by the model when it
// decides a picture would help the answer.
var imgTag = regexp.MustCompile(`^\[IMG:\s*([^\]\n]+)\]\s*`)

// Reply produces the assistant answer for one user message. history holds
// the conversation so far, INCLUDING the new user message. The answer is
// also forwarded to the desktop-pet's say-pipe with its mood tag intact.
// If the model emits an [IMG: ...] tag and images are enabled, the picture is
// fetched from Pixabay (image-source=pixabay, default) or Wikipedia
// (image-source=wiki), or generated via Gemini (image-source=gemini), and
// returned alongside the text.
func (b *Bot) Reply(history []Msg, userText string) ReplyResult {
	if b.Provider == nil {
		return ReplyResult{Text: fmt.Sprintf("you said: %s -- wire in a provider (gemini or bedrock) to wake me up!", userText)}
	}

	rawReply, err := b.Provider.GenerateText(effectiveSystem(b.SystemInstruction, b.ImageSource), history, userText)
	if err != nil {
		return ReplyResult{Text: fmt.Sprintf("ouch - %s call failed: %v", b.Provider.Name(), err)}
	}

	// Split off the mood and image tags: chat shows bare text, the pet gets
	// the mood, and the image tag drives the picture (if enabled).
	mood, imgDesc, text := stripTags(rawReply)
	if b.ForceImageKeyword != "" {
		imgDesc = b.ForceImageKeyword
	}

	var img image.Image
	if imgDesc != "" && b.ImageSource != "off" {
		switch b.ImageSource {
		case "gemini":
			var err error
			img, err = b.GenerateImage(imgDesc)
			if err != nil {
				log.Printf("image generation for %q: %v", imgDesc, err)
			}
		case "pixabay":
			imgResult := FetchPixabayImage(CleanKeyword(imgDesc), b.PixabayKey, b.HTTP)
			if imgResult.Err != nil {
				log.Printf("pixabay image fetch for %q: %v", imgDesc, imgResult.Err)
			} else {
				img = imgResult.Image
			}
		default: // "wiki"
			imgResult := FetchImage(CleanKeyword(imgDesc), b.HTTP)
			if imgResult.Err != nil {
				log.Printf("image fetch for %q: %v", imgDesc, imgResult.Err)
			} else {
				img = imgResult.Image
			}
		}
	}

	petSay(b.PetPipe, mood, text, img)
	return ReplyResult{Text: text, Image: img}
}

// effectiveSystem returns the system prompt to send for a chat reply. When
// images are enabled it appends the image-tag instruction, unless the prompt
// already contains one (custom prompts may define their own convention).
func effectiveSystem(base, imageSource string) string {
	if base == "" {
		base = botPersona
	}
	if imageSource == "off" || strings.Contains(base, "[IMG:") {
		return base
	}
	if imageSource == "wiki" {
		return base + wikiImageTagInstruction
	}
	return base + imageTagInstruction
}

// stripTags extracts an optional leading mood tag and an optional leading
// [IMG: ...] tag from the model reply. The remaining text is returned clean.
func stripTags(raw string) (mood, imgDesc, text string) {
	text = strings.TrimSpace(raw)
	for {
		if m := moodTag.FindStringSubmatch(text); m != nil {
			if petMoods[m[1]] && mood == "" {
				mood = m[1]
			}
			text = strings.TrimSpace(text[len(m[0]):])
			continue
		}
		if m := imgTag.FindStringSubmatch(text); m != nil && imgDesc == "" {
			imgDesc = strings.TrimSpace(m[1])
			text = strings.TrimSpace(text[len(m[0]):])
			continue
		}
		break
	}
	return
}
