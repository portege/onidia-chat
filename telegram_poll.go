// telegram_poll.go - Bot API wire types + the getUpdates poller.
//
// Split from telegram.go so each file stays review-sized. Errors redact the
// token: failures surface the method + HTTP status + Telegram description,
// never the URL.
package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"
)

// ---- Bot API wire types (only the fields v1 uses) -------------------------

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramMessage struct {
	MessageID int64         `json:"message_id"`
	Chat      telegramChat  `json:"chat"`
	From      *telegramUser `json:"from"`
	Date      int64         `json:"date"`
	Text      string        `json:"text"`
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type telegramUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type telegramUpdatesResp struct {
	OK          bool             `json:"ok"`
	Description string           `json:"description"`
	Result      []telegramUpdate `json:"result"`
}

type telegramMeResp struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		Username string `json:"username"`
	} `json:"result"`
}

type telegramSendResp struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// ---- poller ---------------------------------------------------------------

// Start runs the long-poll loop until ctx is done or Stop is called. A stale
// webhook would starve getUpdates, so it is deleted first (harmless when none
// is set). Backoff is exponential with a cap; the offset advances only past
// fully-handled updates.
func (t *telegramChannel) Start(submit func(Incoming)) error {
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()
	defer close(t.done)
	defer cancel()

	if err := t.deleteWebhook(ctx); err != nil {
		log.Printf("telegram: deleteWebhook: %v", err)
	}
	if me, err := t.getMe(ctx); err != nil {
		log.Printf("telegram: getMe: %v (continuing)", err)
	} else {
		log.Printf("telegram: polling as @%s", me)
	}

	backoff := telegramRetryMin
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		updates, err := t.getUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("telegram: poll: %v (retry in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > telegramRetryMax {
				backoff = telegramRetryMax
			}
			continue
		}
		backoff = telegramRetryMin
		for _, u := range updates {
			t.setOffset(u.UpdateID + 1)
			t.handleUpdate(ctx, u, submit)
		}
	}
}

// Stop cancels the poller and waits for Start to return.
func (t *telegramChannel) Stop() error {
	t.mu.Lock()
	c := t.cancel
	t.mu.Unlock()
	if c != nil {
		c()
	}
	select {
	case <-t.done:
	case <-time.After(10 * time.Second):
	}
	return nil
}

func (t *telegramChannel) setOffset(o int64) {
	t.mu.Lock()
	if o > t.offset {
		t.offset = o
	}
	t.mu.Unlock()
}

// handleUpdate filters one update: text messages from allowlisted chats become
// Incoming prompts; /help answers inline; /new clears the shared history;
// everything else is silently skipped (no presence leak to strangers).
func (t *telegramChannel) handleUpdate(ctx context.Context, u telegramUpdate, submit func(Incoming)) {
	m := u.Message
	if m == nil || strings.TrimSpace(m.Text) == "" {
		return
	}
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	username := ""
	userID := ""
	if m.From != nil {
		username = m.From.Username
		userID = strconv.FormatInt(m.From.ID, 10)
	}
	sender := username
	if sender == "" {
		sender = userID
	}
	if !allowMatch(t.cfg.Allow, chatID, username) && !allowMatch(t.cfg.Allow, userID, username) {
		log.Printf("telegram: dropped message from non-allowlisted sender %s (chat %s)", sender, chatID)
		return
	}
	text := strings.TrimSpace(m.Text)
	switch text {
	case "/help":
		_ = t.sendText(ctx, chatID, telegramHelpText)
		return
	case "/new":
		submit(Incoming{Channel: "telegram", ChatID: chatID, User: sender, Text: "/new"})
		return
	}
	log.Printf("telegram: prompt from %s (chat %s, %d runes)", sender, chatID, len([]rune(text)))
	submit(Incoming{Channel: "telegram", ChatID: chatID, User: sender, Text: text})
}
