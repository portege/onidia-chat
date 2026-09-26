package main

// providertools_test.go - Phase 3 wire-format tests (see providertools.go).
//
// One test per provider dialect, covering the two directions the reply loop
// depends on: the registered abilities are advertised as THAT provider's tool
// definitions, and a structured call comes back in the neutral ToolTurn - then
// goes back to the model as that provider's own tool result. The loop itself is
// dialect-free, so these are the tests that keep the four wire formats honest.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// wireToolDefs is the one-ability list the wire tests advertise.
func wireToolDefs() []ToolDef {
	return []ToolDef{{
		Name:        "search",
		Description: "Find a song.",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"q": map[string]any{"type": "string"}},
			"required":   []string{"q"},
		},
	}}
}

// wireExchange is the tool exchange the loop feeds back as history: the model's
// answer asking for an ability, then the user turn carrying its outcome.
func wireExchange() ([]ToolCall, []ToolResult) {
	calls := []ToolCall{{ID: "c1", Name: "search", Args: json.RawMessage(`{"q":"havana"}`)}}
	results := []ToolResult{{CallID: "c1", Name: "search", Content: "OK found it"}}
	return calls, results
}

// TestJSONArgsNormalizesToolArgumentDialects: OpenAI and Gemini send arguments
// as a JSON *string*, ollama and Bedrock as an object; anything unreadable must
// become "no arguments" (the agent's own validation then reports it) instead of
// failing the whole reply.
func TestJSONArgsNormalizesToolArgumentDialects(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"q":"x"}`, `{"q":"x"}`},         // object
		{` "{\"q\":\"x\"}" `, `{"q":"x"}`}, // JSON string
		{``, ``},                           // empty
		{`null`, ``},                       // explicit null
		{`"not json"`, ``},                 // a string that is not an object
		{`[1,2]`, ``},                      // an array is not an args object
		{`{"q":`, ``},                      // truncated JSON
	}
	for _, tc := range cases {
		if got := string(jsonArgs(json.RawMessage(tc.in))); got != tc.want {
			t.Errorf("jsonArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestToolSafeWindowKeepsToolPairsWhole: trimming the context must never cut an
// exchange in half (every provider rejects a call whose response is missing),
// and nothing may be dropped when that would leave the request empty.
func TestToolSafeWindowKeepsToolPairsWhole(t *testing.T) {
	plain := func(s string) bool { return s == "plain" }

	// Nothing to trim: the window is already short enough.
	short := []string{"plain", "tool"}
	if got := toolSafeWindow(short, plain); len(got) != 2 || got[0] != "plain" {
		t.Errorf("short window = %q, want it untouched", got)
	}
	// A one-entry list of a non-user turn stays as-is (better a long request
	// than a rejected empty one).
	if got := toolSafeWindow([]string{"tool"}, plain); len(got) != 1 {
		t.Errorf("all-non-user window = %q, want it kept", got)
	}
	// Over the cap, the oldest entry is a tool result: the whole partial
	// exchange goes with it instead of being sent half-cut.
	msgs := make([]string, maxHistTurns+2)
	for i := range msgs {
		msgs[i] = "plain"
	}
	msgs[2] = "tool" // the first entry of the trimmed window is a tool turn
	got := toolSafeWindow(msgs, plain)
	if len(got) != maxHistTurns-1 {
		t.Fatalf("window = %d entries, want %d (the partial exchange dropped)", len(got), maxHistTurns-1)
	}
	if got[0] != "plain" {
		t.Errorf("window starts with %q, want a plain user turn", got[0])
	}
}

// openrouterRequestBody is the subset of the OpenAI request the tests read.
type openrouterRequestBody struct {
	ToolChoice string `json:"tool_choice"`
	Messages   []struct {
		Role       string `json:"role"`
		Content    string `json:"content"`
		ToolCallID string `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"messages"`
	Tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
}

// TestOpenRouterToolRequestAndToolResultRoundTrip drives a real HTTP round trip
// through the OpenAI-compatible provider: the abilities must travel as
// tools[]/tool_choice and the model's tool_calls (arguments as a JSON string)
// must decode into ToolCall.
func TestOpenRouterToolRequestAndToolResultRoundTrip(t *testing.T) {
	var body openrouterRequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"on it",`+
			`"tool_calls":[{"id":"c1","type":"function","function":{"name":"search",`+
			`"arguments":"{\"q\":\"havana\"}"}}]}}]}`)
	}))
	defer srv.Close()

	p := newOpenRouterProvider("sk-test", srv.URL, "test-model", false).(*openrouterProvider)
	turn, err := p.GenerateWithTools("sys", nil, "play havana", wireToolDefs(), nil)
	if err != nil {
		t.Fatalf("GenerateWithTools: %v", err)
	}
	if turn.Text != "on it" || len(turn.Calls) != 1 {
		t.Fatalf("turn = %+v, want the text plus one call", turn)
	}
	call := turn.Calls[0]
	if call.ID != "c1" || call.Name != "search" || string(call.Args) != `{"q":"havana"}` {
		t.Errorf("call = %+v, want c1/search/{\"q\":\"havana\"}", call)
	}

	// The request advertised the ability the way OpenAI expects.
	if body.ToolChoice != "auto" {
		t.Errorf("tool_choice = %q, want %q", body.ToolChoice, "auto")
	}
	if len(body.Tools) != 1 || body.Tools[0].Function.Name != "search" {
		t.Fatalf("tools = %+v, want the search ability", body.Tools)
	}
	if body.Tools[0].Type != "function" || body.Tools[0].Function.Description != "Find a song." {
		t.Errorf("tool = %+v, want a function named search with its description", body.Tools[0])
	}
	if props, _ := body.Tools[0].Function.Parameters["properties"].(map[string]any); props["q"] == nil {
		t.Errorf("tool parameters = %v, want the q property", body.Tools[0].Function.Parameters)
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
		t.Errorf("messages = %+v, want the system prompt + the user turn", body.Messages)
	}
}

