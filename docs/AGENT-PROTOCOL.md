# AGENT-PROTOCOL.md - manifest + wire protocol for chat-app agents

Version: `agent-line-v1`. Two halves: a **manifest** (`agent.json`) that
makes an agent self-describing for auto-discovery, and a **line protocol**
over stdin/stdout that runs it. Both are intentionally tiny so an agent can
be written in any language in ~30 lines.

## Discovery

Default directory: `$XDG_CONFIG_HOME/chat-app/agents` or
`~/.config/chat-app/agents` (override: `-agents-dir` flag / `agents-dir`
INI key; disable entirely: `-agents-off` / `agents-off = true`).

```
agents-dir/
  <anything>/            # folder name is cosmetic
    agent.json           # required, at folder root
    ...                  # helper files, executables
```

Startup scan (and `agentctl list`) validates every folder:

1. `agent.json` parses and passes `Manifest.Validate` (below),
2. `disabled` is false,
3. `exec` resolves (relative to the folder, absolute, or PATH),
4. `id` is not already registered (**first registration wins** - a
   download can never shadow a built-in or an earlier install).

Anything else is reported as a per-folder problem and skipped - one broken
download never brings the app down. Hidden folders (`.git`, ...) and
folders without `agent.json` are ignored silently. Discovery runs at
startup only: install, then restart chat-app.

## Manifest - agent.json

| field          | required | rules |
|----------------|----------|-------|
| `id`           | yes | `^[a-z][a-z0-9_]{1,31}$` - this is the `[AGENT: <id> ...]` tag name |
| `description`  | yes | shown to the LLM in the system-prompt catalog: say what it does AND when to use it |
| `exec`         | yes | argv array. `[0]`: relative path resolved against the agent folder, absolute path, or a bare name looked up on PATH (`"mpv"`). Remaining elements are static args |
| `version`      | no | free-form, shown by `agentctl list` |
| `protocol`     | no | must be `"agent-line-v1"` or omitted (defaults) |
| `timeout_ms`   | no | per-run deadline; default 15000, max 600000 |
| `disabled`     | no | `true` = skip at discovery (kept on disk) |
| `params`       | no | declared arguments, see below |

### Params

```json
{ "name": "title", "type": "string", "required": true,
  "description": "song title or artist",
  "enum": ["a", "b"], "default": "a" }
```

- `name`: `^[a-z][a-z0-9_]*$`, unique per agent.
- `type`: `string` (default) | `int` | `bool`.
- `enum`: allowed values (string only). `default`: used when omitted.
- **Validation happens in the brain, before your agent starts**: declared
  only, type-checked (bools normalized to `true`/`false`, loose input like
  `yes`/`off` accepted), required params present, defaults filled. An
  unknown/missing/invalid param means your agent is never spawned - the
  error goes straight to the reply.

## Wire protocol

One process per run: chat-app starts it, writes one line to stdin, reads
stdout lines until a terminal line, then waits up to 2s for exit (kill
after that). Working directory = the agent's folder.

```
brain -> agent   RUN <json-object>\n
agent -> brain   INFO <text>\n           # optional, repeatable: logged only
agent -> brain   PET action <name>\n    # optional, before OK: drive the pet
agent -> brain   OK <message>\n          # terminal: message joins the reply
               | ERR <text>\n            # terminal: shown as the failure
```

- The JSON object maps param names to their validated **string** values
  (`{"title":"Havana","shuffle":"true"}`).
- Only the first `OK`/`ERR` line is terminal; everything before it that
  isn't `INFO ` is logged as-is (forward-compat).
- `OK` message: the user-facing result. Empty `OK` is allowed (side
  effect only). Plain text, no markdown/emoji (bitmap font + TTS).
- After the terminal line: **exit**. Work longer than the reply? spawn it
  detached and exit - chat-app must not wait for your daemon.
- No `OK`/`ERR` before exit or EOF = failure; stderr (first 8KB) is
  included in the error shown in the reply.
- Timeout (`timeout_ms`): process killed, reply gets
  `agent <id>: timed out after <d>`.

