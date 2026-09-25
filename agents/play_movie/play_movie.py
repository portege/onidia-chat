#!/usr/bin/env python3
"""play_movie - chat-app media agent (protocol agent-line-v1).

Twin of play_song.py (video edition): fuzzy-match query against the video
folder, launch a detached player, answer "OK Playing <title>". The run records
the player process in a JSON session file (see save_session) so the chat
window's play/pause/stop strip and the media_control agent can drive it. Keep
the two scripts' structure in sync - each folder must stay self-contained.
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

VIDEO_EXT = {".mp4", ".mkv", ".webm", ".avi", ".mov", ".m4v", ".wmv", ".flv", ".mpg", ".mpeg"}

# Player preference (first found on PATH) and per-player mode flags.
PREF = ["mpv", "cvlc", "vlc", "ffplay"]
MOVIE_FLAGS = {
    "mpv": ["--fullscreen"],
    "cvlc": ["--fullscreen"],
    "vlc": ["--fullscreen"],
    "ffplay": ["-fs"],
}

THRESHOLD = 0.45  # fuzzy score below this = "no match"


def out(line):
    print(line, flush=True)  # the brain reads line-by-line: never buffer


def video_dir():
    return os.path.expanduser(os.environ.get("CHAT_APP_VIDEO_DIR") or "~/Videos")


def index(root):
    files = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if not d.startswith(".")]
        for name in filenames:
            if name.startswith("."):
                continue
            if os.path.splitext(name)[1].lower() in VIDEO_EXT:
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
# Same contract as play_song.py: record the process we started so the chat
# window's transport strip and the media_control agent can pause/resume/stop
# it. Best-effort - playback never depends on it (see save_session).

def state_dir():
    d = os.environ.get("CHAT_APP_STATE_DIR")
    if d:
        return d
    base = os.environ.get("XDG_STATE_HOME") or os.path.expanduser("~/.local/state")
    return os.path.join(base, "chat-app")


def ipc_socket(name):
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
        os.replace(tmp, final)
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
    fullscreen = str(args.get("fullscreen", "true")).strip().lower() not in ("0", "false", "no", "off")

    root = video_dir()
    if not os.path.isdir(root):
        out("ERR video folder %s not found - set video-dir in chat-app.ini or CHAT_APP_VIDEO_DIR" % root)
        return 1
    files = index(root)
    out("INFO %d videos in %s" % (len(files), root))
    if not files:
        out("ERR no video files under %s" % root)
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
    flags = list(MOVIE_FLAGS.get(os.path.basename(player[0]), [])) if fullscreen else []
    ipc = ""
    if os.path.basename(player[0]) == "mpv":
        # mpv's control socket: media_control pauses through it and only falls
        # back to signals when it is off (see play_song.py).
        ipc = ipc_socket("play_movie")
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
    save_session("play_movie", proc.pid, title, path, os.path.basename(player[0]), ipc)
    out("INFO player: %s" % " ".join(player + flags))
    out("OK Playing %s" % title)
    return 0


if __name__ == "__main__":
    sys.exit(main())