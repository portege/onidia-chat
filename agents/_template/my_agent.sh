#!/bin/sh
# my_agent - template chat-app agent (protocol agent-line-v1).
# Copy this folder, rename id/params in agent.json, implement the body.
# Contract: read one RUN line, answer OK <message> or ERR <why>, exit.
# Optional: echo "PET action dance" (or "PET event love") before the OK line
# to make the pet act the work out - see docs/AGENT-PROTOCOL.md.
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

# Pull "key":"value" pairs (values are pre-validated plain strings), or
# use jq/python for richer payloads.
get() {
	printf '%s' "$payload" | sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p"
}

query="$(get query)"
[ -n "$query" ] || {
	echo "ERR missing query"
	exit 1
}

echo "INFO working on: $query"
# TODO: do the actual work here.
echo "OK did something with: $query"
exit 0