// telegram_test.go - token precedence + update filtering tests.
package main

import (
	"context"
	"strings"
	"testing"
)

func TestResolveTelegramPrecedence(t *testing.T) {
	cfg := &Config{TelegramToken: "ini-token", TelegramAllow: "1,@a"}
	t.Setenv("TELEGRAM_BOT_TOKEN", "env-token")
	got, src := resolveTelegramConfig(cfg, "flag-token", "")
	if got.Token != "flag-token" || src != "flag" {
		t.Fatalf("flag must win: token=%q src=%q", got.Token, src)
	}
	got, src = resolveTelegramConfig(cfg, "", "")
	if got.Token != "env-token" || src != "env" {
		t.Fatalf("env must beat ini: token=%q src=%q", got.Token, src)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	got, src = resolveTelegramConfig(cfg, "", "")
	if got.Token != "ini-token" || src != "ini" {
		t.Fatalf("ini must apply without flag/env: token=%q src=%q", got.Token, src)
	}
	if !got.SendImages {
		t.Error("send-images must default on")
	}
	if len(got.Allow) != 2 {
		t.Fatalf("allow = %q", got.Allow)
	}
}

func TestResolveTelegramDisabled(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	got, _ := resolveTelegramConfig(&Config{}, "", "")
	if got.enabled() {
		t.Fatal("empty token must mean disabled")
	}
}

func TestTelegramHandleUpdate(t *testing.T) {
	ch := newTelegramChannel(telegramConfig{
		Token: "x", Allow: parseAllowlist("100,@alice"), SendImages: true, PollSec: 30,
	})
	var submitted []Incoming
	submit := func(in Incoming) { submitted = append(submitted, in) }
	ctx := context.Background()

	msg := func(chatID int64, userID int64, username, text string) telegramUpdate {
		return telegramUpdate{UpdateID: 1, Message: &telegramMessage{
			MessageID: 1, Chat: telegramChat{ID: chatID},
			From: &telegramUser{ID: userID, Username: username},
			Text: text,
		}}
	}

	ch.handleUpdate(ctx, msg(100, 7, "alice", "hello"), submit)
	if len(submitted) != 1 || submitted[0].Text != "hello" {
		t.Fatalf("allowlisted prompt must submit: %+v", submitted)
	}
	ch.handleUpdate(ctx, msg(100, 7, "alice", "/help"), submit)
	if len(submitted) != 1 {
		t.Fatalf("/help must answer inline, not submit: %+v", submitted)
	}
	ch.handleUpdate(ctx, msg(999, 9, "mallory", "hello"), submit)
	if len(submitted) != 1 {
		t.Fatalf("stranger must be dropped silently: %+v", submitted)
	}
	ch.handleUpdate(ctx, msg(999, 9, "", ""), submit)
	if len(submitted) != 1 {
		t.Fatalf("empty text must be skipped: %+v", submitted)
	}
	ch.handleUpdate(ctx, telegramUpdate{UpdateID: 2}, submit)
	if len(submitted) != 1 {
		t.Fatalf("updateless message must be skipped: %+v", submitted)
	}
}

func TestTelegramNewCommand(t *testing.T) {
	ch := newTelegramChannel(telegramConfig{
		Token: "x", Allow: parseAllowlist("100"), SendImages: true, PollSec: 30,
	})
	var submitted []Incoming
	ch.handleUpdate(context.Background(), telegramUpdate{UpdateID: 3,
		Message: &telegramMessage{MessageID: 1, Chat: telegramChat{ID: 100},
			From: &telegramUser{ID: 100}, Text: "/new"}},
		func(in Incoming) { submitted = append(submitted, in) })
	if len(submitted) != 1 || submitted[0].Text != "/new" {
		t.Fatalf("/new must submit a reset marker: %+v", submitted)
	}
}

func TestShortTelegramErr(t *testing.T) {
	if got := shortTelegramErr([]byte(`{"ok":false,"description":"Forbidden: bot was blocked"}`)); !strings.Contains(got, "blocked") {
		t.Fatalf("must extract description: %q", got)
	}
	if got := shortTelegramErr([]byte("")); got == "" {
		t.Fatal("empty body needs a placeholder")
	}
	long := strings.Repeat("z", 500)
	if got := shortTelegramErr([]byte(long)); len(got) > 210 {
		t.Fatalf("must truncate hostile bodies (%d chars)", len(got))
	}
}

func TestSanitizeBotToken(t *testing.T) {
	cases := map[string]string{
		"1234567890:AABb_cdef-123":           "1234567890:AABb_cdef-123",
		"1234567890:AABb        # chmod 600": "1234567890:AABb",
		"  1234567890:AABb\n":                "1234567890:AABb",
		"# chmod 600 this file":              "",
		"":                                   "",
	}
	for in, want := range cases {
		if got := sanitizeBotToken(in); got != want {
			t.Errorf("sanitizeBotToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksLikeBotToken(t *testing.T) {
	valid := []string{
		"1234567890:AAElYOvXM-WElWghRVKNOlAFmjjimrUlmTw",
		"1:abc-DEF_123",
	}
	for _, tok := range valid {
		if !looksLikeBotToken(tok) {
			t.Errorf("looksLikeBotToken(%q) = false, want true", tok)
		}
	}
	invalid := []string{
		"", "flag-token", "env-token", "ini-token", "123", ":abc", "123:",
		"12:34:56", "123:abc def", "123:abc#comment", "# chmod 600", "123:abc; def",
	}
	for _, tok := range invalid {
		if looksLikeBotToken(tok) {
			t.Errorf("looksLikeBotToken(%q) = true, want false", tok)
		}
	}
}

// TestBuildChannelMalformedToken locks the fail-closed factory behaviour: a
// broken token must never construct a poller (it could only 404-retry
// forever), a well-formed one must.
func TestBuildChannelMalformedToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	if _, ok := buildChannel("telegram", &Config{TelegramToken: "no-colon-here"}); ok {
		t.Fatal("malformed token must not build a channel")
	}
	if _, ok := buildChannel("telegram", &Config{TelegramToken: "1234567890:AAbcdef"}); !ok {
		t.Fatal("well-formed token must build a channel")
	}
}
