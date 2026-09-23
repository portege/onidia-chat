// providers.go - LLM provider backends for the chatbot brain.
//
// Currently supported:
//   - gemini: Google Gemini generateContent API (uses -api-key / -api-url / -model)
//   - bedrock: Amazon Bedrock Converse API (uses -aws-profile / -aws-region / -model)
//   - ollama: ollama-compatible /api/chat server (uses -api-url / -model; no key)
//   - openrouter: OpenAI-compatible /chat/completions gateway (uses -api-key /
//     -api-url / -model). OpenRouter by default (one key, any vendor: DeepSeek,
//     Kimi/Moonshot, etc.); also works against a vendor's native /chat/completions
//     endpoint (e.g. DeepSeek https://api.deepseek.com) by pointing -api-url at it.

package main

import (
	"bufio"
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

// Streamer is implemented by providers that can deliver the reply
// incrementally over SSE. onDelta receives the accumulated text so far (not
// just the new fragment) after every content fragment and may be nil; the
// full text is still returned, so callers can ignore the callback.
type Streamer interface {
	Provider
	GenerateTextStream(system string, history []Msg, userText string,
		onDelta func(accumulated string)) (string, error)
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

// defaultOllamaModel is the fallback model tag used when provider=ollama and
// no model is configured — a small chat model that runs well on the hailo box.
const defaultOllamaModel = "qwen2:1.5b"

// defaultOllamaURL is the ollama-compatible server assumed for provider=ollama
// (the hailo box on the local network). Override with -api-url / api-url.
const defaultOllamaURL = "http://localhost:8000"

// defaultOpenRouterURL is the OpenRouter endpoint base assumed for
// provider=openrouter — an OpenAI-compatible /chat/completions gateway. Point
// -api-url at a vendor's native endpoint (e.g. https://api.deepseek.com or
// https://api.moonshot.cn/v1) to use that vendor directly with the same
// provider; only the model ID and key differ.
const defaultOpenRouterURL = "https://openrouter.ai/api/v1"

// defaultOpenRouterModel is the fallback model ID used when provider=openrouter
// and no model is configured — a capable, widely-available chat model.
const defaultOpenRouterModel = "deepseek/deepseek-chat-v3-0324"

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

// isOllamaModel reports whether an ID looks like an ollama model tag
// (e.g. "qwen2:1.5b", "llama3.2:3b", "mistral:latest"): a colon tag that is
// neither a Gemini nor a Bedrock identifier.
func isOllamaModel(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" || isGeminiModel(id) || isBedrockModel(id) {
		return false
	}
	tag := strings.LastIndex(id, ":")
	return tag > 0 && tag < len(id)-1
}

// ollamaProvider talks to an ollama-compatible chat server (Ollama itself,
// or the llama.cpp server on the hailo box) via its /api/chat endpoint — a
// local, key-less backend: POST {model, messages, stream:false} and read the
// assistant message back out of the single JSON object.
type ollamaProvider struct {
	apiURL string // server base, e.g. http://localhost:8000 (no trailing slash)
	model  string // e.g. "qwen2:1.5b"
	http   *http.Client
}

func (o *ollamaProvider) Name() string { return "ollama" }

// ollamaChatMsg is one message of the /api/chat payload.
type ollamaChatMsg struct {
	Role    string `json:"role"` // "system" | "user" | "assistant"
	Content string `json:"content"`
}

func (o *ollamaProvider) GenerateText(system string, history []Msg, userText string) (string, error) {
	msgs := make([]ollamaChatMsg, 0, len(history)+2)
	if system != "" {
		msgs = append(msgs, ollamaChatMsg{Role: "system", Content: system})
	}
	for _, m := range history {
		role := "assistant"
		if m.From == "you" {
			role = "user"
		}
		msgs = append(msgs, ollamaChatMsg{Role: role, Content: m.Text})
	}
	// Same rule as the gemini provider: make sure the conversation ends on a
	// user turn carrying the new input.
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != "user" {
		msgs = append(msgs, ollamaChatMsg{Role: "user", Content: userText})
	}
	// Cap the exchanged turns the same way gemini does; the system message
	// always stays at the front.
	if n := len(msgs); n > 1 && n-1 > maxHistTurns {
		msgs = append(msgs[:1:1], msgs[n-maxHistTurns:]...)
	}

	body, err := json.Marshal(map[string]any{
		"model":    o.model,
		"messages": msgs,
		"stream":   false,
	})
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(o.apiURL, "/") + "/api/chat"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("ollama: read reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("ollama: decode reply: %w", err)
	}
	text := strings.TrimSpace(out.Message.Content)
	if text == "" {
		return "", errors.New("empty answer")
	}
	return text, nil
}

// openrouterProvider talks to an OpenAI-compatible chat gateway (OpenRouter by
// default, or a vendor's native endpoint such as DeepSeek's
// https://api.deepseek.com or Kimi/Moonshot's https://api.moonshot.cn/v1). It
// POSTs {model, messages, stream} and reads either the single assistant
// message out of choices[0].message.content (stream:false) or an SSE event
// stream of choices[0].delta.content fragments (stream:true), authenticating
// with a Bearer token. That is the OpenAI chat-completions dialect, which
// OpenRouter forwards to DeepSeek, Kimi, and hundreds of other vendors
// through a single key.
type openrouterProvider struct {
	apiKey string // Bearer token (OpenRouter / DeepSeek / Kimi API key)
	apiURL string // endpoint base, e.g. https://openrouter.ai/api/v1 (no trailing slash)
	model  string // "deepseek/deepseek-chat-v3-0324" (OpenRouter) or "deepseek-chat" (native)
	stream bool   // true = SSE streaming reply; false = single-shot JSON
	http   *http.Client
}

func (p *openrouterProvider) Name() string { return "openrouter" }

var _ Streamer = (*openrouterProvider)(nil)

// newOpenRouterProvider builds an OpenRouter / OpenAI-compatible client. apiURL
// is the endpoint base (no trailing slash); /chat/completions is appended by
// the provider. model is the model ID (vendor-prefixed on OpenRouter, bare on a
// native endpoint). An empty model falls back to defaultOpenRouterModel.
// stream selects the SSE streaming reply (true) or the single-shot JSON
// answer (false).
func newOpenRouterProvider(apiKey, apiURL, model string, stream bool) Provider {
	if model == "" {
		model = defaultOpenRouterModel
	}
	return &openrouterProvider{
		apiKey: apiKey,
		apiURL: strings.TrimRight(apiURL, "/"),
		model:  model,
		stream: stream,
		http:   http.DefaultClient,
	}
}

// openrouterChatMsg is one message of the /chat/completions payload.
type openrouterChatMsg struct {
	Role    string `json:"role"` // "system" | "user" | "assistant"
	Content string `json:"content"`
}

func (p *openrouterProvider) GenerateText(system string, history []Msg, userText string) (string, error) {
	return p.GenerateTextStream(system, history, userText, nil)
}

// GenerateTextStream performs one /chat/completions call. When the provider
// has streaming enabled, the response is read as SSE and onDelta (if
// non-nil) receives the accumulated text after every content fragment;
// otherwise the classic single-shot JSON reply is used. Either way the full
// reply text is returned, so callers that ignore deltas work unchanged.
func (p *openrouterProvider) GenerateTextStream(system string, history []Msg, userText string, onDelta func(accumulated string)) (string, error) {
	if p.apiKey == "" {
		return "", errors.New("no OpenRouter API key (set -api-key, $OPENROUTER_API_KEY, or config api-key)")
	}
	// Same message assembly the ollama/gemini providers use: leading system
	// message, then history (assistant by default, "you" -> user), guarantee a
	// trailing user turn carrying the new input, then cap at maxHistTurns
	// keeping the system message pinned at the front.
	msgs := make([]openrouterChatMsg, 0, len(history)+2)
	if system != "" {
		msgs = append(msgs, openrouterChatMsg{Role: "system", Content: system})
	}
	for _, m := range history {
		role := "assistant"
		if m.From == "you" {
			role = "user"
		}
		msgs = append(msgs, openrouterChatMsg{Role: role, Content: m.Text})
	}
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != "user" {
		msgs = append(msgs, openrouterChatMsg{Role: "user", Content: userText})
	}
	if n := len(msgs); n > 1 && n-1 > maxHistTurns {
		msgs = append(msgs[:1:1], msgs[n-maxHistTurns:]...)
	}

	body, err := json.Marshal(map[string]any{
		"model":    p.model,
		"messages": msgs,
		"stream":   p.stream,
	})
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(p.apiURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("openrouter %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if p.stream {
		// SSE: a non-200 answer arrives as a plain JSON error object, not an
		// event stream, so check the status (reading the body for the
		// message) before handing the stream to the event parser.
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			return "", fmt.Errorf("openrouter %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return p.readSSE(resp.Body, onDelta)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("openrouter: read reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openrouter %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("openrouter: decode reply: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("openrouter: no choices in reply")
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return "", errors.New("empty answer")
	}
	return text, nil
}

// readSSE consumes an OpenAI-style text/event-stream: one "data: {json}" line
// per chunk carrying choices[0].delta.content fragments, terminated by
// "data: [DONE]". onDelta (may be nil) receives the accumulated text after
// each fragment. A mid-stream "error" object aborts with its message, and a
// stream that produced no text at all fails like the single-shot path.
func (p *openrouterProvider) readSSE(r io.Reader, onDelta func(accumulated string)) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20) // long data: lines on big replies
	var sb strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // blank separator, comments, id:/event: fields
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content *string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return "", fmt.Errorf("openrouter: decode stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return "", fmt.Errorf("openrouter: stream: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta.Content == nil {
			continue // role-only opening chunk, usage keep-alives
		}
		if frag := *chunk.Choices[0].Delta.Content; frag != "" {
			sb.WriteString(frag)
			if onDelta != nil {
				onDelta(sb.String())
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("openrouter: read stream: %w", err)
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", errors.New("empty answer")
	}
	return text, nil
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

// newOllamaProvider builds an ollama-compatible /api/chat client (Ollama itself
// or the llama.cpp server on the hailo box). apiURL is the server base (no
// trailing slash); the /api/chat endpoint is appended by the provider. model is
// the server-side tag, e.g. "qwen2:1.5b". No API key is used.
func newOllamaProvider(apiURL, model string) Provider {
	if model == "" {
		model = defaultOllamaModel
	}
	return &ollamaProvider{
		apiURL: strings.TrimRight(apiURL, "/"),
		model:  model,
		http:   http.DefaultClient,
	}
}
