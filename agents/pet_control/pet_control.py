#!/usr/bin/env python3
"""pet_control - chat-app character agent (protocol agent-line-v1).

The pet already knows how to change her face, fire an effect, perform an
animation and walk around; this agent only turns "cheer" into the right
one. It reads a RUN line, resolves kind + name against the pet's own
tables (aliases and all), and answers with a single PET line.

Two channels, because the pet itself has two:

  * action / event / move travel on the pet's cmd-FIFO ("action dance",
    "event love", "walk left") - emitted as PET lines that chat-app writes.
  * expression has NO cmd-FIFO verb. The pet's face is set from its
    say-FIFO by a bare [mood] tag with no text, which holds the face for
    a few seconds without opening a speech bubble. So the agent still
    emits "PET expr <mood>" and chat-app routes that one verb to the
    say-pipe. The agent never touches a pipe itself.

Every name is resolved to its canonical spelling here, so what reaches
chat-app always passes its own validation table. The tables mirror
desktop-pet's registries (characters.MoodExpr, eventRegistry,
actionRegistry, Behavior.HandleCommand); an unknown word is reported with
the valid list rather than sent for the pet to reject in silence.
"""
import json
import re
import sys

# --- expression ---------------------------------------------------------
# Canonical faces, in the order characters.Expressions lists them. The pet
# resolves aliases itself, but the agent normalises to a canonical name
# first so a typo is caught here instead of silently ignored.
EXPRESSIONS = [
    "neutral", "happy", "wink", "sad", "thinking", "anxious", "angry",
    "surprised", "sleepy", "fear", "disgust", "contempt", "confused",
    "skeptical", "embarrassed", "adore",
]
EXPR_ALIASES = {
    "smile": "neutral", "joy": "happy", "cheerful": "happy", "glad": "happy",
    "cry": "sad", "upset": "sad", "down": "sad", "unhappy": "sad",
    "think": "thinking", "hmm": "thinking", "curious": "thinking",
    "worry": "anxious", "worried": "anxious", "nervous": "anxious",
    "mad": "angry", "rage": "angry", "annoyed": "angry", "grumpy": "angry",
    "surprise": "surprised", "shock": "surprised", "shocked": "surprised",
    "tired": "sleepy", "sleeping": "sleepy", "drowsy": "sleepy",
    "afraid": "fear", "scared": "fear", "terrified": "fear",
    "disgusted": "disgust", "gross": "disgust", "eww": "disgust",
    "smug": "contempt", "smirk": "contempt", "sassy": "contempt",
    "puzzled": "confused", "confusion": "confused",
    "doubtful": "skeptical", "suspicious": "skeptical",
    "shy": "embarrassed", "blush": "embarrassed", "flustered": "embarrassed",
    "love": "adore", "loving": "adore", "fond": "adore", "heart eyes": "adore",
}

# --- event --------------------------------------------------------------
# Effect names, with the pet's informal spellings folded in.
EVENTS = {
    "love": ["hearts", "heart", "smile", "loveit"],
    "idea": ["lightbulb", "bulb", "light", "idea"],
    "celebration": ["party", "confetti", "confeti", "tada", "yay"],
    "sleep": ["zzz", "sleepy", "nap", "doze"],
    "peace": ["victory", "vsign"],
    "disappear": ["vanish", "hide", "poof", "gone"],
    "appear": ["show", "reveal", "return", "unhide"],
    "halloween": ["witch", "pumpkin", "spooky", "scary"],
    "matrix": ["hacker", "code"],
    "magic": ["magic", "abracadabra", "wizard"],
    "robot": ["bot", "android"],
}
EVENT_ALIASES = {a: k for k, v in EVENTS.items() for a in v}

# --- action -------------------------------------------------------------
ACTIONS = {
    "skip": ["rope", "jumprope", "skipping", "skippingrope"],
    "juggle": ["juggling", "balls"],
    "dance": ["dancing", "disco", "boogie"],
    "eat": ["eating", "food", "snack", "lunch", "dinner"],
    "work": ["working", "study", "typing", "busy"],
    "guitar": ["playguitar", "music", "sing", "rock"],
    "sneeze": ["sneezing", "achoo"],
    "sixseven": ["67", "hands", "airquote"],
    "basketball": ["hoops", "hoop", "ball", "sport"],
    "drive": ["driving", "car", "race"],
    "ride": ["riding", "skate", "board", "skateboard"],
    "kitten": ["cat", "pussycat", "meow"],
    "wave": ["waving", "hello", "hi", "goodbye", "bye"],
}
ACTION_ALIASES = {a: k for k, v in ACTIONS.items() for a in v}


# --- move ---------------------------------------------------------------
# The pet's own behaviour verbs. walk/go/move take an optional direction.
MOVES_NOARG = {
    "stand": ["stop", "freeze", "hold", "stay"],
    "idle": ["auto", "wander", "roam"],
    "left": ["l"],
    "right": ["r"],
    "jump": ["hop", "bounce"],
    "parachute": ["chute", "drop", "fall"],
    "skateboard": ["skate", "ride", "board"],
}
MOVE_ALIASES = {a: k for k, v in MOVES_NOARG.items() for a in v}
MOVE_WALK = {"walk", "go", "move", "run", "head"}
DIRECTIONS = {"left": "left", "l": "left", "right": "right", "r": "right"}

