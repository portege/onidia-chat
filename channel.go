// channel.go - external chat channels: the seam between the outside world and
// the onidia-chat brain.
//
// A Channel is a transport an external application uses to talk to the bot
// (Telegram today; Discord/Slack/a local HTTP POST /prompt tomorrow). The
// contract is tiny on purpose:
//
//	Channel interface { Name() string; Start(...) error; Stop() error }
//
// Channels do NOT own a brain: prompts are injected into the running X11
// window's own conversation (UI.SubmitExternal, same path as typing + SEND),
// so the desktop shows exactly what a remote user sent, and the rendered
// reply is forked back to the originating channel in main.go's reply handler.
//
// All UI mutation happens on the main/UI goroutine: the channel poller only
// posts Incoming values onto ui.externalIncoming; the main loop drains it and
// calls SubmitExternal. History therefore stays linear (one in-flight turn at
// a time, like the SEND button being disabled while Thinking).
//
// Contrast with agent/ : an agent is an ability the MODEL invokes ([AGENT:
// id ...] tag, one short process per run). A channel is how the WORLD invokes
// the model. The two meet inside Bot.Reply, which is why every installed agent
// works over Telegram with zero agent changes.
package main

import (
	"context"
	"log"
	"strings"
	"sync"
)

// channelGreeting is the pseudo-channel of the startup-greeting marker posted
// to UI.externalIncoming (never a real Channel; drained by the main loop to
// fire Bot.Greeting when idle).
const channelGreeting = "__greeting__"

// Incoming is one prompt arriving from an external channel.
type Incoming struct {
	Channel string // channel name, e.g. "telegram"
	ChatID  string // channel-side conversation id (Telegram chat id)
	User    string // channel-side sender label (username or id), for logs
	Text    string // the prompt text (already trimmed)
}

// externalOrigin tracks which channel chat a submitted turn came from, so the
// main-loop reply handler can fork the rendered reply back. Local (typed)
// turns queue nil, so the handler can tell them apart.
type externalOrigin struct {
	Channel string
	ChatID  string
	User    string
}

// Channel is the transport contract every external integration implements.
// Start runs the poller/listener until ctx is done or Stop is called; every
// accepted prompt is delivered by calling submit (safe from any goroutine -
// the UI serializes from there). Stop releases network resources; it is also
// called on process exit.
type Channel interface {
	Name() string
	Start(submit func(Incoming)) error
	Stop() error
}

// ---- registry ------------------------------------------------------------

// channelFactory builds a Channel from the resolved Config. Registered by
// init() in each channel file (telegram.go), mirroring agent.Register.
type channelFactory func(cfg *Config) (Channel, bool)

var (
	channelMu       sync.Mutex
	channelRegistry = map[string]channelFactory{}
)

// RegisterChannel adds a channel factory under name (e.g. "telegram").
// Registering the same name twice logs and keeps the first.
func RegisterChannel(name string, f channelFactory) {
	channelMu.Lock()
	defer channelMu.Unlock()
	if _, dup := channelRegistry[name]; dup {
		log.Printf("channels: duplicate registration for %q, keeping the first", name)
		return
	}
	channelRegistry[name] = f
}

