package main

// geminitest - standalone Google Gemini API probe for debugging chat-app's
// brain, without the X11 UI in the way.
//
//   go run ./cmd/geminitest -models                # list models the key can use
//   go run ./cmd/geminitest "hello there"          # one generateContent call
//   go run ./cmd/geminitest -model gemini-2.0-flash "hi"
//   go run ./cmd/geminitest -key AIza... -v "hi"   # explicit key + raw request
//   go run ./cmd/geminitest -system "be concise" "hi"  # with system instruction
//   go run ./cmd/geminitest -system-file prompt.txt "hi"
//   go run ./cmd/geminitest -image "Bali"           # test image fetch path
//
// The key comes from -key, $GEMINI_API_KEY, $GOOGLE_API_KEY or (last resort)
// the same built-in key chat.go uses. Diagnostics print the masked key, HTTP
// status, raw response body and a hint for the most common failure modes.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	base = "https://generativelanguage.googleapis.com" // keep in sync with chat.go defaultAPIURL

	// keep in sync with chat.go defaultAPIKey:
	defaultKey = "AIzaSyB7YR3ypNW2A-raPItTfLir-B-vKuuzyR8"
)

type part struct {
	Text string `json:"text"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

func main() {
	var (
		key     = flag.String("key", "", "API key (default $GEMINI_API_KEY / $GOOGLE_API_KEY)")
		model   = flag.String("model", "gemini-3.6-flash", "model name for generateContent")
		baseURL = flag.String("url", base, "API base URL (for relays/mirrors)")
		list    = flag.Bool("models", false, "list models available to this key, then exit")
		timeout = flag.Duration("timeout", 30*time.Second, "HTTP timeout")
		verbose = flag.Bool("v", false, "print the raw request body too")
		system  = flag.String("system", "", "Gemini system instruction (omit = no system prompt)")
		sysFile = flag.String("system-file", "", "load system instruction from file")
		imgTest = flag.String("image", "", "test image fetch: downloads thumbnail for keyword and exits")
	)
	flag.Parse()

	if *key == "" {
		*key = os.Getenv("GEMINI_API_KEY")
	}
	if *key == "" {
		*key = os.Getenv("GOOGLE_API_KEY")
	}
	if *key == "" {
		*key = defaultKey // same built-in key as chat-app
	}
	*key = strings.TrimSpace(*key)

	client := &http.Client{Timeout: *timeout}

	// Package-level helpers from the main package are not visible here, so the
	// -image test is intentionally in the main binary (./chat-app -fetch-image).
	// Keep this flag for future expansion.
	_ = imgTest

	if *imgTest != "" {
		fmt.Println("image fetch test is now: ./chat-app -fetch-image <keyword>")
		return
	}

	fmt.Printf("key: %s...%s (len %d)\n", (*key)[:min(4, len(*key))], (*key)[max(0, len(*key)-4):], len(*key))
	if *list {
		listModels(client, *key, *baseURL)
		return
	}

	prompt := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if prompt == "" {
		prompt = "say hi in five words"
	}

	// Resolve system instruction: -system flag wins, then -system-file
	sysPrompt := strings.TrimSpace(*system)
	if sysPrompt == "" && *sysFile != "" && *sysFile != "off" {
		b, err := os.ReadFile(*sysFile)
		if err != nil {
			fmt.Printf("warning: cannot read %s: %v\n", *sysFile, err)
		} else {
			sysPrompt = strings.TrimSpace(string(b))
		}
	}

	generate(client, *key, *baseURL, *model, prompt, sysPrompt, *verbose)
}

func maskKey(k string) string {
	if len(k) <= 8 {
		return "****"
	}
	return k[:4] + "..." + k[len(k)-4:]
}

// listModels calls GET /v1beta/models and prints every model that supports
// generateContent - the definitive answer to "which model name works?".
func listModels(c *http.Client, key, baseURL string) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1beta/models?pageSize=1000", nil)
	if err != nil {
		fmt.Println("build request:", err)
		os.Exit(1)
	}
	req.Header.Set("x-goog-api-key", key)

	resp, err := c.Do(req)
	if err != nil {
		fmt.Println("NETWORK ERROR:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	fmt.Println("HTTP", resp.StatusCode)
	if resp.StatusCode != 200 {
		fmt.Println(string(raw))
		hint(resp.StatusCode)
		os.Exit(1)
	}

	var out struct {
		Models []struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Methods     []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fmt.Println("unparseable body:", string(raw))
		os.Exit(1)
	}
	fmt.Printf("models available to this key (generateContent-capable):\n")
	n := 0
	for _, m := range out.Models {
		ok := false
		for _, meth := range m.Methods {
			if meth == "generateContent" {
				ok = true
			}
		}
		if !ok {
			continue
		}
		fmt.Printf("  %-42s %s\n", m.Name, m.DisplayName)
		n++
	}
	if n == 0 {
		fmt.Println("  (none!?)")
	}
}

// generate fires one generateContent call and prints everything.
func generate(c *http.Client, key, baseURL, model, prompt, sysPrompt string, verbose bool) {
	type reqBody struct {
		Contents          []content `json:"contents"`
		SystemInstruction *content  `json:"systemInstruction,omitempty"`
	}
	body := reqBody{Contents: []content{{Role: "user", Parts: []part{{Text: prompt}}}}}
	if sysPrompt != "" {
		body.SystemInstruction = &content{Parts: []part{{Text: sysPrompt}}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		fmt.Println("json marshal:", err)
		os.Exit(1)
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", baseURL, model)
	fmt.Println("POST", url)
	if verbose {
		fmt.Println("request body:", string(raw))
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		fmt.Println("build request:", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)

	resp, err := c.Do(req)
	if err != nil {
		fmt.Println("NETWORK ERROR:", err)
		fmt.Println("(is the Pi online? can it reach generativelanguage.googleapis.com?)")
		os.Exit(1)
	}
	defer resp.Body.Close()
	raw, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	fmt.Println("HTTP", resp.StatusCode)
	bodyStr := string(raw)
	if len(bodyStr) > 3000 {
		bodyStr = bodyStr[:3000] + " ...[truncated]"
	}
	fmt.Println(bodyStr)

	if resp.StatusCode != 200 {
		hint(resp.StatusCode)
		os.Exit(1)
	}

	var out struct {
		Candidates []struct {
			Content struct {
				Parts []part `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fmt.Println("unparseable 200 body:", err)
		os.Exit(1)
	}
	var sb strings.Builder
	for _, cand := range out.Candidates {
		for _, p := range cand.Content.Parts {
			sb.WriteString(p.Text)
		}
	}
	fmt.Println("\nREPLY:", strings.TrimSpace(sb.String()))
}

// hint maps HTTP status codes to the fix that usually unblocks them.
func hint(code int) {
	switch code {
	case 400:
		fmt.Println("HINT: 400 - often a malformed key (typos, quotes, whitespace).")
	case 401, 403:
		fmt.Println("HINT: 401/403 - key invalid, restricted (HTTP referrer/IP limits), " +
			"or the Generative Language API is unavailable in your region.")
	case 404:
		fmt.Println("HINT: 404 - unknown model name; run with -models to list valid ones.")
	case 429:
		fmt.Println("HINT: 429 - rate limit / quota exhausted on this key.")
	}
}
