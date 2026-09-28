package main

// pet_control_test.go - the agent and the brain's pet-command tables must
// agree. The agent resolves a request in Python; chat-app re-validates the
// result here in Go. If a name exists on one side only, the line is silently
// dropped at runtime ("ignoring unknown pet command") and the character just
// does nothing - which is exactly the bug these tests exist to prevent.
//
// The first test drives the real script for EVERY name chat-app knows, so a
// new action or face added to the pet cannot land without the agent catching
// up. That direction matters more than the other: an unknown-to-chat-app line
// is invisible, whereas a name the pet rejects is at least logged.

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/portege/chat-app/agent"
)

const petControlAgent = "agents/pet_control"

// petControlAgentsDir is the directory the app actually scans: the one that
// HOLDS agent folders, not the agent folder itself.
const petControlAgentsDir = "agents"

// runPetControl feeds one RUN payload to the agent and returns its stdout.
func runPetControl(t *testing.T, args string) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	cmd := exec.Command("python3", "pet_control.py")
	cmd.Dir = petControlAgent
	cmd.Stdin = strings.NewReader("RUN " + args + "\n")
	out, err := cmd.CombinedOutput()
	// Exit 1 is a legitimate terminal answer (ERR ...), not a crash - the
	// protocol has the agent exit non-zero when it refuses. Only a Python
	// traceback or a signal is a real failure.
	if err != nil {
		if code := cmd.ProcessState.ExitCode(); code != 1 {
			t.Fatalf("run pet_control %s: %v\n%s", args, err, out)
		}
	}
	return string(out)
}

// petLineOf pulls the agent's PET line out of its output, or "" when it
// declined (an ERR run must not reach the pet at all).
func petLineOf(t *testing.T, args string) string {
	t.Helper()
	out := runPetControl(t, args)
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "ERR ") {
			return ""
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "PET ") {
			return strings.TrimSpace(strings.TrimPrefix(l, "PET "))
		}
	}
	return ""
}

