# Telegram remote-control channel

Send a message to the bot on Telegram and it lands in the running onidia-chat
window exactly as if you had typed it and pressed SEND - the reply is rendered
locally (buddy, speech bubble, image, all of it) **and** mirrored back to
Telegram. Telegram is a *channel*, not an agent: it drives the same brain
(`Bot.Reply`) the window uses, so every existing agent works from the phone
for free.

## 1. Create the bot (once)

1. Open Telegram, talk to **@BotFather**, send `/newbot`.
2. Pick a display name and a username ending in `bot`.
3. Copy the token it gives you (`1234567890:AA...`). Treat it like a password.
4. (Optional) `/setinline` etc. are irrelevant here; no commands need
   registering - `/new`, `/help` are handled client-side by the channel.

## 2. Configure

Token precedence (highest first):

1. `-telegram-token` CLI flag
2. `$TELEGRAM_BOT_TOKEN` environment variable
3. `bot-token` in the `[telegram]` section of `chat-app.ini`

```ini
[telegram]
bot-token = 1234567890:AA...
allow-from = 123456789, @me
send-images = true
poll-timeout = 30
```

- **allow-from is fail-closed.** Empty (or missing) means *every* sender is
  refused, silently - the stranger's message is dropped with only a server-side
  log line, so the bot never reveals its presence. Numeric chat-ids and
  `@usernames` both work; find your own id with **@userinfobot**.
- **send-images** defaults **on**: a reply that carries an image is sent to
  Telegram as a photo (caption ≤ 1024 chars, remainder as a follow-up message).
  Set `send-images = false` to get text-only replies on Telegram.
- **poll-timeout** is the `getUpdates` long-poll window in seconds (default 30,
  clamped 1..120). Long-polling means no public URL, no TLS certificate, no
  webhook - the bot works behind NAT. On startup the channel calls
  `deleteWebhook` first, because a bot token may have either a webhook or
  long-polls, never both.
- If the token comes from the INI file and that file is group/world readable,
  the app warns once at startup (`chmod 600` fixes it). The token is never
  written to logs - only a masked form (`1234...AA...`).

Other flags: `-telegram-allow` overrides `allow-from`, `-telegram-off`
disables the channel even when a token is configured.

## 3. Use it

Send any text to the bot. It appears as a normal "you" bubble in the X11
window, the buddy answers, and the answer shows up in both places.

Commands:

| Send        | Does                                                     |
|-------------|----------------------------------------------------------|
| any text    | prompt - same pipeline as the SEND button                |
| `/new`      | fresh conversation (clears the shared history)           |
| `/help`     | short usage note                                         |

## Things that stay on the host

The channel runs **in-process on the machine running onidia-chat**, so:

- Media agents (`play_song`, `play_movie`, `media_control`, ...) execute on
  that machine - asking from the phone starts music on the host, not the phone.
- `PET` / mood / action tags drive the local buddy only; Telegram text just
  doesn't show them.
- Replies are forwarded only to the chat that asked (and only if it is
  allowlisted); local replies typed in the window are *not* broadcast to
  Telegram in v1 - only turns whose origin is the channel are forked back.

## Architecture (for contributors)

```
Telegram ──getUpdates long-poll──▶ telegram_poll.go
                                        │ handleUpdate (allowlist: channel_filter.go)
                                        ▼
                                  Router.Prompt ──▶ ui.externalIncoming queue
                                        │ (main loop drains on the UI goroutine)
                                        ▼
                             UI.SubmitExternal ──▶ Bot.Reply  (the whole agent stack)
                                                        │
                              main.go reply handler ◀───┘  renders locally
                                        │ origin is a channel?
                                        ▼
                             forkReplyToChannel ──▶ sendMessage / sendPhoto
```

New channels (Discord, HTTP, ...) implement the `Channel` interface from
`channel.go`, register in `init()` with `RegisterChannel(name, factory)` and
post `Incoming` values onto `ui.externalIncoming` - the reply fork comes for
free through the origin queue.
