// Package agent defines the plug-in contract every chat-app ability
// ("agent") implements, plus a tiny registry so the model's [AGENT: ...]
// reply tags and the agentctl CLI can discover and run them.
//
// Two ways to add an ability:
//
//   - Native: implement Agent in this repo and call Register (linked into
//     the binary, zero install).
//   - Downloaded: drop a folder containing agent.json + an executable into
//     the agents directory (see discovery.go / cmd/agentctl). The folder
//     speaks the line protocol (external.go) and needs no rebuild - anyone
//     can write one in any language (sh, python, go, ...).
//
// The registry is process-global, mirroring characters.Register in the
// sibling ../onidia pet. All validation happens HERE, in the brain, before
// any agent code runs: the model only ever fills declared parameters.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Param describes one argument an agent accepts. Declared params are the
// ONLY values the model may send; anything else is rejected before Run.
type Param struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"` // "string" (default) | "int" | "bool"
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`    // allowed values (type must be string)
	Default     string   `json:"default,omitempty"` // used when the argument is omitted
}

// Result is what one agent run produced. Message is appended to the chat
// reply (and therefore spoken by the pet); PetCmd optionally drives the
// desktop-pet's cmd-FIFO ("action dance", "event love") when the model did
// not emit its own [ACTION:]/[EVENT:] tag.
type Result struct {
	Message string
	PetCmd  string
}

// Agent is one callable ability.
type Agent interface {
	// ID is the registry key and the [AGENT: <id> ...] tag name
	// (lowercase letters, digits and underscores, starting with a letter).
	ID() string
	// Description teaches the model when to use this agent; it ends up
	// verbatim in the system prompt catalog.
	Description() string
	// Params declares the accepted arguments (validated centrally).
	Params() []Param
	// Run executes the ability with validated args (values are canonical
	// strings; int/bool already type-checked, defaults filled in). It must
	// bound its own runtime.
	Run(ctx context.Context, args map[string]string) (Result, error)
}

// idRe matches agent ids: [a-z][a-z0-9_]{1,31} (2-32 chars total).
var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

// paramNameRe matches parameter names: lowercase, underscore-separated.
var paramNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidID reports whether s is a legal agent id / parameter name, i.e. a
// safe [AGENT: ...] tag target (no spaces, brackets or shell metachars).
func ValidID(s string) bool { return idRe.MatchString(s) }

// ---- registry --------------------------------------------------------------

var (
	registryMu sync.Mutex
	registry   = map[string]Agent{}
)

// Register adds an agent, usually from init() or at startup after
// discovery. Registering the same id twice is an error - the first one
// wins, so a downloaded agent can never silently shadow a built-in.
func Register(a Agent) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, ok := registry[a.ID()]; ok {
		return fmt.Errorf("agent %q already registered", a.ID())
	}
	registry[a.ID()] = a
	return nil
}

// Get looks an agent up by id.
func Get(id string) (Agent, error) {
	registryMu.Lock()
	a, ok := registry[id]
	registryMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown agent %q (available: %s)", id, strings.Join(Names(), ", "))
	}
	return a, nil
}

// Names lists every registered agent id, sorted.
func Names() []string {
	registryMu.Lock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	registryMu.Unlock()
	sort.Strings(out)
	return out
}

// Reset drops every registration. Tests only.
func Reset() {
	registryMu.Lock()
	registry = map[string]Agent{}
	registryMu.Unlock()
}

// ---- argument validation ---------------------------------------------------

// ValidateArgs fills defaults and type-checks args against params. It is
// the single gate between model-generated text and agent code: unknown
// parameters, missing required ones and type/enum violations all return an
// error here, before any agent runs.
func ValidateArgs(params []Param, args map[string]string) (map[string]string, error) {
	declared := make(map[string]Param, len(params))
	for _, p := range params {
		declared[p.Name] = p
	}
	out := make(map[string]string, len(args))
	for k, raw := range args {
		p, ok := declared[k]
		if !ok {
			return nil, fmt.Errorf("unknown parameter %q", k)
		}
		v, err := coerceParam(p, raw)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	for _, p := range params {
		if _, ok := out[p.Name]; ok {
			continue
		}
		if p.Default != "" {
			out[p.Name] = p.Default
			continue
		}
		if p.Required {
			return nil, fmt.Errorf("missing required parameter %q", p.Name)
		}
	}
	return out, nil
}

// coerceParam type-checks one raw value against its Param and returns the
// canonical string form the agent will receive (bools normalized to
// true/false). Empty type means "string".
func coerceParam(p Param, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	switch p.Type {
	case "", "string":
		if len(p.Enum) > 0 {
			for _, e := range p.Enum {
				if e == v {
					return v, nil
				}
			}
			return "", fmt.Errorf("%s must be one of [%s] (got %q)",
				p.Name, strings.Join(p.Enum, ", "), v)
		}
		return v, nil
	case "int":
		if _, err := strconv.Atoi(v); err != nil {
			return "", fmt.Errorf("%s must be an integer (got %q)", p.Name, v)
		}
		return v, nil
	case "bool":
		b, err := parseLooseBool(v)
		if err != nil {
			return "", fmt.Errorf("%s must be a boolean true/false (got %q)", p.Name, v)
		}
		return strconv.FormatBool(b), nil
	}
	return "", fmt.Errorf("parameter %s has unsupported type %q", p.Name, p.Type)
}

// ---- native tool calls (Phase 3) -------------------------------------------

// ArgsFromJSON converts the structured arguments of a native tool call (the
// JSON object a model sends through a provider's function-calling API) into
// the canonical string map ValidateArgs expects. It is deliberately the same
// gate as the [AGENT: ...] path: unknown keys are rejected here, declared
// types/enums are checked by ValidateArgs right after, and nothing the model
// sends is trusted just because it arrived as JSON.
//
// A null value counts as "not provided" (the parameter's default/required
// rule then applies), whole-number floats become integers (models emit
// 2.0 for an int parameter regularly), and nested arrays/objects are
// rejected - declared parameters are scalars only.
func ArgsFromJSON(params []Param, args map[string]any) (map[string]string, error) {
	declared := make(map[string]Param, len(params))
	for _, p := range params {
		declared[p.Name] = p
	}
	out := make(map[string]string, len(args))
	for k, v := range args {
		if _, ok := declared[k]; !ok {
			return nil, fmt.Errorf("unknown parameter %q", k)
		}
		if v == nil {
			continue // treated as omitted: default / required rule applies
		}
		s, err := jsonValueString(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = s
	}
	return out, nil
}

// jsonValueString renders one JSON argument value as the canonical string
// coerceParam understands.
func jsonValueString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		if t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10), nil
		}
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	case json.Number:
		return t.String(), nil
	}
	return "", fmt.Errorf("unsupported value type %T (parameters are strings, ints or bools)", v)
}

// RunArgs is Run with STRUCTURED (native tool call) arguments: the values are
// coerced and validated by the exact same gate as a text tag, so an ability
// called through a provider's function-calling API is no more trusted than one
// called from a [AGENT: ...] tag.
func RunArgs(id string, args map[string]any) (Result, error) {
	return RunArgsContext(context.Background(), id, args)
}

// RunArgsContext is RunArgs with a caller-supplied context (see RunContext).
func RunArgsContext(ctx context.Context, id string, args map[string]any) (Result, error) {
	a, err := Get(id)
	if err != nil {
		return Result{}, err
	}
	raw, err := ArgsFromJSON(a.Params(), args)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", id, err)
	}
	return RunContext(ctx, id, raw)
}

// parseLooseBool accepts what an LLM is likely to emit for a flag.
func parseLooseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "yes", "y", "on":
		return true, nil
	case "no", "n", "off":
		return false, nil
	}
	return strconv.ParseBool(s)
}

// ---- [AGENT: ...] reply-tag parsing ----------------------------------------

// Call is one parsed [AGENT: ...] reply tag: the target id plus arguments.
type Call struct {
	ID   string
	Args map[string]string
}

// ParseCall parses the inside of an [AGENT: ...] reply tag, e.g.
//
//	play_song title="Bohemian Rhapsody" shuffle=true
//
// Values may be double-quoted to contain spaces; there are no escapes
// inside quotes (a quote always ends the value - keep values simple).
func ParseCall(s string) (Call, error) {
	toks, err := splitTokens(s)
	if err != nil {
		return Call{}, err
	}
	if len(toks) == 0 {
		return Call{}, fmt.Errorf("empty agent tag, want [AGENT: id key=value ...]")
	}
	if !idRe.MatchString(toks[0]) {
		return Call{}, fmt.Errorf("bad agent id %q in tag", toks[0])
	}
	args := make(map[string]string, len(toks)-1)
	for _, t := range toks[1:] {
		k, v, ok := strings.Cut(t, "=")
		if !ok || !paramNameRe.MatchString(k) {
			return Call{}, fmt.Errorf("bad parameter %q in agent tag (want key=value)", t)
		}
		args[k] = v
	}
	return Call{ID: toks[0], Args: args}, nil
}

// splitTokens splits a tag payload on spaces, keeping double-quoted runs
// (quotes stripped) together: title="a b" -> one token title=a b.
func splitTokens(s string) ([]string, error) {
	var (
		toks    []string
		cur     strings.Builder
		inQuote bool
		started bool
	)
	flush := func() {
		if started {
			toks = append(toks, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			started = true // a quoted empty string is still a value
		case (r == ' ' || r == '\t' || r == '\n' || r == '\r') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quote in agent tag")
	}
	flush()
	return toks, nil
}

// ---- system-prompt catalog -------------------------------------------------

// agentCatalogIntro teaches the model the tag format before the list of
// registered abilities (mirrors visualLanguageInstruction for pet tags).
const agentCatalogIntro = ` You also control "agents" - special abilities this app can run for you (playing media, reading stories, ...). To perform one, START your reply with ONE "[AGENT: <id> <key>=<value> ...]" tag per ability, quoting values that contain spaces (example: title="some song"); use parameter names exactly as declared, never invent ids or keys, and only use listed abilities. The tag is stripped before display and the app appends the agent's result to your reply. Abilities you may use:`

