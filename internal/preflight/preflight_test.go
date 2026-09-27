package preflight

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// mk builds a check with a canned outcome.
func mk(id string, sev Severity, out Outcome) Check {
	return Check{ID: id, Kind: KindConfig, Severity: sev, Run: func(context.Context) Outcome { return out }}
}

func TestRunOrderCountsAndExitCodes(t *testing.T) {
	checks := []Check{
		mk("a", SeverityFatal, Pass("fine")),
		mk("b", SeverityWarn, Fail("warned", "do this")),
		mk("c", SeverityFatal, Fail("fatal", "fix that")),
		mk("d", SeverityInfo, Fail("info-only", "meh")),
		mk("e", SeverityFatal, Skip("not applicable")),
	}
	rep := Run(context.Background(), checks)

	if len(rep.Items) != len(checks) {
		t.Fatalf("items = %d, want %d", len(rep.Items), len(checks))
	}
	for i, want := range []string{"a", "b", "c", "d", "e"} {
		if rep.Items[i].ID != want {
			t.Errorf("item %d = %q, want %q (input order must be preserved)", i, rep.Items[i].ID, want)
		}
	}
	c := rep.Counts()
	if c.Pass != 1 || c.Fail != 1 || c.Warn != 1 || c.Info != 1 || c.Skip != 1 {
		t.Errorf("counts = %+v, want Pass=1 Fail=1 Warn=1 Info=1 Skip=1", c)
	}
	if !rep.Blocked() || rep.ExitCode() != 2 {
		t.Errorf("Blocked=%v ExitCode=%d, want true/2 with a fatal failure", rep.Blocked(), rep.ExitCode())
	}

	// A warn-severity failure gives exit 1; info failures never matter.
	warnOnly := Run(context.Background(), []Check{mk("w", SeverityWarn, Fail("bad", "fix"))})
	if warnOnly.ExitCode() != 1 || warnOnly.Blocked() {
		t.Errorf("warn-only: ExitCode=%d Blocked=%v, want 1/false", warnOnly.ExitCode(), warnOnly.Blocked())
	}
	infoOnly := Run(context.Background(), []Check{mk("i", SeverityInfo, Fail("bad", "fix"))})
	if infoOnly.ExitCode() != 0 {
		t.Errorf("info-only: ExitCode=%d, want 0", infoOnly.ExitCode())
	}
	pass := Run(context.Background(), []Check{mk("p", SeverityFatal, Pass("ok"))})
	if pass.ExitCode() != 0 || pass.Blocked() {
		t.Errorf("passing: ExitCode=%d Blocked=%v, want 0/false", pass.ExitCode(), pass.Blocked())
	}
}

func TestRunIsConcurrent(t *testing.T) {
	slow := func(context.Context) Outcome {
		time.Sleep(60 * time.Millisecond)
		return Pass("slept")
	}
	start := time.Now()
	Run(context.Background(), []Check{
		{ID: "s1", Run: slow},
		{ID: "s2", Run: slow},
		{ID: "s3", Run: slow},
	})
	if took := time.Since(start); took > 150*time.Millisecond {
		t.Errorf("3x60ms checks took %s - they must run concurrently", took)
	}
}

func TestRunPanicBecomesFailure(t *testing.T) {
	rep := Run(context.Background(), []Check{{
		ID: "boom", Severity: SeverityFatal,
		Run: func(context.Context) Outcome { panic("kaboom") },
	}})
	if rep.Items[0].Status != StatusFail {
		t.Fatalf("status = %v, want fail", rep.Items[0].Status)
	}
	if !strings.Contains(rep.Items[0].Detail, "panicked") {
		t.Errorf("detail = %q, want it to mention the panic", rep.Items[0].Detail)
	}
	if !rep.Blocked() {
		t.Error("a panicking fatal check must block")
	}
}

func TestWriteText(t *testing.T) {
	rep := Run(context.Background(), []Check{
		mk("config.provider", SeverityFatal, Pass("ollama")),
		mk("ollama.server", SeverityFatal, Fail("connection refused", "start the server")),
		mk("env.display", SeverityWarn, Fail("no DISPLAY", "export DISPLAY=:0")),
	})
	rep.Context = map[string]string{"provider": "ollama", "model": "qwen2:1.5b"}

	var buf bytes.Buffer
	if err := rep.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"preflight: provider=ollama model=qwen2:1.5b",
		"ok   config.provider",
		"FAIL ollama.server",
		"connection refused",
		"fix: start the server",
		"warn env.display",
		"summary:",
		"BLOCKED",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "model=qwen2:1.5b provider=") {
		t.Error("context keys must print in a stable order (provider first)")
	}
}

func TestWriteJSON(t *testing.T) {
	rep := Run(context.Background(), []Check{
		mk("a", SeverityFatal, Pass("fine")),
		mk("b", SeverityWarn, Fail("bad", "fix it")),
	})
	rep.Context = map[string]string{"provider": "gemini"}

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Context map[string]string `json:"context"`
		OK      bool              `json:"ok"`
		Blocked bool              `json:"blocked"`
		Exit    int               `json:"exit"`
		Counts  struct {
			Pass     int `json:"pass"`
			Warnings int `json:"warnings"`
			Skipped  int `json:"skipped"`
		} `json:"counts"`
		Checks []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Fix    string `json:"fix"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if got.OK || got.Blocked || got.Exit != 1 {
		t.Errorf("ok=%v blocked=%v exit=%d, want false/false/1", got.OK, got.Blocked, got.Exit)
	}
	if got.Context["provider"] != "gemini" || got.Counts.Pass != 1 || got.Counts.Warnings != 1 {
		t.Errorf("context/counts wrong: %+v %+v", got.Context, got.Counts)
	}
	if len(got.Checks) != 2 || got.Checks[1].ID != "b" || got.Checks[1].Status != "fail" || got.Checks[1].Fix != "fix it" {
		t.Errorf("checks wrong: %+v", got.Checks)
	}
}

func TestWithoutLiveAndAsInfo(t *testing.T) {
	checks := []Check{
		{ID: "cfg", Kind: KindConfig, Severity: SeverityFatal, Run: func(context.Context) Outcome { return Pass("x") }},
		{ID: "live", Kind: KindLive, Severity: SeverityFatal, Run: func(context.Context) Outcome { return Pass("x") }},
	}
	quick := WithoutLive(checks)
	if len(quick) != 1 || quick[0].ID != "cfg" {
		t.Fatalf("WithoutLive = %+v, want only the config check", quick)
	}
	info := AsInfo(checks)
	if info[0].Severity != SeverityInfo || info[1].Severity != SeverityInfo {
		t.Errorf("AsInfo severities = %v/%v, want info/info", info[0].Severity, info[1].Severity)
	}
	if checks[0].Severity != SeverityFatal {
		t.Error("AsInfo must not mutate the input checks")
	}
}

func TestMaskKey(t *testing.T) {
	key := "AIzaSyB7YR3ypNW2A-raPItTfLir-B-vKuuzyR8"
	masked := MaskKey(key)
	if strings.Contains(masked, key) {
		t.Fatalf("MaskKey leaked the full key: %s", masked)
	}
	if !strings.HasPrefix(masked, "AIza") || !strings.Contains(masked, "(len 39)") {
		t.Errorf("MaskKey = %q, want prefix + length", masked)
	}
	if MaskKey("") != "(none)" {
		t.Errorf("MaskKey(\"\") = %q, want (none)", MaskKey(""))
	}
}
