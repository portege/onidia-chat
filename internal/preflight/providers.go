// providers.go - the provider requirements checks (the "is the selected LLM
// backend usable" half of the package).
package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// Keep-in-sync defaults: these mirror chat-app's package-main constants
// (chat.go: defaultAPIURL/defaultModel, providers.go: defaultOllamaURL/
// defaultOllamaModel/defaultOpenRouterURL/defaultOpenRouterModel/
// defaultBedrockModelID). The CLI resolves them the same way main.go does;
// the copy exists because package main is not importable - same rule as
// cmd/geminitest's built-in key.
const (
	defaultGeminiURL       = "https://generativelanguage.googleapis.com"
	defaultGeminiModel     = "gemini-3.6-flash"
	defaultOllamaURL       = "http://localhost:8000"
	defaultOllamaModel     = "qwen2:1.5b"
	defaultOpenRouterURL   = "https://openrouter.ai/api/v1"
	defaultOpenRouterModel = "deepseek/deepseek-chat-v3-0324"
	defaultBedrockModel    = "amazon.nova-lite-v1:0"
)

// Spec is the resolved provider configuration under test: exactly what
// chat-app would launch with after flag/env/config precedence. APIKey is the
// Gemini key for provider=gemini and the OpenRouter token for
// provider=openrouter (the fields are shared across providers, like
// chat-app.ini's single api-key); AWS* only matters for bedrock. Deep adds a
// real (1-token) Bedrock Converse call on top of the credential check.
type Spec struct {
	Provider   string
	APIURL     string
	Model      string
	APIKey     string
	AWSProfile string
	AWSRegion  string
	Deep       bool
}

// Checks returns the requirement checks for the selected provider: one
// config check, then the backend's own key/server checks (fatal where the app
// cannot work without them).
func Checks(spec Spec) []Check {
	checks := []Check{providerConfigCheck(spec.Provider)}
	switch spec.Provider {
	case "gemini":
		checks = append(checks, geminiChecks(spec)...)
	case "bedrock":
		checks = append(checks, bedrockChecks(spec)...)
	case "ollama":
		checks = append(checks, ollamaChecks(spec)...)
	case "openrouter":
		checks = append(checks, openrouterChecks(spec)...)
	}
	return checks
}

// providerConfigCheck guards against a garbage provider name before the
// backend-specific checks run (main.go normalizes it, but the CLI can be
// pointed at anything).
func providerConfigCheck(provider string) Check {
	return Check{
		ID:       "config.provider",
		Kind:     KindConfig,
		Severity: SeverityFatal,
		Run: func(context.Context) Outcome {
			for _, p := range []string{"gemini", "bedrock", "ollama", "openrouter"} {
				if provider == p {
					return Pass(p)
				}
			}
			return Fail(fmt.Sprintf("unknown provider %q", provider),
				"use one of: gemini, bedrock, ollama, openrouter (provider = ... in chat-app.ini, or -provider)")
		},
	}
}

// httpGet issues a bounded GET and returns status + body (capped at 1 MiB).
// The caller's ctx carries the timeout, so no client-side deadline is set.
func httpGet(ctx context.Context, rawURL string, header map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
}

// errDetail compresses a probe error for one report line.
func errDetail(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "no answer before the timeout"
	}
	s := err.Error()
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

