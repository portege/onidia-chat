# Agents - pluggable abilities for chat-app

An agent is a small program the LLM can call with an `[AGENT: ...]` reply
tag: play a song, read a story, control your lights - anything you can
script. chat-app discovers them automatically: **drop a folder into the
agents directory, restart, and the model can use it.** No rebuild, no code
changes in chat-app.

```
~/.config/chat-app/agents/          # ($XDG_CONFIG_HOME/chat-app/agents)
  hello_world/
    agent.json                      # manifest: id, description, params, exec
    hello_world.sh                  # the agent itself (any language)
```

## Try the sample

```sh
make agentctl
./agentctl install agents/hello_world    # copy it into the agents dir
./agentctl list                          # what the model will see
./agentctl run hello_world name=Ada style=formal
```

Then restart chat-app and ask it *"say hello through hello_world"* - the
reply carries `[AGENT: hello_world name=Ada]`, chat-app runs the agent and
appends its `OK` message to the bubble.

## More than one ability, and chaining

One reply may carry several tags; they all run, **in parallel**, and every
`OK` message joins the same reply:

```
[AGENT: search_song query=havana] [AGENT: set_mood mood=party] on it!
```

After the agents ran, chat-app hands their results back to the model (as a
delimited, untrusted data block) so it can *chain* - use what one agent
found to call the next one - or rephrase a call that failed:

```
model: [AGENT: search_song query=havana] let me look
agent: ERR no track matching "havana"
model: [AGENT: search_song query=Havana] ah, wrong spelling
agent: OK Playing "Havana" by Camila Cabello
model: Enjoy!
```

Rules that keep this safe:

- at most **3 model rounds** per reply (initial answer + 2 feedback rounds);
- an ability is **never run twice with the same arguments** in one reply, so
  a model that repeats itself cannot trigger a side effect twice (this also
  ends the loop);
- at most **4 abilities run at the same time**; every agent gets a shared
  **60 s budget** (on top of its own `timeout_ms`) and is cancelled/killed
  when the reply is abandoned;
- agent text in the feedback block is capped, sanitized and marked as data -
  it can never act as instructions to the model.

## Nothing to change for native tool calling

When your provider supports function/tool calling (Gemini, OpenRouter-style
endpoints, ollama ≥ 0.3, Bedrock), chat-app advertises your agent as a **tool**
instead of describing the `[AGENT: ...]` tag: the tool name is your `id`, the
description your manifest `description`, and its parameters are your declared
`params` as a JSON schema. The model then calls it with structured JSON
arguments - validated exactly like a tag - and your agent is unchanged: it still
receives `RUN {json}` on stdin. Providers without tool support keep using the
tag path, so one agent works everywhere (see
[`../docs/AGENT-PROTOCOL.md`](../docs/AGENT-PROTOCOL.md)).

Nothing changes for your agent: you still receive one `RUN` line and answer
one `OK`/`ERR` line. Chaining is the *brain's* job.

## Bundled with chat-app

| id / folder | kind | what it does |
|---|---|---|
| `read_story` | **native** (in the binary) | tells an AI story through the chat/pet/TTS pipeline; params `theme`, `length` (short/medium/long). Native because it reuses the provider keys chat-app already holds - downloaded agents never see API keys |
| `agents/play_song` | downloaded folder | fuzzy-searches your music folder and launches a detached player (`mpv` > `cvlc` > `vlc` > `ffplay`), then asks the pet to dance; param `query` (empty = random) |
| `agents/play_movie` | downloaded folder | same for videos, with a `fullscreen` toggle |
| `agents/media_control` | downloaded folder | **the transport buttons**: `pause` / `resume` / `stop` / `status` of whatever a `play_*` agent started, plus `target` (`any`/`song`/`movie`). Pauses mpv through its control socket, other players with `SIGSTOP`/`SIGCONT`; stops with `SIGINT` → `SIGTERM` → `SIGKILL` |
| `agents/hello_world` | downloaded folder | the reference agent: greets you by name |
| `agents/_template` | template | copy this to start your own |

Media folders come from `music-dir` / `video-dir` in chat-app.ini (or
`-music-dir` / `-video-dir`) and reach the agents as `CHAT_APP_MUSIC_DIR` /
`CHAT_APP_VIDEO_DIR`; unset means the agents fall back to `~/Music` /
`~/Videos`. `CHAT_APP_PLAYER` overrides the player executable for testing.

## Play, pause, stop

When a `play_*` agent starts a player, chat-app shows a **NOW PLAYING** strip
above the input box, with the track name and two buttons: **play/pause** and
**stop**. It appears when the player starts, flips to **PAUSED** when the pause
took, and disappears when the player is gone (or the song ended).