### Control the character (`PET`)

An ability can make the pet **act it out** - the song agent starts a dance, a
"look outside" agent could ask for a peek. Emit one optional line **before**
your `OK`:

```
agent -> brain   PET action dance        # a pose animation
agent -> brain   PET event celebration   # an overlay effect
```

Rules:

- at most **one** `PET` line per run, and only before `OK`/`ERR`; a second one
  is a protocol error (a run cannot quietly swap its command);
- the payload must be `action|event` + **one** name (`[a-z][a-z0-9_]*`) -
  `PET action dance; rm -rf /` is rejected and the run fails. The line ends up
  in the pet's command FIFO, so nothing else may ride in it;
- the name must be one the pet knows, else chat-app logs
  `ignoring unknown pet command` and drops it - the reply still succeeds;
- it is a **fallback**: when the model already used `[ACTION: ...]` /
  `[EVENT: ...]` for that reply, the model's choice wins;
- with `-pet-pipe off` (pet forwarding disabled) nothing is written;
- a native (in-process) agent does the same by returning
  `agent.Result{PetCmd: "action dance"}`.

Names come from the pet's own tables - the same ones the model may use:

| actions | events |
|---|---|
| `skip` `juggle` `dance` `eat` `work` `guitar` `sneeze` `sixseven` `basketball` `drive` `ride` `kitten` `wave` | `love` `idea` `celebration` `sleep` `peace` `halloween` `matrix` `magic` |

(`disappear`/`appear` are internal pet states - never use them.)

### Minimal agent (any language)

```python
#!/usr/bin/env python3
import json, sys
line = sys.stdin.readline()
assert line.startswith("RUN ")
args = json.loads(line[4:])
print("INFO doing work", flush=True)
print("OK " + f"did it: {args.get('query','')}", flush=True)
```

### Example session

```
-> RUN {"title":"Havana"}
<- INFO scanning /home/me/Music
<- OK Playing "Havana" by Camila Cabello (3:01)
```

## The reply loop (brain side)

An agent never changes for chaining - the brain drives it. One model reply may
carry several `[AGENT: ...]` tags:

1. every tag of the reply runs, at most **4 at a time** (`agentConcurrency`),
   each with the reply's **cancel token** (a **60 s** loop budget on top of
   the manifest `timeout_ms`; the token is cancelled when the reply is
   abandoned, killing external agents);
2. the results are handed back to the model as ONE user turn:

   ```
   <<<AGENT>>>
   - search_song: ERR no track matching "havana"
   - set_mood: OK (done)
   <<<END AGENT>>> ...if the request is handled, answer with no tag; otherwise
   lead with ONE new [AGENT: ...] tag - never repeat an ability with the same values.
   ```

   The model's own previous answer rides along as assistant context, with the
   already-dispatched tags removed. Result text is sanitized (`system:` lines
   and chat-template markers are defanged) and truncated to 600 chars per
   line: it is **data**, never instructions;
3. repeat, at most **3 rounds** total. An ability that already ran with the
   same arguments is never run again in the same reply - a repeating model
   ends the loop instead of triggering the side effect twice. Tags left over
   after the last round are stripped from the display, not executed.

Every `OK` message still joins the reply text (and the pet's speech); every
`ERR` is still shown to the user in parentheses - the model just gets to
explain or retry in its own words first.

## Native tool calling (Phase 3 - preferred when the provider supports it)

Nothing changes for the agent author: the same manifest drives **two** ways the
model can ask for an ability, and chat-app picks the better one per reply.

When the active provider has a function/tool-calling API - Gemini
(`functionDeclarations`), OpenRouter / any OpenAI-compatible endpoint
(`tools` + `tool_calls`), ollama ≥ 0.3 (`tools`) or Bedrock Converse
(`toolConfig`) - every registered agent is advertised as one tool:

| tool field    | comes from                                                           |
|---------------|----------------------------------------------------------------------|
| `name`        | the agent id (lowercase, underscores)                                 |
| `description` | the manifest `description`                                            |
| `parameters`  | the declared `params` as a JSON schema (`int` → `integer`, `bool` → `boolean`, `enum` and `required` included) |

The model then calls it with **structured JSON arguments**, which pass the very
same validation as a tag (`unknown parameter`, type/enum and required checks,
defaults applied) before the agent runs - the trust model does not change: a
call arriving as JSON is no more trusted than one written in text. The outcome
goes back as the provider's own tool result (`functionResponse`, `role:"tool"`,
`toolResult`), paired on the provider's call id (a synthetic id is used when the
dialect has none) and marked `success`/`error`, so the model can answer or call
the next ability.

