# 💬 chat-app — a Gemini-powered chatbot window with a desktop-pet voice

A tiny desktop chat window written in **pure Go** on raw X11
([`github.com/jezek/xgb`](https://github.com/jezek/xgb)) — the same
no-cgo, no-GUI-toolkit approach as its sibling [`../desktop-pet`](../desktop-pet).
It shares the pet's visual language: plum outlines, pastel teal and the 5×7
bitmap font (extended here with **true lowercase** glyphs; the pet itself
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
- ✅ **The pet bridge**: `pet.go` writes `[mood] [image pic.png] text` to the pet's
  say-FIFO — best-effort and non-blocking; no pet running? It just skips.
- ✅ **Text-to-speech**: each reply is spoken aloud via the Typecast API while
  its bubble is showing (`tts.go` → `aplay`/`paplay`/`ffplay`; async + queued).

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
-provider gemini              LLM backend: "gemini" (default) or "bedrock"
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
-w 380 -h 520                window width and expanded height (starts collapsed)
-preview                      headless PNG previews, no display needed
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
# LLM backend: "gemini" (default) or "bedrock".
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
# Where bot replies are forwarded so the pet speaks them:
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
| `chat_ui_narrow.png`   | 280×430 window: layout reflow |

## Build & run

```sh
make build       # produces ./chat-app
make run
```

Or without make: `go build -trimpath -ldflags="-s -w" -o chat-app .`
`make` targets: `build`, `run`, `preview`, `clean`.

## Controls

| Input | Action |
|---|---|
| typing | insert into the textarea (printable ASCII, ≤ 280 chars) |
| **Enter** | submit — your bubble appears, Buddy answers via Gemini |
| **Esc** | clear the textarea |
| click **SEND** | submit (only enabled while the textarea has text) |
| click textarea | focus it (border turns teal, caret blinks) |
| click **header** | collapse/expand the conversation history (starts collapsed) |
| **drag** the header | move the window (`_NET_WM_MOVERESIZE`; the frame has no titlebar) |
| click **✕** (header, far right) | quit the app |
| **Alt+F4** | quit too (the WM delete protocol stays enabled) |
| wheel over history | scroll toward older / newer messages |
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
├── ui.go          layout, state, hit-testing and software rendering
├── font.go        5×7 bitmap font (+true lowercase) and draw primitives
├── chat.go        the brain: Gemini client, persona, mood-tag handling
├── pet.go         desktop-pet say-FIFO bridge (non-blocking writes)
├── tts.go         Typecast text-to-speech (async fetch + aplay/paplay/ffplay)
├── preview.go     -preview PNG renderer (like the pet's -debug mode)
├── x11win.go      ARGB window setup, WM hints, cursors, keyboard mapping
├── x11draw.go     frame upload (chunked PutImage)
├── x11events.go   event pump + keycode→keysym decoding
├── cmd/geminitest standalone Gemini API probe (see debugging section)
├── Makefile
└── go.mod
```

## Troubleshooting

| Symptom | Fix |
|---|---|
| Window still has a titlebar/border | WM ignores `_MOTIF_WM_HINTS` (rare); use Alt+F4 or the header ✕ |
| Replies say "set GEMINI_API_KEY..." | export the key (or pass `-api-key`) and restart |
| "gemini call failed: ..." bubbles | check network, key validity, or pick another `-model` |
| Pet doesn't speak | start the desktop-pet first (it creates the FIFO); check `-pet-pipe` |
| Want silence from the pet | run with `-pet-pipe off` |
| `context deadline exceeded` on every call, while other sites work | your network filters `generativelanguage.googleapis.com`. Options: run behind a proxy (`export HTTPS_PROXY=...`, Go honors it), a VPN, or point `-api-url` at a relay you control that forwards to Gemini (any service that proxies `POST <base>/v1beta/models/*:generateContent` transparently). Debug with `go run ./cmd/geminitest -models`. |
