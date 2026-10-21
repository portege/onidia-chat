// telegram_send.go - Bot API calls (apiCall/getUpdates/getMe/send*) + reply
// delivery. Split from telegram.go so each file stays review-sized.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// apiCall POSTs form values to a Bot API method and decodes the envelope.
// The token never appears in the returned error (method name only).
func (t *telegramChannel) apiCall(ctx context.Context, method string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiURL(method), strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%s: %v", method, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.http.Do(req)
	if err != nil {
		return telegramRequestError(method, err, t.cfg.Token)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, telegramMaxPollBytes))
	if err != nil {
		return fmt.Errorf("%s: read: %v", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, shortTelegramErr(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("%s: decode: %v", method, err)
		}
	}
	return nil
}

func telegramRequestError(method string, err error, token string) error {
	message := err.Error()
	if token != "" {
		message = strings.ReplaceAll(message, token, "[REDACTED]")
	}
	return fmt.Errorf("%s: %s", method, message)
}

// shortTelegramErr extracts Telegram's description from an error body,
// truncated so a hostile body cannot flood the log.
func shortTelegramErr(body []byte) string {
	var e struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Description != "" {
		if len(e.Description) > 200 {
			return e.Description[:200] + "..."
		}
		return e.Description
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	if s == "" {
		return "empty error body"
	}
	return s
}

// getUpdates long-polls for new messages (server holds up to poll seconds).
func (t *telegramChannel) getUpdates(ctx context.Context) ([]telegramUpdate, error) {
	t.mu.Lock()
	offset := t.offset
	t.mu.Unlock()
	form := url.Values{}
	if offset > 0 {
		form.Set("offset", strconv.FormatInt(offset, 10))
	}
	form.Set("timeout", strconv.Itoa(t.cfg.PollSec))
	form.Set("allowed_updates", `["message"]`)
	var out telegramUpdatesResp
	if err := t.apiCall(ctx, "getUpdates", form, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("getUpdates: %s", out.Description)
	}
	return out.Result, nil
}

// getMe returns the bot username (log-only, verifies the token works).
func (t *telegramChannel) getMe(ctx context.Context) (string, error) {
	var out telegramMeResp
	if err := t.apiCall(ctx, "getMe", url.Values{}, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("getMe: %s", out.Description)
	}
	return out.Result.Username, nil
}

// deleteWebhook drops a stale webhook so getUpdates can receive.
func (t *telegramChannel) deleteWebhook(ctx context.Context) error {
	var out telegramSendResp
	form := url.Values{"drop_pending_updates": {"false"}}
	if err := t.apiCall(ctx, "deleteWebhook", form, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("deleteWebhook: %s", out.Description)
	}
	return nil
}

// sendText delivers one chunk (already size-checked by the caller).
func (t *telegramChannel) sendText(ctx context.Context, chatID, text string) error {
	form := url.Values{"chat_id": {chatID}, "text": {text}}
	var out telegramSendResp
	if err := t.apiCall(ctx, "sendMessage", form, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("sendMessage: %s", out.Description)
	}
	return nil
}
