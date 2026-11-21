#!/usr/bin/env bash
# clrd hook for Claude Code. Reads the hook's JSON on stdin and passes it to
# clrd. The token comes from the environment and goes to curl on a file
# descriptor, so it never shows on a command line, in a URL or in a log.
#
#   approve   PermissionRequest: ask your phone, print clrd's decision.
#   stop      Stop: ping that the task finished.
#   wait      Notification: ping that Claude is waiting on you.
#   no-raise  PreToolUse on request_approve: an agent never asks to approve.
#   mcp       The MCP server: JSON-RPC lines on stdin, each posted to /mcp.
#
# A hook's failure prints nothing on stdout, so Claude Code shows its own
# prompt. The MCP server answers each request with the reason instead.
set -u
mode=${1:-}

# rpcid LINE: sets id to the request's id; false for a notification. The MCP
# SDK writes id last, after params, so the last "id" in the line is the one.
rpcid() { [[ $1 =~ .*\"id\":(-?[0-9]+|\"[^\"\\]*\")[,}] ]] && id=${BASH_REMATCH[1]}; }

die() {
  echo "clrd: $1" >&2
  if [ "$mode" = mcp ]; then
    while IFS= read -r req; do
      rpcid "$req" && printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"clrd: %s"}}\n' "$id" "$1"
    done
  fi
  exit 1
}

if [ "$mode" = no-raise ]; then
  echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Agents never ask to approve their own prompts. A person changes that in clrd."}}'
  exit 0
fi

url=${CLAUDE_PLUGIN_OPTION_SERVER_URL:-}
url=${url%/}
token=${CLAUDE_PLUGIN_OPTION_TOKEN:-}
topic=${CLAUDE_PLUGIN_OPTION_TOPIC:-agents}
topic=${topic:-agents}

case $url in
  *@* | *[?#]*) url= ;; # no user:password@, query or fragment in the URL
  https://?*) ;;
  http://localhost | http://localhost[:/]* | http://127.0.0.1 | http://127.0.0.1[:/]* | "http://[::1]" | "http://[::1]"[:/]*) ;;
  *) url= ;;
esac
[ -n "$url" ] || die "server URL must be https (or http on localhost)"
[[ $token =~ ^[A-Za-z0-9_]+$ ]] || die "set the token in the plugin's settings"
[[ $topic =~ ^[A-Za-z0-9_-]+$ ]] || die "topic may use letters, digits, _ and -"

# post PATH MAX-SECONDS [curl args]: one request to clrd, token on a file
# descriptor. -q (first) skips ~/.curlrc, so it cannot turn off TLS checks.
post() {
  exec curl -q -sSf --proto =https,http --max-time "$2" \
    -K <(printf 'header = "Authorization: Bearer %s"\n' "$token") "${@:3}" "$url$1"
}

# Pings are titled with the project folder's name, one line.
project=${CLAUDE_PROJECT_DIR:-$PWD}
project=${project##*/}
project=${project//[[:cntrl:]\{\}]/} # no braces: the wait title is a template
case $mode in
  # A stdio bridge rather than an http server in plugin.json: Claude Code
  # sends an http server's headers to any URL, so the token could go out
  # over plain http. Here it goes only where the check above allows (#363).
  # Requests run side by side (wait_for_reply blocks); a lock keeps each
  # answer on its own line.
  mcp)
    lock=$(mktemp -d) || exit 1
    trap 'rm -rf "$lock"' EXIT
    while IFS= read -r req; do
      (
        out=$(printf '%s' "$req" | post /mcp 660 -H 'Content-Type: application/json' -H 'Accept: application/json' --data-binary @- 2>&1)
        rc=$?
        rpcid "$req" || exit 0 # a notification gets no answer
        if [ $rc -ne 0 ] || [ "${out:0:1}" != "{" ]; then
          out=${out//[\"\\[:cntrl:]]/}
          out="{\"jsonrpc\":\"2.0\",\"id\":$id,\"error\":{\"code\":-32000,\"message\":\"clrd: ${out:0:200}\"}}"
        fi
        until mkdir "$lock/w" 2>/dev/null; do sleep 0.01; done
        printf '%s\n' "$out"
        rmdir "$lock/w"
      ) &
    done
    wait
    ;;
  approve) post "/mcp/approve?topic=$topic" 600 -H 'Content-Type: application/json' --data-binary @- ;;
  # Stop's JSON carries Claude's whole last message, often past the 4 KB a
  # publish takes, so the finished ping says only which project finished.
  stop) post "/$topic" 15 -o /dev/null -H "X-Title: Claude finished: $project" --data-binary 'Task finished.' ;;
  # Notification's JSON is small: clrd fills the message in from it.
  wait) post "/$topic" 15 -o /dev/null -H 'X-Template: yes' -H 'X-Priority: 4' -H "X-Title: Claude is waiting: $project" \
    -H 'X-Message: {{or .message "Claude needs you."}}' -H 'Content-Type: application/json' --data-binary @- ;;
  *) echo "clrd: unknown mode $mode" >&2; exit 1 ;;
esac
