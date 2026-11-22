# docs/integrations/claude-code.md: the plugin's hook script against a real
# clrd, with the hook JSON Claude Code sends, then `claude plugin validate`.
P=$ROOT/plugins/claude-code
H=$P/hooks/clrd-hook.sh
export CLAUDE_PLUGIN_OPTION_SERVER_URL=$CLRD/ CLAUDE_PLUGIN_OPTION_TOKEN=$CLRD_TOKEN CLAUDE_PROJECT_DIR=/src/myproj
last() { api "/api/v1/notifications?topic=agents" | jq -c '.notifications[0]'; }

for u in http://example.com http://localhost.example.com https://user:pw@example.com 'https://example.com/?x=1'; do
  out=$(CLAUDE_PLUGIN_OPTION_SERVER_URL=$u "$H" approve </dev/null 2>&1) && fail "ran against $u"
  [ "$out" = "clrd: server URL must be https (or http on localhost)" ] || fail "not refused before sending, $u: $out"
done
ok "plain http off localhost and user:password URLs are refused"

# The MCP server (#363): a bad URL answers every request with the reason and
# sends nothing; a good one carries requests side by side, a notification
# gets no answer.
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize"}' | CLAUDE_PLUGIN_OPTION_SERVER_URL=http://example.com "$H" mcp 2>/dev/null) && fail "mcp ran against http"
[ "$out" = '{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"clrd: server URL must be https (or http on localhost)"}}' ] || fail "mcp did not refuse http: $out"
# The id is read at the top level only (#414): spaces and escapes count, an
# "id" in a notification's params does not.
out=$(printf '%s\n' '{"jsonrpc":"2.0", "id" : "a\"b", "method":"ping"}' '{"jsonrpc":"2.0","method":"notifications/x","params":{"id":5}}' |
  CLAUDE_PLUGIN_OPTION_SERVER_URL=http://example.com "$H" mcp 2>/dev/null) && fail "mcp ran against http"
jq -se 'map(.id) == ["a\"b"]' <<<"$out" >/dev/null || fail "mcp id parsing: $out"
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"send","arguments":{"topic":"agents","title":"via the plugin MCP","message":"hi"}},"id":"s"}' |
  http_proxy=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 ALL_PROXY=http://127.0.0.1:9 "$H" mcp) # a proxy never sees localhost traffic
[ "$(wc -l <<<"$out")" = 2 ] || fail "mcp answers: $out"
jq -se '(sort_by(.id | tostring) | map(.id)) == [1, "s"] and all(.[]; has("result"))' <<<"$out" >/dev/null || fail "mcp answers: $out"
check "$(last)" '.title == "via the plugin MCP"' "mcp send: $out"
# plugin.json must use this bridge, never an http server that sends the token anywhere.
jq -e '.mcpServers.clrd | (has("url") or has("headers") | not) and .args == ["mcp"] and .command == "${CLAUDE_PLUGIN_ROOT}/hooks/clrd-hook.sh"' "$P/.claude-plugin/plugin.json" >/dev/null || fail "plugin.json MCP server is not the bridge"
ok "the MCP server refuses http off localhost and carries tool calls to clrd"