// channelNames lists registered channels, sorted.
func channelNames() []string {
	channelMu.Lock()
	defer channelMu.Unlock()
	out := make([]string, 0, len(channelRegistry))
	for n := range channelRegistry {
		out = append(out, n)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// buildChannel constructs one registered channel by name.
func buildChannel(name string, cfg *Config) (Channel, bool) {
	channelMu.Lock()
	f, ok := channelRegistry[name]
	channelMu.Unlock()
	if !ok {
		return nil, false
	}
	return f(cfg)
}

// ---- external submit path (UI side) --------------------------------------

// SubmitExternal injects a remote prompt into the window's own conversation,
// exactly as if the user had typed it and pressed SEND: the "you" bubble is
// appended to UI.msgs, the "..." bubble shows, and Bot.Reply runs in the
// background with the result landing on u.Replies. Must be called on the UI
// goroutine (the main loop drains ui.externalIncoming for this).
//
// origin is queued in parallel with the reply so the main-loop reply handler
// can fork the rendered reply to the originating channel. The queue is FIFO:
// every Submit/SubmitExternal produces exactly one Replies result, so origins
// stay aligned even when local and remote turns interleave.
func (u *UI) SubmitExternal(text string, origin externalOrigin) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	u.externalPending = append(u.externalPending, &origin)
	u.AddMsg("you", text)
	hist := append([]Msg(nil), u.msgs...)
	u.Thinking = true
	u.streamText = ""
	u.scroll = u.maxScroll()
	bot := u.Bot
	go func() {
		result := bot.Reply(hist, text)
		u.Replies <- result
	}()
}

// takeExternalOrigin pops the origin queued for the next Replies result.
// Local turns queued nil, so the handler can tell them apart.
func (u *UI) takeExternalOrigin() *externalOrigin {
	if len(u.externalPending) == 0 {
		return nil
	}
	o := u.externalPending[0]
	u.externalPending = u.externalPending[1:]
	return o
}

// ---- main-loop helpers (called on the UI goroutine only) ------------------

// fireGreeting fires the startup hello: queues its nil origin (keeping the
// Replies/origin FIFO exact) and runs Bot.Greeting in the background like
// SubmitExternal does.
func fireGreeting(u *UI) {
	u.externalPending = append(u.externalPending, nil)
	u.Thinking = true
	u.streamText = ""
	u.scroll = u.maxScroll()
	bot := u.Bot
	go func() {
		result := bot.Greeting()
		u.Replies <- result
	}()
}

// submitIncoming injects a remote prompt and starts its typing indicator on
// the originating channel. Must be called on the UI goroutine when no turn
// is in flight (the drain + reply handler enforce this).
func submitIncoming(u *UI, in Incoming, channels []Channel) {
	origin := externalOrigin{Channel: in.Channel, ChatID: in.ChatID, User: in.User}
	u.SubmitExternal(in.Text, origin)
	for _, ch := range channels {
		if ch.Name() != in.Channel {
			continue
		}
		if tg, ok := ch.(*telegramChannel); ok {
			startTyping(tg, in.ChatID)
		}
	}
}

// resetConversation clears the shared window history back to a fresh hello,
// so /new from Telegram and the local window stay the same conversation.
func resetConversation(u *UI) {
	u.msgs = nil
	u.scroll = 0
	u.Thinking = false
	u.streamText = ""
	u.externalPending = nil
	u.AddMsg(u.Bot.Name, "fresh conversation - history cleared. ask me anything!")
}

// forkReplyToChannel delivers a rendered reply to the originating channel.
// Unknown channels are ignored (log-only): the window already shows the reply.
func forkReplyToChannel(channels []Channel, origin externalOrigin, reply ReplyResult) {
	for _, ch := range channels {
		if ch.Name() != origin.Channel {
			continue
		}
		if tg, ok := ch.(*telegramChannel); ok {
			stopTyping()
			ctx, cancel := context.WithTimeout(context.Background(), telegramHTTPTimeout)
			defer cancel()
			tg.deliverReply(ctx, origin.ChatID, reply.Text, reply.Image)
			return
		}
		log.Printf("channels: no sender for %q, reply stays in the window", origin.Channel)
	}
}

// telegramByName finds a running *telegramChannel by channel name.
func telegramByName(channels []Channel, name string) *telegramChannel {
	for _, ch := range channels {
		if ch.Name() != name {
			continue
		}
		if tg, ok := ch.(*telegramChannel); ok {
			return tg
		}
	}
	return nil
}

// ---- typing indicator ----------------------------------------------------

// typingCancel stops the current turn's typing loop. Package-level is safe:
// turns never overlap (remote/greeting held while Thinking; local overlap is
// nil-origin and starts no loop), and both ends run on the UI goroutine.
var typingCancel context.CancelFunc

// startTyping begins the "typing..." indicator for a chat, stopping any
// previous one first (defensive: normally none is running).
func startTyping(tg *telegramChannel, chatID string) {
	stopTyping()
	ctx, cancel := context.WithCancel(context.Background())
	typingCancel = cancel
	go tg.typingLoop(ctx, chatID)
}

// stopTyping ends the current typing indicator, if any.
func stopTyping() {
	if typingCancel != nil {
		typingCancel()
		typingCancel = nil
	}
}
