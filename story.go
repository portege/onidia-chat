package main

// story.go - the built-in read_story agent: an AI-native ability that
// reuses this Bot's LLM provider to tell a story, delivered through the
// normal reply pipeline (chat bubble + pet bubble + TTS + pager - the
// story's paragraph newlines become bubble pages for free).
//
// It is NATIVE (not a downloaded folder) because it needs the provider
// credentials chat-app already holds - downloaded agents run as plain
// child processes and should never require handing them API keys.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"

	"github.com/portege/chat-app/agent"
)

// storySystem is the storyteller persona: plain text only, paragraphs
// split by single newlines (finishReply converts those to page breaks),
// no preamble - the same constraints as botPersona (bitmap font, pet TTS).
const storySystem = `You are a warm, imaginative storyteller. Reply with ONLY the story ` +
	`in plain text: no markdown, no emoji, no lists, no preamble like "Sure!" or ` +
	`"Once upon a time" disclaimers, and no closing remarks. Separate paragraphs ` +
	`with a single newline. Start directly with the first sentence of the story.`

// story is registered at startup (BEFORE discovery, so a downloaded
// read_story folder can never shadow this built-in); its bot is wired in
// main once ui.Bot has a provider.
var story = &storyAgent{}

type storyAgent struct {
	bot *Bot // nil until main wires ui.Bot (Run fails cleanly before that)
}

func (s *storyAgent) ID() string { return "read_story" }
func (s *storyAgent) Description() string {
	return "Tell an AI-generated story. Use when the user asks for a story, tale, or bedtime story."
}

func (s *storyAgent) Params() []agent.Param {
	return []agent.Param{
		{Name: "theme", Description: "what the story is about; omit for a surprise theme"},
		{Name: "length", Enum: []string{"short", "medium", "long"}, Default: "short",
			Description: "how long to read"},
	}
}

// lengthRule maps the length param to the word budget in the prompt.
var lengthRule = map[string]string{
	"short":  "under 150 words in 2-3 short paragraphs",
	"medium": "about 400 words in 4-6 paragraphs",
	"long":   "about 800 words in 8-10 paragraphs",
}

// storyThemes is the pool used when the model omits `theme`.
var storyThemes = []string{
	"a lighthouse keeper who befriends the sea",
	"a robot learning to paint",
	"a cat running a tiny bakery",
	"two planets who become pen pals",
	"a dragon who loses its shadow",
	"a courier delivering letters by balloon",
}

// Run generates the story through the bot's provider. The reply pipeline
// bounds provider calls with its own HTTP timeouts; the story text joins
// the reply as the agent message (see runAgentCall).
func (s *storyAgent) Run(_ context.Context, args map[string]string) (agent.Result, error) {
	if s.bot == nil || s.bot.Provider == nil {
		return agent.Result{}, errors.New("storyteller not ready: no AI provider configured (set provider + api-key in chat-app.ini)")
	}
	theme := strings.TrimSpace(args["theme"])
	if theme == "" {
		theme = storyThemes[rand.Intn(len(storyThemes))]
	}
	length := args["length"]
	rule, ok := lengthRule[length]
	if !ok {
		length, rule = "short", lengthRule["short"]
	}
	prompt := fmt.Sprintf("Tell a %s-length story (%s) about %s.",
		length, rule, theme)
	raw, err := s.bot.Provider.GenerateText(storySystem, nil, prompt)
	if err != nil {
		return agent.Result{}, fmt.Errorf("storyteller: %w", err)
	}
	// Defensive: drop any stray tag the model might emit anyway (the
	// storyteller prompt forbids them, but providers can surprise).
	_, _, _, _, _, text := stripTags(raw)
	if strings.TrimSpace(text) == "" {
		return agent.Result{}, errors.New("storyteller returned an empty story")
	}
	return agent.Result{Message: strings.TrimSpace(text)}, nil
}
