#!/usr/bin/env bash
# clrd hook for Claude Code. Reads the hook's JSON on stdin and passes it to
# clrd. The token comes from the environment and goes to curl on a file
# descriptor, so it never shows on a command line, in a URL or in a log.
#
#   approve   PermissionRequest: ask your phone, print clrd's decision.
#   stop      Stop: ping that the task finished.
#   wait      Notification: ping that Claude is waiting on you.
#   no-raise  PreToolUse on request_approve: an agent never asks to approve.
#   sweep     PostToolUse: a prompt answered on screen leaves the phone.
#   mcp       The MCP server: JSON-RPC lines on stdin, each posted to /mcp.
#
# A hook's failure prints nothing on stdout, so Claude Code shows its own
# prompt. The MCP server answers each request with the reason instead.
set -u
mode=${1:-}

# rpcid LINE: sets id to the request's top-level id, as JSON; false for a
# notification. A small scan, not a regex over the line, so an "id" inside
# params never counts and spaces or escapes in the id do (#414).
rpcid() {
  local s=$1 i=0 d=0 str='^("([^"\\]|\\.)*")[[:space:]]*(:?)' val='^[[:space:]]*(-?[0-9]+|"([^"\\]|\\.)*")'
  while ((i < ${#s})); do
    case ${s:i:1} in
      \")
        [[ ${s:i} =~ $str ]] || return 1
        ((i += ${#BASH_REMATCH[0]}))
        if ((d == 1)) && [ "${BASH_REMATCH[1]}" = '"id"' ] && [ -n "${BASH_REMATCH[3]}" ]; then
          [[ ${s:i} =~ $val ]] && id=${BASH_REMATCH[1]}
          return
        fi
        continue ;;
      '{' | '[') ((d++)) ;;
      '}' | ']') ((d--)) ;;
    esac
    ((i++))
  done
  return 1
}

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

# Pending phone prompts, one file per approve hook: <session>.<tool>.<pid>.
# Answering on screen does not stop the hook (#364), so when the tool then
# runs, sweep stops it and clrd clears the phone when the request hangs up.
pending=${CLAUDE_PLUGIN_DATA:-${XDG_RUNTIME_DIR:-$HOME/.cache}/clrd}/pending
key() {
  [[ $1 =~ \"session_id\":\"([A-Za-z0-9-]+)\" ]] && sid=${BASH_REMATCH[1]} &&
    [[ $1 =~ \"tool_name\":\"([A-Za-z0-9_-]+)\" ]] && key=$sid.${BASH_REMATCH[1]}
}
if [ "$mode" = sweep ]; then
  key "$(cat)" || exit 0
  for f in "$pending/$key".*; do
    [ -e "$f" ] || continue
    pid=${f##*.}
    # Still bash, not yet curl: keep the file for the tool's own sweep.
    if [[ $(ps -o args= -p "$pid" 2>/dev/null) == *curl*/mcp/approve* ]]; then
      kill "$pid" && rm -f "$f"
    elif ! kill -0 "$pid" 2>/dev/null; then
      rm -f "$f"
    fi
  done
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
# --noproxy: plain http to localhost never goes through a proxy, which would
# see the token.
post() {
  exec curl -q -sSf --proto =https,http --noproxy localhost,127.0.0.1,::1 --max-time "$2" \
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
  # Requests run side by side (wait_for_reply blocks). Each leaves its answer
  # in a file and names it on a pipe; one writer prints each whole, so a
  # worker that dies holds nothing up (#414). The writer ends, and removes
  # the folder, once every worker has closed the pipe.
  mcp)
    dir=$(mktemp -d) || exit 1
    exec 3> >(while IFS= read -r f; do cat "$dir/$f" && rm -f "$dir/$f"; done; rm -rf "$dir")
    n=0
    while IFS= read -r req; do
      n=$((n + 1))
      (
        out=$(printf '%s' "$req" | post /mcp 660 -H 'Content-Type: application/json' -H 'Accept: application/json' --data-binary @- 2>&1 3>&-)
        rc=$?
        if [ $rc -ne 0 ] || [ "${out:0:1}" != "{" ]; then
          rpcid "$req" || exit 0 # a notification gets no answer; clrd sends 202 and no body
          out=${out//[\"\\[:cntrl:]]/}
          out="{\"jsonrpc\":\"2.0\",\"id\":$id,\"error\":{\"code\":-32000,\"message\":\"clrd: ${out:0:200}\"}}"
        fi
        printf '%s\n' "$out" >"$dir/$n" && echo "$n" >&3
      ) &
    done
    exec 3>&-
    wait
    ;;
  # exec keeps this pid for curl, so sweep can stop it.
  approve)
    body=$(cat)
    # shellcheck disable=SC2174 # only the pending dir itself needs 700
    key "$body" && mkdir -p -m 700 "$pending" && : >"$pending/$key.$$"
    post "/mcp/approve?topic=$topic" 600 -H 'Content-Type: application/json' --data-binary @- <<<"$body"
    ;;
  # Stop's JSON carries Claude's last message, often past the 4 KB a topic
  # publish takes, so it goes to /mcp's send as is, still a JSON string.
  stop)
    msg='"Task finished."'
    [[ $(cat) =~ \"last_assistant_message\":[[:space:]]*(\"([^\"\\]|\\.)+\") ]] && msg=${BASH_REMATCH[1]}
    printf '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send","arguments":{"topic":"%s","title":"Claude finished: %s","message":%s}}}' \
      "$topic" "${project//[\"\\]/}" "$msg" |
      post /mcp 15 -o /dev/null -H 'Content-Type: application/json' -H 'Accept: application/json' --data-binary @-
    ;;
  # Notification's JSON is small: clrd fills the message in from it.
  wait) post "/$topic" 15 -o /dev/null -H 'X-Template: yes' -H 'X-Priority: 4' -H "X-Title: Claude is waiting: $project" \
    -H 'X-Message: {{or .message "Claude needs you."}}' -H 'Content-Type: application/json' --data-binary @- ;;
  *) echo "clrd: unknown mode $mode" >&2; exit 1 ;;
esac
