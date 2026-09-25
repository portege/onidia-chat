package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// external.go - the line-protocol client for downloaded agents.
//
// Protocol agent-line-v1 (see docs/AGENT-PROTOCOL.md):
//
//	brain -> agent (stdin):  RUN {"title":"Havana","shuffle":"true"}\n
//	agent -> brain (stdout): [INFO <text> ...]  (optional progress, logged)
//	                          PET action <name>  (optional, before OK: the pet
//	                                              should act this out)
//	                          OK <message>       (done, message shown to user)
//	                          - or -
//	                          ERR <text>         (failed, shown as the error)
//	                          then the process MUST exit (the brain gives it
//	                          graceExit before killing it).
//
// Args arrive as one JSON object of strings; validation already happened
// in the brain (ValidateArgs) - the agent may trust the shape but should
// still handle missing keys defensively.

const (
	defaultTimeout = 15 * time.Second // manifest timeout_ms unset
	maxTimeout     = 10 * time.Minute // hard cap on timeout_ms
	graceExit      = 2 * time.Second  // wait after OK/ERR before SIGKILL
	maxStderrBytes = 8 << 10          // keep of the agent's stderr for errors
	maxInfoLines   = 50               // cap INFO lines logged per run
)

// ExternalAgent is an Agent backed by a child process speaking
// agent-line-v1. One process per Run: start, RUN, read reply, exit.
type ExternalAgent struct {
	m    *Manifest
	argv []string // resolved exec (absolute or PATH)
	dir  string   // working directory: the agent's own folder
}

// NewExternal wraps a manifest + its folder into a runnable agent. The
// executable is resolved eagerly so a broken exec fails at discovery time,
// not on the first model reply.
func NewExternal(m *Manifest, dir string) (*ExternalAgent, error) {
	argv, err := resolveExec(dir, m.Exec)
	if err != nil {
		return nil, err
	}
	return &ExternalAgent{m: m, argv: argv, dir: dir}, nil
}

func (e *ExternalAgent) ID() string          { return e.m.ID }
func (e *ExternalAgent) Description() string { return e.m.Description }
func (e *ExternalAgent) Params() []Param     { return e.m.Params }
func (e *ExternalAgent) Dir() string         { return e.dir }
func (e *ExternalAgent) Version() string     { return e.m.Version }

// resolveExec turns manifest exec into a runnable argv. The first element
// is resolved: an absolute path as-is; a relative path against the agent
// folder if it exists there; otherwise looked up on PATH (e.g. "mpv").
func resolveExec(dir string, spec []string) ([]string, error) {
	prog := spec[0]
	var path string
	switch {
	case filepath.IsAbs(prog):
		path = prog
	default:
		cand := filepath.Join(dir, prog)
		if fileExists(cand) {
			path = cand
		} else if lp, err := exec.LookPath(prog); err == nil {
			path = lp
		} else {
			return nil, fmt.Errorf("executable %q not found in %s or PATH", prog, dir)
		}
	}
	argv := make([]string, 0, len(spec))
	argv = append(argv, path)
	argv = append(argv, spec[1:]...)
	return argv, nil
}

// fileExists reports whether p exists and is a regular file.
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// limitedWriter keeps at most cap bytes (the head - enough for a
// diagnostic) and never fails the child's writes.
type limitedWriter struct {
	buf *bytes.Buffer
	cap int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.cap - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil // report full write so the child never sees an error
}

