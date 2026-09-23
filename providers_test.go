package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsOllamaModel(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"qwen2:1.5b", true},
		{"llama3.2:3b", true},
		{"mistral:latest", true},
		{"QWEN2:1.5B", true},
		{"gemini-3.6-flash", false},
		{"amazon.nova-lite-v1:0", false},
		{"anthropic.claude-3-5-sonnet-20240620-v1:0", false},
		{"my-model", false},
		{":tag", false},
		{"model:", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isOllamaModel(tc.id); got != tc.want {
			t.Errorf("isOllamaModel(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestIsGeminiModel(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"gemini-3.6-flash", true},
		{"gemini-2.0-flash", true},
		{"GEMINI-1.5-PRO", true},
		{"amazon.nova-lite-v1:0", false},
		{"anthropic.claude-3-5-sonnet-20240620-v1:0", false},
		{"", false},
		{"my-model", false},
	}
	for _, tc := range cases {
		if got := isGeminiModel(tc.id); got != tc.want {
			t.Errorf("isGeminiModel(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestIsBedrockModel(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"amazon.nova-lite-v1:0", true},
		{"amazon.nova-pro-v1:0", true},
		{"anthropic.claude-3-5-sonnet-20240620-v1:0", true},
		{"meta.llama3-70b-instruct-v1:0", true},
		{"mistral.mistral-large-2402-v1:0", true},
		{"cohere.command-text-v14", true},
		{"gemini-3.6-flash", false},
		{"", false},
		{"my-model", false},
	}
	for _, tc := range cases {
		if got := isBedrockModel(tc.id); got != tc.want {
			t.Errorf("isBedrockModel(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestPickModel(t *testing.T) {
	t.Run("defaults per provider", func(t *testing.T) {
		cases := []struct {
			provider, want string
		}{
			{"bedrock", defaultBedrockModelID},
			{"gemini", defaultModel},
			{"openrouter", defaultOpenRouterModel},
		}
		for _, tc := range cases {
			m, warn := pickModel("", false, "", tc.provider)
			if m != tc.want || warn != "" {
				t.Errorf("pickModel(provider=%s) = (%q, %q), want (%q, \"\")",
					tc.provider, m, warn, tc.want)
			}
		}
	})

	t.Run("explicit flag always wins", func(t *testing.T) {
		m, warn := pickModel("amazon.nova-micro-v1:0", true, "gemini-3.6-flash", "bedrock")
		if m != "amazon.nova-micro-v1:0" {
			t.Errorf("explicit flag: got %q", m)
		}
		if warn != "" {
			t.Errorf("explicit flag should not warn, got %q", warn)
		}
	})

	t.Run("leftover gemini model with bedrock is swapped", func(t *testing.T) {
		m, warn := pickModel("", false, "gemini-3.6-flash", "bedrock")
		if m != defaultBedrockModelID {
			t.Errorf("got %q, want %q (auto-swap to the Bedrock default)", m, defaultBedrockModelID)
		}
		if warn == "" {
			t.Error("expected a warning explaining the swap")
		}
	})

	t.Run("leftover bedrock model with gemini is swapped", func(t *testing.T) {
		m, warn := pickModel("", false, "amazon.nova-lite-v1:0", "gemini")
		if m != defaultModel {
			t.Errorf("got %q, want %q (auto-swap to the Gemini default)", m, defaultModel)
		}
		if warn == "" {
			t.Error("expected a warning explaining the swap")
		}
	})

	t.Run("valid config model is kept", func(t *testing.T) {
		m, warn := pickModel("", false, "amazon.nova-pro-v1:0", "bedrock")
		if m != "amazon.nova-pro-v1:0" || warn != "" {
			t.Errorf("got (%q, %q), want (amazon.nova-pro-v1:0, \"\")", m, warn)
		}
	})

	t.Run("explicit wrong-family model warns but is kept", func(t *testing.T) {
		m, warn := pickModel("gemini-2.0-flash", true, "", "bedrock")
		if m != "gemini-2.0-flash" {
			t.Errorf("explicit -model must be kept, got %q", m)
		}
		if warn == "" {
			t.Error("expected a warning that the model looks wrong for bedrock")
		}
	})

	t.Run("openrouter family swap", func(t *testing.T) {
		// empty model -> openrouter default
		m, warn := pickModel("", false, "", "openrouter")
		if m != defaultOpenRouterModel || warn != "" {
			t.Errorf("empty openrouter: got (%q,%q), want (%q,\"\")", m, warn, defaultOpenRouterModel)
		}
		// leftover Gemini ID from config is swapped + warned
		m, warn = pickModel("", false, "gemini-3.6-flash", "openrouter")
		if m != defaultOpenRouterModel || warn == "" {
			t.Errorf("gemini id with openrouter: got (%q,%q), want (%q, <warn>)", m, warn, defaultOpenRouterModel)
		}
		// leftover Bedrock ID from config is swapped + warned
		m, warn = pickModel("", false, "amazon.nova-lite-v1:0", "openrouter")
		if m != defaultOpenRouterModel || warn == "" {
			t.Errorf("bedrock id with openrouter: got (%q,%q), want (%q, <warn>)", m, warn, defaultOpenRouterModel)
		}
		// valid OpenRouter ID kept, no warning
		m, warn = pickModel("deepseek/deepseek-chat-v3-0324", false, "", "openrouter")
		if m != "deepseek/deepseek-chat-v3-0324" || warn != "" {
			t.Errorf("explicit openrouter id: got (%q,%q), want (id, \"\")", m, warn)
		}
	})
}

func TestNewOpenRouterProvider(t *testing.T) {
	// An empty model falls back to the openrouter default, and a trailing slash
	// on the URL is trimmed.
	p := newOpenRouterProvider("sk-test", "https://openrouter.ai/api/v1/", "", false)
	op := p.(*openrouterProvider)
	if op.model != defaultOpenRouterModel {
		t.Errorf("model: got %q, want %q", op.model, defaultOpenRouterModel)
	}
	if op.apiURL != "https://openrouter.ai/api/v1" {
		t.Errorf("apiURL not trimmed: got %q", op.apiURL)
	}
	if op.apiKey != "sk-test" {
		t.Errorf("apiKey: got %q, want %q", op.apiKey, "sk-test")
	}
}

func TestOpenRouterProviderNoKey(t *testing.T) {
	p := newOpenRouterProvider("", "https://openrouter.ai/api/v1", "deepseek/deepseek-chat", false)
	if _, err := p.GenerateText("sys", nil, "hi"); err == nil {
		t.Fatal("expected an error for a missing key, got nil")
	}
}

func TestOpenRouterProviderRoundTrip(t *testing.T) {
	var gotAuth, gotPath, gotModel string
	var gotStream bool
	var gotMsgs []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		var payload struct {
			Model    string              `json:"model"`
			Messages []map[string]string `json:"messages"`
			Stream   bool                `json:"stream"`
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		gotModel = payload.Model
		gotStream = payload.Stream
		gotMsgs = payload.Messages
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi there"}}]}`))
	}))
	defer srv.Close()

	p := newOpenRouterProvider("sk-test", srv.URL, "deepseek/deepseek-chat", false)
	got, err := p.GenerateText("you are helpful", []Msg{{From: "you", Text: "hello?"}}, "")
	if err != nil {
		t.Fatalf("GenerateText: %v", err)
	}
	if got != "hi there" {
		t.Errorf("content: got %q, want %q", got, "hi there")
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization header: got %q, want %q", gotAuth, "Bearer sk-test")
	}
	if gotPath != "/chat/completions" {
		t.Errorf("endpoint path: got %q, want %q", gotPath, "/chat/completions")
	}
	if gotModel != "deepseek/deepseek-chat" {
		t.Errorf("model: got %q, want %q", gotModel, "deepseek/deepseek-chat")
	}
	if gotStream {
		t.Error("expected stream=false")
	}
	// System message first, then the history user turn; userText is "" and the
	// last history turn is already "you", so nothing extra is appended.
	wantMsgs := []map[string]string{
		{"role": "system", "content": "you are helpful"},
		{"role": "user", "content": "hello?"},
	}
	if len(gotMsgs) != len(wantMsgs) {
		t.Fatalf("messages: got %d, want %d (%+v)", len(gotMsgs), len(wantMsgs), gotMsgs)
	}
	for i, want := range wantMsgs {
		if gotMsgs[i]["role"] != want["role"] || gotMsgs[i]["content"] != want["content"] {
			t.Errorf("message[%d]: got %+v, want %+v", i, gotMsgs[i], want)
		}
	}
}

// TestOpenRouterProviderStreamRoundTrip verifies the SSE path: the request
// carries stream=true and the reply is assembled from data: delta events,
// with onDelta called after every non-empty fragment carrying the
// accumulated text so far.
func TestOpenRouterProviderStreamRoundTrip(t *testing.T) {
	var gotAuth, gotPath, gotModel string
	var gotStream bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		var payload struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		gotModel = payload.Model
		gotStream = payload.Stream
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		events := []string{
			`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}`,
			`data: {"choices":[{"delta":{"content":"Hi "}}]}`,
			"",
			`data: {"choices":[{"delta":{"content":"there"}}]}`,
			`data: [DONE]`,
			"",
		}
		for _, ev := range events {
			_, _ = io.WriteString(w, ev+"\n")
			fl.Flush()
		}
	}))
	defer srv.Close()

	p := newOpenRouterProvider("sk-test", srv.URL, "deepseek/deepseek-chat", true).(*openrouterProvider)
	var snapshots []string
	got, err := p.GenerateTextStream("sys", nil, "hello", func(acc string) {
		snapshots = append(snapshots, acc)
	})
	if err != nil {
		t.Fatalf("GenerateTextStream: %v", err)
	}
	if got != "Hi there" {
		t.Errorf("content: got %q, want %q", got, "Hi there")
	}
	if !gotStream {
		t.Error("expected stream=true")
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization header: got %q, want %q", gotAuth, "Bearer sk-test")
	}
	if gotPath != "/chat/completions" {
		t.Errorf("endpoint path: got %q, want %q", gotPath, "/chat/completions")
	}
	if gotModel != "deepseek/deepseek-chat" {
		t.Errorf("model: got %q, want %q", gotModel, "deepseek/deepseek-chat")
	}
	// One snapshot per non-empty fragment; the role-only opener is skipped.
	want := []string{"Hi ", "Hi there"}
	if len(snapshots) != len(want) {
		t.Fatalf("deltas: got %d (%q), want %d (%q)", len(snapshots), snapshots, len(want), want)
	}
	for i := range want {
		if snapshots[i] != want[i] {
			t.Errorf("delta[%d]: got %q, want %q", i, snapshots[i], want[i])
		}
	}

	// GenerateText must buffer the same stream when no callback is given.
	full, err := p.GenerateText("sys", nil, "hello")
	if err != nil {
		t.Fatalf("GenerateText over SSE: %v", err)
	}
	if full != "Hi there" {
		t.Errorf("GenerateText over SSE: got %q, want %q", full, "Hi there")
	}
}

// TestOpenRouterProviderStreamError verifies a mid-stream error object
// aborts with its message instead of returning a partial reply.
func TestOpenRouterProviderStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"error":{"message":"quota exceeded"}}`+"\n\n")
	}))
	defer srv.Close()
	p := newOpenRouterProvider("sk-test", srv.URL, "deepseek/deepseek-chat", true)
	if _, err := p.GenerateText("sys", nil, "hello"); err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("expected the mid-stream quota error, got %v", err)
	}
}

// TestOpenRouterProviderStreamHTTPError: a non-200 answer arrives as plain
// JSON, so the status check must fire before any SSE parsing.
func TestOpenRouterProviderStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newOpenRouterProvider("sk-test", srv.URL, "deepseek/deepseek-chat", true)
	_, err := p.GenerateText("sys", nil, "hello")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("expected HTTP 401, got %v", err)
	}
}

// TestOpenRouterProviderStreamEmpty: a stream that produces no text fails
// like the single-shot path ("empty answer").
func TestOpenRouterProviderStreamEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	p := newOpenRouterProvider("sk-test", srv.URL, "deepseek/deepseek-chat", true)
	if _, err := p.GenerateText("sys", nil, "hello"); err == nil || err.Error() != "empty answer" {
		t.Fatalf("expected \"empty answer\", got %v", err)
	}
}
