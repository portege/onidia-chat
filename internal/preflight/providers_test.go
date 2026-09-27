package preflight

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// run executes checks with a short bound so a hung probe fails the test fast.
func run(t *testing.T, checks []Check) Report {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return Run(ctx, checks)
}

// ids lists the check IDs of a spec's checks, joined for comparison.
func ids(checks []Check) string {
	var out []string
	for _, c := range checks {
		out = append(out, c.ID)
	}
	return strings.Join(out, ",")
}

// dump renders a report for test failure messages.
func dump(rep Report) string {
	var b strings.Builder
	_ = rep.WriteText(&b)
	return b.String()
}

func TestChecksCoverTheSelectedProvider(t *testing.T) {
	for provider, want := range map[string]string{
		"gemini":     "config.provider,gemini.key,gemini.api",
		"ollama":     "config.provider,ollama.server,ollama.model",
		"openrouter": "config.provider,openrouter.key,openrouter.api",
		"bedrock":    "config.provider,bedrock.credentials",
	} {
		got := ids(Checks(Spec{Provider: provider}))
		if got != want {
			t.Errorf("%s checks = %q, want %q", provider, got, want)
		}
	}
	// -deep adds the real Converse probe; an unknown provider only gets the
	// config guard.
	if got := ids(Checks(Spec{Provider: "bedrock", Deep: true})); got != "config.provider,bedrock.credentials,bedrock.invoke" {
		t.Errorf("bedrock -deep checks = %q", got)
	}
	if got := ids(Checks(Spec{Provider: "banana"})); got != "config.provider" {
		t.Errorf("unknown provider checks = %q, want only the config guard", got)
	}
	if rep := run(t, Checks(Spec{Provider: "banana"})); rep.ExitCode() != 2 {
		t.Errorf("unknown provider exit = %d, want 2 (blocked)", rep.ExitCode())
	}
}

func TestOllamaChecks(t *testing.T) {
	t.Run("server up and model present", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/tags" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"models":[{"name":"qwen2:1.5b"},{"name":"llama3.2:3b"}]}`)
		}))
		defer srv.Close()
		rep := run(t, Checks(Spec{Provider: "ollama", APIURL: srv.URL, Model: "qwen2:1.5b"}))
		if rep.ExitCode() != 0 {
			t.Fatalf("exit = %d, want 0:\n%s", rep.ExitCode(), dump(rep))
		}
	})
	t.Run("model missing is a warning, not a block", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"models":[{"name":"llama3.2:3b"}]}`)
		}))
		defer srv.Close()
		rep := run(t, Checks(Spec{Provider: "ollama", APIURL: srv.URL, Model: "qwen2:1.5b"}))
		if rep.ExitCode() != 1 || rep.Blocked() {
			t.Fatalf("exit = %d blocked = %v, want 1/false:\n%s", rep.ExitCode(), rep.Blocked(), dump(rep))
		}
		if !strings.Contains(dump(rep), "ollama pull qwen2:1.5b") {
			t.Errorf("missing pull hint:\n%s", dump(rep))
		}
	})
	t.Run("server down blocks", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		dead := srv.URL
		srv.Close() // connection refused from here on
		rep := run(t, Checks(Spec{Provider: "ollama", APIURL: dead, Model: "qwen2:1.5b"}))
		if rep.ExitCode() != 2 {
			t.Fatalf("exit = %d, want 2:\n%s", rep.ExitCode(), dump(rep))
		}
		// The model check must skip (pointing at the server failure), not
		// report its own duplicate failure.
		for _, it := range rep.Items {
			if it.ID == "ollama.model" && it.Status != StatusSkip {
				t.Errorf("ollama.model status = %v, want skip while the server is down", it.Status)
			}
		}
	})
	t.Run("not an ollama server blocks", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		defer srv.Close()
		rep := run(t, Checks(Spec{Provider: "ollama", APIURL: srv.URL}))
		if rep.ExitCode() != 2 || !strings.Contains(dump(rep), "not an ollama-compatible server") {
			t.Fatalf("want blocked with the not-an-ollama hint:\n%s", dump(rep))
		}
	})
}

func TestGeminiChecks(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1beta/models" {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("x-goog-api-key") != "test-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"models":[{"name":"models/gemini-3.6-flash"}]}`)
		}))
		defer srv.Close()
		rep := run(t, Checks(Spec{Provider: "gemini", APIURL: srv.URL, APIKey: "test-key"}))
		if rep.ExitCode() != 0 {
			t.Fatalf("exit = %d, want 0:\n%s", rep.ExitCode(), dump(rep))
		}
	})
	t.Run("rejected key blocks", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		rep := run(t, Checks(Spec{Provider: "gemini", APIURL: srv.URL, APIKey: "bad-key"}))
		if rep.ExitCode() != 2 || !strings.Contains(dump(rep), "HTTP 403") {
			t.Fatalf("want blocked with HTTP 403:\n%s", dump(rep))
		}
	})
	t.Run("no key is stub mode: warning only, api skipped", func(t *testing.T) {
		rep := run(t, Checks(Spec{Provider: "gemini", APIURL: "http://127.0.0.1:1", APIKey: ""}))
		if rep.ExitCode() != 1 || rep.Blocked() {
			t.Fatalf("exit = %d blocked = %v, want 1/false (stub mode still launches):\n%s",
				rep.ExitCode(), rep.Blocked(), dump(rep))
		}
		for _, it := range rep.Items {
			if it.ID == "gemini.api" && it.Status != StatusSkip {
				t.Errorf("gemini.api status = %v, want skip without a key", it.Status)
			}
		}
	})
	t.Run("unreachable API blocks", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		dead := srv.URL
		srv.Close()
		rep := run(t, Checks(Spec{Provider: "gemini", APIURL: dead, APIKey: "test-key"}))
		if rep.ExitCode() != 2 {
			t.Fatalf("exit = %d, want 2:\n%s", rep.ExitCode(), dump(rep))
		}
	})
}