// Run starts the agent process, sends RUN <json>\n, and reads one
// OK/ERR terminal line. The context carries the deadline (from the
// manifest timeout); on timeout the process is killed.
func (e *ExternalAgent) Run(ctx context.Context, args map[string]string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, e.m.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, e.argv[0], e.argv[1:]...)
	cmd.Dir = e.dir
	if env := envFor(); env != nil {
		cmd.Env = env // CHAT_APP_* extras on top of the inherited env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("agent %s: %w", e.m.ID, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("agent %s: %w", e.m.ID, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{buf: &stderr, cap: maxStderrBytes}
	// WaitDelay bounds cmd.Wait after process exit: grandchildren that
	// inherited stderr/stdout (a daemonized player, a stray sleep) would
	// otherwise keep the copy goroutine open and Wait would block until
	// THEY exit. 2s after the process dies, Wait gives up on the pipes.
	cmd.WaitDelay = graceExit

	payload, err := json.Marshal(args)
	if err != nil {
		return Result{}, fmt.Errorf("agent %s: encode args: %w", e.m.ID, err)
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("agent %s: start: %w", e.m.ID, err)
	}
	// Write RUN after start; a broken pipe means the agent crashed early -
	// fall through to the read loop, which reports stderr/exit status.
	if _, werr := fmt.Fprintf(stdin, "RUN %s\n", payload); werr != nil && !errors.Is(werr, io.ErrClosedPipe) {
		log.Printf("agent %s: write RUN: %v", e.m.ID, werr)
	}
	stdin.Close()

	// Scan in a goroutine feeding lines: a hung agent (or a grandchild like
	// `sleep` that inherited stdout and keeps the write end open) would
	// otherwise block Scan() forever - close(2) does NOT interrupt a blocked
	// read, so the main loop must be able to walk away on ctx.Done while the
	// scanner drains in the background. The goroutine exits at EOF or ctx.
	lines := make(chan string)
	go func() {
		defer close(lines) // EOF -> outer loop sees !ok and stops waiting
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // long messages OK
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	var (
		term   string // payload after OK / ERR
		failed bool
		done   bool
		infos  int
		petCmd string // optional character command from a PET line
		petErr error  // malformed/unsupported PET directive
	)
scan:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				break scan // EOF: no terminal line, no more output
			}
			line = strings.TrimRight(line, "\r")
			switch {
			case line == "OK" || strings.HasPrefix(line, "OK "):
				term, failed, done = strings.TrimSpace(line[2:]), false, true
			case line == "ERR" || strings.HasPrefix(line, "ERR "):
				term, failed, done = strings.TrimSpace(line[3:]), true, true
			case line == "PET" || strings.HasPrefix(line, "PET "):
				// Optional character control, emitted at most once and before
				// OK. Keep only the first valid directive; a later one is a
				// protocol error rather than a way to overwrite it.
				if petErr == nil {
					if petCmd != "" {
						petErr = errors.New("duplicate PET directive")
					} else {
						petCmd, petErr = normalizePetCmd(strings.TrimSpace(line[3:]))
					}
				}
			default: // INFO lines and anything unknown (forward-compat) are logged
				if infos < maxInfoLines {
					infos++
					log.Printf("agent %s: %s", e.m.ID, strings.TrimSpace(line))
				}
			}
			if done {
				break scan
			}
		case <-ctx.Done():
			break scan // deadline: treat as timeout below
		}
	}
	if failed && term == "" {
		term = "no error text"
	}

	// Harvest the exit: terminal line or EOF -> the process should be
	// exiting on its own, graceExit then kill. ctx already done -> return
	// now; the background Wait (bounded by WaitDelay) reaps the corpse.
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		cancel()
		// Deliberately NOT waiting for waitCh: a grandchild holding an
		// inherited pipe must not delay the reply (WaitDelay still bounds
		// the background Wait, so the goroutine cannot leak forever).
	case <-time.After(graceExit):
		cancel()
		select {
		case waitErr = <-waitCh:
		case <-time.After(graceExit):
		}
	}

	if failed {
		return Result{}, errors.New(term)
	}
	if done {
		if petErr != nil {
			return Result{}, fmt.Errorf("agent %s: %w", e.m.ID, petErr)
		}
		return Result{Message: term, PetCmd: petCmd}, nil
	}
	// No terminal line: crashed, hung, or wrong protocol.
	if ctx.Err() != nil {
		return Result{}, fmt.Errorf("agent %s: timed out after %s%s",
			e.m.ID, e.m.Timeout(), stderrSuffix(&stderr))
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = "no OK/ERR line"
	}
	if waitErr != nil {
		return Result{}, fmt.Errorf("agent %s: exited without OK/ERR: %v: %s",
			e.m.ID, waitErr, msg)
	}
	return Result{}, fmt.Errorf("agent %s: exited without OK/ERR: %s", e.m.ID, msg)
}

// stderrSuffix folds captured stderr into a timeout/protocol error.
func stderrSuffix(b *bytes.Buffer) string {
	if s := strings.TrimSpace(b.String()); s != "" {
		return ": " + s
	}
	return ""
}

// petNameRe matches a pet pose/FX name as the brain spells it on the cmd-FIFO:
// lowercase word, digits and underscores ("dance", "celebration", "sixseven").
var petNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// normalizePetCmd validates the payload of a "PET <verb> <name>" line and
// returns the canonical cmd-FIFO line ("action dance"), so an ability can make
// the character act the ability out - play a song, start a dance; fail, look
// worried.
//
// That line goes straight to the desktop-pet's cmd-FIFO, which is the one
// place where third-party agent output could smuggle in a second command, so
// the shape is strict: a known verb, ONE pet name, nothing else - no spaces,
// no separators, no newlines. Whether the pet actually knows the name is the
// brain's business (it owns the action/event tables and drops unknown names
// with a log line), so a well-formed but unknown name passes through here and
// fails soft instead of failing the whole run.
func normalizePetCmd(s string) (string, error) {
	verb, name, ok := strings.Cut(strings.TrimSpace(s), " ")
	if !ok {
		return "", fmt.Errorf("PET %q: want \"action <name>\" or \"event <name>\"", s)
	}
	verb = strings.ToLower(verb)
	name = strings.ToLower(strings.TrimSpace(name))
	if verb != "action" && verb != "event" {
		return "", fmt.Errorf("PET: unknown command %q (want action or event)", verb)
	}
	if !petNameRe.MatchString(name) {
		return "", fmt.Errorf("PET: bad %s name %q", verb, name)
	}
	return verb + " " + name, nil
}
