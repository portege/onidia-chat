package main

// providertools.go - Phase 3 of the pluggable agents (plan_agent.md): the
// NATIVE function/tool-calling wire format of each provider.
//
// providers.go keeps the plain text calls; here every provider learns the same
// three things:
//
//  1. how to advertise the registered abilities as tools (agentToolDefs from
//     toolcalls.go, whose JSON schema comes from agent.ParamSchema),
//  2. how to read back the structured calls the model asked for, and
//  3. how to hand the outcomes back as that provider's tool/function response.
//
// The loop (agentbridge.go) only ever sees ToolDef / ToolCall / ToolResult /
// ToolTurn, so the four dialects stay contained in this file:
//
//	gemini      tools.functionDeclarations / functionCall / functionResponse
//	openrouter  tools[tool_choice]        / message.tool_calls / role "tool"
//	ollama      tools                     / message.tool_calls / role "tool"
//	bedrock     toolConfig.tools          / toolUse block     / toolResult block
//
// A provider whose API rejects tools (an old ollama server, a model without
// tool support) makes the first tool request fail; the loop then falls back to
// the Phase 2 [AGENT: ...] tags. See toolcalls.go for the neutral types.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// toolSafeWindow caps a message list to the last maxHistTurns entries and
// drops any leading entry that is not a plain user turn.
//
// Why: a tool exchange (model turn asking for an ability, user turn carrying
// its response) must never be cut in half - every provider's API rejects a
// call or a response whose pair is missing. The loop's exchanges always sit at
// the tail, so starting the window at the nearest plain user turn removes a
// partial exchange whole. Nothing is dropped when that would leave the list
// empty (better a long request than a rejected one).
func toolSafeWindow[T any](msgs []T, plainUser func(T) bool) []T {
	full := msgs
	if len(msgs) > maxHistTurns {
		msgs = msgs[len(msgs)-maxHistTurns:]
	}
	for len(msgs) > 0 && !plainUser(msgs[0]) {
		msgs = msgs[1:]
	}
	if len(msgs) == 0 {
		return full
	}
	return msgs
}

// jsonArgs normalizes one call's arguments to a JSON object. OpenAI and Gemini
// send a JSON *string* ("{\"title\":\"x\"}"), ollama and Bedrock an object; an
// empty or unparsable value becomes "no arguments" (logged), so a sloppy model
// gets the agent's own validation error instead of failing the whole reply.
func jsonArgs(raw json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	switch t[0] {
	case '{':
		if !json.Valid(t) {
			log.Printf("tools: arguments are not valid JSON: %s", truncate(string(t), 60))
			return nil
		}
		return append(json.RawMessage(nil), t...)
	case '"':
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			log.Printf("tools: unreadable arguments %s: %v", truncate(string(t), 60), err)
			return nil
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		if !strings.HasPrefix(s, "{") || !json.Valid([]byte(s)) {
			log.Printf("tools: arguments are not a JSON object: %s", truncate(s, 60))
			return nil
		}
		return json.RawMessage(s)
	}
	log.Printf("tools: unexpected arguments %s", truncate(string(t), 60))
	return nil
}

// callArgsOrEmpty is the request-side counterpart of jsonArgs: a call with no
// arguments still sends an empty object (the dialects expect the field).
func callArgsOrEmpty(c ToolCall) json.RawMessage {
	if len(c.Args) == 0 {
		return json.RawMessage("{}")
	}
	return c.Args
}