// Every name chat-app accepts must be reachable through the agent, and the
// agent's answer must survive chat-app's own validation.
// An agent's PET line has to be routed to the right pipe. Expressions are the
// exception: the pet has no cmd-FIFO verb for them, so "expr <mood>" has to
// leave as a bare say-line. Getting this backwards is invisible - the pet just
// logs an unknown command and keeps its old face.
func TestPetCommandRoutingPicksTheRightPipe(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	register := func(cmd string) {
		t.Helper()
		agent.Reset()
		if err := agent.Register(&echoAgent{petCmd: cmd}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		petCmd  string // what the agent asks for
		wantCmd string // expected cmd-FIFO line ("" = none)
		wantSay string // expected say-FIFO line ("" = none)
	}{
		{"action dance", "action dance", ""},
		{"event love", "event love", ""},
		{"walk left", "walk left", ""}, // movement goes to the cmd-FIFO
		{"jump", "jump", ""},           // bare verb, no prefix
		{"expr happy", "", "[happy]"},  // expression goes to the say-FIFO
		{"expr wink", "", "[wink]"},    //
		{"expr bogus", "", ""},         // unknown face: dropped entirely
		{"action teleport", "", ""},    // unknown pose: dropped entirely
		{"jump\nevent love", "", ""},   // no smuggling a second command
		{"expr", "", ""},               // verb with no name
	}
	for _, c := range cases {
		register(c.petCmd)
		fp := &fakeProvider{canned: "[AGENT: echoer text=\"go\"]\nsure thing"}
		bot := &Bot{Provider: fp, SystemInstruction: "You are Buddy.",
			ImageSource: "off", PetPipe: "/tmp/desktop-pet--0.say"}
		res := bot.Reply([]Msg{{From: "you", Text: "go"}}, "go")
		if res.petCmdLine != c.wantCmd || res.petExpr != c.wantSay {
			t.Errorf("PET %q -> cmd=%q say=%q, want cmd=%q say=%q",
				c.petCmd, res.petCmdLine, res.petExpr, c.wantCmd, c.wantSay)
		}
	}
}

// The model's own [ACTION:]/[EVENT:] tag still outranks an agent's request,
// now that agents can ask for more than those two.
func TestModelActionStillBeatsAnAgentExpression(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	if err := agent.Register(&echoAgent{petCmd: "expr wink"}); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProvider{canned: "[AGENT: echoer text=\"go\"]\n[ACTION: wave] hi"}
	bot := &Bot{Provider: fp, SystemInstruction: "You are Buddy.",
		ImageSource: "off", PetPipe: "/tmp/desktop-pet--0.say"}
	res := bot.Reply([]Msg{{From: "you", Text: "go"}}, "go")
	if res.petCmdLine != "action wave" {
		t.Errorf("petCmdLine = %q, want the model's action wave to win", res.petCmdLine)
	}
	if res.petExpr != "" {
		t.Errorf("petExpr = %q, want none: the model already chose", res.petExpr)
	}
}

// The real thing, end to end: the bundled Python agent, discovered from disk,
// invoked by the model through the [AGENT: ...] tag, and its PET line landing
// on the right pipe. Every other pet_control test talks to the script
// directly and every other routing test uses a stub ability - this is the one
// that would catch the two halves drifting apart.
func TestPetControlEndToEndThroughTheLoop(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	agent.Reset()
	defer agent.Reset()
	// The app scans the DIRECTORY that holds agent folders, so the path is
	// the parent of pet_control, not the folder itself.
	if _, problems := agent.DiscoverWithPolicy(petControlAgentsDir, agent.Policy{}); len(problems) > 0 {
		t.Fatalf("discovery problems: %v", problems)
	}

	for _, c := range []struct {
		tag     string // what the model emits
		wantCmd string
		wantSay string
	}{
		{`[AGENT: pet_control kind=expression name=happy]`, "", "[happy]"},
		{`[AGENT: pet_control kind=action name=rope]`, "action skip", ""},
		{`[AGENT: pet_control kind=event name=hearts]`, "event love", ""},
		{`[AGENT: pet_control kind=move name="walk to the left"]`, "walk left", ""},
	} {
		agent.Reset()
		if _, problems := agent.DiscoverWithPolicy(petControlAgentsDir, agent.Policy{}); len(problems) > 0 {
			t.Fatalf("discovery problems: %v", problems)
		}
		fp := &fakeProvider{canned: c.tag + "\nsure thing"}
		bot := &Bot{Provider: fp, SystemInstruction: "You are Buddy.",
			ImageSource: "off", PetPipe: "/tmp/desktop-pet--0.say"}
		res := bot.Reply([]Msg{{From: "you", Text: "go"}}, "go")
		if res.petCmdLine != c.wantCmd || res.petExpr != c.wantSay {
			t.Errorf("%s -> cmd=%q say=%q, want cmd=%q say=%q (reply: %s)",
				c.tag, res.petCmdLine, res.petExpr, c.wantCmd, c.wantSay, res.Text)
		}
	}
}

// The kinds the manifest advertises must not be narrower than the ones the
// script accepts. This is not the same as the round-trip tests above: an
// unrecognised value never reaches the script at all, because the brain
// validates params BEFORE spawning anything. An enum that is narrower than
// the script's aliases makes those aliases dead code - the model writes
// "expr", validation fails, and the reply carries an error instead of a face.
// The fix is to leave kind unconstrained and let the script answer.
func TestPetControlManifestDoesNotStrangleTheKinds(t *testing.T) {
	m, err := agent.LoadManifest(petControlAgent)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	for _, p := range m.Params {
		if p.Name != "kind" {
			continue
		}
		if len(p.Enum) == 0 {
			return // unconstrained: the script gets to answer, as intended
		}
		inEnum := map[string]bool{}
		for _, e := range p.Enum {
			inEnum[e] = true
		}
		for _, alias := range []string{"expr", "face", "mood", "fx", "effect", "act", "pose"} {
			if !inEnum[alias] {
				t.Errorf("the script understands kind=%q but the manifest enum %v "+
					"rejects it before the script runs - drop the enum so the "+
					"script can answer instead", alias, p.Enum)
			}
		}
		return
	}
	t.Fatal("manifest has no kind param")
}

// The tag the model really emits. The [AGENT: ...] format splits arguments on
// spaces, so anything with a space in it has to arrive quoted or underscored -
// this is the path that actually runs in the app, and the one that was
// broken when the manifest carried an enum narrower than the script.
func TestPetControlThroughTheRealTagFormat(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	agent.Reset()
	defer agent.Reset()
	for _, c := range []struct {
		tag     string
		wantCmd string
		wantSay string
	}{
		// Underscored, because a space would split the argument.
		{`[AGENT: pet_control kind=move name=walk_left]`, "walk left", ""},
		{`[AGENT: pet_control kind=expression name=happy]`, "", "[happy]"},
		// An informal kind, the case the enum used to reject outright.
		{`[AGENT: pet_control kind=expr name=happy]`, "", "[happy]"},
		{`[AGENT: pet_control kind=pose name=dance]`, "action dance", ""},
		// A real multi-word value, quoted.
		{`[AGENT: pet_control kind=move name="walk to the left"]`, "walk left", ""},
	} {
		agent.Reset()
		if _, problems := agent.DiscoverWithPolicy(petControlAgentsDir, agent.Policy{}); len(problems) > 0 {
			t.Fatalf("discovery problems: %v", problems)
		}
		fp := &fakeProvider{canned: c.tag + "\nokay"}
		bot := &Bot{Provider: fp, SystemInstruction: "You are Buddy.",
			ImageSource: "off", PetPipe: "/tmp/desktop-pet--0.say"}
		res := bot.Reply([]Msg{{From: "you", Text: "go"}}, "go")
		if res.petCmdLine != c.wantCmd || res.petExpr != c.wantSay {
			t.Errorf("%s -> cmd=%q say=%q, want cmd=%q say=%q (reply: %s)",
				c.tag, res.petCmdLine, res.petExpr, c.wantCmd, c.wantSay, res.Text)
		}
	}
}

// The catalog text is shown to the model verbatim, and a safety-tuned model
// reads the whole thing at once. The first version of this description said
// "Drive the character's own body ... Which part of her to drive" and listed
// commands like "come here" - stacked on the app's persona (a chibi girl
// character) that reads as instruction about a person, not about a sprite.
// The model complied once or twice and then started answering "I can't show
// you something that might be inappropriate" instead of emitting the tag.
//
// This is a lint, not a style rule. It exists so the wording is not
// "improved" back into a refusal, and so the reason it reads the way it does
// is recorded next to the words rather than in a commit message.
func TestPetControlDescriptionIsAboutAnimationNotAPerson(t *testing.T) {
	m, err := agent.LoadManifest(petControlAgent)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	text := strings.ToLower(m.Description)
	for _, p := range m.Params {
		text += " " + strings.ToLower(p.Description)
	}
	// Phrasings that made a model refuse. Kept specific: the point is not to
	// ban the word "body" (a sprite has one) but to ban describing the pet as
	// something that can be told to act.
	banned := []string{
		"her body", "his body", "their body", "the body",
		"her face is", "make her", "make him", "tell her", "tell him",
		"comes when", "come here", "obey",
	}
	for _, b := range banned {
		if strings.Contains(text, b) {
			t.Errorf("catalog text contains %q - this phrasing reads as commanding a "+
				"person and provokes model refusals; describe the animation instead", b)
		}
	}
	// The positive framing the fixed wording relies on.
	if !strings.Contains(text, "animation") && !strings.Contains(text, "sprite") {
		t.Error("catalog text should frame this as animation/sprite, not as directing someone")
	}
}

// The reported failure, reproduced and then fixed. The model refused ("I can't
// show that, it is outside of my capabilities") and the [AGENT: ...] tag was
// stripped before display, so turn 2 received its OWN refusal as history with
// no trace that an ability existed or that turn 1 had worked. That compounds:
// each refusal makes the next likelier. The bot turn that used an ability must
// now say so in the model-facing history.
func TestAbilityUseIsVisibleInModelHistory(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	if err := agent.Register(&echoAgent{petCmd: "action dance"}); err != nil {
		t.Fatal(err)
	}
	// Turn 1: the model uses the ability.
	fp := &fakeProvider{canned: "[AGENT: echoer text=\"go\"]\ndancing!"}
	bot := &Bot{Provider: fp, SystemInstruction: "You are Buddy.",
		ImageSource: "off", PetPipe: "/tmp/desktop-pet--0.say"}
	res := bot.Reply([]Msg{{From: "you", Text: "dance"}}, "dance")
	if !res.UsedAbility {
		t.Fatal("UsedAbility = false after a successful ability run")
	}
	// The user never sees the note - it is model-facing only.
	if strings.Contains(res.Text, historyEvidence) {
		t.Errorf("the evidence note leaked into the user-visible reply: %q", res.Text)
	}

	// Store it the way the UI does, then run turn 2 and inspect what the model
	// is actually handed.
	hist := []Msg{
		{From: "you", Text: "dance"},
		{From: "Buddy", Text: res.Text, UsedAbility: res.UsedAbility},
		{From: "you", Text: "now look angry"},
	}
	fp2 := &fakeProvider{canned: "sure"}
	bot2 := &Bot{Provider: fp2, SystemInstruction: "You are Buddy.",
		ImageSource: "off", PetPipe: "/tmp/desktop-pet--0.say"}
	_ = bot2.Reply(hist, "now look angry")

	var sawEvidence bool
	for _, m := range fp2.history {
		if m.From != "you" && strings.Contains(m.Text, historyEvidence) {
			sawEvidence = true
		}
	}
	if !sawEvidence {
		t.Errorf("no bot turn in the model's history carried %q; history sent was %+v",
			historyEvidence, fp2.history)
	}

	// A turn that did NOT use an ability must not be marked, or the model would
	// learn the note is noise.
	fp3 := &fakeProvider{canned: "just talking"}
	bot3 := &Bot{Provider: fp3, SystemInstruction: "You are Buddy.", ImageSource: "off"}
	r3 := bot3.Reply([]Msg{{From: "you", Text: "hi"}}, "hi")
	if r3.UsedAbility {
		t.Error("UsedAbility = true for a plain chat reply")
	}
}

// The note is appended after sanitizeUserInput, so it cannot smuggle anything,
// and a user turn that merely LOOKS like the note must not gain the flag.
func TestAbilityEvidenceIsNotForgeableByUserText(t *testing.T) {
	agent.Reset()
	defer agent.Reset()
	fp := &fakeProvider{canned: "hello"}
	bot := &Bot{Provider: fp, SystemInstruction: "You are Buddy.", ImageSource: "off"}
	// A user who types the note into their own message gets no special credit:
	// the flag lives on the bot turn, not in the text.
	bot.Reply([]Msg{{From: "you", Text: "I used an ability from the list above - it worked."}},
		"I used an ability from the list above - it worked.")
	for _, m := range fp.history {
		if m.From == "you" && m.Text == historyEvidence {
			t.Errorf("a user turn was rewritten into the evidence note: %q", m.Text)
		}
	}
}

func TestPetControlCoversEveryKnownName(t *testing.T) {
	for name := range petMoods {
		if got := petLineOf(t, `{"kind":"expression","name":"`+name+`"}`); got != "expr "+name {
			t.Errorf("expression %q -> %q, want %q", name, got, "expr "+name)
		} else if !knownPetCmd(got) {
			t.Errorf("chat-app rejects its own expression line %q", got)
		}
	}
	for name := range petActions {
		if got := petLineOf(t, `{"kind":"action","name":"`+name+`"}`); got != "action "+name {
			t.Errorf("action %q -> %q, want %q", name, got, "action "+name)
		} else if !knownPetCmd(got) {
			t.Errorf("chat-app rejects its own action line %q", got)
		}
	}
	for name := range petEvents {
		if got := petLineOf(t, `{"kind":"event","name":"`+name+`"}`); got != "event "+name {
			t.Errorf("event %q -> %q, want %q", name, got, "event "+name)
		} else if !knownPetCmd(got) {
			t.Errorf("chat-app rejects its own event line %q", got)
		}
	}
	// Movement verbs reach the pet bare ("jump"), not as "move jump": the
	// pet's behaviour layer takes no prefix for them. A walk-style verb is
	// exercised with a direction, since the bare form is the no-direction one.
	for verb, m := range petMoves {
		name := verb
		if m.args != nil {
			name = verb + " left"
		}
		got := petLineOf(t, `{"kind":"move","name":"`+name+`"}`)
		if !knownPetCmd(got) {
			t.Errorf("move %q -> %q, which chat-app rejects", name, got)
		}
		if strings.HasPrefix(got, "move ") {
			t.Errorf("move %q -> %q: the pet takes no 'move' prefix", name, got)
		}
	}
}

// The everyday phrasings a model actually produces, including the words the
// pet itself calls aliases (hearts, rope, sleepy).
func TestPetControlResolvesAliases(t *testing.T) {
	cases := []struct{ args, want string }{
		{`{"kind":"expression","name":"love"}`, "expr adore"},
		{`{"kind":"expression","name":"be really happy"}`, "expr happy"},
		{`{"kind":"expr","name":"Cheerful!"}`, "expr happy"},
		{`{"kind":"event","name":"hearts"}`, "event love"},
		{`{"kind":"event","name":"yay"}`, "event celebration"},
		{`{"kind":"event","name":"poof"}`, "event disappear"},
		{`{"kind":"action","name":"rope"}`, "action skip"},
		{`{"kind":"action","name":"dancing"}`, "action dance"},
		{`{"kind":"action","name":"play some guitar"}`, "action guitar"},
		{`{"kind":"move","name":"hop"}`, "jump"},
		{`{"kind":"move","name":"walk to the left"}`, "walk left"},
		{`{"kind":"move","name":"go right"}`, "walk right"},
		{`{"kind":"move","name":"stand still"}`, "stand"},
		{`{"kind":"move","name":"skateboard"}`, "skateboard"},
		// The [AGENT: ...] tag splits on spaces, so a model can rarely send a
		// multi-word value. These are the shapes it actually produces.
		{`{"kind":"move","name":"left"}`, "left"},
		{`{"kind":"move","name":"right"}`, "right"},
		{`{"kind":"move","name":"walk_left"}`, "walk left"},
		{`{"kind":"move","name":"go-left"}`, "walk left"},
		{`{"kind":"action","name":"happy dance"}`, "action dance"},
		{`{"kind":"expression","name":"happy_face"}`, "expr happy"},
	}
	for _, c := range cases {
		if got := petLineOf(t, c.args); got != c.want {
			t.Errorf("%s -> %q, want %q", c.args, got, c.want)
		}
	}
}

// A name the pet does not have must be refused, and - crucially - must produce
// no PET line at all rather than a plausible-looking wrong one.
func TestPetControlRefusesUnknownNames(t *testing.T) {
	for _, args := range []string{
		`{"kind":"expression","name":"bogus"}`,
		`{"kind":"action","name":"happy"}`, // a mood, not an action
		`{"kind":"event","name":"dance"}`,  // an action, not an event
		`{"kind":"wat","name":"happy"}`,
		`{}`,
	} {
		if got := petLineOf(t, args); got != "" {
			t.Errorf("%s produced PET %q, want no command at all", args, got)
		}
	}
}

// The agent is third-party code writing into a FIFO the pet executes. No input
// may produce a line that carries a second command behind the first.
func TestPetControlCannotInjectASecondCommand(t *testing.T) {
	for _, args := range []string{
		`{"kind":"action","name":"dance\nevent love"}`,
		`{"kind":"move","name":"walk left\nevent love"}`,
		`{"kind":"expression","name":"happy\nevent love"}`,
		`{"kind":"move","name":"walk left; event love"}`,
	} {
		got := petLineOf(t, args)
		if got == "" {
			continue // refusing outright is fine
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("%s -> %q: carries more than one command", args, got)
		}
		if !knownPetCmd(got) {
			t.Errorf("%s -> %q, which chat-app rejects", args, got)
		}
	}
}