Because the tool schema already carries every parameter, the `[AGENT: ...]`
catalog is left out of the system prompt in this mode: the tag format is not
advertised, so the model does not mix the two.

**Fallback:** a provider, server or model without tool support answers the first
tool request with an error (an ollama server older than 0.3, a model that
rejects `tools`, a quota 4xx). chat-app then runs the `[AGENT: ...]` path for
that reply - catalog included - so the ability still happens; a provider that
can remember the rejection (ollama, on a 4xx) skips the doomed call from the
next reply on.

Both paths share every rule of the loop above: at most 4 abilities at a time,
one 60 s budget, at most 3 rounds, results sanitized into the same
`<<<AGENT>>>` data block (native tool results are one-line `OK …` / `ERR …`
texts), dedupe by ability + arguments, and `OK`/`ERR` still reach the reply and
the pet.

## CLI - agentctl

```sh
make agentctl
./agentctl list                  # catalog the model sees
./agentctl install <src>         # folder | .zip | https://...zip
./agentctl run <id> [k=v ...]    # one run, exactly like the chat pipeline
./agentctl validate <folder>     # manifest + exec check, no install
./agentctl -dir <path> ...       # override the agents directory
```

Install validates BEFORE writing (invalid source leaves the destination
untouched), replaces an existing folder with the same id, caps downloads
and unpacked size at 64MB, and rejects zip-slip (`../`) entries. One agent
per archive.

Packaging: `make pack` zips every shippable folder under `agents/` into
`dist/agents/<id>.zip` (folders starting with `_` or `.` are skipped) - the
same convention as `onidia`'s character packs.

## Environment

Agents inherit chat-app's full environment, plus these extras chat-app
sets from its configuration:

| variable | set from | purpose |
|---|---|---|
| `CHAT_APP_MUSIC_DIR` | `-music-dir` / `music-dir` | music folder for `play_song` (unset = agent uses `~/Music`) |
| `CHAT_APP_VIDEO_DIR` | `-video-dir` / `video-dir` | video folder for `play_movie` (unset = agent uses `~/Videos`) |
| `CHAT_APP_STATE_DIR` | derived (`$XDG_STATE_HOME/chat-app`) | where a media agent records the player it started, so the chat window can show a transport strip and `media_control` can pause/stop it. Best-effort: if it cannot be written, playback still works, there is just no strip |

Standard convention for your own agents: prefix config-derived variables
with `CHAT_APP_`. `CHAT_APP_PLAYER` is a built-in override (whole command
line, shell-split) that forces the media player - used by tests and
useful for debugging. **Never pass API keys to downloaded agents**; a
native agent (linked into chat-app, like `read_story`) is the right tool
when an ability needs the LLM.

## Security (v1 trust model)

Agents run with your user's privileges, straight from a user-writable
directory - equivalent to installing a script from the internet. No
sandbox, no signature check (deliberately deferred; "no stricter for now").
Reasonable boundaries already in place: params are validated and never
shell-interpolated by the brain (they arrive as JSON on stdin), paths and
argv come from the manifest (never from the model), ids are
regex-restricted, and timeouts bound every run. The agent's `OK`/`ERR` text
is untrusted too: when the brain feeds it back to the model it is sanitized
(role lines and chat-template markers defanged), length-capped and wrapped in
the `<<<AGENT>>>` data block. Install only agents you trust; review
`agent.json` + the script of anything you download.