// Package preflight runs chat-app's startup requirements checks: is the
// selected LLM provider actually usable (ollama server up, Gemini/Bedrock/
// OpenRouter credentials valid), and is the environment sane (display, audio
// player, pet pipe, agents dir, image source)? - "and other stuff later"
// lands here as one more Check.
//
// The package is deliberately importable so both surfaces share one behavior:
//   - the standalone CLI, cmd/preflight (the deep/human/scripting surface), and
//   - chat-app's own startup gate in preflight_gate.go (blocks before the X
//     window opens when a fatal check fails).
//
// Severity drives the exit code / gating: Fatal fails block, Warn fails only
// report, Info never affects the result (used by the CLI's -all mode for the
// non-selected backends).
package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Severity classifies what a failing check means for the caller.
type Severity int

const (
	// SeverityWarn failures are reported but never block (exit code 1).
	SeverityWarn Severity = iota
	// SeverityInfo failures never affect the exit code (informational rows,
	// e.g. the non-selected backends in the CLI's -all mode).
	SeverityInfo
	// SeverityFatal failures block: exit code 2, and chat-app's gate refuses
	// to open the window.
	SeverityFatal
)

func (s Severity) String() string {
	switch s {
	case SeverityFatal:
		return "fatal"
	case SeverityInfo:
		return "info"
	default:
		return "warn"
	}
}

// Kind is where a check looks: static config, a live network probe, or the
// host environment. cmd/preflight -quick drops KindLive checks.
type Kind string

const (
	KindConfig Kind = "config"
	KindLive   Kind = "live"
	KindEnv    Kind = "env"
)

// Status is one check's outcome.
type Status int

const (
	StatusPass Status = iota
	StatusFail
	StatusSkip
)

func (s Status) String() string {
	switch s {
	case StatusFail:
		return "fail"
	case StatusSkip:
		return "skip"
	default:
		return "pass"
	}
}

// Outcome is what a Check's Run function reports.
type Outcome struct {
	Status Status
	Detail string // what was found (shown for every status)
	Fix    string // how to unblock; shown on FAIL
}

// Pass reports a satisfied requirement.
func Pass(detail string) Outcome { return Outcome{Status: StatusPass, Detail: detail} }

// Fail reports a broken requirement with an actionable fix hint.
func Fail(detail, fix string) Outcome {
	return Outcome{Status: StatusFail, Detail: detail, Fix: fix}
}

// Skip reports a check that does not apply (missing precondition, -quick, ...).
func Skip(detail string) Outcome { return Outcome{Status: StatusSkip, Detail: detail} }

// Check is one requirement. ID is stable and machine-readable (JSON output,
// tests); Run is executed concurrently and a panic is converted into a failed
// Outcome, so a buggy check can never take the caller down.
type Check struct {
	ID       string
	Kind     Kind
	Severity Severity
	Run      func(ctx context.Context) Outcome
}

// Item is one finished Check.
type Item struct {
	ID       string
	Kind     Kind
	Severity Severity
	Status   Status
	Detail   string
	Fix      string
	Took     time.Duration
}

// Report is the finished run. Context is free-form caller metadata (provider,
// model, ...) echoed by the text and JSON writers.
type Report struct {
	Context map[string]string
	Items   []Item
	Took    time.Duration
}

// safeRun calls run, turning a panic into a failed Outcome.
func safeRun(ctx context.Context, run func(context.Context) Outcome) (out Outcome) {
	defer func() {
		if r := recover(); r != nil {
			out = Fail(fmt.Sprintf("check panicked: %v", r), "")
		}
	}()
	if run == nil {
		return Skip("no run function")
	}
	return run(ctx)
}

// Counts tallies a report. Info-severity passes count as Pass; Info-severity
// failures land in Info (they never affect the exit code).
type Counts struct {
	Pass int
	Fail int // fatal failures (these block)
	Warn int // warn-severity failures
	Info int // info-severity failures
	Skip int
}

