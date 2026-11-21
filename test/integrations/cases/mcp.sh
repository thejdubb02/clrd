# docs/integrations/mcp.md through the Claude Desktop config: the real
# mcp-remote bridge, spawned from the guide's JSON as Claude Desktop does.
# Claude Code and Cursor connect straight to the same /mcp over HTTP.
snippet mcp.md '"mcpServers"' | sed 's/"mcp-remote"/"mcp-remote@0.16.7"/' >"$WORK/desktop.json"
grep -q '"command": "npx"' "$WORK/desktop.json" || fail "the Desktop block did not match"
# The guide's send call, argument for argument.
send=$(snippet mcp.md 'wait_for_reply {' | sed -n '/^send /,/^wait_for_reply/p' | sed '$d' | sed 's/^send //' | jq -c .)
out=$(dock -e CLRD -e CLRD_TOKEN -e CLRD_PHONE -e SEND="$send" -e HOME=/tmp -e npm_config_update_notifier=false \
  -v "$HERE/mcp:/d:ro" -v "$WORK/desktop.json:/w/desktop.json:ro" node:22-slim node /d/drive.mjs /w/desktop.json)
res() { jq -c --arg t "$1" 'select(.tool == $t) | .out' <<<"$out" | head -1; }
check "$(res tools/list)" '. == ["ack","clear","counts","create_gotify_app","create_token","delete_hook","delete_template","device_log","devices","dismiss","end_silence","format_check","format_dismiss","get","hook_deliveries","hooks","list","ntfy_ignored","preview_template","reopen","request_approve","resolve","revoke_token","send","server_env","set_hook","set_template","set_topic","settings","silence","silences","snooze","sound","templates","tokens","topics","unlink","update","wait_for_reply"]' "tools/list"
ok "tools/list has the 34 tools in the guide"
check "$(res get)" '.message == "Step 3 of 3" and .state == "active"' "get after update"
check "$(res topics)" 'tostring | contains("agents")' "topics lists agents"
ok "update edits in place, get and topics read it back"
check "$(res list)" 'tostring | contains("deploy-1")' "list sees the sent notification"
check "$(res wait_for_reply)" '.reply == "acknowledged" and .answer == "yes"' "wait_for_reply"
ok "wait_for_reply returns the tapped answer"
expect agents deploy-1 '.state == "acknowledged" and .reply == "yes" and .message == "OK to deploy?"' "send with actions, answered"
expect agents job-2 '.state == "resolved"' "resolve by topic + external_id"
expect agents job-3 '.state == "acknowledged"' "ack by id"
expect agents job-4 '.state == "snoozed"' "snooze by id"
