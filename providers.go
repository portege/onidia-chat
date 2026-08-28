// providers.go - LLM provider backends for the chatbot brain.
//
// Currently supported:
//   - gemini: Google Gemini generateContent API (uses -api-key / -api-url / -model)
//   - bedrock: Amazon Bedrock Converse API (uses -aws-profile / -aws-region / -model)

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// Provider abstracts the LLM used for chat replies.
type Provider interface {
	Name() string
	GenerateText(system string, history []Msg, userText string) (string, error)
}

// geminiProvider talks to Google's Gemini generateContent API.
type geminiProvider struct {
	apiKey string
	apiURL string
	model  string
	http   *http.Client
}

func (g *geminiProvider) Name() string { return "gemini" }

func (g *geminiProvider) GenerateText(system string, history []Msg, userText string) (string, error) {
	if g.apiKey == "" {
		return "", errors.New("no Gemini API key")
	}
	contents := make([]geminiContent, 0, len(history)+1)
	for _, m := range history {
		role := "model"
		if m.From == "you" {
			role = "user"
		}
		contents = append(contents, geminiContent{Role: role,
			Parts: []geminiPart{{Text: m.Text}}})
	}
	if len(contents) == 0 || contents[len(contents)-1].Role != "user" {
		contents = append(contents, geminiContent{Role: "user",
			Parts: []geminiPart{{Text: userText}}})
	}
	if len(contents) > maxHistTurns {
		contents = contents[len(contents)-maxHistTurns:]
	}

	out, err := g.generateRaw(contents, g.model, system, nil)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range out.Candidates {
		for _, p := range c.Content.Parts {
			if p.Thought {
				continue
			}
			sb.WriteString(p.Text)
		}
		if sb.Len() > 0 {
			break
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		if len(out.Candidates) > 0 && out.Candidates[0].FinishReason == "SAFETY" {
			return "", errors.New("the answer was blocked by safety filters")
		}
		return "", errors.New("empty answer")
	}
	return text, nil
}

// generateImage asks a Gemini image model to create a picture from the prompt.
func (g *geminiProvider) generateImage(prompt string) (image.Image, error) {
	if g.apiKey == "" {
		return nil, errors.New("no API key")
	}
	contents := []geminiContent{{
		Role:  "user",
		Parts: []geminiPart{{Text: prompt}},
	}}
	cfg := &generationConfig{ResponseModalities: []string{"IMAGE", "TEXT"}}
	out, err := g.generateRaw(contents, geminiImageModel, "", cfg)
	if err != nil {
		return nil, err
	}
	for _, cand := range out.Candidates {
		for _, p := range cand.Content.Parts {
			if p.InlineData == nil || p.InlineData.Data == "" {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
			if err != nil {
				return nil, fmt.Errorf("decode base64: %w", err)
			}
			img, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				return nil, fmt.Errorf("decode image: %w", err)
			}
			return scaleImage(img), nil
		}
	}
	return nil, errors.New("no image part in response")
}

// generateRaw fires a generateContent call for any model, retrying transient
// failures.
func (g *geminiProvider) generateRaw(contents []geminiContent, model, system string, genCfg *generationConfig) (*geminiResponse, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 900 * time.Millisecond)
		}
		out, err := g.generateRawOnce(contents, model, system, genCfg, attempt)
		if err == nil {
			return out, nil
		}
		lastErr = err
		var se *httpStatusError
		if errors.As(err, &se) && se.code >= 400 && se.code < 500 && se.code != 429 {
			return nil, err
		}
	}
	return nil, lastErr
}

