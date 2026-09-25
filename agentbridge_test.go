package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/portege/chat-app/agent"
)

// echoAgent is a native (in-process) agent for bridge tests.
type echoAgent struct {
	petCmd string
	got    map[string]string
}

func (e *echoAgent) ID() string { return "echoer" }
func (e *echoAgent) Description() string {
	return "Echoes the text back."
}
func (e *echoAgent) Params() []agent.Param {
	return []agent.Param{{Name: "text", Required: true}}
}
func (e *echoAgent) Run(_ context.Context, args map[string]string) (agent.Result, error) {
	e.got = args
	return agent.Result{Message: "echo: " + args["text"], PetCmd: e.petCmd}, nil
}

func TestStripTagsAgentPayload(t *testing.T) {
	cases := []struct {
		raw, wantText string
		wantAgent     []string
		wantMood      string
	}{
		{"[AGENT: echoer text=hi] done", "done", []string{"echoer text=hi"}, ""},
		{`[AGENT: echoer text="hi there"] ok`, "ok", []string{`echoer text="hi there"`}, ""},
		{"[happy] [AGENT: echoer text=hi] chained", "chained", []string{"echoer text=hi"}, "happy"},
		{"[AGENT: echoer text=hi]\nnew line", "new line", []string{"echoer text=hi"}, ""},
		{"no tag here", "no tag here", nil, ""},
		// Phase 2: every header-position tag is captured, in order.
		{"[AGENT: echoer text=a] [AGENT: echoer text=b] both", "both",
			[]string{"echoer text=a", "echoer text=b"}, ""},
		{"[AGENT: a x=1]\n[AGENT: b y=2]\ntwo", "two", []string{"a x=1", "b y=2"}, ""},
		// Mid-sentence: stripped from display but NOT dispatched.
		{"sneaky [AGENT: echoer text=x] end", "sneaky end", nil, ""},
	}
	for _, tc := range cases {
		mood, _, _, _, agent, text := stripTags(tc.raw)
		if !slices.Equal(agent, tc.wantAgent) || text != tc.wantText || mood != tc.wantMood {
			t.Errorf("stripTags(%q) = agent(%q) text(%q) mood(%q), want agent(%q) text(%q) mood(%q)",
				tc.raw, agent, text, mood, tc.wantAgent, tc.wantText, tc.wantMood)
		}
	}
}

func TestRunAgentCall(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	a := &echoAgent{}
	if err := agent.Register(a); err != nil {
		t.Fatal(err)
	}

	// Success: the run carries the validated args and the agent message.
	run := runAgentCall(context.Background(), `echoer text=hi`)
	if run.Err != nil || run.Msg != "echo: hi" || run.ID != "echoer" {
		t.Fatalf("runAgentCall = %+v, want echoer/echo: hi", run)
	}
	if a.got["text"] != "hi" {
		t.Errorf("agent got %v, want validated text=hi", a.got)
	}
	// Folding: message joins as its own paragraph, duplicates are skipped.
	if got := foldAgentRuns("on it", []agentRun{run}); got != "on it\necho: hi" {
		t.Errorf("foldAgentRuns = %q, want model text + agent message", got)
	}
	if got := foldAgentRuns("echo: hi", []agentRun{run}); got != "echo: hi" {
		t.Errorf("foldAgentRuns = %q, want no duplicate line", got)
	}
	if got := foldAgentRuns("", []agentRun{run}); got != "echo: hi" {
		t.Errorf("foldAgentRuns = %q, want the message alone", got)
	}

	// Unknown agent: error surfaced (visible, not swallowed) as a run error.
	run = runAgentCall(context.Background(), `no_such_agent x=1`)
	if run.Err == nil || !strings.Contains(run.Err.Error(), "unknown agent") {
		t.Errorf("run.Err = %v, want unknown-agent error", run.Err)
	}
	if got := foldAgentRuns("sure thing", []agentRun{run}); !strings.Contains(got, "sure thing") ||
		!strings.Contains(got, "unknown agent") {
		t.Errorf("foldAgentRuns = %q, want model text + unknown-agent error", got)
	}

	// Malformed tag: same visible-error path.
	run = runAgentCall(context.Background(), `echoer novalue`)
	if run.Err == nil || !strings.Contains(run.Err.Error(), "bad parameter") {
		t.Errorf("run.Err = %v, want parse error surfaced", run.Err)
	}
}

func TestReplyRunsAgentEndToEnd(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	a := &echoAgent{petCmd: "action dance"}
	if err := agent.Register(a); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProvider{canned: "[AGENT: echoer text=\"spin it\"]\n[happy] here we go"}
	bot := &Bot{Provider: fp, SystemInstruction: "You are Buddy.", ImageSource: "off",
		PetPipe: "/tmp/desktop-pet--0.say"}
	res := bot.Reply([]Msg{{From: "you", Text: "go"}}, "go")

	if !strings.Contains(res.Text, "echo: spin it") {
		t.Errorf("reply text = %q, want the agent message folded in", res.Text)
	}
	if !strings.Contains(res.Text, "here we go") {
		t.Errorf("reply text = %q, want the model's own text kept", res.Text)
	}
	if strings.Contains(res.Text, "AGENT:") {
		t.Errorf("reply text still contains the tag: %q", res.Text)
	}
	// Agent PetCmd surfaces when the model sent no [ACTION:]/[EVENT:].
	if res.petCmdLine != "action dance" {
		t.Errorf("petCmdLine = %q, want action dance from the agent", res.petCmdLine)
	}
	// The system prompt advertises the registered agent.
	if !strings.Contains(fp.system, "echoer") || !strings.Contains(fp.system, "[AGENT:") {
		t.Errorf("system prompt lacks the agent catalog:\n%s", fp.system)
	}
}

func TestEffectiveSystemAgentCatalog(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	// No agents: no catalog block.
	if got := effectiveSystem("", "off"); strings.Contains(got, "[AGENT:") {
		t.Errorf("no agents but catalog present:\n%s", got)
	}
	if err := agent.Register(&echoAgent{}); err != nil {
		t.Fatal(err)
	}
	got := effectiveSystem("", "off")
	if !strings.Contains(got, "[AGENT:") || !strings.Contains(got, "echoer") {
		t.Errorf("catalog missing from system prompt:\n%s", got)
	}
	// Custom persona that already defines the convention: left alone.
	custom := "I define my own [AGENT: ...] rules."
	if got := effectiveSystem(custom, "off"); strings.Contains(got, "echoer") {
		t.Errorf("custom persona should not gain the catalog:\n%s", got)
	}
}
