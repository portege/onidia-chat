// channel_filter.go - allowlist matching + Telegram-sized message chunking.
//
// Split from channel.go so each file stays review-sized. Both are package main
// (Bot/Msg/UI are not importable), and both are channel-agnostic: future
// channels reuse parseAllowlist/allowMatch/splitOutgoing unchanged.
package main

import (
	"strings"
)

// ---- allowlist -----------------------------------------------------------

// parseAllowlist splits a comma/space separated allow-from value into match
// entries. Entries may be numeric chat ids ("12345678") or usernames with or
// without a leading @ ("@alice", "alice"); matching is case-insensitive for
// names. An empty allowlist matches NOTHING (fail closed): enabling a channel
// without allow-from drops every message with a log line.
func parseAllowlist(s string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ReplaceAll(s, ",", " ")) {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		out = append(out, f)
	}
	return out
}

// allowMatch reports whether a sender (numeric id + username, either possibly
// empty) is on the allowlist. Unknown senders never match.
func allowMatch(list []string, id, username string) bool {
	id = strings.TrimSpace(id)
	username = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(username), "@"))
	if id == "" && username == "" {
		return false
	}
	for _, e := range list {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.HasPrefix(e, "@") || !isDigits(strings.TrimPrefix(e, "@")) {
			name := strings.TrimPrefix(strings.ToLower(e), "@")
			if username != "" && name == username {
				return true
			}
			continue
		}
		if id != "" && e == id {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---- message chunking ----------------------------------------------------

// Telegram sendMessage caps text at 4096 UTF-16 code units; photo captions at
// 1024. These helpers split on rune boundaries, preferring newlines.
//
// The pager convention (every \n is a page break in the X11 bubble) maps to
// separate Telegram messages: splitPages cuts \f-joined pages so each page
// goes out as its own message, preserving the multi-page reply shape.
const (
	telegramMsgLimit     = 4096
	telegramCaptionLimit = 1024
)

// splitOutgoing maps a finished reply onto sendable Telegram parts: one
// message per \f page (reusing the pager's own splitPages from ui.go), each
// page chunked to the sendMessage limit.
func splitOutgoing(text string) []string {
	var out []string
	for _, page := range splitPages(text) {
		out = append(out, chunkText(page, telegramMsgLimit)...)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// chunkText splits s into chunks of at most limit runes, preferring to cut at
// the last newline inside the window so paragraphs stay whole. A single
// over-long line is hard-cut.
func chunkText(s string, limit int) []string {
	if limit <= 0 {
		return []string{s}
	}
	var out []string
	r := []rune(s)
	for len(r) > 0 {
		if len(r) <= limit {
			// Tail: trimmed like every other chunk (a trailing newline
			// would render as a dangling blank line on Telegram).
			if tail := strings.TrimSpace(string(r)); tail != "" {
				out = append(out, tail)
			} else if len(out) == 0 {
				out = append(out, "")
			}
			break
		}
		cut := limit
		if i := lastNewlineWithin(r[:limit]); i >= 0 {
			cut = i + 1
		}
		part := strings.TrimSpace(string(r[:cut]))
		if part != "" {
			out = append(out, part)
		}
		r = r[cut:]
		for len(r) > 0 && (r[0] == '\n' || r[0] == '\r') {
			r = r[1:]
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func lastNewlineWithin(r []rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == '\n' {
			return i
		}
	}
	return -1
}

// splitCaption splits the lead text for a photo message: the caption holds up
// to telegramCaptionLimit, the rest follows as plain messages.
func splitCaption(text string) (caption string, rest []string) {
	r := []rune(text)
	if len(r) <= telegramCaptionLimit {
		return text, nil
	}
	cut := telegramCaptionLimit
	if i := lastNewlineWithin(r[:telegramCaptionLimit]); i >= 0 {
		cut = i + 1
	}
	caption = strings.TrimSpace(string(r[:cut]))
	tail := strings.TrimSpace(string(r[cut:]))
	if tail == "" {
		return caption, nil
	}
	return caption, splitOutgoing(tail)
}
