package main

// story_test.go - the native read_story agent: prompt shaping, tag
// stripping, provider-less errors, catalog, and the full Reply pipeline.

import (
	"context"
	"strings"
	"testing"

	"github.com/portege/chat-app/agent"
)

func TestStoryAgentRun(t *testing.T) {
	fp := &fakeProvider{canned: "The lighthouse blinked twice.\n\nA ship answered."}
	s := &storyAgent{bot: &Bot{Provider: fp}}
	res, err := s.Run(context.Background(),
		map[string]string{"theme": "a lighthouse", "length": "medium"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Message, "lighthouse blinked") {
		t.Errorf("Message = %q, want the story passthrough", res.Message)
	}
	if !strings.Contains(fp.instant, "a lighthouse") {
		t.Errorf("prompt lacks the theme: %q", fp.instant)
	}
	if !strings.Contains(fp.instant, "400 words") {
		t.Errorf("prompt lacks the medium length budget: %q", fp.instant)
	}
	if !strings.Contains(fp.system, "storyteller") {
		t.Errorf("system prompt is not the storyteller persona: %q", fp.system)
	}
}

func TestStoryAgentStripsStrayTags(t *testing.T) {
	fp := &fakeProvider{canned: "[happy] Once upon a time. "}
	s := &storyAgent{bot: &Bot{Provider: fp}}
	res, err := s.Run(context.Background(), map[string]string{"length": "short"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Message != "Once upon a time." {
		t.Errorf("Message = %q, want the stray tag stripped", res.Message)
	}
}

func TestStoryAgentErrors(t *testing.T) {
	// No provider wired (startup race or agents used headless).
	if _, err := (&storyAgent{}).Run(context.Background(), nil); err == nil ||
		!strings.Contains(err.Error(), "not ready") {
		t.Errorf("nil bot error = %v, want 'not ready'", err)
	}
	// Provider returns nothing usable.
	fp := &fakeProvider{canned: "   [happy]  "}
	if _, err := (&storyAgent{bot: &Bot{Provider: fp}}).Run(context.Background(), nil); err == nil ||
		!strings.Contains(err.Error(), "empty story") {
		t.Errorf("empty reply error = %v, want 'empty story'", err)
	}
}

func TestStoryInCatalog(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	if err := agent.Register(story); err != nil {
		t.Fatalf("register: %v", err)
	}
	cat := agent.CatalogInstruction()
	for _, want := range []string{"[AGENT:", "read_story", "theme", "short|medium|long"} {
		if !strings.Contains(cat, want) {
			t.Errorf("catalog missing %q:\n%s", want, cat)
		}
	}
	// A second registration (e.g. a downloaded read_story) must not shadow
	// the built-in - first wins.
	if err := agent.Register(story); err == nil {
		t.Error("re-registering read_story should fail (built-in wins)")
	}
}

// TestReplyRunsStoryAgentEndToEnd: model emits [AGENT: read_story ...],
// the native agent generates the story, it joins the reply as pages.
func TestReplyRunsStoryAgentEndToEnd(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	s := &storyAgent{bot: &Bot{Provider: &fakeProvider{canned: "A ship sailed.\n\nThe end."}}}
	if err := agent.Register(s); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProvider{canned: "[AGENT: read_story theme=ships] once upon"}
	bot := &Bot{Provider: fp, SystemInstruction: "You are Buddy.", ImageSource: "off"}
	res := bot.Reply([]Msg{{From: "you", Text: "tell me a story"}}, "tell me a story")

	if !strings.Contains(res.Text, "A ship sailed.") {
		t.Errorf("reply text = %q, want the generated story folded in", res.Text)
	}
	if !strings.Contains(res.Text, "once upon") {
		t.Errorf("reply text = %q, want the model's lead-in kept", res.Text)
	}
	if strings.Contains(res.Text, "AGENT:") {
		t.Errorf("reply text still contains the tag: %q", res.Text)
	}
	// The story's paragraph split became a page break for the pager.
	if !strings.Contains(res.Text, "\f") {
		t.Errorf("reply text = %q, want page breaks between paragraphs", res.Text)
	}
}