// TestOpenRouterToolExchangeHistory: the exchange the loop feeds back must go
// out as an assistant tool_calls turn plus one role:"tool" answer per call
// (OpenAI pairs them on the id, and rejects an assistant tool_calls turn whose
// answers are missing).
func TestOpenRouterToolExchangeHistory(t *testing.T) {
	var body openrouterRequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"[happy] done"}}]}`)
	}))
	defer srv.Close()

	calls, results := wireExchange()
	history := []Msg{
		{From: "you", Text: "play havana"},
		{From: "Buddy", Text: "on it", Calls: calls},
		{From: "you", Text: "<<<AGENT>>>\n- search: OK found it\n<<<END AGENT>>>", Results: results},
	}
	p := newOpenRouterProvider("sk-test", srv.URL, "test-model", false).(*openrouterProvider)
	turn, err := p.GenerateWithTools("sys", history, "the results block", wireToolDefs(), nil)
	if err != nil {
		t.Fatalf("GenerateWithTools: %v", err)
	}
	if turn.Text != "[happy] done" {
		t.Errorf("text = %q, want the model's answer", turn.Text)
	}

	msgs := body.Messages
	if len(msgs) != 5 {
		t.Fatalf("messages = %d (%+v), want system + 4 turns", len(msgs), msgs)
	}
	if msgs[0].Role != "system" || msgs[1].Role != "user" || msgs[2].Role != "assistant" {
		t.Errorf("roles = %s/%s/%s, want system/user/assistant", msgs[0].Role, msgs[1].Role, msgs[2].Role)
	}
	if len(msgs[2].ToolCalls) != 1 || msgs[2].ToolCalls[0].ID != "c1" ||
		msgs[2].ToolCalls[0].Function.Name != "search" ||
		msgs[2].ToolCalls[0].Function.Arguments != `{"q":"havana"}` {
		t.Errorf("assistant turn = %+v, want the call echoed back with its arguments", msgs[2].ToolCalls)
	}
	if msgs[3].Role != "tool" || msgs[3].ToolCallID != "c1" || msgs[3].Content != "OK found it" {
		t.Errorf("tool turn = %+v, want role tool / id c1 / the outcome", msgs[3])
	}
	if msgs[4].Role != "user" {
		t.Errorf("last turn role = %q, want user (the results block follows the tool answers)", msgs[4].Role)
	}
}

// TestOpenRouterStreamedToolCalls: a streamed tool call arrives in fragments
// that must be concatenated per index, and an index gap (a nameless hole) must
// be dropped rather than sent to an agent.
func TestOpenRouterStreamedToolCalls(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}`,
		`data: {"choices":[{"delta":{"content":"on "}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"search"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"havana\"}"}},{"index":1,"function":{"arguments":"{\"q\":\"orphan\"}"}}]}}]}`,
		`data: [DONE]`,
		"",
	}, "\n") + "\n"

	p := &openrouterProvider{}
	turn, err := p.readSSETurn(strings.NewReader(sse), nil)
	if err != nil {
		t.Fatalf("readSSETurn: %v", err)
	}
	if turn.Text != "on" {
		t.Errorf("text = %q, want %q", turn.Text, "on")
	}
	if len(turn.Calls) != 1 {
		t.Fatalf("calls = %+v, want only the named call", turn.Calls)
	}
	if turn.Calls[0].ID != "c1" || turn.Calls[0].Name != "search" ||
		string(turn.Calls[0].Args) != `{"q":"havana"}` {
		t.Errorf("call = %+v, want c1/search/{\"q\":\"havana\"}", turn.Calls[0])
	}
}

