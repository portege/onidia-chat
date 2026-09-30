# 💬 chat-app — a Gemini-powered chatbot window with a desktop-pet voice

A tiny desktop chat window written in **pure Go** on raw X11
([`github.com/jezek/xgb`](https://github.com/jezek/xgb)) — the same
no-cgo, no-GUI-toolkit approach as its sibling [`../desktop-pet`](../desktop-pet).
It shares the buddy's visual language: plum outlines, pastel teal and the 5×7
bitmap font (extended here with **true lowercase** glyphs; the buddy itself
renders small-caps).

Ask it anything: the answer comes from the **Google Gemini API**
(`generateContent`, conversation context included) — and every reply is also
forwarded to the **desktop-pet's say-pipe**, so Onidia speaks it out loud with
a matching facial expression - and any picture fetched or generated for the
reply is shown inside her speech bubble as well.

## Status: UI + implementation done

- ✅ **Phase 1 — the UI**: header, scrollable message bubbles (bot left / you
  right), multi-line **textarea** with placeholder and blinking caret,
  **SEND (submit) button** with hover / pressed / disabled states, live
  typing, mouse + wheel scrolling, reflow on resize, a **collapsible
  conversation box** (click the header to collapse/expand; the app starts
  collapsed with just the prompt box showing), and a **rounded window shell**
  (the four frame corners are transparent, so the compositor draws the window
- ✅ **Frameless rounded window**: WM decorations are stripped
  (`_MOTIF_WM_HINTS`), so no square titlebar wraps the rounded ARGB shell —
  the compositor draws exactly the pixels the app paints. Closing is via the
  header's own **✕ button** or Alt+F4 (`WM_DELETE_WINDOW` is still set).
  without hard square corners).
- ✅ **Phase 2 — the brain**: `chat.go` calls Gemini asynchronously (the UI
  shows a "..." bubble while waiting, so it never freezes), keeps the last 20
  turns as context, and parses an optional `[mood]` tag out of each answer.
- ✅ **The buddy bridge**: `pet.go` writes `[mood] [image pic.png] text` to the pet's
  say-FIFO — best-effort and non-blocking; no buddy running? It just skips.
- ✅ **Speech-to-text**: a mic button in the input bar records a take and drops
  the transcript into the textarea — Amazon Transcribe streaming by default, or
  local `faster-whisper` via `stt = whisper`.
- ✅ **Text-to-speech**: each reply is spoken aloud via the Typecast API
  (`tts.go` → `aplay`/`paplay`/`ffplay`; async + queued). The buddy bubble is
  shown only once the audio is ready to play and closes the moment playback
  ends, so the words and the bubble stay in sync.

## Wire in your Gemini key

1. Get a key from [Google AI Studio](https://aistudio.google.com/apikey).
2. Export it, then run:

```sh
export GEMINI_API_KEY="your-key"
make run
```

Without a key the app still works in **stub mode** (each answer echoes your
message plus a hint). The key is read from `-api-key`, `$GEMINI_API_KEY`, or
`$GOOGLE_API_KEY`, in that order.

### Other LLM providers

chat-app selects its backend with `-provider`. Gemini is the default; Bedrock and
OpenRouter are also built in (documented in `chat-app.ini`):

| backend     | endpoint                                          | key                                | example `-model`               |
|-------------|---------------------------------------------------|------------------------------------|--------------------------------|
| `gemini`    | Google Generative Language                        | `-api-key` / `$GEMINI_API_KEY`     | `gemini-3.6-flash`             |
| `bedrock`   | Amazon Bedrock Converse API                       | AWS SDK chain (`-aws-profile`)     | `amazon.nova-lite-v1:0`        |
| `ollama`    | Ollama / llama.cpp `/api/chat` (local, no key)    | none                               | `qwen2:1.5b`                   |
| `openrouter`| OpenRouter, or any OpenAI-compatible gateway      | `-api-key` / `$OPENROUTER_API_KEY` | `deepseek/deepseek-chat-v3-0324` |

`-provider openrouter` is the "one key, any model" path: it talks to OpenRouter,
which forwards to DeepSeek, Kimi/Moonshot, and hundreds of other vendors. The
same code path also works against a **vendor's native** `/chat/completions` API
— just point `-api-url` at it, so DeepSeek and Kimi need no extra code:

```sh
# OpenRouter (any model they expose, e.g. DeepSeek or Kimi):
export OPENROUTER_API_KEY="your-key"
./chat-app -provider openrouter -model deepseek/deepseek-chat-v3-0324

# ...or DeepSeek's native /chat/completions endpoint (no OpenRouter):
./chat-app -provider openrouter -api-url https://api.deepseek.com -model deepseek-chat

# ...or Kimi/Moonshot native endpoint:
./chat-app -provider openrouter -api-url https://api.moonshot.cn/v1 -model kimi-k2-0905-preview
```

With `-provider openrouter` replies stream over **SSE** by default: the chat bubble
fills word-by-word while the model generates (the other providers stay
single-shot). Set `stream = false` in the `[openrouter]` section of
`chat-app.ini` to fall back to one-shot replies.

A wrong-family `-model` (e.g. a Gemini ID with `-provider bedrock`, or a Bedrock
ID with `-provider openrouter`) is auto-swapped to that backend's default and
logged as a warning, so a leftover ID in `chat-app.ini` won't surface as a
confusing API error.

### Flags

```sh
-api-key  KEY                  Gemini API key ("off" = stub mode; default: env
                               vars above, then the built-in free-tier key)
-model    gemini-3.6-flash     any Gemini model name (list: go run ./cmd/geminitest -models;
                               note: pre-3.6 models are retired from generateContent,
                               and a model can 503 "high demand" - retries handle it)
-api-url  <base>               endpoint base override for relays/mirrors
-system-prompt "be brief"      custom Gemini system instruction / persona
                               (default: built-in Buddy persona - short, playful,
                               no markdown/emoji, optional [mood] tag)
-system-file /path/system.txt  load system instruction from file (overrides default)
-config    chat-app.ini        INI file with defaults (flags and env override it;
                               auto-loads ./chat-app.ini when not given)
-provider gemini              LLM backend: "gemini" (default), "bedrock", "ollama", or "openrouter"
-aws-profile default          AWS shared profile for Bedrock
-aws-region us-east-1         AWS region for Bedrock
-model gemini-3.6-flash       model ID (Gemini or Bedrock; default depends on -provider)
-api-key KEY                  Google Gemini API key (default: $GEMINI_API_KEY, $GOOGLE_API_KEY, or built-in; "off" = stub)
-api-url URL                  Gemini endpoint base (default: https://generativelanguage.googleapis.com)
-image-source pixabay         image replies: "pixabay" (Pixabay photo, default),
                               "wiki" (Wikipedia thumbnail), "gemini" (AI generated), or "off"
-pixabay-key KEY              Pixabay API key (default: $PIXABAY_API_KEY, config pixabay-key, or built-in)
-images    true               legacy alias; use -image-source (false = off)
-force-image "Bali"           always fetch/generate an image for this keyword (testing)
-fetch-image "Bali"           test the configured image fetch path, then exit (no window)
-gen-image "robot in Bali"    test the Gemini image generation path, then exit
-pet-pipe /tmp/path.say       say FIFO (default: auto from $DISPLAY; "off" disables)
-tts on|off                   speak replies aloud via Typecast (default: on)
-tts-key KEY                  Typecast API key (default: $TYPECAST_API_KEY, config tts-key, or built-in)
-tts-voice vc_xxx             Typecast voice id (default: config tts-voice, or built-in)
-stt transcribe|whisper|off   speech input backend for the mic button (default: transcribe)
-stt-language en-US           spoken language hint passed to the backend
-stt-device NAME            capture device (default: system default; list them with `preflight -mic`)
-stt-mic                    (preflight) list the microphones you can point stt-device at
-stt-mic-test               (preflight) record 2s and report the microphone level
-stt-test                   headless speech self test: record, transcribe, print every step
-stt-debug                  log the full speech-input trace (file, level, backend, result)
-stt-whisper-vad=true       enable faster-whisper VAD (default off: it eats quiet takes)
-stt-whisper-model base       faster-whisper size: tiny|base|small|medium|large
-stt-whisper-cmd ./venv/bin/python3   interpreter that has faster-whisper installed
-w 380 -h 520                window width and expanded height (starts collapsed)
-preview                      headless PNG previews, no display needed
-preflight strict|warn|off     startup requirements gate (default: strict - a fatal
                               check fails the launch before the window opens)
```

## Speech-to-text (the mic button)

The input bar carries a microphone button. Press it to start a take, press it
again to stop: the audio is transcribed and the words are typed into the
textarea for you to edit (or press **Esc** to discard the take). Nothing is
sent to the model until you press SEND, because a misheard sentence is much
cheaper to fix in the textarea than to re-record.

Backends are pluggable behind the `STT` interface in `stt.go`, chosen by the
`stt` key:

| `stt` | How it works | Needs |
|---|---|---|
| `transcribe` (default) | Amazon Transcribe streaming, over the same AWS credentials and profile/region as Bedrock | the IAM user must be allowed `transcribe:StartStreamTranscription` |
| `whisper` | local `faster-whisper` (CPU, int8) through a small generated python helper | `pip install faster-whisper` |
| `off` | no microphone button at all | — |

Recording itself is the same for both: the first available of
`pw-record`, `parecord`, `arecord`, `ffmpeg` is used to capture **16 kHz mono
s16 WAV**, which is the native input format of both backends, so nothing ever
needs resampling. The WAV header is parsed rather than assumed at 44 bytes,
because recorders pad their chunk lists differently and Transcribe is given
headerless PCM.

**On a machine with no cloud permission, use whisper:**

```bash
pip install faster-whisper          # once; the first run also downloads the model
./chat-app -stt whisper
```

A `tiny` model transcribes a sentence in a few seconds on a Pi; `base` (the
default) is more accurate, `small` and up are slower still.

### "My mic is not recognised"

Three different problems look identical from the app, so there is a command
for each:

```bash
./preflight -mic        # list the microphones, with the one in use marked *
./preflight -mic-test   # record 2s and report the level in dBFS
```

`-mic` prints values you can paste straight into `stt-device` in
`chat-app.ini`. The spelling depends on the recorder: a PipeWire node name for
`pw-record`/`parecord`, an ALSA device (`plughw:2,0`) for `arecord`/`ffmpeg`.
Leaving `stt-device` empty uses whatever the system defaults to.

`-mic-test` is the one that actually diagnoses it. A take can be recognised as
*silence* rather than as a missing device, and the two need opposite fixes:

| Result | Meaning | Fix |
|---|---|---|
| no microphones found | nothing is plugged in / no source node | plug one in |
| `DIGITAL SILENCE` (peak never leaves 0) | the device recorded, but every sample is zero | the mic's own mute switch, then input gain, then a different `stt-device` |
| `signal present but quiet` | low RMS, but a real peak — it will transcribe | move closer, or raise the gain if recognition suffers |
| `signal present`, nothing recognised | level is healthy | run the self test below; it shows whether the backend or the audio is at fault |

When the level is healthy but transcription still fails, run the whole chain
headless and read every step:

```bash
go run . -stt-test            # record ~4s, transcribe, print each step
go run . -stt-test -stt-debug # also print the backend's own diagnostics
```

It reports the backend, the recorder, the resolved device, the captured file
and its level, and then either the transcript or the exact failure. The same
trace goes to the app log with `-stt-debug` (or `CHAT_APP_STT_DEBUG=1`).

**VAD is off by default.** faster-whisper's voice-activity filter classifies
speech against a fixed threshold and on a quiet headset it can discard the
whole take — `duration_after_vad = 0.00` — which surfaces as "nothing
recognised" no matter how healthy the level probe was. Enable it only for a
genuinely noisy room:

```ini
stt-whisper-vad = true
```

Mute is judged on the **peak**, not the RMS. A working mic in a quiet room
sits far below -50 dBFS RMS while still carrying speech, so an RMS-based test
reports a healthy microphone as dead.

PipeWire's virtual `Dummy-Driver` is filtered out of the listing: it is always
present, it is never a microphone, and recording it yields pure silence.

## Preflight: requirements check (before launch)

`chat-app` verifies its own requirements **before the X window opens**: is the
selected LLM backend actually usable, and is the environment sane? The checks
live in `internal/preflight` and run in two places:

1. **the startup gate** (built in, `preflight = strict` by default): a *fatal*
   failure prints the failed checks with fixes to stderr and exits `2` before
   any window appears;
2. **the standalone CLI**: the same checks with the same exit codes, for
   humans and scripts - `./preflight`, `make preflight`, `go run ./cmd/preflight`.

| Check | Severity | What it probes |
|---|---|---|
| `config.provider` | fatal | the `provider` name is one of the four backends |
| `ollama.server` / `ollama.model` | fatal / warn | `GET <api-url>/api/tags` answers; the configured model tag exists on it |
| `gemini.key` / `gemini.api` | warn / fatal | key present (missing = the documented stub mode, which still launches); the free `GET /v1beta/models` listing accepts it |
| `bedrock.credentials` (plus `bedrock.invoke` with `-deep`) | fatal | the AWS SDK chain resolves credentials for profile/region (`-deep` adds a real 1-token Converse call) |
| `openrouter.key` / `openrouter.api` | fatal / fatal | token present (there is no built-in key for this backend); `GET /models` accepts it |
| `env.display`, `env.audio-player`, `env.pet-pipe`, `env.agents-dir`, `env.images` | warn | `$DISPLAY`, an audio player on `PATH`, the pet's say-FIFO, `agents-dir`, the image-source key |

```sh
./preflight                     # exactly what ./chat-app would launch with
./preflight -all                # ...plus the other backends (informational)
./preflight -quick              # config + environment only: no network probes
./preflight -deep               # bedrock: also fire a real 1-token Converse
./preflight -json               # machine-readable report
./preflight -provider ollama -model qwen2:1.5b   # test an alternative setup
make preflight ARGS="-all"      # go run ./cmd/preflight -all
```

Exit codes (both surfaces): `0` = all good, `1` = warnings only,
`2` = blocked (a fatal check failed). Warn-level failures - stub mode, a
missing ollama model tag, no audio player, no pet pipe - are reported but
never block.

**Gate modes** - `chat-app.ini`, overridable per run with `-preflight`:

```ini
preflight = strict   # default: failed fatal check -> report + exit 2, no window
# preflight = warn   # log the failures and launch anyway (the pre-gate behavior)
# preflight = off     # no startup checks at all
```

## Configuration file (`chat-app.ini`)

All settings can live in an INI file so you don't need long command lines.
`./chat-app` (and `make run`) **auto-loads `chat-app.ini`** from the working
directory or the binary's directory; pass `-config <path>` to use a different
file, or edit `chat-app.ini` in place and just relaunch.

```sh
./chat-app               # auto-loads ./chat-app.ini when present
./chat-app -config chat-app.ini   # explicit (same result)
```

Example `chat-app.ini`:

```ini
# LLM backend: "gemini" (default), "bedrock", "ollama", or "openrouter".
provider = gemini

[gemini]
api-key = AIzaSyB7YR3ypNW2A-raPItTfLir-B-vKuuzyR8
model = gemini-3.6-flash
api-url = https://generativelanguage.googleapis.com

[aws]
# For Bedrock. Empty aws-profile uses the default profile.
aws-profile = default
aws-region = us-east-1

[ui]
# Where bot replies are forwarded so the buddy speaks them:
#   auto (default) - derive the FIFO path from $DISPLAY (/tmp/desktop-pet-<display>.say)
#   off            - disable forwarding
#   /absolute/path - write to this exact FIFO
pet-pipe = auto

# Text-to-speech: speak each reply aloud through the Typecast API while its
# bubble shows. Requires aplay/paplay/ffplay on PATH and $TYPECAST_API_KEY
# (or the bundled demo key). Set tts = off to disable.
tts = on
# tts-key = your-typecast-key
# tts-voice = tc_6359e7f6467f9e240b68292c

# Speech-to-text: the microphone button in the input bar (press to record,
# press again to stop; the transcript lands in the textarea for you to edit).
# transcribe = Amazon Transcribe streaming (reuses aws-profile/aws-region);
# whisper = local faster-whisper; off = no mic button.
stt = transcribe
# stt-language = en-US
# stt-whisper-model = base

# Single-line system instruction:
# system-prompt = You are a terse Linux expert.

# Or a multi-line fenced block:
system-prompt-multi = ```
You are Buddy, a tiny cheerful chat companion...
Keep every reply SHORT and playful...
```

# Image replies: "pixabay" = Pixabay photo (default, free shared key),
# "wiki" = Wikipedia thumbnail, "gemini" = AI-generated illustration,
# "off" = text only.
image-source = pixabay
# pixabay-key = your-key (a free shared key is built in)
# force-image = Bali

[character]
# Name of the character, typed into the settings dialog's NAME field; it
# labels the bot's chat bubbles and is told to the persona.
# character-name = Onidia
#
# Age of the chat character (7-13). Also editable in-app: click the gear
# button in the header, pick an age, SAVE - the dialog rewrites this key
# in place and the persona is told the age.
# character-age = 10
#
# Sleep window "HH:00-HH:00", picked with the FROM/TO dropdowns in the same
# dialog; the persona is told the schedule.
# sleep-time = 22:00-07:00
#
# Mute checkbox from the same dialog: while checked, replies still show as
# bubbles but are never spoken aloud (text-to-speech is skipped). Saving the
# dialog rewrites this key in place.
# mute = false
#
# Demo-mode checkbox from the same dialog (the row directly below MUTE
# SPEECH): off (the default) keeps the buddy "planted" - it never
# wanders and never starts talking on its own, though it still blinks,
# idles and answers what you send it. Either way the buddy enters with its
# launch flourish (parachute / poof-in) and then walks - never running - to its
# parking spot three character widths in from the right edge, where its
# speech bubble is not clipped by the screen; planted mode then stays
# there. On restores the classic demo behavior: the buddy roams the screen
# and chatters randomly. Saving the dialog rewrites this key in place, and
# a buddy that is already running is quit and relaunched automatically so the
# new mode takes effect immediately.
# demo-mode = false
#
# Buddy character from the same dialog: the CHARACTER row's buttons carry the
# character names - ONIDIA (the chibi girl) or KAMA (the boy in the red
# hoodie). Saving
# the dialog rewrites this key in place; a buddy that is already running keeps
# its character until it is quit (pink Haiya! button) and launched again.
# character-gender = girl
#
# SAVE also writes the name and age into the stored persona: the "your name
# is ..." sentence in system-prompt-multi (or system-prompt) is rewritten
# with the dialog's values on every save. The rest of your persona text is
# preserved.
```

Precedence (highest first):
1. Explicitly-set CLI flag (`-api-key`, `-model`, `-system-prompt`, ...)
2. Environment variable (`GEMINI_API_KEY`, `GOOGLE_API_KEY`)
3. Config file value
4. Built-in default constant

A sample `chat-app.ini` is included in this repository.

## Image replies

When a prompt looks visual, Buddy emits an `[IMG: <description>]` tag. The app
strips the tag and either fetches a real photo from Pixabay (default) or
Wikipedia, or asks Gemini to generate an illustration, depending on
`-image-source`:

| source | what happens | cost / speed |
|--------|--------------|--------------|
| `pixabay` (default) | searches Pixabay for the description and shows the web-format photo (safesearch on) | free shared key, ~1 s |
| `wiki` | searches Wikipedia for the description and shows the article thumbnail | free, ~1 s |
| `gemini` | calls `gemini-3.1-flash-image` to generate a picture from the description | image-gen quota, ~5–15 s |
| `off` | tag is ignored, text-only replies | - |

- The Pixabay source uses a bundled free key; override with `-pixabay-key`,
  `$PIXABAY_API_KEY`, or `pixabay-key` in `chat-app.ini`.
- The image is best-effort: if the source fails, the reply is text-only.
- Disable entirely: `./chat-app -image-source off` (or legacy `-images=false`).
- Force a keyword for testing: `./chat-app -force-image Bali`.
- Test just the fetch path: `./chat-app -fetch-image Bali`.
- Test just the generation path: `./chat-app -gen-image "a cute robot in Bali"`.

## Bedrock support

You can swap the text backend from Gemini to **Amazon Bedrock** without touching
the UI or image pipeline. The default Bedrock model is `amazon.nova-lite-v1:0`;
override it with `-model`.

```sh
# Use the AWS "default" profile and region from ~/.aws/config.
./chat-app -provider bedrock -aws-profile default -aws-region us-east-1

# Or in chat-app.ini:
provider = bedrock
aws-profile = default
aws-region = us-east-1
model = amazon.nova-lite-v1:0
```

> **Model IDs must match the provider.** Bedrock rejects Gemini IDs (and vice
> versa) with `ValidationException: the provided model identifier is invalid`.
> The app guards against the common leftover-config mistake: a config `model`
> that clearly belongs to the other provider family is auto-replaced by the
> provider's default and a warning is logged at startup. An explicit `-model`
> flag always wins and is never second-guessed.


Bedrock reads the usual AWS credential chain (`AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, `AWS_PROFILE`, `~/.aws/credentials`).
Image replies still use `-image-source pixabay` (Pixabay) by default, or
`-image-source wiki` / `gemini` if you prefer those sources.

Example prompts that should trigger an image:

```text
tell me about Bali
what does a capybara look like
show me the Eiffel Tower
who is Marie Curie
```

## Trigger the API separately (debugging)

`cmd/geminitest` is a tiny CLI that talks to Gemini without the X11 UI, so
API problems can be isolated from window/render issues:

```sh
cd chat-app

# 1. Which models does this key actually see? (resolves any model-name doubt)
go run ./cmd/geminitest -models          # or: make test-api ARGS="-models"

# 2. One raw generateContent call with full HTTP status + body + hints
go run ./cmd/geminitest "tell me a joke in five words"

# 3. Try a specific model / explicit key / see the request body
go run ./cmd/geminitest -model gemini-2.0-flash -key AIza... -v "hi"

# 4. Test with a custom system instruction (matches the chat-app's persona)
go run ./cmd/geminitest -system "you are a grumpy robot" "hello"
go run ./cmd/geminitest -system-file prompt.txt "hello"
```

Reading the output: `HTTP 200` + `REPLY: ...` means the key and network are
fine — if the chat window still misbehaves after that, the bug is in the app
(not the API). `400 API_KEY_INVALID` → re-copy the key; `404` → the model
name is wrong for your key (use the `-models` list); `403` → region/API
restriction; `429` → quota exhausted.

## See the UI without a display

```sh
make preview     # renders chat_ui_*.png sample states and exits
```

| preview | shows |
|---|---|
| `chat_ui_collapsed.png` | default prompt-only window: header + textarea, no history |
| `chat_ui_empty.png`    | fresh window: welcome bubble, empty textarea, disabled SEND |
| `chat_ui_convo.png`    | conversation, typed text + caret, hovered SEND |
| `chat_ui_sent.png`     | after submit: your bubble + the bot's answer |
| `chat_ui_thinking.png` | the "..." bubble while the Gemini call is in flight |
| `chat_ui_cloud.png`     | a `<THINKING>` cloud above the answer bubble (long + one-liner) |
| `chat_ui_settings.png` | the settings dialog over a conversation |
| `chat_ui_settings_open.png` | settings dialog with the age dropdown expanded |
| `chat_ui_settings_sleep.png` | settings dialog with the sleep FROM hour list scrolled open |
| `chat_ui_paged.png`       | a paginated bot reply: < 1/3 > pager in the bubble's foot strip |
| `chat_ui_narrow.png`   | 280×430 window: layout reflow |

## Build & run

```sh
make build       # produces ./chat-app
make run
```

Or without make: `go build -trimpath -ldflags="-s -w" -o chat-app .`
`make` targets: `build`, `run`, `preview`, `clean`.

### One instance only

A second `chat-app` refuses to start:

```
chat-app: another chat-app is already running (pid 12345); close it first
```

This is a `flock` on `$XDG_RUNTIME_DIR/chat-app.lock` (falling back to
`/tmp/chat-app-<uid>.lock`), taken before the window opens. A second window
would fight the first over the pet's say-FIFO, and would sit invisibly behind
it accepting keystrokes nobody can see.

The lock is a kernel `flock`, not a PID file, so a crash or `kill -9` cannot
leave the app permanently unlaunchable — the lock dies with the process and
the next launch just works. The file itself can survive; only the lock on it
matters, and you can delete it freely while nothing is running.

The read-only diagnostic modes deliberately do **not** take the lock, because
they are most useful *while* the app is running:

```sh
chat-app -stt-test     # diagnose the microphone while the app is open
chat-app -preview      # render PNGs
```

To run a second copy on purpose (two accounts, two chats), give one of them
its own runtime directory — they will not see each other:

```sh
XDG_RUNTIME_DIR=/tmp/chat-b chat-app
```

### Thinking clouds (`<THINKING>`)

When the provider returns its reasoning inline, wrapped in
`<THINKING>...</THINKING>`, it gets its own bubble — a small, muted thought
cloud drawn **above** the answer, with the answer in the normal speech bubble
below it. The cloud is deliberately understated: half the font size, a pale
lilac instead of the pet's plum-outlined white, and capped at 6 lines with an
ellipsis, so a long ramble can never crowd out what the character actually
said. See `chat_ui_cloud.png`.

The reasoning is **never** spoken by the pet and never enters the reply — only
the answer is. It is also peeled off **before** any tag parsing, so a `[mood]`,
`[ACTION:]` or `[AGENT: ...]` tag written while the model is thinking cannot
reach the pet, the pager or an ability. A model that muses "I could use
`play_song` here" has not asked for a song. The cloud shows the model's words,
not its markup: tag text is hidden there too, so a `[AGENT: ...]` the model was
only considering is not what you read.

The cloud fills live while the reply streams: an unclosed `<THINKING>` counts
as open, so mid-stream text lands in the cloud rather than flashing into the
answer.

**The pet has one too.** onidia parses the same block out of the say-pipe
line and draws a smaller cloud above her speech bubble, which is how her
"thinking" is visible at all - the chat window may be collapsed or behind
another window while she answers. The **THINKING BUBBLE** setting (the row
under MUTE SPEECH, `thinking = on|off`) governs *both*: switching it off stops
the block being forwarded on the say-pipe, so she stops showing it as well as
the window. Before that was wired up the checkbox only silenced the window and
left the character still musing, which reads as the setting not working.

## Controls

| Input | Action |
|---|---|
| typing | insert into the textarea (printable ASCII, ≤ 280 chars) |
| **Enter** | submit — your bubble appears, Buddy answers via Gemini |
| **Esc** | cancel a recording in flight, else clear the textarea |
| click **mic** (input bar, left of SEND) | start recording; click again to stop and transcribe. The button only appears when `stt` is not `off` and a recorder is installed. A live take pulses red; "Recording…" then "Transcribing…" shows in the textarea, and the transcript replaces it as soon as it arrives |
| transcript arrives | the words are typed into the textarea for you to edit before sending — a misheard sentence is cheaper to fix than to re-record |
| click **SEND** | submit (only enabled while the textarea has text) |
| click textarea | focus it (border turns teal, caret blinks) |
| click **+ / −** (header, left of ⚙) | show/hide the conversation history (starts collapsed) |
| click **⚙ gear** (header, left of ✕) | open the settings dialog (character age 7-13, sleep window FROM/TO, busy window, CHARACTER picker — the buttons carry the character names: **ONIDIA** = Haiya! launches the girl, **KAMA** = the boy, MUTE SPEECH and DEMO MODE checkboxes below it; SAVE writes `character-name`, `character-age`, `sleep-time`, `busy-time`, `mute`, `demo-mode` + `character-gender` to `chat-app.ini` and rewrites the stored persona's "your name is …" sentence with the name + age) |
| in the dialog | type the character's name into NAME, click a dropdown to drop its list (hour lists scroll with the wheel), pick a value, pick **ONIDIA**/**KAMA** in the CHARACTER row for who the Haiya! button launches, tick/untick **MUTE SPEECH** to silence the text-to-speech voice, tick/untick **DEMO MODE** to switch the buddy between roaming+chattering and planted (it walks to its parking spot three character widths in from the right edge and stays there), **SAVE** (or **Enter**); **CANCEL** / **Esc** discards |
| click **About** (header, left of ✕) | open the About dialog: a teal hero strip with the word-art name (drop shadow, plum outline, sparkles) and the round character badge — her happy face, drawn in code — above the tagline, a live line naming whichever buddy is running, and the engineering credit; **OK**, a backdrop click or **Esc** dismisses it |
| **drag** the header | move the window (`_NET_WM_MOVERESIZE`; the frame has no titlebar) |
| click **✕** (header, far right) | quit the app |
| **Alt+F4** | quit too (the WM delete protocol stays enabled) |
| wheel over history | scroll toward older / newer messages |
| click **<** / **>** on a paginated bubble | flip that bubble one page (multi-paragraph bot replies paginate — the persona instructs the LLM to separate paragraphs with a newline, which the app converts to a form-feed page break and renders one paragraph per page) |
| window corner | resize — bubbles, textarea and scroll reflow |

## How the pieces fit

```
you ──▶ textarea ──▶ SEND/Enter ──▶ UI appends your bubble, shows "..."
                                    │ (goroutine, UI stays responsive)
                                    ▼
                     chat.go: Gemini generateContent
                     (persona + last 20 turns + [mood] convention)
                                    │
                     ┌──────────────┴──────────────┐
                     ▼                             ▼
          chat bubble (tag stripped)      pet.go: write "[mood] text"
                                          to /tmp/desktop-pet-<disp>.say
                                                │
                                                ▼
                                     Onidia speaks it, face matches
```

## Project layout

```
.
├── main.go        flags, config, window lifecycle, event loop
├── preflight_gate.go  startup requirements gate (strict/warn/off, exit 2 pre-window)
├── singleton.go   the one-instance flock (taken before the window opens)
├── internal/preflight/ shared checks: provider reachability + environment
│                  (the startup gate and cmd/preflight run the same registry)
├── ui.go          layout, state, hit-testing and software rendering
├── font.go        5×7 bitmap font (+true lowercase) and draw primitives
├── about_art.go   the About modal's hand-drawn character badge + sparkles
├── chat.go        the brain: Gemini client, persona, mood-tag handling
├── pet.go         desktop-pet say-FIFO bridge (non-blocking writes)
├── stt.go         speech-to-text: mic take -> transcribe -> textarea
├── stt_transcribe.go  Amazon Transcribe streaming backend
├── stt_whisper.go     local faster-whisper backend
├── internal/mic/     microphone listing + level probe (used by the app and preflight)
├── stt_selftest.go   -stt-test: the whole speech chain, headless and verbose
├── tts.go         Typecast text-to-speech (async fetch + aplay/paplay/ffplay)
├── preview.go     -preview PNG renderer (like the buddy's -debug mode)
├── agentbridge.go [AGENT: ...] bridge + the multi-round agent loop
├── toolcalls.go   provider-neutral tool types + the native tool-call loop
├── providertools.go  each provider's own tool dialect (Gemini, OpenRouter,
│                  ollama, Bedrock Converse)
├── story.go       the native read_story agent (AI tales, no API keys leaked)
├── agent/         importable agent package: interface, registry, manifest,
│                  discovery, external line-protocol client, env
├── agents/        shippable agent folders + the authoring guide (make pack)
├── docs/AGENT-PROTOCOL.md  manifest + wire protocol reference
├── cmd/agentctl/  install/list/run/validate CLI (same code path as chat)
├── x11win.go      ARGB window setup, WM hints, cursors, keyboard mapping
├── x11draw.go     frame upload (chunked PutImage)
├── x11events.go   event pump + keycode→keysym decoding
├── cmd/geminitest standalone Gemini API probe (see debugging section)
├── cmd/preflight/ the standalone requirements check (same checks + exit
│                  codes as the startup gate; see the Preflight section)
├── Makefile
└── go.mod
```

## Agents - pluggable abilities (`[AGENT: ...]`)

The model can invoke **agents**: small programs discovered at startup from
the agents directory (`~/.config/chat-app/agents`, override with
`-agents-dir` / `agents-dir`). Each registered agent is advertised in the
system prompt catalog, so the reply can lead with
`[AGENT: play_song title="Havana"]` - chat-app strips the tag, validates
the parameters against the agent's declared `params` (the model can only
send what the manifest declares), runs the agent, and folds its `OK`
message into the reply (spoken by the pet too). The tag never reaches the
bubble: if the model writes one the grammar cannot match whole (a bracket
inside a value, a tag cut off by its token limit, a tag in another case) the
leftover markup is hidden as well, so you read the reply, not the plumbing.

One reply may ask for **several** abilities: they run in parallel (4 at a
time, 60s shared budget) and their results are handed back to the model as
a delimited, sanitized data block, so it can **chain** a second ability
(feed a search result into a play call) or rephrase a failure - at most 3
rounds, and the same ability+arguments never runs twice in one reply (see
[`docs/AGENT-PROTOCOL.md`](docs/AGENT-PROTOCOL.md#the-reply-loop-brain-side)).

```sh
make agentctl
./agentctl install agents/hello_world     # folder | .zip | https://...zip
./agentctl list                           # catalog the model sees
./agentctl run hello_world name=Ada       # test one run, no chat needed
make pack                                 # zip shippable agents -> dist/agents/
```

Drop-in, no rebuild: write `agent.json` + any executable speaking the
3-line protocol, zip it, `agentctl install` - restart chat-app and the
model can use it. Full guide: [`agents/README.md`](agents/README.md),
wire protocol: [`docs/AGENT-PROTOCOL.md`](docs/AGENT-PROTOCOL.md).
Disable with `-agents-off` / `agents-off = true`.

An agent can also **act the work out on the character**: one optional
`PET <command>` line before its `OK` reaches the pet when the model didn't
choose an `[ACTION: ...]`/`[EVENT: ...]` itself. Four kinds, and they do not
all travel the same pipe:

| line | channel | example |
|------|---------|---------|
| `action <name>` | cmd-FIFO | `PET action dance` |
| `event <name>` | cmd-FIFO | `PET event love` |
| movement (**no prefix**) | cmd-FIFO | `PET walk left`, `PET jump`, `PET stand` |
| `expr <mood>` | **say-FIFO**, as a bare `[mood]` tag | `PET expr happy` |

`expr` is the odd one out: the pet has no cmd-FIFO verb for expressions at
all - its face is set by a bare `[mood]` tag with no text, which holds the
expression without opening a speech bubble. The agent still writes `PET
expr <mood>`; chat-app is what routes it to the other pipe. Names are
validated twice (shape in the agent package, name against the pet's tables
in the brain), so an agent can only pick a pose, face or step the pet
really has ([details](docs/AGENT-PROTOCOL.md#control-the-character-pet)).

The bundled **`pet_control`** agent wraps all four behind one call - `kind`
(`expression` / `event` / `action` / `move`) plus a `name`, with the pet's
own informal words folded in ("cheer" -> a happy face, "hearts" -> the
love effect, "rope" -> skipping):

```sh
./agentctl -dir agents run pet_control kind=action name=rope
#   INFO action -> skip
#   She is skipping rope.
#   (pet command: action skip)
```

- **Why it might ignore you**: the system prompt is the *only* place the
  catalog lives, and the model reads it alongside the whole conversation. If
  it once answers "I can't show that, it is outside of my capabilities" for
  something an ability actually does, that refusal goes into the chat
  history like any other reply and the next turn inherits it — few-shot
  learning on its own prior output, and it compounds until the ability is
  never offered again. Two guards against that: a turn that ran an ability
  is marked in the model-facing history (`(I used an ability from the list
  above to do this - it worked.)`, never shown on screen), and the catalog
  itself says an ability's effect is real and must not be apologised for.
- Bundled: `pet_control` (animates the pet), `play_song`, `play_movie`,
  `media_control`, `hello_world`, plus the native `read_story`.

### Transport strip (play / pause / stop)

When a media agent (`play_song`, `play_movie`) starts a player, a **NOW
PLAYING** strip appears above the input box with the track name and two
buttons - **play/pause** and **stop** - and the whole row toggles. It flips to
**PAUSED** when the pause took and disappears when the player is gone.

The buttons never touch the player directly: a click runs the `media_control`
agent (`./agentctl install agents/media_control`), the same ability the model
calls when you say *"pause the music"* - one implementation, one behaviour.
mpv is paused through its control socket (playback resumes from the exact
position); other players get `SIGSTOP`/`SIGCONT`, and stop escalates
`SIGINT` → `SIGTERM` → `SIGKILL`.

### Native tool calling (Phase 3)

When the active provider has a function/tool-calling API (Gemini, OpenRouter
and every OpenAI-compatible endpoint, ollama ≥ 0.3, Bedrock Converse), the
registered abilities are handed to the model as **tool definitions** instead of
being described in the prompt: the tool name is the agent `id`, the description
the manifest `description`, and its parameters the declared `params` rendered as
a JSON schema. The model then asks for an ability with **structured JSON
arguments** - validated by the same gate a tag passes, so `unknown parameter`,
type/enum and required checks still refuse a bad call before the agent runs -
and the outcome returns as the provider's own tool result, so it can chain or
rephrase exactly like on the tag path (same 4-at-a-time concurrency, 60 s
budget, 3-round limit and dedupe).

The `[AGENT: ...]` tag path is **kept as the fallback**: a provider, server or
model without tool support makes the first tool request fail, and chat-app
answers through the tags instead - so one agent works everywhere. See
[`docs/AGENT-PROTOCOL.md`](docs/AGENT-PROTOCOL.md#native-tool-calling-phase-3---preferred-when-the-provider-supports-it).

Bundled: **`read_story`** (native - AI tales through the pet/TTS
pipeline), and downloadable **`play_song`** / **`play_movie`** folders
(fuzzy search over `music-dir` / `video-dir`, detached player launch with
mpv/cvlc/vlc/ffplay fallback). Install the latter two with
`./agentctl install agents/play_song` (and `play_movie`).

## Troubleshooting

| Symptom | Fix |
|---|---|
| Window still has a titlebar/border | WM ignores `_MOTIF_WM_HINTS` (rare); use Alt+F4 or the header ✕ |
| Exits with `preflight: BLOCKED` before the window opens | run `make preflight` for the full report + fix hints; launch anyway with `-preflight warn` (or `preflight = warn` in chat-app.ini) |
| Want to know if the backend works before launching | `make preflight` (or `./preflight -quick` for config/env only) |
| Replies say "set GEMINI_API_KEY..." | export the key (or pass `-api-key`) and restart |
| "gemini call failed: ..." bubbles | check network, key validity, or pick another `-model` |
| Buddy doesn't speak | start the desktop-pet first (it creates the FIFO); check `-pet-pipe` |
| Want silence from the buddy | run with `-pet-pipe off` |
| `context deadline exceeded` on every call, while other sites work | your network filters `generativelanguage.googleapis.com`. Options: run behind a proxy (`export HTTPS_PROXY=...`, Go honors it), a VPN, or point `-api-url` at a relay you control that forwards to Gemini (any service that proxies `POST <base>/v1beta/models/*:generateContent` transparently). Debug with `go run ./cmd/geminitest -models`. |
