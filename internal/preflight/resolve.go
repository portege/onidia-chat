// resolve.go - chat-app's provider/model/url resolution, shared by both
// preflight surfaces so the CLI and the startup gate always test exactly what
// the app will launch with.
package preflight

import "strings"

// Keep-in-sync: the model defaults themselves live beside the provider
// checks in providers.go (defaultGeminiModel, defaultBedrockModel,
// defaultOllamaModel, defaultOpenRouterModel) - values only. The logic below
// is the single copy of chat-app's pickModel rules: main.go delegates here.

// ResolveModel picks the effective model ID: explicit -model flag > config
// file value > provider default. A leftover model from the other provider
// family (a Gemini ID while provider=bedrock, ...) is auto-swapped to this
// backend's default and reported in the returned warning - sending it would
// fail with a confusing API error.
//
// This is chat-app main.go's pickModel: it is here so chat-app itself and
// cmd/preflight resolve the same model from the same inputs, always.
func ResolveModel(provider, flagVal string, flagSet bool, cfgModel string) (model, warn string) {
	model = strings.TrimSpace(flagVal)
	if !flagSet && strings.TrimSpace(cfgModel) != "" {
		model = strings.TrimSpace(cfgModel)
	}
	if model == "" {
		switch provider {
		case "bedrock":
			return defaultBedrockModel, ""
		case "ollama":
			return defaultOllamaModel, ""
		case "openrouter":
			return defaultOpenRouterModel, ""
		}
		return defaultGeminiModel, ""
	}
	if !flagSet {
		switch {
		case provider == "bedrock" && isGeminiFamily(model):
			return defaultBedrockModel, "provider=bedrock but model " + q(model) +
				" is a Gemini ID (from config) - using " + q(defaultBedrockModel) +
				"; set model in chat-app.ini or -model to a Bedrock ID"
		case provider == "gemini" && isBedrockFamily(model):
			return defaultGeminiModel, "provider=gemini but model " + q(model) +
				" is a Bedrock ID (from config) - using " + q(defaultGeminiModel) +
				"; set -model to a Gemini model"
		case provider == "ollama" && (isGeminiFamily(model) || isBedrockFamily(model)):
			return defaultOllamaModel, "provider=ollama but model " + q(model) +
				" is not an ollama tag (from config) - using " + q(defaultOllamaModel) +
				"; set model in chat-app.ini or -model to a tag like qwen2:1.5b"
		case provider == "openrouter" && isGeminiFamily(model):
			return defaultOpenRouterModel, "provider=openrouter but model " + q(model) +
				" is a Gemini ID (from config) - using " + q(defaultOpenRouterModel) +
				"; set -model to an OpenRouter ID (e.g. deepseek/deepseek-chat)"
		case provider == "openrouter" && isBedrockFamily(model):
			return defaultOpenRouterModel, "provider=openrouter but model " + q(model) +
				" is a Bedrock ID (from config) - using " + q(defaultOpenRouterModel) +
				"; set -model to an OpenRouter ID (e.g. deepseek/deepseek-chat)"
		}
	}
	if provider == "bedrock" && !isBedrockFamily(model) {
		return model, "model " + q(model) + " does not look like a Bedrock model ID (e.g. " +
			defaultBedrockModel + ") - requests will likely be rejected"
	}
	return model, ""
}

// q quotes a value the way chat-app's warnings do.
func q(s string) string { return `"` + s + `"` }

// SpecContext is the report header metadata: what the spec resolved to
// (provider first, then model/url or profile/region).
func SpecContext(spec Spec) map[string]string {
	m := map[string]string{"provider": spec.Provider, "model": spec.Model}
	if spec.Provider == "bedrock" {
		m["profile"] = spec.AWSProfile
		if spec.AWSRegion != "" {
			m["region"] = spec.AWSRegion
		}
	} else {
		m["url"] = spec.APIURL
	}
	return m
}

// isGeminiFamily reports whether an ID belongs to Google's Gemini family
// (keep in sync with chat-app's isGeminiModel).
func isGeminiFamily(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), "gemini-")
}

// isBedrockFamily reports whether an ID looks like a Bedrock foundation-model
// identifier (keep in sync with chat-app's isBedrockModel).
func isBedrockFamily(id string) bool {
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