The buttons do not touch the player themselves: a click calls the
`media_control` agent, exactly as the model would when you say *"pause the
music"*, so the transport behaves the same however it is triggered. So:

- the LLM path works on its own - *"stop the music"*, *"what's playing?"*;
- the buttons need the agent installed (`./agentctl install agents/media_control`);
  without it there is simply no strip;
- any other agent can join in: write a session file (or just call
  `media_control`) and it gets the same transport.

Install it alongside the players:

```sh
./agentctl install agents/media_control
```

Install them:

```sh
./agentctl install agents/play_song
./agentctl install agents/play_movie
```

## Write your own agent

1. Copy `agents/_template/` and edit `agent.json`:

```json
{
  "id": "play_song",                     // [a-z][a-z0-9_]{1,31}, unique
  "version": "1.0.0",
  "description": "Play a song from the local music library. Use when the user asks to hear music.",
  "exec": ["./play_song.sh"],            // or ["mpv", ...] - relative to this folder, or PATH
  "timeout_ms": 15000,                   // optional, default 15000, max 600000
  "params": [
    { "name": "title", "description": "song title or artist", "required": true },
    { "name": "shuffle", "type": "bool", "description": "pick a random track" }
  ]
}
```

   - `description` is read by the LLM: say **when** to use the agent.
   - `params` are the ONLY keys the model may send. Types: `string`
     (default), `int`, `bool`, plus optional `enum` and `default`.
     chat-app validates everything BEFORE your agent runs.

2. Implement the line protocol (`docs/AGENT-PROTOCOL.md`): read one
   `RUN {json}` line from stdin, answer `OK <message>` or `ERR <why>` on
   stdout, exit. `INFO <text>` lines are logged for debugging.

```sh
#!/bin/sh
read -r line                            # RUN {"title":"Havana"}
echo "PET action dance"                 # optional: make the pet act it out
echo "OK Playing Havana by Camila Cabello"
```

   The optional `PET action <name>` / `PET event <name>` line (at most one,
   before `OK`) lets your agent **drive the character** - `play_song` asks for
   a dance, a "storytime" agent could ask for `event celebration`. Valid names
   and the trust rules are in
   [`../docs/AGENT-PROTOCOL.md`](../docs/AGENT-PROTOCOL.md#control-the-character-pet).

3. Test without the chat: `./agentctl validate ./my_agent` then
   `./agentctl run my_agent title=Havana`.

4. Package: `make pack` zips every shippable folder in `agents/` into
   `dist/agents/<id>.zip` (folders starting with `_` are skipped), or just
   `zip -r my_agent.zip my_agent/`. Recipients run
   `./agentctl install my_agent.zip` (folders and https:// URLs work too).

## Rules

- **One agent per folder/zip**, `agent.json` at the root (one level of
  wrapper folder in zips is tolerated).
- After `OK`/`ERR` you **must exit**. Long work: spawn it detached
  (double-fork, `setsid`, systemd-run) and return immediately - chat-app
  kills a still-running agent 2s after the terminal line.
- `OK` message becomes part of the reply (keep it short, plain text, no
  markdown/emoji - the pet reads it aloud with a bitmap font).
- v1 trust model: agents run with your privileges from a user-writable
  folder, no sandbox. Install only agents you trust.

## Files
## Security: Verification & Registry (Phase 4)

Agents can be cryptographically signed using Ed25519 signatures and hosted on remote or local registry indexes:

- **Key generation**: `./agentctl -out keys/ keygen` generates an Ed25519 public/private keypair.
- **Signing**: `./agentctl -key keys/agent_ed25519.priv sign path/to/agent` creates `agent.json.sig`.
- **Packaging**: `./agentctl -out my_agent.zip pack path/to/agent` produces a zip archive and outputs its SHA-256 checksum.
- **Verification Policy**: Enforce signatures with `-require-signature` and `-key <pubkey_hex>` in `agentctl`, or in `chat-app.ini`:
  ```ini
  agents-key = <hex_ed25519_pubkey>
  agents-require-sig = true
  agents-registry = https://example.com/registry.json
  ```
- **Registry & Updates**:
  - Search registry: `./agentctl -registry <url|file> search [query]`
  - Install from registry by name: `./agentctl -registry <url> install play_song`
  - Update installed agents: `./agentctl -registry <url> update`


- `hello_world/` - the reference agent (sh, params, enum, INFO lines)
- `_template/` - start here (ignored by the Go toolchain, like
  `onidia/characters/_template`)
- `../docs/AGENT-PROTOCOL.md` - full protocol + manifest reference
- `../agent/` - the Go package: registry, discovery, validation, client
- `../cmd/agentctl/` - the CLI (list/install/run/validate)