// compactCalls drops the nameless holes a streamed tool call can leave behind
// (fragments are indexed, so a gap means "no call at that index").
func compactCalls(calls []ToolCall) []ToolCall {
	var out []ToolCall
	for _, c := range calls {
		if c.Name == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ---- gemini ----------------------------------------------------------------

var _ ToolCaller = (*geminiProvider)(nil)

// GenerateWithTools advertises the abilities as functionDeclarations and
// returns the answer's text plus its functionCall parts. Gemini's
// generateContent is not streamed here, so onDelta is unused.
func (g *geminiProvider) GenerateWithTools(system string, history []Msg, userText string,
	tools []ToolDef, onDelta func(accumulated string)) (ToolTurn, error) {
	if g.apiKey == "" {
		return ToolTurn{}, errors.New("no Gemini API key")
	}
	contents := geminiContents(history, userText)
	out, err := g.generateRaw(contents, g.model, system, nil, geminiToolList(tools))
	if err != nil {
		return ToolTurn{}, err
	}
	return geminiTurn(out)
}

// geminiToolList renders the ability definitions as the request's tools field.
func geminiToolList(tools []ToolDef) []geminiTools {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]geminiFunctionDecl, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, geminiFunctionDecl{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  geminiToolSchema(t.Schema),
		})
	}
	return []geminiTools{{FunctionDeclarations: decls}}
}

// geminiToolSchema converts a standard JSON-schema map into the uppercase-type
// dialect Gemini's FunctionDeclaration Schema expects (OBJECT, STRING, etc.).
func geminiToolSchema(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return nil
	}
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		switch k {
		case "type":
			if s, ok := v.(string); ok {
				out[k] = strings.ToUpper(s)
			} else {
				out[k] = v
			}
		case "properties":
			if props, ok := v.(map[string]any); ok {
				newProps := make(map[string]any, len(props))
				for pk, pv := range props {
					if pm, ok := pv.(map[string]any); ok {
						newProps[pk] = geminiToolSchema(pm)
					} else {
						newProps[pk] = pv
					}
				}
				out[k] = newProps
			} else {
				out[k] = v
			}
		case "items":
			if item, ok := v.(map[string]any); ok {
				out[k] = geminiToolSchema(item)
			} else {
				out[k] = v
			}
		default:
			out[k] = v
		}
	}
	return out
}

// geminiTurn converts a generateContent answer into the loop's dialect: the
// display text plus every functionCall part of the first candidate.
func geminiTurn(out *geminiResponse) (ToolTurn, error) {
	var sb strings.Builder
	var calls []ToolCall
	for _, c := range out.Candidates {
		for _, p := range c.Content.Parts {
			if p.FunctionCall != nil {
				raw, err := json.Marshal(p) // keeps the part verbatim (Raw)
				if err != nil {
					raw = nil
				}
				calls = append(calls, ToolCall{Name: p.FunctionCall.Name,
					Args: jsonArgs(p.FunctionCall.Args), Raw: raw})
				continue
			}
			if p.Thought {
				continue
			}
			sb.WriteString(p.Text)
		}
		if sb.Len() > 0 || len(calls) > 0 {
			break
		}
	}
	turn := ToolTurn{Text: strings.TrimSpace(sb.String()), Calls: compactCalls(calls)}
	if turn.Text == "" && len(turn.Calls) == 0 {
		if len(out.Candidates) > 0 && out.Candidates[0].FinishReason == "SAFETY" {
			return ToolTurn{}, errors.New("the answer was blocked by safety filters")
		}
		return ToolTurn{}, errors.New("empty answer")
	}
	return turn, nil
}

// geminiContents converts history + the newest user text into generateContent
// contents, encoding tool exchanges (Msg.Calls / Msg.Results) as functionCall
// and functionResponse parts.
func geminiContents(history []Msg, userText string) []geminiContent {
	contents := make([]geminiContent, 0, len(history)+1)
	for _, m := range history {
		switch {
		case len(m.Calls) > 0:
			parts := make([]geminiPart, 0, len(m.Calls)+1)
			if m.Text != "" {
				parts = append(parts, geminiPart{Text: m.Text})
			}
			for _, c := range m.Calls {
				parts = append(parts, geminiCallPart(c))
			}
			contents = append(contents, geminiContent{Role: "model", Parts: parts})
		case len(m.Results) > 0:
			parts := make([]geminiPart, 0, len(m.Results)+1)
			if m.Text != "" {
				parts = append(parts, geminiPart{Text: m.Text})
			}
			for _, r := range m.Results {
				parts = append(parts, geminiPart{FunctionResponse: geminiResponsePart(r)})
			}
			contents = append(contents, geminiContent{Role: "user", Parts: parts})
		default:
			role := "model"
			if m.From == "you" {
				role = "user"
			}
			contents = append(contents, geminiContent{Role: role,
				Parts: []geminiPart{{Text: m.Text}}})
		}
	}
	if len(contents) == 0 || contents[len(contents)-1].Role != "user" {
		contents = append(contents, geminiContent{Role: "user",
			Parts: []geminiPart{{Text: userText}}})
	}
	return toolSafeWindow(contents, geminiPlainUser)
}

