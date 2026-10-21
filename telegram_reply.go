// telegram_reply.go - sendPhoto, reply delivery, typing indicator.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"
)

// sendChatAction sends the "typing..." indicator.
func (t *telegramChannel) sendChatAction(ctx context.Context, chatID, action string) error {
	form := url.Values{"chat_id": {chatID}, "action": {action}}
	var out telegramSendResp
	if err := t.apiCall(ctx, "sendChatAction", form, &out); err != nil {
		return err
	}
	return nil
}

// sendPhoto uploads a PNG with an optional caption (already size-checked).
func (t *telegramChannel) sendPhoto(ctx context.Context, chatID string, img image.Image, caption string) error {
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return fmt.Errorf("sendPhoto: encode: %v", err)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("photo", "reply.png")
	if err != nil {
		return fmt.Errorf("sendPhoto: %v", err)
	}
	if _, err := io.Copy(fw, &pngBuf); err != nil {
		return fmt.Errorf("sendPhoto: %v", err)
	}
	_ = w.WriteField("chat_id", chatID)
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("sendPhoto: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiURL("sendPhoto"), &body)
	if err != nil {
		return fmt.Errorf("sendPhoto: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := t.http.Do(req)
	if err != nil {
		return telegramRequestError("sendPhoto", err, t.cfg.Token)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("sendPhoto: read: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sendPhoto: HTTP %d: %s", resp.StatusCode, shortTelegramErr(respBody))
	}
	var out telegramSendResp
	if err := json.Unmarshal(respBody, &out); err != nil {
		return fmt.Errorf("sendPhoto: decode: %v", err)
	}
	if !out.OK {
		return fmt.Errorf("sendPhoto: %s", out.Description)
	}
	return nil
}

func (t *telegramChannel) enqueueSend(send func(context.Context)) {
	t.sendMu.Lock()
	t.sendQueue = append(t.sendQueue, send)
	t.sendMu.Unlock()
	select {
	case t.sendWake <- struct{}{}:
	default:
	}
}

func (t *telegramChannel) sendLoop(ctx context.Context) {
	for {
		t.sendMu.Lock()
		if len(t.sendQueue) > 0 {
			send := t.sendQueue[0]
			t.sendQueue[0] = nil
			t.sendQueue = t.sendQueue[1:]
			t.sendMu.Unlock()
			send(ctx)
			continue
		}
		t.sendMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-t.sendWake:
		}
	}
}

// deliverReply sends a finished reply to a chat: photo first (caption holds
// the lead text), then one message per page/chunk. A failed chunk is logged
// and skipped so one bad part cannot wedge the rest.
func (t *telegramChannel) deliverReply(ctx context.Context, chatID string, text string, img image.Image) {
	if img != nil && t.cfg.SendImages {
		caption, rest := splitCaption(text)
		if err := t.sendPhoto(ctx, chatID, img, caption); err != nil {
			log.Printf("telegram: sendPhoto to %s: %v", chatID, err)
			for _, part := range splitOutgoing(text) {
				if err := t.sendText(ctx, chatID, part); err != nil {
					log.Printf("telegram: sendMessage to %s: %v", chatID, err)
					break
				}
			}
			return
		}
		for _, part := range rest {
			if err := t.sendText(ctx, chatID, part); err != nil {
				log.Printf("telegram: sendMessage to %s: %v", chatID, err)
				break
			}
		}
		return
	}
	for _, part := range splitOutgoing(text) {
		if err := t.sendText(ctx, chatID, part); err != nil {
			log.Printf("telegram: sendMessage to %s: %v", chatID, err)
			break
		}
	}
}

// typingLoop refreshes the "typing..." indicator until ctx is done. Telegram
// expires each action after ~5s, hence the ticker.
func (t *telegramChannel) typingLoop(ctx context.Context, chatID string) {
	ticker := time.NewTicker(telegramTypingEvery)
	defer ticker.Stop()
	_ = t.sendChatAction(ctx, chatID, "typing")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = t.sendChatAction(ctx, chatID, "typing")
		}
	}
}
