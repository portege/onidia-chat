package agent

import (
	"context"
	"strings"
	"testing"
)

// stubAgent is a minimal in-process Agent for registry/validation tests.
type stubAgent struct {
	id   string
	desc string
	ps   []Param
}

func (s *stubAgent) ID() string          { return s.id }
func (s *stubAgent) Description() string { return s.desc }
func (s *stubAgent) Params() []Param     { return s.ps }
func (s *stubAgent) Run(context.Context, map[string]string) (Result, error) {
	return Result{}, nil
}

func TestRegisterAndDuplicate(t *testing.T) {
	Reset()
	defer Reset()
	if err := Register(&stubAgent{id: "alpha", desc: "A"}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := Register(&stubAgent{id: "alpha", desc: "shadow"}); err == nil {
		t.Fatal("duplicate register should fail - first registration wins")
	}
	if got := Names(); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("Names() = %v, want [alpha]", got)
	}
	if _, err := Get("alpha"); err != nil {
		t.Fatalf("Get(alpha): %v", err)
	}
	if _, err := Get("beta"); err == nil {
		t.Fatal("Get(beta) should fail")
	}
	Reset()
	if got := Names(); len(got) != 0 {
		t.Fatalf("Names() after Reset = %v, want empty", got)
	}
}

func TestValidateArgs(t *testing.T) {
	params := []Param{
		{Name: "title", Required: true, Description: "song"},
		{Name: "shuffle", Type: "bool"},
		{Name: "count", Type: "int", Default: "1"},
		{Name: "style", Enum: []string{"short", "long"}, Default: "short"},
	}
	cases := []struct {
		name string
		args map[string]string
		want map[string]string
		bad  string // expected error substring, "" = ok
	}{
		{"all provided", map[string]string{"title": "Havana", "shuffle": "yes", "count": "3", "style": "long"},
			map[string]string{"title": "Havana", "shuffle": "true", "count": "3", "style": "long"}, ""},
		{"defaults filled", map[string]string{"title": "x"},
			map[string]string{"title": "x", "count": "1", "style": "short"}, ""},
		{"missing required", map[string]string{"shuffle": "true"}, nil, `missing required parameter "title"`},
		{"unknown param", map[string]string{"title": "x", "rm": "-rf"}, nil, `unknown parameter "rm"`},
		{"bad bool", map[string]string{"title": "x", "shuffle": "maybe"}, nil, "must be a boolean"},
		{"bad int", map[string]string{"title": "x", "count": "3.5"}, nil, "must be an integer"},
		{"bad enum", map[string]string{"title": "x", "style": "med"}, nil, "must be one of [short, long]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateArgs(params, tc.args)
			if tc.bad != "" {
				if err == nil || !strings.Contains(err.Error(), tc.bad) {
					t.Fatalf("error = %v, want containing %q", err, tc.bad)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("got[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestParseCall(t *testing.T) {
	cases := []struct {
		in      string
		wantID  string
		wantArg map[string]string
		bad     string
	}{
		{"play_song", "play_song", map[string]string{}, ""},
		{"play_song title=Havana shuffle=true", "play_song",
			map[string]string{"title": "Havana", "shuffle": "true"}, ""},
		{`play_song title="Bohemian Rhapsody"`, "play_song",
			map[string]string{"title": "Bohemian Rhapsody"}, ""},
		{"", "", nil, "empty agent tag"},
		{"Bad-Id x=1", "", nil, "bad agent id"},
		{"play_song novalue", "", nil, `bad parameter "novalue"`},
		{`play_song title="unterminated`, "", nil, "unterminated quote"},
	}
	for _, tc := range cases {
		got, err := ParseCall(tc.in)
		if tc.bad != "" {
			if err == nil || !strings.Contains(err.Error(), tc.bad) {
				t.Errorf("ParseCall(%q) error = %v, want containing %q", tc.in, err, tc.bad)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseCall(%q): %v", tc.in, err)
			continue
		}
		if got.ID != tc.wantID {
			t.Errorf("ParseCall(%q).ID = %q, want %q", tc.in, got.ID, tc.wantID)
		}
		if len(got.Args) != len(tc.wantArg) {
			t.Errorf("ParseCall(%q).Args = %v, want %v", tc.in, got.Args, tc.wantArg)
			continue
		}
		for k, v := range tc.wantArg {
			if got.Args[k] != v {
				t.Errorf("ParseCall(%q).Args[%q] = %q, want %q", tc.in, k, got.Args[k], v)
			}
		}
	}
}

func TestCatalogInstruction(t *testing.T) {
	Reset()
	defer Reset()
	if s := CatalogInstruction(); s != "" {
		t.Fatalf("empty registry should render no catalog, got %q", s)
	}
	if err := Register(&stubAgent{
		id: "play_song", desc: "Play a song from the library.",
		ps: []Param{{Name: "title", Required: true, Description: "song title"},
			{Name: "shuffle", Type: "bool"}},
	}); err != nil {
		t.Fatal(err)
	}
	s := CatalogInstruction()
	for _, want := range []string{"[AGENT:", "play_song", "Play a song from the library.",
		"title (string, required)", "shuffle (bool, optional)"} {
		if !strings.Contains(s, want) {
			t.Errorf("catalog missing %q:\n%s", want, s)
		}
	}
}