// geminiPlainUser reports a user turn carrying only text (no functionResponse)
// - the only safe place for a trimmed window to start.
func geminiPlainUser(c geminiContent) bool {
	if c.Role != "user" {
		return false
	}
	for _, p := range c.Parts {
		if p.FunctionResponse != nil {
			return false
		}
	}
	return true
}

// geminiCallPart re-emits one tool call as a functionCall part: the raw part
// straight from the model's answer when we have it (Gemini 3 requires its
// signed thoughtSignature back verbatim), a plain part otherwise.
func geminiCallPart(c ToolCall) geminiPart {
	if len(c.Raw) > 0 {
		var p geminiPart
		if err := json.Unmarshal(c.Raw, &p); err == nil && p.FunctionCall != nil {
			return p
		}
	}
	return geminiPart{FunctionCall: &geminiFunctionCall{Name: c.Name, Args: c.Args}}
}

// geminiResponsePart wraps one outcome as the functionResponse part Gemini
// requires (its response field must be a JSON object).
func geminiResponsePart(r ToolResult) *geminiFunctionResponse {
	status := "success"
	if r.Err {
		status = "error"
	}
	body, err := json.Marshal(map[string]string{"status": status, "result": r.Content})
	if err != nil {
		body = []byte(`{"status":"success","result":"ok"}`)
	}
	return &geminiFunctionResponse{Name: r.Name, Response: body}
}

// ---- ollama ----------------------------------------------------------------

var _ ToolCaller = (*ollamaProvider)(nil)

// ollamaTool is one entry of the request's tools array (the same shape OpenAI
// uses: a function object with a JSON-schema parameters object).
type ollamaTool struct {
	Type     string `json:"type"` // always "function"
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	} `json:"function"`
}

// ollamaToolCall is one call inside an assistant message or the reply.
// Arguments is an object here (unlike OpenAI's JSON string).
type ollamaToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	} `json:"function"`
}

// GenerateWithTools advertises the abilities through /api/chat's tools field.
func (o *ollamaProvider) GenerateWithTools(system string, history []Msg, userText string,
	tools []ToolDef, onDelta func(accumulated string)) (ToolTurn, error) {
	return o.generateTurn(system, history, userText, tools, onDelta)
}

