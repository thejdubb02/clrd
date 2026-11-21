#!/usr/bin/env bash
# clrd hook for Claude Code. Reads the hook's JSON on stdin and passes it to
# clrd. The token comes from the environment and goes to curl on a file
# descriptor, so it never shows on a command line, in a URL or in a log.
#
#   approve   PermissionRequest: ask your phone, print clrd's decision.
#   stop      Stop: ping that the task finished.
#   wait      Notification: ping that Claude is waiting on you.
#   no-raise  PreToolUse on request_approve: an agent never asks to approve.
#
# Any failure prints nothing on stdout, so Claude Code shows its own prompt.
set -u

if [ "${1:-}" = no-raise ]; then
  echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Agents never ask to approve their own prompts. A person changes that in clrd."}}'
  exit 0
fi

url=${CLAUDE_PLUGIN_OPTION_SERVER_URL:-}
url=${url%/}
token=${CLAUDE_PLUGIN_OPTION_TOKEN:-}
topic=${CLAUDE_PLUGIN_OPTION_TOPIC:-agents}
topic=${topic:-agents}

case $url in
  *@*) url= ;; # no user:password@ in the URL
  https://?*) ;;
  http://localhost | http://localhost[:/]* | http://127.0.0.1 | http://127.0.0.1[:/]* | "http://[::1]" | "http://[::1]"[:/]*) ;;
  *) url= ;;
esac
[ -n "$url" ] || { echo "clrd: server URL must be https (or http on localhost)" >&2; exit 1; }
[[ $token =~ ^[A-Za-z0-9_]+$ ]] || { echo "clrd: set the token in the plugin's settings" >&2; exit 1; }
[[ $topic =~ ^[A-Za-z0-9_-]+$ ]] || { echo "clrd: topic may use letters, digits, _ and -" >&2; exit 1; }

# post PATH MAX-SECONDS [curl args]: one request to clrd, token on a file descriptor.
post() {
  exec curl -sSf --proto =https,http --max-time "$2" \
    -K <(printf 'header = "Authorization: Bearer %s"\n' "$token") "${@:3}" "$url$1"
}

# Pings are titled with the project folder's name, one line.
project=${CLAUDE_PROJECT_DIR:-$PWD}
project=${project##*/}
project=${project//[[:cntrl:]\{\}]/} # no braces: the wait title is a template
case ${1:-} in
  approve) post "/mcp/approve?topic=$topic" 600 -H 'Content-Type: application/json' --data-binary @- ;;
  # Stop's JSON carries Claude's whole last message, often past the 4 KB a
  # publish takes, so the finished ping says only which project finished.
  stop) post "/$topic" 15 -o /dev/null -H "X-Title: Claude finished: $project" --data-binary 'Task finished.' ;;
  # Notification's JSON is small: clrd fills the message in from it.
  wait) post "/$topic" 15 -o /dev/null -H 'X-Template: yes' -H 'X-Priority: 4' -H "X-Title: Claude is waiting: $project" \
    -H 'X-Message: {{or .message "Claude needs you."}}' -H 'Content-Type: application/json' --data-binary @- ;;
  *) echo "clrd: unknown mode ${1:-}" >&2; exit 1 ;;
esac
