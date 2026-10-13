// telegram.go - the Telegram Channel: remote control for onidia-chat.
//
// A Telegram DM becomes a prompt in the running X11 window's own conversation
// (same bubbles, same TTS, same buddy FIFOs), and the rendered reply is sent
// back as Telegram message(s) + an optional photo. Agents work unchanged:
// they run inside Bot.Reply, which this channel drives like the SEND button.
//
// Transport is stdlib-only long-poll (getUpdates?timeout=30): one goroutine
// blocked in an HTTPS read, ~0% CPU idle, no public URL/TLS/port-forward.
// Mutually exclusive with webhooks per bot token: start calls deleteWebhook
// so a leftover webhook cannot starve the poller.
//
// Security: token precedence flag > env TELEGRAM_BOT_TOKEN > INI bot-token;
// the token is NEVER logged (maskTelegramToken). allow-from is fail-closed:
// empty drops everything with a log line (no presence leak to strangers).
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Telegram tuning: poll timeout (server hold), HTTP timeout (margin above
// it), retry backoff after a failed poll, typing-action refresh while the
// brain works.
const (
	telegramAPI            = "https://api.telegram.org"
	telegramPollTimeoutSec = 30
	telegramHTTPTimeout    = 45 * time.Second
	telegramRetryMin       = 2 * time.Second
	telegramRetryMax       = 60 * time.Second
	telegramTypingEvery    = 5 * time.Second
	telegramMaxPollBytes   = 4 << 20 // 4 MiB cap per getUpdates body
)

// telegram throat-clearing commands (v1, prompt-only + photos).
const (
	telegramHelpText = "Send any message as a prompt - it appears in the onidia-chat window too.\n" +
		"/new - start a fresh conversation (clears shared history)\n" +
		"/help - this help"
)

// telegramConfig is the resolved [telegram] section + flag/env overrides.
type telegramConfig struct {
	Token      string
	Allow      []string
	SendImages bool
	PollSec    int
}

func (c telegramConfig) enabled() bool { return strings.TrimSpace(c.Token) != "" }

// resolveTelegramConfig applies flag > env > INI precedence. iniSrc reports
// where the token came from for the 0600-permission warning ("ini").
func resolveTelegramConfig(cfg *Config, flagToken, flagAllow string) (telegramConfig, string) {
	out := telegramConfig{SendImages: true, PollSec: telegramPollTimeoutSec}
	src := ""
	if cfg != nil {
		out.Token = strings.TrimSpace(cfg.TelegramToken)
		if out.Token != "" {
			src = "ini"
		}
		out.Allow = parseAllowlist(cfg.TelegramAllow)
		// send-images defaults ON; only an explicitly present INI key can
		// override it (absent = default on, same rule as Stream/StreamSet).
		if cfg.TelegramImagesSet {
			out.SendImages = cfg.TelegramImages
		}
		if cfg.TelegramPoll > 0 {
			out.PollSec = cfg.TelegramPoll
		}
	}
	if env := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")); env != "" {
		out.Token = env
		src = "env"
	}
	if strings.TrimSpace(flagToken) != "" {
		out.Token = strings.TrimSpace(flagToken)
		src = "flag"
	}
	if strings.TrimSpace(flagAllow) != "" {
		out.Allow = parseAllowlist(flagAllow)
	}
	if out.PollSec <= 0 || out.PollSec > 120 {
		out.PollSec = telegramPollTimeoutSec
	}
	return out, src
}

// maskTelegramToken renders a token for logs without leaking it.
func maskTelegramToken(t string) string {
	if t == "" {
		return "(none)"
	}
	if len(t) <= 8 {
		return "**** (len " + strconv.Itoa(len(t)) + ")"
	}
	return t[:4] + "..." + t[len(t)-4:] + fmt.Sprintf(" (len %d)", len(t))
}

// init registers the telegram channel factory.
func init() {
	RegisterChannel("telegram", func(cfg *Config) (Channel, bool) {
		tc, _ := resolveTelegramConfig(cfg, "", "")
		if !tc.enabled() {
			return nil, false
		}
		return newTelegramChannel(tc), true
	})
}

// telegramChannel is the Channel implementation. All network I/O uses plain
// net/http; state (offset) is guarded by mu because Start/Stop race on exit.
type telegramChannel struct {
	cfg    telegramConfig
	http   *http.Client
	mu     sync.Mutex
	offset int64
	cancel context.CancelFunc
	done   chan struct{}
}

func newTelegramChannel(tc telegramConfig) *telegramChannel {
	return &telegramChannel{
		cfg:  tc,
		http: &http.Client{Timeout: telegramHTTPTimeout},
		done: make(chan struct{}),
	}
}

func (t *telegramChannel) Name() string { return "telegram" }

// apiURL builds a Bot API method URL. The token is part of the path by
// Telegram's design; it never appears in logs (all errors redact it).
func (t *telegramChannel) apiURL(method string) string {
	return telegramAPI + "/bot" + t.cfg.Token + "/" + method
}