// generateTurn performs one /api/chat call - text-only when tools is empty,
// tool-calling otherwise. onDelta is unused (this provider has no streaming
// path) and exists for the shared ToolCaller shape.
func (o *ollamaProvider) generateTurn(system string, history []Msg, userText string,
	tools []ToolDef, onDelta func(accumulated string)) (ToolTurn, error) {
	payload := map[string]any{
		"model":    o.model,
		"messages": ollamaMessages(system, history, userText),
		"stream":   false,
	}
	if defs := ollamaToolList(tools); len(defs) > 0 {
		payload["tools"] = defs
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ToolTurn{}, err
	}
	endpoint := strings.TrimRight(o.apiURL, "/") + "/api/chat"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ToolTurn{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.http.Do(req)
	if err != nil {
		return ToolTurn{}, fmt.Errorf("ollama %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ToolTurn{}, fmt.Errorf("ollama: read reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// A server/model that cannot do tools answers the request shape with
		// a 4xx or a deserialization 500 (e.g. hailo-ollama); remember it so
		// only the first reply pays for the fallback.
		if len(tools) > 0 && toolRejection(resp.StatusCode, raw) {
			o.noTools.Store(true)
			log.Printf("ollama: tools unsupported (HTTP %d) - using [AGENT: ...] tags", resp.StatusCode)
		}
		return ToolTurn{}, fmt.Errorf("ollama %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Message struct {
			Role      string           `json:"role"`
			Content   string           `json:"content"`
			ToolCalls []ollamaToolCall `json:"tool_calls"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return ToolTurn{}, fmt.Errorf("ollama: decode reply: %w", err)
	}
	turn := ToolTurn{Text: strings.TrimSpace(out.Message.Content)}
	for _, tc := range out.Message.ToolCalls {
		turn.Calls = append(turn.Calls, ToolCall{
			Name: tc.Function.Name,
			Args: jsonArgs(tc.Function.Arguments),
		})
	}
	turn.Calls = compactCalls(turn.Calls)
	if turn.Text == "" && len(turn.Calls) == 0 {
		return ToolTurn{}, errors.New("empty answer")
	}
	return turn, nil
}

// toolRejection reports whether an HTTP status + body indicates the server
// refused the request shape specifically because of the tools field. Standard
// ollama gives 4xx; non-standard forks (like hailo-ollama's deserialization
// mapper) give 500 when unknown fields are passed.
func toolRejection(code int, body []byte) bool {
	if code >= 400 && code < 500 {
		return true
	}
	if code == http.StatusInternalServerError {
		lower := strings.ToLower(string(body))
		return strings.Contains(lower, "mapper") ||
			strings.Contains(lower, "deserialize") ||
			strings.Contains(lower, "tools") ||
			strings.Contains(lower, "unknown field")
	}
	return false
}

// ollamaToolList renders the ability definitions as the request's tools array.
func ollamaToolList(tools []ToolDef) []ollamaTool {
	if len(tools) == 0 {
		return nil
	}
	defs := make([]ollamaTool, 0, len(tools))
	for _, t := range tools {
		var def ollamaTool
		def.Type = "function"
		def.Function.Name = t.Name
		def.Function.Description = t.Description
		def.Function.Parameters = t.Schema
		defs = append(defs, def)
	}
	return defs
}

// ollamaMessages builds the /api/chat message list: the system message stays
// pinned at the front and the exchanged turns are capped, tool-safely.
func ollamaMessages(system string, history []Msg, userText string) []ollamaChatMsg {
	conv := make([]ollamaChatMsg, 0, len(history)+1)
	for _, m := range history {
		switch {
		case len(m.Calls) > 0:
			msg := ollamaChatMsg{Role: "assistant", Content: m.Text}
			for _, c := range m.Calls {
				var call ollamaToolCall
				call.Function.Name = c.Name
				call.Function.Arguments = callArgsOrEmpty(c)
				msg.ToolCalls = append(msg.ToolCalls, call)
			}
			conv = append(conv, msg)
		case len(m.Results) > 0:
			for _, r := range m.Results {
				// ollama pairs a tool message with the call it answers by
				// name (tool_name), as its calls carry no id.
				conv = append(conv, ollamaChatMsg{Role: "tool", Content: r.Content, ToolName: r.Name})
			}
		default:
			role := "assistant"
			if m.From == "you" {
				role = "user"
			}
			conv = append(conv, ollamaChatMsg{Role: role, Content: m.Text})
		}
	}
	// Same rule as the gemini provider: make sure the conversation ends on a
	// user turn carrying the new input.
	if len(conv) == 0 || conv[len(conv)-1].Role != "user" {
		conv = append(conv, ollamaChatMsg{Role: "user", Content: userText})
	}
	conv = toolSafeWindow(conv, func(m ollamaChatMsg) bool { return m.Role == "user" })

	msgs := make([]ollamaChatMsg, 0, len(conv)+1)
	if system != "" {
		msgs = append(msgs, ollamaChatMsg{Role: "system", Content: system})
	}
	return append(msgs, conv...)
}

// ---- openrouter / OpenAI chat completions ----------------------------------

var _ ToolCaller = (*openrouterProvider)(nil)

// openrouterTool is one entry of the request's tools array.
type openrouterTool struct {
	Type     string `json:"type"` // always "function"
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	} `json:"function"`
}

// openrouterToolCall decodes one call of an assistant message / reply. The
// arguments arrive as a JSON *string* in OpenAI's dialect, sometimes as an
// object from a compatible gateway - jsonArgs normalizes both.
type openrouterToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	} `json:"function"`
}

// openrouterToolCallOut is a call echoed back in an assistant message: OpenAI
// wants the arguments as a JSON string, so the request side needs its own type.
type openrouterToolCallOut struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"` // always "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// GenerateWithTools performs one /chat/completions call with the abilities
// advertised as tools, streaming included when the provider has it enabled.
func (p *openrouterProvider) GenerateWithTools(system string, history []Msg, userText string,
	tools []ToolDef, onDelta func(accumulated string)) (ToolTurn, error) {
	return p.generateTurn(system, history, userText, tools, onDelta)
}

// generateTurn performs one /chat/completions call - text-only when tools is
// empty, tool-calling otherwise - and returns the text plus any tool calls.
func (p *openrouterProvider) generateTurn(system string, history []Msg, userText string,
	tools []ToolDef, onDelta func(accumulated string)) (ToolTurn, error) {
	if p.apiKey == "" {
		return ToolTurn{}, errors.New("no OpenRouter API key (set -api-key, $OPENROUTER_API_KEY, or config api-key)")
	}
	payload := map[string]any{
		"model":    p.model,
		"messages": openrouterMessages(system, history, userText),
		"stream":   p.stream,
	}
	if defs := openrouterToolList(tools); len(defs) > 0 {
		payload["tools"] = defs
		payload["tool_choice"] = "auto"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ToolTurn{}, err
	}
	endpoint := strings.TrimRight(p.apiURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ToolTurn{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.http.Do(req)
	if err != nil {
		return ToolTurn{}, fmt.Errorf("openrouter %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if p.stream {
		// SSE: a non-200 answer arrives as a plain JSON error object, not an
		// event stream, so check the status (reading the body for the
		// message) before handing the stream to the event parser.
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			return ToolTurn{}, fmt.Errorf("openrouter %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return p.readSSETurn(resp.Body, onDelta)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ToolTurn{}, fmt.Errorf("openrouter: read reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ToolTurn{}, fmt.Errorf("openrouter %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string               `json:"content"`
				ToolCalls []openrouterToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return ToolTurn{}, fmt.Errorf("openrouter: decode reply: %w", err)
	}
	if len(out.Choices) == 0 {
		return ToolTurn{}, errors.New("openrouter: no choices in reply")
	}
	return openrouterTurn(out.Choices[0].Message.Content, out.Choices[0].Message.ToolCalls)
}

// openrouterTurn builds the loop's turn from one assistant message.
func openrouterTurn(content string, calls []openrouterToolCall) (ToolTurn, error) {
	turn := ToolTurn{Text: strings.TrimSpace(content)}
	for _, c := range calls {
		turn.Calls = append(turn.Calls, ToolCall{
			ID:   c.ID,
			Name: c.Function.Name,
			Args: jsonArgs(c.Function.Arguments),
		})
	}
	turn.Calls = compactCalls(turn.Calls)
	if turn.Text == "" && len(turn.Calls) == 0 {
		return ToolTurn{}, errors.New("empty answer")
	}
	return turn, nil
}

// openrouterToolList renders the ability definitions as the request's tools.
func openrouterToolList(tools []ToolDef) []openrouterTool {
	if len(tools) == 0 {
		return nil
	}
	defs := make([]openrouterTool, 0, len(tools))
	for _, t := range tools {
		var def openrouterTool
		def.Type = "function"
		def.Function.Name = t.Name
		def.Function.Description = t.Description
		def.Function.Parameters = t.Schema
		defs = append(defs, def)
	}
	return defs
}

// openrouterMessages builds the /chat/completions message list:
//   - assistant turns asking for abilities carry tool_calls,
//   - the results turn after them becomes one role:"tool" message per call
//     (OpenAI pairs them through tool_call_id, the call's own id),
//   - the system message stays pinned at the front and the exchanged turns are
//     capped, tool-safely.
func openrouterMessages(system string, history []Msg, userText string) []openrouterChatMsg {
	conv := make([]openrouterChatMsg, 0, len(history)+1)
	for _, m := range history {
		switch {
		case len(m.Calls) > 0:
			msg := openrouterChatMsg{Role: "assistant", Content: m.Text}
			for _, c := range m.Calls {
				var call openrouterToolCallOut
				call.ID = c.ID
				call.Type = "function"
				call.Function.Name = c.Name
				call.Function.Arguments = string(callArgsOrEmpty(c))
				msg.ToolCalls = append(msg.ToolCalls, call)
			}
			conv = append(conv, msg)
		case len(m.Results) > 0:
			for _, r := range m.Results {
				conv = append(conv, openrouterChatMsg{Role: "tool", ToolCallID: r.CallID, Content: r.Content})
			}
		default:
			role := "assistant"
			if m.From == "you" {
				role = "user"
			}
			conv = append(conv, openrouterChatMsg{Role: role, Content: m.Text})
		}
	}
	// Same rule as the other providers: guarantee a trailing user turn
	// carrying the new input.
	if len(conv) == 0 || conv[len(conv)-1].Role != "user" {
		conv = append(conv, openrouterChatMsg{Role: "user", Content: userText})
	}
	conv = toolSafeWindow(conv, func(m openrouterChatMsg) bool { return m.Role == "user" })

	msgs := make([]openrouterChatMsg, 0, len(conv)+1)
	if system != "" {
		msgs = append(msgs, openrouterChatMsg{Role: "system", Content: system})
	}
	return append(msgs, conv...)
}

// openrouterToolDelta is one streamed tool-call fragment: the name and the
// arguments arrive in pieces that must be concatenated per index.
type openrouterToolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// readSSETurn consumes an OpenAI-style text/event-stream: one "data: {json}"
// line per chunk carrying choices[0].delta content and/or tool_call fragments,
// terminated by "data: [DONE]". onDelta (may be nil) receives the accumulated
// text after each content fragment. A mid-stream "error" object aborts with its
// message, and a stream that produced neither text nor a call fails like the
// single-shot path.
func (p *openrouterProvider) readSSETurn(r io.Reader, onDelta func(accumulated string)) (ToolTurn, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20) // long data: lines on big replies
	var sb strings.Builder
	var calls []ToolCall
	var args []strings.Builder // argument fragments, parallel to calls
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
					Content   *string               `json:"content"`
					ToolCalls []openrouterToolDelta `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return ToolTurn{}, fmt.Errorf("openrouter: decode stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return ToolTurn{}, fmt.Errorf("openrouter: stream: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue // usage keep-alives
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != nil && *delta.Content != "" {
			sb.WriteString(*delta.Content)
			if onDelta != nil {
				onDelta(sb.String())
			}
		}
		for _, frag := range delta.ToolCalls {
			for len(calls) <= frag.Index {
				calls = append(calls, ToolCall{})
				args = append(args, strings.Builder{})
			}
			if frag.ID != "" {
				calls[frag.Index].ID = frag.ID
			}
			if frag.Function.Name != "" {
				calls[frag.Index].Name = frag.Function.Name
			}
			args[frag.Index].WriteString(frag.Function.Arguments)
		}
	}
	if err := sc.Err(); err != nil {
		return ToolTurn{}, fmt.Errorf("openrouter: read stream: %w", err)
	}
	for i := range calls {
		calls[i].Args = jsonArgs(json.RawMessage(args[i].String()))
	}
	turn := ToolTurn{Text: strings.TrimSpace(sb.String()), Calls: compactCalls(calls)}
	if turn.Text == "" && len(turn.Calls) == 0 {
		return ToolTurn{}, errors.New("empty answer")
	}
	return turn, nil
}

// ---- bedrock (Converse) ----------------------------------------------------

var _ ToolCaller = (*bedrockProvider)(nil)

// GenerateWithTools advertises the abilities through the Converse toolConfig.
// Converse is not streamed here, so onDelta is unused.
func (p *bedrockProvider) GenerateWithTools(system string, history []Msg, userText string,
	tools []ToolDef, onDelta func(accumulated string)) (ToolTurn, error) {
	return p.generateTurn(system, history, userText, tools)
}

// generateTurn performs one Converse call - text-only when tools is empty,
// tool-calling otherwise - and returns the text plus any toolUse blocks.
func (p *bedrockProvider) generateTurn(system string, history []Msg, userText string, tools []ToolDef) (ToolTurn, error) {
	messages := bedrockMessages(history, userText)
	var systemBlocks []types.SystemContentBlock
	if system != "" {
		systemBlocks = []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: system},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), geminiTimeout)
	defer cancel()
	out, err := p.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId:    &p.model,
		Messages:   messages,
		System:     systemBlocks,
		ToolConfig: bedrockToolConfig(tools),
	})
	if err != nil {
		// Include the model ID: "model identifier is invalid" errors are
		// otherwise confusing (usually a Gemini ID leftover in the config).
		log.Printf("bedrock: model %s: %v", p.model, err)
		return ToolTurn{}, fmt.Errorf("model %s: %w", p.model, err)
	}

	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return ToolTurn{}, errors.New("bedrock: unexpected output type")
	}
	var sb strings.Builder
	var calls []ToolCall
	for _, block := range msg.Value.Content {
		switch b := block.(type) {
		case *types.ContentBlockMemberText:
			sb.WriteString(b.Value)
		case *types.ContentBlockMemberToolUse:
			calls = append(calls, bedrockCall(b.Value))
		}
	}
	turn := ToolTurn{Text: strings.TrimSpace(sb.String()), Calls: calls}
	if turn.Text == "" && len(turn.Calls) == 0 {
		return ToolTurn{}, errors.New("empty answer")
	}
	return turn, nil
}

// bedrockToolConfig renders the ability definitions as Converse's toolConfig.
// The schema is the same JSON-schema object every dialect takes; Bedrock wants
// it as a document.
func bedrockToolConfig(tools []ToolDef) *types.ToolConfiguration {
	if len(tools) == 0 {
		return nil
	}
	list := make([]types.Tool, 0, len(tools))
	for _, t := range tools {
		spec := types.ToolSpecification{
			Name:        strPtr(t.Name),
			InputSchema: &types.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(t.Schema)},
		}
		if t.Description != "" {
			spec.Description = strPtr(t.Description)
		}
		list = append(list, &types.ToolMemberToolSpec{Value: spec})
	}
	return &types.ToolConfiguration{Tools: list}
}

// bedrockCall converts one toolUse block into the loop's dialect. The input
// document is read through MarshalSmithyDocument + encoding/json rather than
// UnmarshalSmithyDocument: the document interface's lazy flavor handles both
// directions, so this decodes a real Converse response block and a document we
// built ourselves (the echo path) alike.
func bedrockCall(u types.ToolUseBlock) ToolCall {
	call := ToolCall{ID: strPtrValue(u.ToolUseId), Name: strPtrValue(u.Name)}
	if u.Input == nil {
		return call
	}
	raw, err := u.Input.MarshalSmithyDocument()
	if err != nil {
		log.Printf("bedrock: tool %s: unreadable input: %v", call.Name, err)
		return call
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return call
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		log.Printf("bedrock: tool %s: unreadable input: %v", call.Name, err)
		return call
	}
	if len(args) > 0 {
		if encoded, err := json.Marshal(args); err == nil {
			call.Args = encoded
		}
	}
	return call
}

// bedrockUseBlock re-emits one call as a toolUse block. Converse requires a
// non-empty tool-use id, so a call without one gets a synthetic id (the result
// block below synthesizes the same one, keeping the pair matched).
func bedrockUseBlock(c ToolCall) types.ToolUseBlock {
	args := map[string]any{}
	if len(c.Args) > 0 {
		if err := json.Unmarshal(c.Args, &args); err != nil {
			args = map[string]any{}
		}
	}
	return types.ToolUseBlock{
		ToolUseId: strPtr(bedrockCallID(c.ID, c.Name)),
		Name:      strPtr(c.Name),
		Input:     document.NewLazyDocument(args),
	}
}

// bedrockResultBlock wraps one outcome as a toolResult block.
func bedrockResultBlock(r ToolResult) types.ToolResultBlock {
	status := types.ToolResultStatusSuccess
	if r.Err {
		status = types.ToolResultStatusError
	}
	return types.ToolResultBlock{
		ToolUseId: strPtr(bedrockCallID(r.CallID, r.Name)),
		Status:    status,
		Content: []types.ToolResultContentBlock{
			&types.ToolResultContentBlockMemberText{Value: r.Content},
		},
	}
}

// bedrockCallID returns a call's id, or a stable synthetic one (Converse
// rejects an empty tool-use id).
func bedrockCallID(id, name string) string {
	if id != "" {
		return id
	}
	return "call_" + name
}

// bedrockMessages converts history + the newest user text into Converse
// messages: the conversation must start with a user turn and alternate
// user/assistant roles, and tool exchanges (Msg.Calls / Msg.Results) become
// toolUse / toolResult blocks.
func bedrockMessages(history []Msg, userText string) []types.Message {
	// Skip the welcome bot message(s) that may appear before the first user
	// message (Converse requires the conversation to start with a user turn).
	start := 0
	for i, m := range history {
		if m.From == "you" && len(m.Results) == 0 {
			start = i
			break
		}
	}

	messages := make([]types.Message, 0, len(history)+1-start)
	for i := start; i < len(history); i++ {
		m := history[i]
		var blocks []types.ContentBlock
		role := types.ConversationRoleAssistant
		switch {
		case len(m.Calls) > 0:
			if m.Text != "" {
				blocks = append(blocks, &types.ContentBlockMemberText{Value: m.Text})
			}
			for _, c := range m.Calls {
				blocks = append(blocks, &types.ContentBlockMemberToolUse{Value: bedrockUseBlock(c)})
			}
		case len(m.Results) > 0:
			role = types.ConversationRoleUser
			if m.Text != "" {
				blocks = append(blocks, &types.ContentBlockMemberText{Value: m.Text})
			}
			for _, r := range m.Results {
				blocks = append(blocks, &types.ContentBlockMemberToolResult{Value: bedrockResultBlock(r)})
			}
		default:
			if m.From == "you" {
				role = types.ConversationRoleUser
			}
			blocks = append(blocks, &types.ContentBlockMemberText{Value: m.Text})
		}
		// Drop any message that would create two consecutive turns with the
		// same role (defensive against malformed history).
		if len(messages) > 0 && messages[len(messages)-1].Role == role {
			continue
		}
		messages = append(messages, types.Message{Role: role, Content: blocks})
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != types.ConversationRoleUser {
		messages = append(messages, types.Message{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: userText}},
		})
	}
	return toolSafeWindow(messages, bedrockPlainUser)
}

// bedrockPlainUser reports a user turn carrying only text (no toolResult) -
// the only safe place for a trimmed window to start.
func bedrockPlainUser(m types.Message) bool {
	if m.Role != types.ConversationRoleUser {
		return false
	}
	for _, block := range m.Content {
		if _, ok := block.(*types.ContentBlockMemberToolResult); ok {
			return false
		}
	}
	return true
}

// strPtr is the *string helper the AWS SDK's pointer fields need.
func strPtr(s string) *string { return &s }

// strPtrValue dereferences an SDK string pointer ("" when nil).
func strPtrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