KINDS = {
    "expression": sorted(EXPRESSIONS),
    "event": sorted(EVENTS),
    "action": sorted(ACTIONS),
    "move": sorted(set(MOVE_WALK) | set(MOVES_NOARG)),
}

# The pet's wire verbs. These are NOT the same as our kind names: chat-app
# validates the verb against its own table, and it knows "expr", not
# "expression". Getting this wrong is silent - the line is just dropped.
# Movement is the exception: those verbs are bare on the wire already
# ("jump", "walk left"), which is why move has no entry here.
PET_VERB = {"expression": "expr", "event": "event", "action": "action"}

# How each action reads when spoken. The OK line joins the reply and is read
# aloud, so "She dance." is not good enough; these are gerunds or short
# phrases, all of them grammatical after "She is".
ACTION_PHRASE = {
    "skip": "skipping rope", "juggle": "juggling", "dance": "dancing",
    "eat": "eating", "work": "working", "guitar": "playing guitar",
    "sneeze": "sneezing", "sixseven": "counting to six",
    "basketball": "playing basketball", "drive": "driving",
    "ride": "riding a skateboard", "kitten": "playing with a kitten",
    "wave": "waving",
}

# Friendly spellings for each kind, so the same "cheer" can be a face or a
# confetti burst and the agent still knows which was meant.
KIND_ALIASES = {
    "expr": "expression", "face": "expression", "mood": "expression",
    "emotion": "expression", "feeling": "expression",
    "fx": "event", "effect": "event",
    "act": "action", "pose": "action", "animation": "action",
    "motion": "move",
}


def out(line):
    print(line, flush=True)  # the brain reads line-by-line: never buffer


def norm(s):
    """Lowercase, keep only bare words, so "Happy Face!" still matches."""
    return re.sub(r"[^a-z0-9]+", " ", str(s).lower()).strip()


def canon_kind(raw):
    """Fold an informal kind spelling onto its canonical name.

    main() calls this BEFORE anything else and uses the result everywhere, so
    the kind that is validated, turned into a PET verb and described in the OK
    line is always the same string. Passing the raw one to describe() was a
    KeyError for every alias - the script crashed after printing a perfectly
    good PET line, and the run was reported as a failure.
    """
    k = norm(raw).replace(" ", "")
    return KIND_ALIASES.get(k, k)


def resolve(kind, raw):
    """canonical kind + the user's words -> (pet_line, canonical_name, error)."""
    if kind not in KINDS:
        return "", "", "unknown kind %r - use expression, event, action or move" % kind

    if kind == "move":
        words = norm(raw).split()
        if not words:
            return "", "", "no movement named"
        verb = MOVE_ALIASES.get(words[0], words[0])
        if verb in MOVE_WALK:
            # "walk to the left" / "go right" / "walk" (direction optional)
            for w in words[1:]:
                if w in DIRECTIONS:
                    d = DIRECTIONS[w]
                    return "walk " + d, "walk " + d, ""
            return "walk", "walk", ""
        if verb in MOVES_NOARG:
            # Extra words are ignored rather than rejected: "stand still" and
            # "stand there please" both mean stand, and the pet only ever
            # receives the bare verb.
            return verb, verb, ""

    table, aliases = {
        "expression": (EXPRESSIONS, EXPR_ALIASES),
        "event": (sorted(EVENTS), EVENT_ALIASES),
        "action": (sorted(ACTIONS), ACTION_ALIASES),
    }[kind]
    name = match(raw, table, aliases)
    if not name:
        return "", "", "unknown %s %r" % (kind, raw)
    return "%s %s" % (PET_VERB[kind], name), name, ""


def match(raw, table, aliases):
    """Resolve a name to its canonical form.

    Tries the whole phrase first, then each word in turn, so "be really happy"
    still lands on happy. When several different words match ("happy dance" as
    an action) it refuses rather than picking one: guessing here would fire a
    random animation at the user.
    """
    squashed = norm(raw).replace(" ", "")
    if squashed in table:
        return squashed
    if squashed in aliases:
        return aliases[squashed]
    found = set()
    for w in norm(raw).split():
        if w in table:
            found.add(w)
        elif w in aliases:
            found.add(aliases[w])
        # multi-word aliases like "light bulb" are covered by the squash above
    return found.pop() if len(found) == 1 else ""


def describe(kind, name):
    return {
        "expression": "Her face is %s now." % name,
        "event": "She is doing the %s effect." % name,
        "action": "She is %s." % ACTION_PHRASE.get(name, name + "ing"),
        "move": "She is moving: %s." % name,
    }[kind]


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

    kind = canon_kind(str(args.get("kind", "")).strip())
    name = str(args.get("name", "")).strip()
    if not kind and not name:
        # Nothing to go on: list the catalogue rather than shrugging, so the
        # model can retry with a real name instead of repeating the mistake.
        out("ERR need kind and name - " + "; ".join(
            "%s: %s" % (k, ", ".join(v)) for k, v in sorted(KINDS.items())))
        return 1

    pet_line, canonical, err = resolve(kind, name)
    if err:
        out("ERR %s" % err)
        out("INFO valid %s: %s" % (kind, ", ".join(KINDS.get(kind, []))))
        return 1

    out("INFO %s -> %s" % (kind, canonical))
    out("PET %s" % pet_line)
    out("OK %s" % describe(kind, canonical))
    return 0


if __name__ == "__main__":
    sys.exit(main())