// urlHost renders just the host of a base URL for report lines.
func urlHost(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

// orDefault returns v, or def when v is blank (after trimming slashes).
func orDefault(v, def string) string {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v == "" {
		return def
	}
	return v
}

// geminiChecks: the key must exist (warn - a missing key is chat-app's
// documented stub mode, the app still opens) and the API must accept it
// (fatal). The probe is the free GET /v1beta/models listing, never a paid
// generateContent call.
func geminiChecks(spec Spec) []Check {
	base := orDefault(spec.APIURL, defaultGeminiURL)
	key := strings.TrimSpace(spec.APIKey)
	return []Check{
		{
			ID:       "gemini.key",
			Kind:     KindConfig,
			Severity: SeverityWarn,
			Run: func(context.Context) Outcome {
				if key == "" {
					return Fail("no API key - chat-app runs in stub mode (replies echo your message)",
						"export GEMINI_API_KEY=... or set api-key in chat-app.ini (api-key = off forces stub mode)")
				}
				return Pass("key " + MaskKey(key))
			},
		},
		{
			ID:       "gemini.api",
			Kind:     KindLive,
			Severity: SeverityFatal,
			Run: func(ctx context.Context) Outcome {
				if key == "" {
					return Skip("no API key to test (see gemini.key)")
				}
				st, body, err := httpGet(ctx, base+"/v1beta/models",
					map[string]string{"x-goog-api-key": key})
				if err != nil {
					return Fail("cannot reach "+base+"/v1beta/models: "+errDetail(err),
						"check connectivity, or point api-url at a relay/mirror; raw probe: go run ./cmd/geminitest -models")
				}
				if st == 401 || st == 403 {
					return Fail(fmt.Sprintf("HTTP %d - the key was rejected", st),
						"key invalid, restricted, or the API is blocked in your region - set a fresh $GEMINI_API_KEY")
				}
				if st != 200 {
					return Fail(fmt.Sprintf("HTTP %d from %s/v1beta/models", st, urlHost(base)),
						"raw probe: go run ./cmd/geminitest -models")
				}
				var out struct {
					Models []json.RawMessage `json:"models"`
				}
				if err := json.Unmarshal(body, &out); err != nil {
					return Fail("unparseable /v1beta/models body", "raw probe: go run ./cmd/geminitest -models")
				}
				return Pass(fmt.Sprintf("%s reachable (%d models for this key)", urlHost(base), len(out.Models)))
			},
		},
	}
}

// ollamaChecks: the server must answer its own dialect (fatal), and the
// configured model tag must exist on it (warn - chat-app would 404 on send).
// Both probe GET /api/tags, the cheapest ollama-native liveness endpoint.
func ollamaChecks(spec Spec) []Check {
	base := orDefault(spec.APIURL, defaultOllamaURL)
	model := strings.TrimSpace(spec.Model)
	if model == "" {
		model = defaultOllamaModel
	}

	// tags fetches the model list once per check; on any failure it reports
	// why so the dependent check can skip with a pointer instead of failing.
	fetch := func(ctx context.Context) (models []string, detail string, ok bool) {
		st, body, err := httpGet(ctx, base+"/api/tags", nil)
		if err != nil {
			return nil, errDetail(err), false
		}
		if st != 200 {
			return nil, fmt.Sprintf("HTTP %d", st), false
		}
		var out struct {
			Models []struct {
				Name  string `json:"name"`
				Model string `json:"model"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, "unparseable /api/tags body", false
		}
		for _, m := range out.Models {
			if m.Name != "" {
				models = append(models, m.Name)
			} else {
				models = append(models, m.Model)
			}
		}
		return models, "", true
	}

	return []Check{
		{
			ID:       "ollama.server",
			Kind:     KindLive,
			Severity: SeverityFatal,
			Run: func(ctx context.Context) Outcome {
				_, detail, ok := fetch(ctx)
				switch {
				case ok:
					return Pass(fmt.Sprintf("%s answers /api/tags", base))
				case detail == "unparseable /api/tags body" || strings.HasPrefix(detail, "HTTP"):
					return Fail(fmt.Sprintf("%s returned %s - not an ollama-compatible server?", base, detail),
						"check api-url in chat-app.ini (default http://localhost:8000)")
				default:
					return Fail("no answer from "+base+"/api/tags: "+detail,
						"start the server (ollama serve, or the llama.cpp server on the hailo box) or point api-url at it")
				}
			},
		},
		{
			ID:       "ollama.model",
			Kind:     KindLive,
			Severity: SeverityWarn,
			Run: func(ctx context.Context) Outcome {
				models, detail, ok := fetch(ctx)
				if !ok {
					return Skip("cannot list models: " + detail + " (see ollama.server)")
				}
				for _, m := range models {
					if m == model {
						return Pass(fmt.Sprintf("model %q on the server", model))
					}
				}
				return Fail(fmt.Sprintf("model %q not on the server (%d available)", model, len(models)),
					fmt.Sprintf("ollama pull %s (or set model in chat-app.ini)", model))
			},
		},
	}
}

// openrouterChecks: there is no built-in OpenRouter key (unlike Gemini), so
// a missing one is fatal - chat-app would fail on the first send. The API
// probe is the free GET /models listing of the OpenAI dialect.
func openrouterChecks(spec Spec) []Check {
	base := orDefault(spec.APIURL, defaultOpenRouterURL)
	key := strings.TrimSpace(spec.APIKey)
	return []Check{
		{
			ID:       "openrouter.key",
			Kind:     KindConfig,
			Severity: SeverityFatal,
			Run: func(context.Context) Outcome {
				if key == "" {
					return Fail("no API key",
						"export OPENROUTER_API_KEY=... or set api-key in chat-app.ini (no built-in key for this backend)")
				}
				return Pass("key " + MaskKey(key))
			},
		},
		{
			ID:       "openrouter.api",
			Kind:     KindLive,
			Severity: SeverityFatal,
			Run: func(ctx context.Context) Outcome {
				if key == "" {
					return Skip("no API key to test (see openrouter.key)")
				}
				st, body, err := httpGet(ctx, base+"/models",
					map[string]string{"Authorization": "Bearer " + key})
				if err != nil {
					return Fail("cannot reach "+base+"/models: "+errDetail(err),
						"check connectivity, or point api-url at the gateway you meant (default https://openrouter.ai/api/v1)")
				}
				if st == 401 || st == 403 {
					return Fail(fmt.Sprintf("HTTP %d - the key was rejected", st),
						"key invalid for this endpoint - set the matching $OPENROUTER_API_KEY / api-key")
				}
				if st != 200 {
					return Fail(fmt.Sprintf("HTTP %d from %s/models", st, urlHost(base)),
						"check api-url: it must speak the OpenAI dialect (GET /models)")
				}
				var out struct {
					Data []json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(body, &out); err != nil {
					return Fail("unparseable /models body", "check api-url: it must speak the OpenAI dialect")
				}
				return Pass(fmt.Sprintf("%s reachable (%d models)", urlHost(base), len(out.Data)))
			},
		},
	}
}

// bedrockChecks: the AWS SDK credential chain must resolve for the configured
// profile/region (fatal - chat-app's provider construction even exits without
// it), plus an opt-in 1-token Converse call (Deep) proving model access.
func bedrockChecks(spec Spec) []Check {
	profile := strings.TrimSpace(spec.AWSProfile)
	model := strings.TrimSpace(spec.Model)
	if model == "" {
		model = defaultBedrockModel
	}

	loadConfig := func(ctx context.Context) (aws.Config, error) {
		var opts []func(*awsconfig.LoadOptions) error
		if profile != "" {
			opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
		}
		if region := strings.TrimSpace(spec.AWSRegion); region != "" {
			opts = append(opts, awsconfig.WithRegion(region))
		}
		return awsconfig.LoadDefaultConfig(ctx, opts...)
	}

	checks := []Check{
		{
			ID:       "bedrock.credentials",
			Kind:     KindConfig,
			Severity: SeverityFatal,
			Run: func(ctx context.Context) Outcome {
				shown := profile
				if shown == "" {
					shown = "default"
				}
				ac, err := loadConfig(ctx)
				if err != nil {
					return Fail(fmt.Sprintf("AWS config for profile %q: %s", shown, errDetail(err)),
						"check aws-profile / aws-region and ~/.aws/config")
				}
				if _, err := ac.Credentials.Retrieve(ctx); err != nil {
					hint := "aws configure --profile " + shown +
						" (or export AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY)"
					return Fail(fmt.Sprintf("no credentials resolve for profile %q: %s", shown, errDetail(err)),
						hint)
				}
				return Pass(fmt.Sprintf("credentials resolve for profile %q", shown))
			},
		},
	}
	if spec.Deep {
		checks = append(checks, Check{
			ID:       "bedrock.invoke",
			Kind:     KindLive,
			Severity: SeverityFatal,
			Run: func(ctx context.Context) Outcome {
				ac, err := loadConfig(ctx)
				if err != nil {
					return Fail(fmt.Sprintf("AWS config for profile %q: %s", profile, errDetail(err)),
						"check aws-profile / aws-region and ~/.aws/config")
				}
				client := bedrockruntime.NewFromConfig(ac)
				if _, err := client.Converse(ctx, &bedrockruntime.ConverseInput{
					ModelId: aws.String(model),
					Messages: []types.Message{{
						Role:    types.ConversationRoleUser,
						Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}},
					}},
				}); err != nil {
					return Fail(fmt.Sprintf("Converse on %s: %s", model, errDetail(err)),
						"check model access in the Bedrock console for this region/profile (deep probe)")
				}
				return Pass(fmt.Sprintf("%s answered the deep probe", model))
			},
		})
	}
	return checks
}