// CatalogInstruction renders the system-prompt block listing every
// registered agent with its declared parameters. Empty string when no
// agents are registered, so prompts stay untouched on a fresh install.
func CatalogInstruction() string {
	names := Names()
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(agentCatalogIntro)
	for _, id := range names {
		a, err := Get(id)
		if err != nil {
			continue
		}
		b.WriteString("\n- " + id + ": " + a.Description())
		if ps := formatParams(a.Params()); ps != "" {
			b.WriteString(" params: " + ps)
		}
	}
	return b.String()
}

// formatParams renders a param list for the model, e.g.
// `title (string, required); shuffle (bool, optional, default false)`.
func formatParams(params []Param) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, 0, len(params))
	for _, p := range params {
		t := p.Type
		if t == "" {
			t = "string"
		}
		s := p.Name + " (" + t
		if p.Required {
			s += ", required"
		} else {
			s += ", optional"
		}
		if len(p.Enum) > 0 {
			s += ", one of: " + strings.Join(p.Enum, "|")
		}
		if p.Default != "" {
			s += ", default " + p.Default
		}
		s += ")"
		if p.Description != "" {
			s += " - " + p.Description
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

// ParamSchema renders declared params as the JSON-schema object providers
// expect for a native function/tool definition (the OpenAPI subset: an object
// with properties and required). Type names follow JSON schema conventions
// (int -> integer, bool -> boolean). An agent without params still gets a
// valid object schema, because a provider will not accept a tool without one.
func ParamSchema(params []Param) map[string]any {
	schema := map[string]any{"type": "object"}
	if len(params) == 0 {
		return schema
	}
	props := make(map[string]any, len(params))
	var required []string
	for _, p := range params {
		t := p.Type
		if t == "" {
			t = "string"
		}
		if t == "int" {
			t = "integer"
		}
		if t == "bool" {
			t = "boolean"
		}
		prop := map[string]any{"type": t}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		props[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema["properties"] = props
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}

// Run validates args for the agent and executes it. This is the single
// entry point used by the reply pipeline and agentctl alike.
func Run(id string, args map[string]string) (Result, error) {
	return RunContext(context.Background(), id, args)
}

// RunContext is Run with a caller-supplied context. The reply pipeline's
// agent loop passes its cancel token, so an abandoned reply (or a loop that
// hit its budget) stops an in-flight agent promptly; agentctl uses Run's
// background context. An external agent's manifest timeout still applies on
// top of ctx.
func RunContext(ctx context.Context, id string, args map[string]string) (Result, error) {
	a, err := Get(id)
	if err != nil {
		return Result{}, err
	}
	valid, err := ValidateArgs(a.Params(), args)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", id, err)
	}
	return a.Run(ctx, valid)
}