func TestOpenRouterChecks(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/models" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"deepseek/deepseek-chat-v3-0324"}]}`)
		}))
		defer srv.Close()
		rep := run(t, Checks(Spec{Provider: "openrouter", APIURL: srv.URL, APIKey: "or-key"}))
		if rep.ExitCode() != 0 {
			t.Fatalf("exit = %d, want 0:\n%s", rep.ExitCode(), dump(rep))
		}
	})
	t.Run("missing key blocks", func(t *testing.T) {
		rep := run(t, Checks(Spec{Provider: "openrouter", APIKey: ""}))
		if rep.ExitCode() != 2 {
			t.Fatalf("exit = %d, want 2 (no built-in key, first send would fail):\n%s", rep.ExitCode(), dump(rep))
		}
	})
	t.Run("rejected key blocks", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		rep := run(t, Checks(Spec{Provider: "openrouter", APIURL: srv.URL, APIKey: "or-key"}))
		if rep.ExitCode() != 2 {
			t.Fatalf("exit = %d, want 2:\n%s", rep.ExitCode(), dump(rep))
		}
	})
}

func TestBedrockCredentialsCheck(t *testing.T) {
	// Hermetic: keep the machine's real AWS files out of the way and disable
	// the IMDS/container hops so nothing leaves the box.
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/no-credentials")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/no-config")

	t.Run("environment credentials resolve", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
		// Empty profile: the default chain falls through to the env vars
		// without demanding a shared profile that the temp files lack.
		rep := run(t, Checks(Spec{Provider: "bedrock", AWSRegion: "us-east-1"}))
		if rep.ExitCode() != 0 {
			t.Fatalf("exit = %d, want 0:\n%s", rep.ExitCode(), dump(rep))
		}
	})
	t.Run("no credentials anywhere blocks", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "")
		rep := run(t, Checks(Spec{Provider: "bedrock"}))
		if rep.ExitCode() != 2 {
			t.Fatalf("exit = %d, want 2:\n%s", rep.ExitCode(), dump(rep))
		}
		if !strings.Contains(dump(rep), "aws configure") {
			t.Errorf("missing aws configure hint:\n%s", dump(rep))
		}
	})
	t.Run("named profile that does not exist blocks", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
		rep := run(t, Checks(Spec{Provider: "bedrock", AWSProfile: "no-such-profile"}))
		if rep.ExitCode() != 2 || !strings.Contains(dump(rep), "aws-profile") {
			t.Fatalf("want blocked with the aws-profile hint:\n%s", dump(rep))
		}
	})
}
