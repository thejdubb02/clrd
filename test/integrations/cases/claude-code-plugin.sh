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
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"send","arguments":{"topic":"agents","title":"via the plugin MCP","message":"hi"}},"id":"s"}' | "$H" mcp)
[ "$(wc -l <<<"$out")" = 2 ] || fail "mcp answers: $out"
jq -se '(map(.id | tostring) | sort) == ["1", "s"] and all(.[]; has("result"))' <<<"$out" >/dev/null || fail "mcp answers: $out"
check "$(last)" '.title == "via the plugin MCP"' "mcp send: $out"
# plugin.json must use this bridge, never an http server that sends the token anywhere.
jq -e '.mcpServers.clrd | (has("url") or has("headers") | not) and .args == ["mcp"]' "$P/.claude-plugin/plugin.json" >/dev/null || fail "plugin.json MCP server is not the bridge"
ok "the MCP server refuses http off localhost and carries tool calls to clrd"

echo '{"hook_event_name":"Stop","cwd":"/src/myproj"}' | "$H" stop || fail "stop hook"
check "$(last)" '.title == "Claude finished: myproj" and .message == "Task finished."' "finished ping"
ok "Stop sends a finished ping titled with the project"

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

cmd="npm i -g --silent --no-fund --no-audit @anthropic-ai/claude-code@2.1.329 >/dev/null && claude plugin validate /repo && claude plugin validate /repo/plugins/claude-code"
out=$(dock -v "$ROOT:/repo:ro" -e HOME=/tmp -e npm_config_prefix=/tmp/npm -e PATH=/tmp/npm/bin:/usr/local/bin:/usr/bin:/bin -e DISABLE_AUTOUPDATER=1 node:22-slim sh -ec "$cmd" 2>&1) || fail "claude plugin validate: $out"
ok "claude plugin validate passes for the marketplace and the plugin"