// Blocked reports whether any fatal check failed: chat-app's gate and the
// standalone exit code both key off this.
func (r Report) Blocked() bool {
	for _, it := range r.Items {
		if it.Status == StatusFail && it.Severity == SeverityFatal {
			return true
		}
	}
	return false
}

// Counts tallies the report.
func (r Report) Counts() Counts {
	var c Counts
	for _, it := range r.Items {
		switch it.Status {
		case StatusPass:
			c.Pass++
		case StatusSkip:
			c.Skip++
		case StatusFail:
			switch it.Severity {
			case SeverityFatal:
				c.Fail++
			case SeverityInfo:
				c.Info++
			default:
				c.Warn++
			}
		}
	}
	return c
}

// ExitCode is the CLI exit status contract: 0 all good, 1 warnings only,
// 2 blocked (a fatal check failed). Info failures never raise it.
func (r Report) ExitCode() int {
	c := r.Counts()
	switch {
	case r.Blocked() || c.Fail > 0:
		return 2
	case c.Warn > 0:
		return 1
	default:
		return 0
	}
}

// Failures lists every failed check (any severity), in report order.
func (r Report) Failures() []Item {
	var out []Item
	for _, it := range r.Items {
		if it.Status == StatusFail {
			out = append(out, it)
		}
	}
	return out
}

// Summary is the one-line verdict, e.g.
// "3 ok, 1 failed, 0 warnings, 1 skipped - BLOCKED".
func (r Report) Summary() string {
	c := r.Counts()
	verdict := "OK"
	switch r.ExitCode() {
	case 2:
		verdict = "BLOCKED"
	case 1:
		verdict = "WARNINGS"
	}
	return fmt.Sprintf("%d ok, %d failed, %d warnings, %d info, %d skipped - %s",
		c.Pass, c.Fail, c.Warn, c.Info, c.Skip, verdict)
}

// header renders the report's context line, e.g.
// "preflight: provider=gemini model=gemini-3.6-flash url=https://...".
func (r Report) header() string {
	if len(r.Context) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("preflight:")
	for _, k := range contextKeyOrder(r.Context) {
		fmt.Fprintf(&sb, " %s=%s", k, r.Context[k])
	}
	return sb.String()
}

