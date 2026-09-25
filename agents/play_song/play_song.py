#!/usr/bin/env python3
"""play_song - chat-app media agent (protocol agent-line-v1).

Reads one "RUN {json}" line on stdin, fuzzy-matches query against the
music folder index, launches a detached player, answers
"OK Playing <title>". INFO lines are logged by the brain; ERR is shown
to the user. After the player is up it asks the pet to dance
("PET action dance"), so the character acts the music out. Folder must
stay self-contained (one zip = one agent); keep the structure in sync
with play_movie.py.
"""
import json
import os
import random
import re
import shlex
import shutil
import subprocess
import sys
import time
from difflib import SequenceMatcher

AUDIO_EXT = {".mp3", ".flac", ".ogg", ".opus", ".m4a", ".aac", ".wav", ".wma"}

# Preference chain: first found on PATH wins; per-player mode flags.
PREF = ["mpv", "cvlc", "vlc", "ffplay"]
SONG_FLAGS = {
    "mpv": ["--no-video", "--really-quiet"],
    "cvlc": ["--play-and-exit"],
    "vlc": ["--play-and-exit", "--intf", "dummy"],
    "ffplay": ["-nodisp", "-autoexit", "-loglevel", "quiet"],
}

THRESHOLD = 0.45  # fuzzy score below this = "no match"


def out(line):
    print(line, flush=True)  # the brain reads line-by-line: never buffer


def music_dir():
    return os.path.expanduser(os.environ.get("CHAT_APP_MUSIC_DIR") or "~/Music")


def index(root):
    files = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if not d.startswith(".")]
        for name in filenames:
            if name.startswith("."):
                continue
            if os.path.splitext(name)[1].lower() in AUDIO_EXT:
                files.append(os.path.join(dirpath, name))
    return files


def score(query, stem):
    """0..~1.3 fuzzy score of query against a lowercased filename stem."""
    s = SequenceMatcher(None, query, stem).ratio()
    if query in stem:
        s += 0.5
    tokens = [t for t in re.split(r"[^a-z0-9]+", query) if t]
    if tokens and all(t in stem for t in tokens):
        s += 0.3
    return s


def pick_player():
    forced = shlex.split(os.environ.get("CHAT_APP_PLAYER", ""))
    if forced:
        return forced
    for name in PREF:
        if shutil.which(name):
            return [name]
    return None


# --- transport control -----------------------------------------------------
# chat-app draws a play/pause/stop strip for whatever is playing, and its
# media_control agent drives the player. Both need to know WHICH process we
# started, so every successful run records it in a small JSON session file.
# None of this is required for playback: if the state dir cannot be written we
# still answer OK, we just do not get a transport strip.

def state_dir():
    """Where session files live. chat-app exports CHAT_APP_STATE_DIR so the
    app and its agents always agree; the fallbacks only matter for a manual
    `agentctl run`."""
    d = os.environ.get("CHAT_APP_STATE_DIR")
    if d:
        return d
    base = os.environ.get("XDG_STATE_HOME") or os.path.expanduser("~/.local/state")
    return os.path.join(base, "chat-app")


def ipc_socket(name):
    """mpv control-socket path for this agent, or "" when it would be too long
    for AF_UNIX (then media_control falls back to signals)."""
    sock = os.path.join(state_dir(), name + ".sock")
    return sock if len(sock) <= 100 else ""


def save_session(name, pid, title, path, player, ipc):
    try:
        os.makedirs(state_dir(), exist_ok=True)
        final = os.path.join(state_dir(), name + ".json")
        tmp = final + ".tmp"
        with open(tmp, "w") as f:
            json.dump({"id": name, "pid": pid, "title": title, "path": path,
                       "player": player, "ipc": ipc, "paused": False,
                       "started": int(time.time())}, f)
        os.replace(tmp, final)  # atomic: a reader never sees a half-written file
    except (OSError, ValueError) as e:
        out("INFO transport state not recorded: %s" % e)


def main():
    line = sys.stdin.readline()
    if not line.startswith("RUN "):
        out("ERR protocol: expected a RUN line")
        return 1
    try:
        args = json.loads(line[4:])
    except ValueError as e:
        out("ERR bad RUN payload: %s" % e)
        return 1
    query = str(args.get("query", "")).strip()

    root = music_dir()
    if not os.path.isdir(root):
        out("ERR music folder %s not found - set music-dir in chat-app.ini or CHAT_APP_MUSIC_DIR" % root)
        return 1
    files = index(root)
    out("INFO %d tracks in %s" % (len(files), root))
    if not files:
        out("ERR no audio files under %s" % root)
        return 1

    if query:
        ranked = sorted(
            ((score(query.lower(), os.path.splitext(os.path.basename(f))[0].lower()), f)
             for f in files),
            reverse=True)
        best_score, path = ranked[0]
        if best_score < THRESHOLD:
            hints = ", ".join(
                os.path.splitext(os.path.basename(f))[0][:40] for _, f in ranked[:3])
            out("ERR no match for '%s' (closest: %s)" % (query, hints))
            return 1
    else:
        path = random.choice(files)

    player = pick_player()
    if not player:
        out("ERR no media player found - install mpv, vlc or ffmpeg (ffplay)")
        return 1
    flags = list(SONG_FLAGS.get(os.path.basename(player[0]), []))
    ipc = ""
    if os.path.basename(player[0]) == "mpv":
        # mpv's control socket: media_control pauses through it (a real pause,
        # audio thread included) and only falls back to signals when it is off.
        ipc = ipc_socket("play_song")
        if ipc:
            flags.append("--input-ipc-server=" + ipc)
    title = os.path.splitext(os.path.basename(path))[0]
    title = title.replace("\n", " ").replace("\r", " ")  # protocol = one line
    try:
        proc = subprocess.Popen(
            player + flags + [path],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,  # fully detached: never holds our pipes
        )
    except OSError as e:
        out("ERR cannot start %s: %s" % (player[0], e))
        return 1
    save_session("play_song", proc.pid, title, path, os.path.basename(player[0]), ipc)
    out("INFO player: %s" % " ".join(player + flags))
    out("PET action dance")  # the pet should act the music out (disco!)
    out("OK Playing %s" % title)
    return 0


if __name__ == "__main__":
    sys.exit(main())