echo '{"hook_event_name":"Stop","cwd":"/src/myproj"}' | "$H" stop || fail "stop hook"
check "$(last)" '.title == "Claude finished: myproj" and .message == "Task finished."' "finished ping"
# #365: Claude's last message, past the 4 KB a topic publish takes, with quotes and escapes.
long=$(printf 'Done. Said \\"hi\\" \\\\ and\\nmore %.0s' $(seq 300)) # JSON-escaped
printf '{"hook_event_name": "Stop", "last_assistant_message": "%s","stop_hook_active":false}' "$long" | "$H" stop || fail "stop hook, long message"
want=$(printf '"%s"' "$long" | jq -r .)
[ ${#want} -gt 4096 ] || fail "test message is not past 4 KB"
jq -e --arg m "$want" '.title == "Claude finished: myproj" and .message == $m' <<<"$(last)" >/dev/null || fail "finished ping without the last message: $(last | head -c 300)"
ok "Stop sends a finished ping titled with the project, carrying Claude's last message"

echo '{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"Claude is waiting for your input"}' | "$H" wait || fail "wait hook"
check "$(last)" '.title == "Claude is waiting: myproj" and .message == "Claude is waiting for your input" and .priority == 4' "waiting ping"
ok "Notification sends a waiting ping with Claude's message"

"$H" no-raise | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null || fail "no-raise did not deny"
# The matcher must name the MCP tool as Claude Code does: mcp__plugin_<plugin>_<server>__<tool>.
jq -e '.hooks.PreToolUse[] | select(.matcher == "mcp__plugin_clrd_clrd__request_approve") | .hooks[].command | endswith("no-raise")' "$P/hooks/hooks.json" >/dev/null || fail "no-raise is not on request_approve"
ok "request_approve is denied before it runs"

# A permission prompt: the agent's token cannot answer it, the phone can.
echo '{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"touch proof"},"cwd":"/src/myproj"}' |
  "$H" approve >"$WORK/decision" &
hook=$!
for _ in $(seq 40); do
  id=$(api "/api/v1/notifications?topic=agents" | jq -r '[.notifications[] | select(.title | startswith("Allow"))][0].id // empty')
  [ -n "$id" ] && break
  sleep 0.5
done
[ -n "$id" ] || fail "no approval notification"
reply() { curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $1" -H 'Content-Type: application/json' -d '{"reply":"allow"}' "$CLRD/api/v1/notifications/$id/reply"; }
[ "$(reply "$CLRD_TOKEN")" = 403 ] || fail "the agent's own token answered its prompt"
[ "$(reply "$CLRD_PHONE")" = 200 ] || fail "the phone could not answer"
wait $hook || fail "approve hook failed"
check "$(cat "$WORK/decision")" '.hookSpecificOutput.hookEventName == "PermissionRequest" and .hookSpecificOutput.decision.behavior == "allow"' "decision"
ok "a permission prompt is allowed from the phone, never from the agent's token"

# Answered on screen (#364): Claude Code leaves the hook running, then runs
# the tool. PostToolUse's sweep stops the hook and the phone's prompt clears.
export CLAUDE_PLUGIN_DATA=$WORK/plugin-data
echo '{"session_id":"s-364","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"touch onscreen"},"cwd":"/src/myproj"}' |
  "$H" approve >"$WORK/decision2" &
hook=$!
id=
for _ in $(seq 40); do
  id=$(api "/api/v1/notifications?topic=agents" | jq -r '[.notifications[] | select(.message == "touch onscreen")][0].id // empty')
  [ -n "$id" ] && break
  sleep 0.5
done
[ -n "$id" ] || fail "no approval notification for the on-screen prompt"
echo '{"session_id":"s-other","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{}}' | "$H" sweep
echo '{"session_id":"s-364","hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{}}' | "$H" sweep
sleep 1
kill -0 $hook 2>/dev/null || fail "a sweep for another session or tool stopped the hook"
echo '{"session_id":"s-364","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"touch onscreen"},"tool_use_id":"t1"}' | "$H" sweep
for _ in $(seq 20); do kill -0 $hook 2>/dev/null || break; sleep 0.5; done
kill -0 $hook 2>/dev/null && fail "sweep did not stop the approve hook"
wait $hook && fail "a stopped approve hook exited 0"
[ ! -s "$WORK/decision2" ] || fail "a stopped approve hook printed a decision: $(cat "$WORK/decision2")"
for _ in $(seq 20); do
  state=$(api "/api/v1/notifications/$id" | jq -r .state)
  [ "$state" = resolved ] && break
  sleep 0.5
done
[ "$state" = resolved ] || fail "the phone's prompt is still $state after an on-screen answer"
ok "a prompt answered on screen leaves the phone"

cmd="npm i -g --silent --no-fund --no-audit @anthropic-ai/claude-code@2.1.329 >/dev/null && claude plugin validate /repo && claude plugin validate /repo/plugins/claude-code"
out=$(dock -v "$ROOT:/repo:ro" -e HOME=/tmp -e npm_config_prefix=/tmp/npm -e PATH=/tmp/npm/bin:/usr/local/bin:/usr/bin:/bin -e DISABLE_AUTOUPDATER=1 node:22-slim sh -ec "$cmd" 2>&1) || fail "claude plugin validate: $out"
ok "claude plugin validate passes for the marketplace and the plugin"
