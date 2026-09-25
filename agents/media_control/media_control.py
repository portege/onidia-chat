#!/usr/bin/env python3
"""media_control - pause / resume / stop a player started by play_song or play_movie.

chat-app shows a transport strip (play/pause + stop) for whatever is playing,
and the model can call this agent directly ("pause the music"). Both paths end
here: the buttons run the same agent, so there is exactly one implementation of
what pause means.

How it works: every successful play_* run records the process it started in a
small JSON session file (see play_song.py:save_session). This agent reads those
files, drops the ones whose process is gone, and drives what is left:

  * mpv runs with --input-ipc-server, so pause/resume go through its control
    socket (a real pause: the audio thread parks and playback resumes from the
    same position);
  * every other player (vlc, cvlc, ffplay) is signalled - SIGSTOP/SIGCONT for
    pause/resume, SIGINT -> SIGTERM -> SIGKILL for stop.

Protocol agent-line-v1: read one RUN {json} line, answer OK <message> or
ERR <why>, exit. Keep this folder self-contained (one zip = one agent).
"""
import errno
import json
import os
import signal
import socket
import sys
import time

CMDS = ("pause", "resume", "stop", "status")
TARGETS = ("any", "song", "movie")
STOP_GRACE = 1.5   # seconds to wait after SIGINT before escalating
KILL_GRACE = 0.5   # seconds after SIGTERM before SIGKILL


def out(line):
    print(line, flush=True)  # the brain reads line-by-line: never buffer


def state_dir():
    d = os.environ.get("CHAT_APP_STATE_DIR")
    if d:
        return d
    base = os.environ.get("XDG_STATE_HOME") or os.path.expanduser("~/.local/state")
    return os.path.join(base, "chat-app")


def alive(pid):
    if pid <= 0:
        return False
    try:
        os.kill(pid, 0)
    except OSError as e:
        return e.errno == errno.EPERM  # exists but not ours
    return True



def load_sessions():
    """[(path, dict)] for every session file, pruning the dead ones."""
    root = state_dir()
    found = []
    try:
        names = sorted(os.listdir(root))
    except OSError:
        return found
    for name in names:
        if not name.endswith(".json"):
            continue
        path = os.path.join(root, name)
        try:
            with open(path) as f:
                s = json.load(f)
        except (OSError, ValueError):
            continue
        if not isinstance(s, dict) or not isinstance(s.get("pid"), int):
            continue
        s.setdefault("id", name[:-5])
        s.setdefault("title", "")
        s["paused"] = bool(s.get("paused"))
        if alive(s["pid"]):
            found.append((path, s))
        else:
            # The player is gone (it ended, or the user closed it): forget it,
            # so the strip disappears and a later run starts clean.
            try:
                os.remove(path)
            except OSError:
                pass
    return found


def save(path, s):
    try:
        tmp = path + ".tmp"
        with open(tmp, "w") as f:
            json.dump(s, f)
        os.replace(tmp, path)
    except OSError as e:
        out("INFO session not updated: %s" % e)


def pick(sessions, target):
    """The session a command applies to: newest first, filtered by target."""
    cand = sessions
    if target in ("song", "movie"):
        cand = [(p, s) for p, s in sessions if target in str(s.get("id", ""))]
    if not cand:
        return None
    return max(cand, key=lambda ps: ps[1].get("started", 0))


def mpv_cmd(sock, *command):
    """Send one JSON command to mpv's control socket. True when mpv answered."""
    if not sock:
        return False
    try:
        c = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        c.settimeout(1.0)
        c.connect(sock)
        c.sendall((json.dumps({"command": list(command)}) + "\n").encode())
        data = c.recv(4096)  # mpv answers every command
        c.close()
        return b"error" not in data
    except (OSError, ValueError):
        return False  # no socket / not mpv / older mpv: signals take over


def describe(s):
    state = "paused" if s["paused"] else "playing"
    return "%s (%s, %s)" % (s.get("title") or s.get("id") or "?", s.get("player") or "player", state)



def do_pause(path, s):
    if s["paused"]:
        return "Already paused: %s" % describe(s)
    if not mpv_cmd(s.get("ipc"), "set_property", "pause", True):
        try:
            os.kill(s["pid"], signal.SIGSTOP)
        except OSError as e:
            return None, "cannot pause %s: %s" % (s["pid"], e)
    s["paused"] = True
    save(path, s)
    return "Paused: %s" % describe(s)


def do_resume(path, s):
    if not s["paused"]:
        return "Already playing: %s" % describe(s)
    if not mpv_cmd(s.get("ipc"), "set_property", "pause", False):
        try:
            os.kill(s["pid"], signal.SIGCONT)
        except OSError as e:
            return None, "cannot resume %s: %s" % (s["pid"], e)
    s["paused"] = False
    save(path, s)
    return "Resumed: %s" % describe(s)


def do_stop(path, s):
    pid = s["pid"]
    # A stopped (SIGSTOP) process cannot act on SIGINT until it runs again.
    if s["paused"]:
        try:
            os.kill(pid, signal.SIGCONT)
        except OSError:
            pass
    for sig, wait in ((signal.SIGINT, STOP_GRACE), (signal.SIGTERM, KILL_GRACE), (signal.SIGKILL, 0.0)):
        if not alive(pid):
            break
        try:
            os.kill(pid, sig)
        except OSError as e:
            if e.errno != errno.ESRCH:
                return None, "cannot stop %s: %s" % (pid, e)
            break
        deadline = time.time() + wait
        while time.time() < deadline and alive(pid):
            time.sleep(0.05)
    if alive(pid):
        return None, "player %s ignored the stop request" % pid
    try:
        os.remove(path)
    except OSError:
        pass
    try:
        os.remove(s.get("ipc") or "")
    except OSError:
        pass
    return "Stopped: %s" % describe(s)


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
    cmd = str(args.get("cmd", "status")).strip().lower()
    target = str(args.get("target", "any")).strip().lower() or "any"
    if cmd not in CMDS:
        out("ERR cmd must be one of: %s" % ", ".join(CMDS))
        return 1
    if target not in TARGETS:
        out("ERR target must be one of: %s" % ", ".join(TARGETS))
        return 1

    sessions = load_sessions()
    picked = pick(sessions, target)
    if picked is None:
        if cmd == "status":
            out("OK nothing is playing")
            return 0
        out("ERR nothing is playing - nothing to %s" % cmd)
        return 1
    path, s = picked

    if cmd == "status":
        others = len(sessions) - 1
        msg = describe(s)
        if others > 0:
            msg += " (+%d more)" % others
        out("OK " + msg)
        return 0

    fn = {"pause": do_pause, "resume": do_resume, "stop": do_stop}[cmd]
    res = fn(path, s)
    if isinstance(res, tuple):  # (None, error)
        out("ERR " + res[1])
        return 1
    if cmd == "stop":
        # Mirror of play_song's dance: the music is gone, so the pet stops
        # celebrating and goes back to hopping. It goes BEFORE the OK line,
        # because the OK ends the protocol stream (see external.go).
        out("PET action skip")
    out("OK " + res)
    return 0


if __name__ == "__main__":
    sys.exit(main())
