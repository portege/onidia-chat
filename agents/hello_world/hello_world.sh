#!/bin/sh
# hello_world - the reference chat-app agent (protocol agent-line-v1).
#
# Contract (see docs/AGENT-PROTOCOL.md):
#   - one "RUN {json}" line arrives on stdin, keys = declared params only
#     (the brain validated them; defaults already filled in)
#   - reply "OK <message>" (shown to the user) or "ERR <why>" (failure)
#   - optional "INFO <text>" lines are logged, never shown
#   - then exit. Long work must be handed off (noholding the reply open).
set -eu

payload=""
IFS= read -r line || true
case "$line" in
RUN\ *) payload="${line#RUN }" ;;
*)
	echo "ERR expected a RUN line, got: ${line:-<eof>}"
	exit 1
	;;
esac

# Minimal extraction without jq: pull "key":"value" pairs. Values are
# brain-validated plain strings, so this is enough for the sample.
get() {
	printf '%s' "$payload" | sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p"
}

name="$(get name)"
[ -n "$name" ] || name="world"
style="$(get style)"
[ -n "$style" ] || style="friendly"

echo "INFO greeting $name ($style)"
case "$style" in
formal)
	echo "OK Good day, $name. It is a pleasure to greet you."
	;;
*)
	echo "OK Hey $name! hello_world says hi."
	;;
esac
exit 0