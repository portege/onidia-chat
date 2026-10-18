// Package diagnose collects onidia-chat troubleshooting information for
// support: system, hardware, installation, settings, network, proxy, model
// providers, audio/soundcard, TTS, and dependencies.
//
// It is safe to share: credentials are never recorded (API keys and tokens
// are masked to "AIza...wzyR8 (len 39)"-style, or shown as present/absent),
// and no PII is collected (no usernames, hostnames, IP addresses, email
// addresses, or serial numbers). The report is produced locally; nothing is
// transmitted unless the user explicitly chooses to send it (copy, save file,
// or mailto:). Network probes are bounded by probeTimeout so a slow host can
// never hang the diagnosis.
package diagnose

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/portege/onidia-chat/internal/confpath"
	"github.com/portege/onidia-chat/internal/preflight"
)

// probeTimeout bounds each individual network/hardware probe.
const probeTimeout = 8 * time.Second

// secretKeys are INI keys whose values are credentials and must be masked.
var secretKeys = map[string]bool{
	"api-key":     true,
	"bot-token":   true,
	"tts-key":     true,
	"tts-voice":   true,
	"pixabay-key": true,
}

// Finding is one checked item.
type Finding struct {
	Category string `json:"category"`
	ID       string `json:"id"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
	Hint     string `json:"hint,omitempty"`
}

// Report is the complete diagnosis.
type Report struct {
	Generated string    `json:"generated"`
	Findings  []Finding `json:"findings"`
}

// finding appends one checked item to the report.
func (r *Report) finding(cat, id string, ok bool, detail, hint string) {
	r.Findings = append(r.Findings, Finding{Category: cat, ID: id, OK: ok, Detail: detail, Hint: hint})
}

// Collect runs every check and returns the report. A cancelled ctx stops
// in-flight probes early; checks that cannot run still appear as findings.
func Collect(ctx context.Context) *Report {
	r := &Report{Generated: time.Now().UTC().Format(time.RFC3339)}

	r.system()
	r.hardware()
	r.install()
	r.settings()
	r.network(ctx)
	r.proxy()
	r.providers(ctx)
	r.audio()
	r.tts()
	r.deps()

	return r
}

// Text renders the report grouped by category for humans.
func (r *Report) Text(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "onidia-chat troubleshooting report\n")
	fmt.Fprintf(&b, "generated: %s\n\n", r.Generated)

	cur := ""
	counts := map[string]struct{ ok, fail int }{}
	for _, f := range r.Findings {
		c := counts[f.Category]
		if f.OK {
			c.ok++
		} else {
			c.fail++
		}
		counts[f.Category] = c
		if f.Category != cur {
			if cur != "" {
				fmt.Fprintf(&b, "\n")
			}
			cur = f.Category
			fmt.Fprintf(&b, "=== %s ===\n", strings.ToUpper(f.Category))
		}
		mark := "[ok]"
		if !f.OK {
			mark = "[fail]"
		}
		fmt.Fprintf(&b, "  %-4s %-24s %s", mark, f.ID, f.Detail)
		if !f.OK && f.Hint != "" {
			fmt.Fprintf(&b, "\n  %-4s %-24s fix: %s", "", "", f.Hint)
		}
		fmt.Fprintf(&b, "\n")
	}
	fmt.Fprintf(&b, "\nsummary:\n")
	for cat, c := range counts {
		fmt.Fprintf(&b, "  %-24s %d ok, %d failed\n", strings.ToUpper(cat), c.ok, c.fail)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// JSON renders the report as machine-readable JSON.
func (r *Report) JSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// ---------------------------------------------------------------------------
// system
// ---------------------------------------------------------------------------

func (r *Report) system() {
	r.finding("system", "architecture", true, runtime.GOARCH, "")
	r.finding("system", "os", true, runtime.GOOS, "")
	r.finding("system", "bitness", true, fmt.Sprintf("%d-bit", strconv.IntSize), "")
	r.finding("system", "cpu-count", true, fmt.Sprintf("%d", runtime.NumCPU()), "")
	r.finding("system", "go-version", true, runtime.Version(), "")
}

// ---------------------------------------------------------------------------
// hardware
// ---------------------------------------------------------------------------

func (r *Report) hardware() {
	if total, avail := readMeminfo(); total != "" {
		r.finding("hardware", "memory", true, fmt.Sprintf("total=%s available=%s", total, avail), "")
	} else {
		r.finding("hardware", "memory", false, "could not read /proc/meminfo", "check /proc/meminfo readability")
	}
	if model := readCPUModel(); model != "" {
		r.finding("hardware", "cpu", true, model, "")
	} else {
		r.finding("hardware", "cpu", false, "could not read /proc/cpuinfo", "check /proc/cpuinfo readability")
	}
	seen := map[string]bool{}
	for _, dir := range []string{"/", installDir()} {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if total, avail := diskUsage(dir); total != "" {
			label := "disk-root"
			if dir != "/" {
				label = "disk-install"
			}
			r.finding("hardware", label, true, fmt.Sprintf("total=%s available=%s (%s)", total, avail, dir), "free up disk space or move onidia-chat to a larger volume")
		}
	}
}

// ---------------------------------------------------------------------------
// install
// ---------------------------------------------------------------------------

func (r *Report) install() {
	exe, err := os.Executable()
	binDir := ""
	if err == nil {
		binDir = filepath.Dir(exe)
		r.finding("install", "onidia-chat-binary", true, exe, "")
	} else {
		r.finding("install", "onidia-chat-binary", false, "could not locate executable", "")
	}

	cfg := confpath.Default()
	if cfg == "" {
		r.finding("install", "config-file", false, "no chat-app.ini found - built-in defaults only", "copy chat-app.ini.template to chat-app.ini")
	} else {
		r.finding("install", "config-file", true, cfg+" (in use)", "")
	}
	r.finding("install", "config-candidates", true, strings.Join(confpath.Candidates(), ", "), "")

	onidia := findOnidia(binDir)
	r.finding("install", "onidia-binary", onidia != "", onidia, "onidia buddy not on PATH and not next to onidia-chat")
	r.finding("install", "install-directory", binDir != "", binDir, "")
}

// ---------------------------------------------------------------------------
// settings
// ---------------------------------------------------------------------------

func (r *Report) settings() {
	cfgPath := confpath.Default()
	if cfgPath == "" {
		r.finding("settings", "config-file", false, "no chat-app.ini found (built-in defaults only)", "copy chat-app.ini.template to chat-app.ini and edit it")
		return
	}
	kv, err := preflight.ReadINI(cfgPath)
	if err != nil {
		r.finding("settings", "config-file", false, fmt.Sprintf("could not read %s: %v", cfgPath, err), "")
		return
	}
	r.finding("settings", "config-file", true, fmt.Sprintf("%s (%d keys)", cfgPath, len(kv)), "")

	for k, v := range kv {
		if isSecretKey(k) {
			r.finding("settings", "key:"+k, true, preflight.MaskKey(v), "")
			continue
		}
		display := v
		if len(display) > 120 {
			display = display[:120] + "…"
		}
		r.finding("settings", "key:"+k, true, display, "")
	}
}

// ---------------------------------------------------------------------------
// network
// ---------------------------------------------------------------------------

func (r *Report) network(ctx context.Context) {
	ok, detail := probeHTTPS(ctx, []string{
		"https://www.google.com",
		"https://example.com",
	})
	r.finding("network", "internet", ok, detail, "check your router/Wi-Fi and firewall; a proxy may be required (see proxy below)")
}

// ---------------------------------------------------------------------------
// proxy
// ---------------------------------------------------------------------------

func (r *Report) proxy() {
	proxies := map[string]string{}
	for _, e := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy"} {
		if v := os.Getenv(e); v != "" {
			proxies[e] = maskProxy(v)
		}
	}
	if len(proxies) > 0 {
		var parts []string
		for k, v := range proxies {
			parts = append(parts, k+"="+v)
		}
		r.finding("proxy", "environment", true, strings.Join(parts, " "), "unset the proxy variables if onidia-chat should connect directly")
	} else {
		r.finding("proxy", "environment", true, "no proxy environment variables", "")
	}

	testURL := "https://example.com"
	u, err := url.Parse(testURL)
	if err != nil {
		r.finding("proxy", "effective", false, "could not parse test URL", "")
		return
	}
	proxyURL, err := http.ProxyFromEnvironment(&http.Request{URL: u})
	if err != nil {
		r.finding("proxy", "effective", false, "proxy resolution error: "+err.Error(), "")
	} else if proxyURL == nil {
		r.finding("proxy", "effective", true, "direct connection (no proxy)", "")
	} else {
		r.finding("proxy", "effective", true, "via "+maskProxy(proxyURL.String()), "")
	}
}

// ---------------------------------------------------------------------------
// model providers
// ---------------------------------------------------------------------------

func (r *Report) providers(ctx context.Context) {
	kv := map[string]string{}
	if cfgPath := confpath.Default(); cfgPath != "" {
		kv, _ = preflight.ReadINI(cfgPath)
	}
	provider := strings.TrimSpace(preflight.Get(kv, "provider"))
	r.finding("providers", "selected", provider != "", provider, "provider = gemini|bedrock|ollama|openrouter in chat-app.ini")

	type probe struct {
		id   string
		name string
		fn   func(ctx context.Context, kv map[string]string) (ok bool, detail string)
	}
	probes := []probe{
		{"providers.gemini", "Gemini", probeGemini},
		{"providers.bedrock", "Bedrock", probeBedrock},
		{"providers.ollama", "Ollama", probeOllama},
		{"providers.openrouter", "OpenRouter", probeOpenRouter},
	}
	for _, p := range probes {
		ok, detail := p.fn(ctx, kv)
		r.finding(p.id, p.name, ok, detail, providerHint(p.id))
	}
}

func providerHint(id string) string {
	switch id {
	case "providers.gemini":
		return "set api-key in [gemini] or $GEMINI_API_KEY; api-url = https://generativelanguage.googleapis.com"
	case "providers.bedrock":
		return "check ~/.aws/config and AWS credentials for the configured aws-profile/aws-region"
	case "providers.ollama":
		return "start ollama (http://localhost:8000 by default) and pull the model: ollama pull <tag>"
	case "providers.openrouter":
		return "set api-key in [openrouter] or $OPENROUTER_API_KEY; api-url default https://openrouter.ai/api/v1"
	}
	return ""
}

func probeGemini(ctx context.Context, kv map[string]string) (bool, string) {
	key := strings.TrimSpace(preflight.Get(kv, "api-key"))
	apiURL := strings.TrimSpace(preflight.Get(kv, "gemini-api-url"))
	if apiURL == "" {
		apiURL = "https://generativelanguage.googleapis.com"
	}
	configured := key != ""
	reachable := probeHost(ctx, apiURL)
	switch {
	case !configured:
		return false, "no api-key configured (provider not usable)"
	case !reachable:
		return false, "api-key present (" + preflight.MaskKey(key) + "), but " + hostOf(apiURL) + " is unreachable"
	default:
		return true, "api-key present (" + preflight.MaskKey(key) + "), " + hostOf(apiURL) + " reachable"
	}
}

func probeBedrock(ctx context.Context, kv map[string]string) (bool, string) {
	profile := strings.TrimSpace(preflight.Get(kv, "aws-profile"))
	region := strings.TrimSpace(preflight.Get(kv, "aws-region"))
	model := strings.TrimSpace(preflight.Get(kv, "bedrock-model"))
	if model == "" {
		model = "amazon.nova-lite-v1:0"
	}
	ac, err := awsLoadConfig(profile, region)
	if err != nil {
		return false, "AWS credentials do not resolve (" + err.Error() + ")"
	}
	if _, err := ac.Creds().Retrieve(ctx); err != nil {
		return false, "no credentials resolve for profile " + defaultStr(profile, "default") + " (" + err.Error() + ")"
	}
	detail := "credentials resolve (profile " + defaultStr(profile, "default") + ", model " + model + ")"
	if region != "" {
		detail += ", region " + region
	}
	return true, detail
}

func probeOllama(ctx context.Context, kv map[string]string) (bool, string) {
	apiURL := strings.TrimSpace(preflight.Get(kv, "ollama-api-url"))
	if apiURL == "" {
		apiURL = "http://localhost:8000"
	}
	model := strings.TrimSpace(preflight.Get(kv, "ollama-model"))
	if model == "" {
		model = "qwen2:1.5b"
	}
	st, _, err := httpGet(ctx, apiURL+"/api/tags", nil)
	if err != nil {
		return false, "ollama at " + apiURL + " is unreachable (" + err.Error() + ")"
	}
	if st != 200 {
		return false, "ollama at " + apiURL + " returned HTTP " + strconv.Itoa(st)
	}
	detail := "ollama at " + apiURL + " answers /api/tags"
	if model != "" {
		detail += " (model " + model + ")"
	}
	return true, detail
}

func probeOpenRouter(ctx context.Context, kv map[string]string) (bool, string) {
	key := strings.TrimSpace(preflight.Get(kv, "api-key"))
	apiURL := strings.TrimSpace(preflight.Get(kv, "openrouter-api-url"))
	if apiURL == "" {
		apiURL = "https://openrouter.ai/api/v1"
	}
	configured := key != ""
	reachable := probeHost(ctx, apiURL)
	switch {
	case !configured:
		return false, "no api-key configured (provider not usable)"
	case !reachable:
		return false, "api-key present (" + preflight.MaskKey(key) + "), but " + hostOf(apiURL) + " is unreachable"
	default:
		return true, "api-key present (" + preflight.MaskKey(key) + "), " + hostOf(apiURL) + " reachable"
	}
}

// ---------------------------------------------------------------------------
// audio / soundcard
// ---------------------------------------------------------------------------

func (r *Report) audio() {
	if cards := readSoundcards(); cards != "" {
		r.finding("audio", "soundcards", true, cards, "")
	} else {
		r.finding("audio", "soundcards", false, "no soundcards found (/proc/asound/cards empty or unreadable)", "install ALSA/PipeWire audio drivers or check /proc/asound")
	}
	if dev := defaultPCM(); dev != "" {
		r.finding("audio", "default-device", true, dev, "")
	} else {
		r.finding("audio", "default-device", false, "no default PCM device found", "set an ALSA default or configure a sound card")
	}
	player := findFirst(ttsPlayers())
	r.finding("audio", "players", true, strings.Join(ttsPlayers(), ", "), "install alsa-utils (aplay), pulseaudio-utils (paplay) or ffmpeg (ffplay) for TTS playback")
	r.finding("audio", "player-selected", player != "", player, "no playable audio device - replies will not be spoken")
	recorders := sttRecorders()
	rec := findFirst(recorders)
	r.finding("audio", "recorders", true, strings.Join(recorders, ", "), "install pipewire-utils (pw-record), alsa-utils (arecord) or ffmpeg for speech input")
	r.finding("audio", "recorder-selected", rec != "", rec, "no audio recorder - speech-to-text is unavailable")
}

// ---------------------------------------------------------------------------
// text-to-speech
// ---------------------------------------------------------------------------

func (r *Report) tts() {
	cfgPath := confpath.Default()
	kv := map[string]string{}
	if cfgPath != "" {
		kv, _ = preflight.ReadINI(cfgPath)
	}
	tts := strings.ToLower(strings.TrimSpace(preflight.Get(kv, "tts")))
	ttsOn := tts != "off"
	r.finding("tts", "enabled", ttsOn, fmt.Sprintf("tts = %q (default on)", tts), "set tts = on in chat-app.ini to enable spoken replies")

	key := strings.TrimSpace(preflight.Get(kv, "tts-key"))
	r.finding("tts", "key", key != "", "typecast api-key "+preflight.MaskKey(key), "set tts-key in chat-app.ini or $TYPECAST_API_KEY (bundled demo key used when empty)")

	player := findFirst(ttsPlayers())
	r.finding("tts", "player", player != "", player, "install alsa-utils/pulseaudio-utils/ffmpeg so TTS audio can play")

	reachable := probeHost(context.Background(), "https://api.typecast.ai")
	r.finding("tts", "endpoint", reachable, "typecast.ai "+boolStr(reachable, "reachable", "unreachable"), "check internet and proxy settings if TTS synthesis fails")
}

// ---------------------------------------------------------------------------
// dependencies
// ---------------------------------------------------------------------------

func (r *Report) deps() {
	disp := os.Getenv("DISPLAY")
	r.finding("deps", "x11-display", disp != "", "DISPLAY="+disp, "run inside an X/Wayland session or export DISPLAY=:0")
	r.finding("deps", "x11-library", true, "raw X11 (github.com/jezek/xgb) - built into the binary", "")

	recorders := sttRecorders()
	r.finding("deps", "speech2text-recorder", len(recorders) > 0, strings.Join(recorders, ", "), "install pipewire-utils (pw-record), alsa-utils (arecord) or ffmpeg for speech input")
	r.finding("deps", "tts-player", len(ttsPlayers()) > 0, strings.Join(ttsPlayers(), ", "), "install alsa-utils (aplay), pulseaudio-utils (paplay) or ffplay for speech output")

	if _, err := exec.LookPath("ffmpeg"); err == nil {
		r.finding("deps", "ffmpeg", true, "ffmpeg available", "")
	} else {
		r.finding("deps", "ffmpeg", false, "ffmpeg not found on PATH", "install ffmpeg as an audio fallback")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func readMeminfo() (total, avail string) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return "", ""
	}
	defer f.Close()
	var totalKB, availKB int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			totalKB = parseMemKB(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			availKB = parseMemKB(line)
		}
	}
	if totalKB == 0 {
		return "", ""
	}
	return humanBytes(uint64(totalKB) * 1024), humanBytes(uint64(availKB) * 1024)
}

func parseMemKB(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, _ := strconv.ParseInt(fields[1], 10, 64)
	return n
}

func readCPUModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "model name") {
			if i := strings.Index(line, ":"); i >= 0 {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return ""
}

func readSoundcards() string {
	f, err := os.Open("/proc/asound/cards")
	if err != nil {
		return ""
	}
	defer f.Close()
	var out strings.Builder
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("; ")
		}
		out.WriteString(line)
	}
	if out.Len() == 0 {
		return ""
	}
	return out.String()
}

func defaultPCM() string {
	f, err := os.Open("/proc/asound/pcm")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "00-") {
			continue
		}
		return line
	}
	return ""
}

func diskUsage(dir string) (total, avail string) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return "", ""
	}
	bsize := uint64(st.Bsize)
	tot := uint64(st.Blocks) * bsize
	free := uint64(st.Bavail) * bsize
	return humanBytes(tot), humanBytes(free)
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	prefixes := []string{"KB", "MB", "GB", "TB"}
	for exp = 0; exp < len(prefixes); exp++ {
		if b < div*unit {
			return fmt.Sprintf("%.1f %s", float64(b)/float64(div), prefixes[exp])
		}
		div *= unit
	}
	return fmt.Sprintf("%.1f TB", float64(b)/float64(div))
}

func ttsPlayers() []string {
	return []string{"pw-play", "paplay", "aplay", "ffplay"}
}

func sttRecorders() []string {
	return []string{"pw-record", "parecord", "arecord", "ffmpeg"}
}

func findFirst(cands []string) string {
	for _, c := range cands {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return ""
}

func probeHost(ctx context.Context, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return false
		}
	}
	d := net.Dialer{Timeout: probeTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// probeHTTPS tries each URL with a bounded GET and reports the first reachable
// one. Used for the internet connectivity check.
func probeHTTPS(ctx context.Context, urls []string) (bool, string) {
	for _, u := range urls {
		st, _, err := httpGet(ctx, u, nil)
		if err == nil && st < 500 {
			return true, fmt.Sprintf("reachable (%s HTTP %d)", u, st)
		}
	}
	return false, "no reachable HTTPS endpoint"
}

func httpGet(ctx context.Context, rawURL string, header map[string]string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
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

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

func maskProxy(s string) string {
	if at := strings.Index(s, "@"); at > 0 {
		before := s[:at]
		if colon := strings.Index(before, ":"); colon > 0 {
			before = before[:colon] + ":***"
		}
		return before + s[at:]
	}
	return s
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func boolStr(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func mustParseURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	return u
}

func isSecretKey(k string) bool {
	kl := strings.ToLower(k)
	if secretKeys[kl] {
		return true
	}
	return strings.Contains(kl, "secret") || strings.Contains(kl, "token") || strings.Contains(kl, "key")
}

func installDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return ""
}

func findOnidia(binDir string) string {
	if p, err := exec.LookPath("onidia"); err == nil {
		return p
	}
	if binDir != "" {
		cand := filepath.Join(binDir, "onidia")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}
