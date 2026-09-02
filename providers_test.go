package main

import "testing"

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
}