// TestGeminiToolDialect covers the generateContent dialect: the tools field,
// the contents encoder (Msg.Calls / Msg.Results as functionCall /
// functionResponse parts), the answer decoder, and the raw-part echo Gemini 3
// needs for a signed functionCall.
func TestGeminiToolDialect(t *testing.T) {
	tools := geminiToolList(wireToolDefs())
	if len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools = %+v, want one functionDeclarations entry", tools)
	}
	decl := tools[0].FunctionDeclarations[0]
	if decl.Name != "search" || decl.Description != "Find a song." {
		t.Errorf("declaration = %+v, want the search ability", decl)
	}
	body, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"functionDeclarations"`, `"name":"search"`, `"parameters"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("tools JSON = %s, want %s", body, want)
		}
	}
	// Gemini's Schema.Type enum requires uppercase types (OBJECT, STRING, etc.).
	for _, want := range []string{`"type":"OBJECT"`, `"type":"STRING"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("tools JSON = %s, want uppercase schema type %s", body, want)
		}
	}

	// The exchange the loop feeds back becomes functionCall/functionResponse.
	calls, results := wireExchange()
	contents := geminiContents([]Msg{
		{From: "you", Text: "play havana"},
		{From: "Buddy", Text: "on it", Calls: calls},
		{From: "you", Text: "the results block", Results: results},
	}, "")
	if len(contents) != 3 {
		t.Fatalf("contents = %d (%+v), want 3 turns", len(contents), contents)
	}
	if contents[0].Role != "user" || contents[1].Role != "model" || contents[2].Role != "user" {
		t.Errorf("roles = %s/%s/%s, want user/model/user", contents[0].Role, contents[1].Role, contents[2].Role)
	}
	if got := contents[1].Parts[len(contents[1].Parts)-1].FunctionCall; got == nil || got.Name != "search" {
		t.Errorf("model turn = %+v, want a functionCall part for search", contents[1].Parts)
	}
	resp := contents[2].Parts[len(contents[2].Parts)-1].FunctionResponse
	if resp == nil || resp.Name != "search" {
		t.Fatalf("user turn = %+v, want a functionResponse part", contents[2].Parts)
	}
	var payload map[string]string
	if err := json.Unmarshal(resp.Response, &payload); err != nil {
		t.Fatalf("functionResponse body %s: %v", resp.Response, err)
	}
	if payload["status"] != "success" || payload["result"] != "OK found it" {
		t.Errorf("functionResponse = %v, want success + the outcome", payload)
	}
	// A failed run flips the status, so the model can tell OK from ERR.
	errBody := geminiResponsePart(ToolResult{Name: "search", Content: "ERR nope", Err: true}).Response
	if !strings.Contains(string(errBody), `"status":"error"`) {
		t.Errorf("error response = %s, want status error", errBody)
	}

	// Decoding an answer: the text plus every functionCall part, raw kept.
	var out geminiResponse
	if err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[`+
		`{"functionCall":{"name":"search","args":{"q":"havana"},"thoughtSignature":"sig1"}},`+
		`{"text":"checking"}]},"finishReason":"STOP"}]}`), &out); err != nil {
		t.Fatal(err)
	}
	turn, err := geminiTurn(&out)
	if err != nil {
		t.Fatalf("geminiTurn: %v", err)
	}
	if turn.Text != "checking" || len(turn.Calls) != 1 || turn.Calls[0].Name != "search" ||
		string(turn.Calls[0].Args) != `{"q":"havana"}` {
		t.Fatalf("turn = %+v, want the text plus the search call", turn)
	}
	// The raw part is echoed back untouched: a re-signed call is rejected.
	partJSON, err := json.Marshal(geminiCallPart(turn.Calls[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(partJSON), `"thoughtSignature":"sig1"`) {
		t.Errorf("echoed part = %s, want the original part (with its signature)", partJSON)
	}

	// A safety-blocked answer is an error, not an empty reply.
	var blocked geminiResponse
	if err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[]},"finishReason":"SAFETY"}]}`), &blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := geminiTurn(&blocked); err == nil || !strings.Contains(err.Error(), "safety") {
		t.Errorf("blocked answer err = %v, want a safety error", err)
	}
}

// TestGeminiToolRequestRoundTrip: the tools field really reaches the
// generateContent body, and a functionCall answer comes back as a ToolTurn.
func TestGeminiToolRequestRoundTrip(t *testing.T) {
	var gotPath string
	var body struct {
		Tools []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		SystemInstruction *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"on it"},`+
			`{"functionCall":{"name":"search","args":{"q":"havana"}}}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()

	g := &geminiProvider{apiKey: "k", apiURL: srv.URL, model: "gemini-test", http: srv.Client()}
	turn, err := g.GenerateWithTools("sys", nil, "play havana", wireToolDefs(), nil)
	if err != nil {
		t.Fatalf("GenerateWithTools: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/models/gemini-test:generateContent") {
		t.Errorf("path = %q, want the generateContent endpoint", gotPath)
	}
	if len(body.Tools) != 1 || body.Tools[0].FunctionDeclarations[0].Name != "search" {
		t.Errorf("tools = %+v, want the search declaration", body.Tools)
	}
	if body.SystemInstruction == nil || body.SystemInstruction.Parts[0].Text != "sys" {
		t.Errorf("systemInstruction = %+v, want the system prompt", body.SystemInstruction)
	}
	if turn.Text != "on it" || len(turn.Calls) != 1 || turn.Calls[0].Name != "search" {
		t.Errorf("turn = %+v, want the text plus the search call", turn)
	}
}

// TestOllamaToolDialect: /api/chat advertises the abilities as tools, reads a
// message.tool_calls reply (arguments are an object here, unlike OpenAI's JSON
// string), and a 4xx to a tool request retires the tool API so later replies go
// straight to the [AGENT: ...] tag path.
func TestOllamaToolDialect(t *testing.T) {
	var body struct {
		Messages []struct {
			Role     string `json:"role"`
			Content  string `json:"content"`
			ToolName string `json:"tool_name"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want /api/chat", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"on it",`+
			`"tool_calls":[{"function":{"name":"search","arguments":{"q":"havana"}}}]}}`)
	}))
	defer srv.Close()

	o := newOllamaProvider(srv.URL, "qwen2:1.5b").(*ollamaProvider)
	turn, err := o.GenerateWithTools("sys", nil, "play havana", wireToolDefs(), nil)
	if err != nil {
		t.Fatalf("GenerateWithTools: %v", err)
	}
	if len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Function.Name != "search" {
		t.Errorf("tools = %+v, want the search function", body.Tools)
	}
	if turn.Text != "on it" || len(turn.Calls) != 1 ||
		turn.Calls[0].Name != "search" || string(turn.Calls[0].Args) != `{"q":"havana"}` {
		t.Errorf("turn = %+v, want the text plus the search call", turn)
	}

	// The exchange goes back as an assistant tool_calls turn plus a role:"tool"
	// answer carrying tool_name (ollama's calls have no id to pair on).
	calls, results := wireExchange()
	msgs := ollamaMessages("sys", []Msg{
		{From: "you", Text: "play havana"},
		{From: "Buddy", Text: "on it", Calls: calls},
		{From: "you", Text: "the results block", Results: results},
	}, "the results block")
	if len(msgs) != 5 {
		t.Fatalf("messages = %d (%+v), want system + 4 turns", len(msgs), msgs)
	}
	if msgs[2].Role != "assistant" || len(msgs[2].ToolCalls) != 1 ||
		msgs[2].ToolCalls[0].Function.Name != "search" ||
		string(msgs[2].ToolCalls[0].Function.Arguments) != `{"q":"havana"}` {
		t.Errorf("assistant turn = %+v, want the call echoed back", msgs[2])
	}
	if msgs[3].Role != "tool" || msgs[3].ToolName != "search" || msgs[3].Content != "OK found it" {
		t.Errorf("tool turn = %+v, want role tool / tool_name search / the outcome", msgs[3])
	}
	if msgs[4].Role != "user" {
		t.Errorf("last turn role = %q, want user (the results block follows)", msgs[4].Role)
	}

	// A server that rejects the tools payload must leave no doubt: the 4xx
	// retires the API (the loop's trigger to fall back to tags).
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"tools unsupported"}`, http.StatusBadRequest)
	}))
	defer bad.Close()
	old := newOllamaProvider(bad.URL, "qwen2:1.5b").(*ollamaProvider)
	if _, err := old.GenerateWithTools("sys", nil, "hi", wireToolDefs(), nil); err == nil {
		t.Fatal("a 4xx answer to a tool request must be an error")
	}
	if !old.ToolsUnsupported() {
		t.Error("a 4xx with tools must retire the tool API")
	}
	// A text-only call against the same broken server must NOT set the flag:
	// a lost server is the model's problem, not evidence about tool support.
	if _, err := old.GenerateText("sys", nil, "hi"); err == nil {
		t.Fatal("the text-only call should still fail against the broken server")
	}
	if !old.ToolsUnsupported() {
		t.Error("a text-only failure must not flip the retirement flag")
	}

	// A hailo-ollama style HTTP 500 (deserialization mapper error on unknown
	// field) must also retire the tool API, while a generic 500 does not.
	hailoBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"No suitable mapper found to deserialize the request body"}`, http.StatusInternalServerError)
	}))
	defer hailoBad.Close()
	hailoProv := newOllamaProvider(hailoBad.URL, "qwen2:1.5b").(*ollamaProvider)
	if _, err := hailoProv.GenerateWithTools("sys", nil, "hi", wireToolDefs(), nil); err == nil {
		t.Fatal("hailo 500 must return an error")
	}
	if !hailoProv.ToolsUnsupported() {
		t.Error("hailo mapper 500 must retire the tool API")
	}

	generic500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"database unavailable"}`, http.StatusInternalServerError)
	}))
	defer generic500.Close()
	genericProv := newOllamaProvider(generic500.URL, "qwen2:1.5b").(*ollamaProvider)
	if _, err := genericProv.GenerateWithTools("sys", nil, "hi", wireToolDefs(), nil); err == nil {
		t.Fatal("generic 500 must return an error")
	}
	if genericProv.ToolsUnsupported() {
		t.Error("a generic 500 must NOT retire the tool API")
	}
}

// TestBedrockToolDialect covers the Converse dialect without AWS credentials:
// the toolConfig, the toolUse/toolResult blocks (paired on the tool-use id,
// synthesized when the model sent none) and the messages encoder.
func TestBedrockToolDialect(t *testing.T) {
	cfg := bedrockToolConfig(wireToolDefs())
	if cfg == nil || len(cfg.Tools) != 1 {
		t.Fatalf("toolConfig = %+v, want one tool", cfg)
	}
	spec, ok := cfg.Tools[0].(*types.ToolMemberToolSpec)
	if !ok {
		t.Fatalf("tool = %T, want a toolSpec", cfg.Tools[0])
	}
	if strPtrValue(spec.Value.Name) != "search" || strPtrValue(spec.Value.Description) != "Find a song." {
		t.Errorf("spec = %+v, want the search ability", spec.Value)
	}
	jsonSchema, ok := spec.Value.InputSchema.(*types.ToolInputSchemaMemberJson)
	if !ok {
		t.Fatalf("input schema = %T, want JSON", spec.Value.InputSchema)
	}
	var schema map[string]any
	if raw, err := jsonSchema.Value.MarshalSmithyDocument(); err != nil {
		t.Fatalf("encode input schema: %v", err)
	} else if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode input schema %s: %v", raw, err)
	}
	if props, _ := schema["properties"].(map[string]any); props["q"] == nil {
		t.Errorf("schema = %v, want the q property", schema)
	}
	if bedrockToolConfig(nil) != nil {
		t.Error("no abilities must mean no toolConfig")
	}

	// A call without an id gets a synthetic one - and the result block must
	// reuse the SAME id, or Converse rejects the pair.
	use := bedrockUseBlock(ToolCall{Name: "search", Args: json.RawMessage(`{"q":"havana"}`)})
	if strPtrValue(use.ToolUseId) != "call_search" || strPtrValue(use.Name) != "search" {
		t.Errorf("toolUse = %+v, want the synthetic call_search id", use)
	}
	var input map[string]any
	if use.Input == nil {
		t.Fatal("toolUse must carry its input document")
	}
	if raw, err := use.Input.MarshalSmithyDocument(); err != nil {
		t.Fatalf("encode toolUse input: %v", err)
	} else if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatalf("decode toolUse input %s: %v", raw, err)
	}
	if input["q"] != "havana" {
		t.Errorf("toolUse input = %v, want q=havana", input)
	}
	okResult := bedrockResultBlock(ToolResult{Name: "search", Content: "OK found it"})
	if strPtrValue(okResult.ToolUseId) != strPtrValue(use.ToolUseId) {
		t.Errorf("result id = %q, want the call's %q",
			strPtrValue(okResult.ToolUseId), strPtrValue(use.ToolUseId))
	}
	if okResult.Status != types.ToolResultStatusSuccess {
		t.Errorf("status = %q, want success", okResult.Status)
	}
	if text, ok := okResult.Content[0].(*types.ToolResultContentBlockMemberText); !ok || text.Value != "OK found it" {
		t.Errorf("content = %+v, want the outcome as text", okResult.Content)
	}
	if failed := bedrockResultBlock(ToolResult{Name: "search", Content: "ERR nope", Err: true}); failed.Status != types.ToolResultStatusError {
		t.Errorf("failed status = %q, want error", failed.Status)
	}

	// Decoding a toolUse answer block into the loop's dialect.
	decoded := bedrockCall(types.ToolUseBlock{
		ToolUseId: strPtr("c1"),
		Name:      strPtr("search"),
		Input:     document.NewLazyDocument(map[string]any{"q": "havana"}),
	})
	if decoded.ID != "c1" || decoded.Name != "search" || string(decoded.Args) != `{"q":"havana"}` {
		t.Errorf("decoded = %+v, want c1/search/{\"q\":\"havana\"}", decoded)
	}

	// The exchange the loop feeds back becomes toolUse/toolResult blocks.
	calls, results := wireExchange()
	msgs := bedrockMessages([]Msg{
		{From: "you", Text: "play havana"},
		{From: "Buddy", Text: "on it", Calls: calls},
		{From: "you", Text: "the results block", Results: results},
	}, "")
	if len(msgs) != 3 {
		t.Fatalf("messages = %d (%+v), want 3 turns", len(msgs), msgs)
	}
	wantRoles := []types.ConversationRole{
		types.ConversationRoleUser, types.ConversationRoleAssistant, types.ConversationRoleUser,
	}
	for i, want := range wantRoles {
		if msgs[i].Role != want {
			t.Errorf("messages[%d].Role = %q, want %q", i, msgs[i].Role, want)
		}
	}
	if _, ok := msgs[1].Content[len(msgs[1].Content)-1].(*types.ContentBlockMemberToolUse); !ok {
		t.Errorf("assistant turn = %+v, want a trailing toolUse block", msgs[1].Content)
	}
	if _, ok := msgs[2].Content[len(msgs[2].Content)-1].(*types.ContentBlockMemberToolResult); !ok {
		t.Errorf("user turn = %+v, want a trailing toolResult block", msgs[2].Content)
	}
}