// generateRawOnce builds the JSON request body and fires one generateContent
// call for the supplied model.
func (g *geminiProvider) generateRawOnce(contents []geminiContent, model, system string, genCfg *generationConfig, retryAttempt int) (*geminiResponse, error) {
	reqBody := geminiRequest{Contents: contents}
	if system != "" {
		reqBody.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: system}}}
	}
	if genCfg != nil {
		reqBody.GenerationConfig = genCfg
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/v1beta/models/%s:generateContent", g.apiURL, model),
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.http.Do(req)
	if err != nil {
		log.Printf("gemini: network error (attempt %d/4): %v", retryAttempt+1, err)
		return nil, err
	}
	defer resp.Body.Close()

	readDeadline := time.Now().Add(geminiTimeout)
	if dl, ok := resp.Body.(interface{ SetReadDeadline(t time.Time) error }); ok {
		_ = dl.SetReadDeadline(readDeadline)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out geminiResponse
	if uerr := json.Unmarshal(raw, &out); uerr != nil && resp.StatusCode == http.StatusOK {
		log.Printf("gemini: unparseable 200 body: %v\nbody: %.600s", uerr, raw)
		return nil, errors.New("unparseable response (network mangled it?)")
	}
	if resp.StatusCode != http.StatusOK {
		msg := ""
		if out.Error != nil {
			msg = out.Error.Message
		}
		log.Printf("gemini: http %d (model=%s, attempt %d/4)\nbody: %.1200s",
			resp.StatusCode, model, retryAttempt+1, raw)
		return nil, &httpStatusError{code: resp.StatusCode, msg: msg}
	}
	return &out, nil
}

// bedrockProvider talks to Amazon Bedrock via the Converse API.
type bedrockProvider struct {
	client *bedrockruntime.Client
	model  string
}

// defaultBedrockModelID is the fallback foundation-model ID used when
// provider=bedrock and no model is configured. Nova Lite is fast and cheap,
// and available in most Bedrock regions.
const defaultBedrockModelID = "amazon.nova-lite-v1:0"

// isGeminiModel reports whether an ID belongs to Google's Gemini family
// (e.g. "gemini-3.6-flash").
func isGeminiModel(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), "gemini-")
}

// isBedrockModel reports whether an ID looks like a Bedrock foundation-model
// identifier: it starts with a provider prefix (amazon., anthropic., meta.,
// mistral., cohere., ...).
func isBedrockModel(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, prefix := range []string{
		"amazon.", "anthropic.", "meta.", "mistral.", "cohere.",
		"ai21.", "deepseek.", "amazon",
	} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

func (p *bedrockProvider) Name() string { return "bedrock" }

func (p *bedrockProvider) GenerateText(system string, history []Msg, userText string) (string, error) {
	// Bedrock requires the conversation to start with a user message and to
	// alternate user/assistant roles. Skip the welcome bot message(s) that may
	// appear before the first user message.
	start := 0
	for i, m := range history {
		if m.From == "you" {
			start = i
			break
		}
	}

	messages := make([]types.Message, 0, len(history)+1-start)
	for i := start; i < len(history); i++ {
		m := history[i]
		role := types.ConversationRoleAssistant
		if m.From == "you" {
			role = types.ConversationRoleUser
		}
		// Drop any message that would create two consecutive turns with the
		// same role (defensive against malformed history).
		if len(messages) > 0 && messages[len(messages)-1].Role == role {
			continue
		}
		messages = append(messages, types.Message{
			Role:    role,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: m.Text}},
		})
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != types.ConversationRoleUser {
		messages = append(messages, types.Message{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: userText}},
		})
	}
	if len(messages) > maxHistTurns {
		messages = messages[len(messages)-maxHistTurns:]
		// After truncation the first remaining message must still be user.
		if messages[0].Role != types.ConversationRoleUser {
			for i, msg := range messages {
				if msg.Role == types.ConversationRoleUser {
					messages = messages[i:]
					break
				}
			}
		}
	}

	var systemBlocks []types.SystemContentBlock
	if system != "" {
		systemBlocks = []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: system},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), geminiTimeout)
	defer cancel()
	out, err := p.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId:  &p.model,
		Messages: messages,
		System:   systemBlocks,
	})
	if err != nil {
		// Include the model ID: "model identifier is invalid" errors are
		// otherwise confusing (usually a Gemini ID leftover in the config).
		log.Printf("bedrock: model %s: %v", p.model, err)
		return "", fmt.Errorf("model %s: %w", p.model, err)
	}

	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return "", errors.New("bedrock: unexpected output type")
	}
	var sb strings.Builder
	for _, block := range msg.Value.Content {
		if textBlock, ok := block.(*types.ContentBlockMemberText); ok {
			sb.WriteString(textBlock.Value)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", errors.New("empty answer")
	}
	return text, nil
}

// newBedrockProvider loads AWS credentials and creates a Bedrock runtime client.
// The default profile is used if profile is empty; region is read from the
// profile or environment when region is empty.
func newBedrockProvider(profile, region, model string) (Provider, error) {
	opts := []func(*config.LoadOptions) error{}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &bedrockProvider{
		client: bedrockruntime.NewFromConfig(cfg),
		model:  model,
	}, nil
}
