// channel_test.go - allowlist + chunking + registry tests for the channel core.
package main

import (
	"strings"
	"testing"
)

func TestParseAllowlist(t *testing.T) {
	got := parseAllowlist("123,@alice, bob")
	if len(got) != 3 || got[0] != "123" || got[1] != "@alice" || got[2] != "bob" {
		t.Fatalf("parseAllowlist = %q", got)
	}
	if len(parseAllowlist("")) != 0 {
		t.Fatal("empty allowlist must parse to nothing (fail closed)")
	}
}

func TestAllowMatch(t *testing.T) {
	list := parseAllowlist("12345678,@alice")
	cases := []struct {
		id, user string
		want     bool
	}{
		{"12345678", "", true},    // numeric id match
		{"999", "alice", true},    // username match (id differs)
		{"999", "@alice", true},   // @-prefixed sender form
		{"999", "ALICE", true},    // case-insensitive name
		{"12345678", "bob", true}, // id wins even with other name
		{"999", "bob", false},     // stranger
		{"", "", false},           // unknown sender never matches
		{"999", "", false},        // id-only stranger
		{"", "bob", false},        // name-only stranger
	}
	for _, c := range cases {
		if got := allowMatch(list, c.id, c.user); got != c.want {
			t.Errorf("allowMatch(id=%q user=%q) = %v, want %v", c.id, c.user, got, c.want)
		}
	}
	if allowMatch(nil, "123", "alice") {
		t.Error("nil allowlist must match nothing (fail closed)")
	}
}

func TestAllowMatchNegativeChatID(t *testing.T) {
	if !allowMatch(parseAllowlist("-1001234567890"), "-1001234567890", "") {
		t.Fatal("negative numeric chat ID should match")
	}
	if allowMatch(parseAllowlist("-1001234567890"), "1001234567890", "") {
		t.Fatal("negative chat ID must not match its unsigned form")
	}
}

func TestSplitOutgoingPages(t *testing.T) {
	parts := splitOutgoing("page one\fpage two")
	if len(parts) != 2 || parts[0] != "page one" || parts[1] != "page two" {
		t.Fatalf("splitOutgoing = %q", parts)
	}
	// Blank input yields one empty part (caller skips sending empties).
	if parts := splitOutgoing("  \f  "); len(parts) != 1 || parts[0] != "" {
		t.Fatalf("blank splitOutgoing = %q", parts)
	}
}

func TestSplitOutgoingTrims(t *testing.T) {
	parts := splitOutgoing("  padded  ")
	if len(parts) != 1 || parts[0] != "padded" {
		t.Fatalf("splitOutgoing should reuse pager trimming: %q", parts)
	}
}

func TestChunkTextRespectsLimit(t *testing.T) {
	s := strings.Repeat("x", 9000)
	chunks := chunkText(s, telegramMsgLimit)
	if len(chunks) != 3 {
		t.Fatalf("9000 runes at 4096 limit = %d chunks, want 3", len(chunks))
	}
	for _, c := range chunks {
		if len([]rune(c)) > telegramMsgLimit {
			t.Fatalf("chunk of %d runes exceeds limit", len([]rune(c)))
		}
	}
	joined := strings.Join(chunks, "")
	if joined != s {
		t.Fatal("chunks must reassemble to the original")
	}
}

func TestChunkTextPrefersNewline(t *testing.T) {
	s := strings.Repeat("a\n", 3000) // 6000 runes, newline every 2
	chunks := chunkText(s, telegramMsgLimit)
	if len(chunks) < 2 {
		t.Fatalf("want >= 2 chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if strings.HasSuffix(c, "\n") {
			t.Errorf("chunk should be trimmed, got trailing newline")
		}
	}
}

func TestSplitCaption(t *testing.T) {
	short := "hello"
	cap, rest := splitCaption(short)
	if cap != short || len(rest) != 0 {
		t.Fatalf("short caption = %q rest = %q", cap, rest)
	}
	long := strings.Repeat("y", telegramCaptionLimit+100)
	cap, rest = splitCaption(long)
	if len([]rune(cap)) > telegramCaptionLimit {
		t.Fatalf("caption of %d runes exceeds limit", len([]rune(cap)))
	}
	if len(rest) == 0 {
		t.Fatal("overflow must produce follow-up messages")
	}
}

func TestMaskTelegramToken(t *testing.T) {
	if maskTelegramToken("") != "(none)" {
		t.Error("empty token mask")
	}
	m := maskTelegramToken("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11")
	if strings.Contains(m, "DEF1234") {
		t.Errorf("mask leaks the middle: %q", m)
	}
	if !strings.Contains(m, "1234") || !strings.Contains(m, "ew11") {
		t.Errorf("mask should keep head/tail: %q", m)
	}
}
