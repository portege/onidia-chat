package diagnose

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeSettingValue(t *testing.T) {
	tests := []struct {
		key, value, want string
		ok               bool
	}{
		{key: "provider", value: " Gemini ", want: "gemini", ok: true},
		{key: "tts", value: "on", want: "on", ok: true},
		{key: "tts", value: "off", want: "off", ok: true},
		{key: "tts", value: "", want: "on (default)", ok: true},
		{key: "allow-from", value: "-1001234567890"},
		{key: "system-prompt", value: "private prompt"},
		{key: "ollama-api-url", value: "******example.test"},
		{key: "provider", value: "custom-provider"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"/"+tt.value, func(t *testing.T) {
			got, ok := safeSettingValue(tt.key, tt.value)
			if got != tt.want || ok != tt.ok {
				t.Errorf("safeSettingValue(%q, %q) = (%q, %t), want (%q, %t)", tt.key, tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestReportExportsSanitizePathsAndURLs(t *testing.T) {
	report := &Report{
		Generated: "2026-10-21T00:00:00Z",
		Findings: []Finding{{
			Category: "install",
			ID:       "config-file",
			Detail:   "could not read /home/alice/.config/chat-app/chat-app.ini",
			Hint:     `check C:\Users\Alice\chat-app.ini`,
		}},
	}

	var text bytes.Buffer
	if err := report.Text(&text); err != nil {
		t.Fatal(err)
	}
	assertRedactedReport(t, text.String())

	var encoded bytes.Buffer
	if err := report.JSON(&encoded); err != nil {
		t.Fatal(err)
	}
	assertRedactedReport(t, encoded.String())

	if !strings.Contains(report.Findings[0].Detail, "/home/alice") {
		t.Fatal("rendering mutated the original report")
	}
}

func assertRedactedReport(t *testing.T, output string) {
	t.Helper()
	for _, sensitive := range []string{
		"/home/alice",
		"alice",
		"C:\\Users",
	} {
		if strings.Contains(output, sensitive) {
			t.Errorf("report contains sensitive value %q: %s", sensitive, output)
		}
	}
	if !strings.Contains(output, "<path>") && !strings.Contains(output, `\u003cpath\u003e`) {
		t.Errorf("report is missing redaction markers: %s", output)
	}
}

func TestReportJSONRemainsValidAfterSanitization(t *testing.T) {
	report := &Report{Findings: []Finding{{Detail: "https://redacted.invalid/api?credential=fake"}}}
	var encoded bytes.Buffer
	if err := report.JSON(&encoded); err != nil {
		t.Fatal(err)
	}

	var decoded Report
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON is invalid: %v", err)
	}
	if got := decoded.Findings[0].Detail; got != "<url>" {
		t.Errorf("sanitized detail = %q, want %q", got, "<url>")
	}
	if strings.Contains(encoded.String(), "redacted.invalid") || strings.Contains(encoded.String(), "credential=fake") {
		t.Errorf("JSON contains a URL value: %s", encoded.String())
	}
	if report.Findings[0].Detail != "https://redacted.invalid/api?credential=fake" {
		t.Errorf("JSON rendering mutated the original report: %q", report.Findings[0].Detail)
	}
}

func TestQuickCollectionSkipsNetworkAndCredentialProbes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat-app.ini")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	report := CollectWithOptions(context.Background(), Options{
		ConfigPath: path,
		Settings: map[string]string{
			"provider":           "openrouter",
			"openrouter-api-key": "private-diagnostic-key",
			"openrouter-api-url": "http://127.0.0.1:1",
			"ollama-api-url":     "http://127.0.0.1:1",
		},
		SkipNetwork: true,
	})
	findings := map[string]Finding{}
	for _, finding := range report.Findings {
		findings[finding.ID] = finding
		findings[finding.Category] = finding
	}
	for _, id := range []string{"internet", "providers.gemini", "providers.bedrock", "providers.ollama", "providers.openrouter", "endpoint"} {
		finding, ok := findings[id]
		if !ok || finding.Detail != "skipped in quick mode" {
			t.Errorf("%s finding = %+v, want quick-mode skip", id, finding)
		}
	}
	if got := findings["selected"].Detail; got != "openrouter" {
		t.Errorf("resolved provider = %q, want openrouter", got)
	}
	var output bytes.Buffer
	if err := report.Text(&output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private-diagnostic-key") {
		t.Fatal("diagnostic output exposed a credential")
	}
}

func TestProbeEndpointUsesHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	status, reachable := probeEndpoint(context.Background(), server.URL)
	if !reachable || status != http.StatusForbidden {
		t.Fatalf("probeEndpoint = (%d, %t), want (%d, true)", status, reachable, http.StatusForbidden)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, reachable := probeEndpoint(ctx, server.URL); reachable {
		t.Fatal("probeEndpoint must honor a cancelled context")
	}
}