// contextKeyOrder lists the known metadata keys first (deterministic output),
// then any extras in map order.
func contextKeyOrder(ctx map[string]string) []string {
	var ordered, rest []string
	seen := map[string]bool{}
	for _, k := range []string{"provider", "model", "url", "profile", "region"} {
		if _, ok := ctx[k]; ok {
			ordered = append(ordered, k)
			seen[k] = true
		}
	}
	for k := range ctx {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sortStrings(rest)
	return append(ordered, rest...)
}

func sortStrings(s []string) { // insertion sort: tiny slices only
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// Run executes every check concurrently (one shared ctx: the caller's timeout
// bounds the whole run) and returns a Report in the checks' original order.
func Run(ctx context.Context, checks []Check) Report {
	start := time.Now()
	items := make([]Item, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func(i int, c Check) {
			defer wg.Done()
			t0 := time.Now()
			out := safeRun(ctx, c.Run)
			items[i] = Item{
				ID:       c.ID,
				Kind:     c.Kind,
				Severity: c.Severity,
				Status:   out.Status,
				Detail:   out.Detail,
				Fix:      out.Fix,
				Took:     time.Since(t0),
			}
		}(i, c)
	}
	wg.Wait()
	return Report{Items: items, Took: time.Since(start)}
}

// WithoutLive drops the network-probe checks (the CLI's -quick mode).
func WithoutLive(checks []Check) []Check {
	kept := make([]Check, 0, len(checks))
	for _, c := range checks {
		if c.Kind == KindLive {
			continue
		}
		kept = append(kept, c)
	}
	return kept
}

// AsInfo downgrades every severity to SeverityInfo - the CLI's -all mode uses
// it so the non-selected backends report without affecting the exit code.
func AsInfo(checks []Check) []Check {
	out := make([]Check, len(checks))
	copy(out, checks)
	for i := range out {
		out[i].Severity = SeverityInfo
	}
	return out
}

// WriteText renders the human report:
//
//	preflight: provider=ollama model=qwen2:1.5b url=http://localhost:8000
//	  ok   ollama.server          http://localhost:8000 answers /api/tags (7 models)
//	  FAIL ollama.model           model "qwen2:1.5b" not on the server (7 available)
//	           fix: ollama pull qwen2:1.5b (or set model in chat-app.ini)
//	summary: 1 ok, 1 failed, 0 warnings, 0 info, 1 skipped - BLOCKED
func (r Report) WriteText(w io.Writer) error {
	var b strings.Builder
	if h := r.header(); h != "" {
		b.WriteString(h + "\n")
	}
	for _, it := range r.Items {
		marker := "ok"
		switch {
		case it.Status == StatusSkip:
			marker = "skip"
		case it.Status == StatusFail && it.Severity == SeverityFatal:
			marker = "FAIL"
		case it.Status == StatusFail && it.Severity == SeverityInfo:
			marker = "info"
		case it.Status == StatusFail:
			marker = "warn"
		}
		fmt.Fprintf(&b, "  %-4s %-22s %s\n", marker, it.ID, it.Detail)
		if it.Status == StatusFail && it.Fix != "" {
			fmt.Fprintf(&b, "  %-4s %-22s fix: %s\n", "", "", it.Fix)
		}
	}
	fmt.Fprintf(&b, "summary: %s\n", r.Summary())
	_, err := io.WriteString(w, b.String())
	return err
}

// jsonItem / jsonReport are the -json wire shape.
type jsonItem struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Severity string  `json:"severity"`
	Status   string  `json:"status"`
	Detail   string  `json:"detail"`
	Fix      string  `json:"fix,omitempty"`
	TookMS   float64 `json:"took_ms"`
}

type jsonReport struct {
	Context map[string]string `json:"context,omitempty"`
	OK      bool              `json:"ok"`
	Blocked bool              `json:"blocked"`
	Exit    int               `json:"exit"`
	Counts  struct {
		Pass     int `json:"pass"`
		Failed   int `json:"failed"`
		Warnings int `json:"warnings"`
		Info     int `json:"info"`
		Skipped  int `json:"skipped"`
	} `json:"counts"`
	TookMS float64    `json:"took_ms"`
	Checks []jsonItem `json:"checks"`
}

// WriteJSON renders the machine-readable report for scripts.
func (r Report) WriteJSON(w io.Writer) error {
	c := r.Counts()
	out := jsonReport{Context: r.Context, Blocked: r.Blocked(), Exit: r.ExitCode()}
	out.OK = out.Exit == 0
	out.Counts.Pass, out.Counts.Failed, out.Counts.Warnings = c.Pass, c.Fail, c.Warn
	out.Counts.Info, out.Counts.Skipped = c.Info, c.Skip
	out.TookMS = float64(r.Took.Microseconds()) / 1000
	out.Checks = make([]jsonItem, 0, len(r.Items))
	for _, it := range r.Items {
		out.Checks = append(out.Checks, jsonItem{
			ID: it.ID, Kind: string(it.Kind), Severity: it.Severity.String(),
			Status: it.Status.String(), Detail: it.Detail, Fix: it.Fix,
			TookMS: float64(it.Took.Microseconds()) / 1000,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// MaskKey renders a key for humans without leaking it: "AIza...wzyR8 (len 39)".
func MaskKey(key string) string {
	if key == "" {
		return "(none)"
	}
	if len(key) <= 8 {
		return "**** (len " + fmt.Sprint(len(key)) + ")"
	}
	return key[:4] + "..." + key[len(key)-4:] + fmt.Sprintf(" (len %d)", len(key))
}
