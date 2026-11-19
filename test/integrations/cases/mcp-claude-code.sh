# docs/integrations/mcp.md, Claude Code: the guide's `claude mcp add`, then
# `claude mcp list`, which connects to each server and reports its health.
add=$(snippet mcp.md 'claude mcp add')
cmd="npm i -g --silent --no-fund --no-audit @anthropic-ai/claude-code@2.1.329 >/dev/null && cd /tmp && $add && claude mcp list"
out=$(dock -e HOME=/tmp -e npm_config_prefix=/tmp/npm -e PATH=/tmp/npm/bin:/usr/local/bin:/usr/bin:/bin -e DISABLE_AUTOUPDATER=1 node:22-slim sh -ec "$cmd" 2>&1) || fail "claude mcp: $out"
grep -q "clrd: $CLRD/mcp (HTTP) - .*Connected" <<<"$out" || fail "claude mcp list did not connect: $out"
ok "claude mcp add, then claude mcp list says Connected"
# And it really used the token: a wrong one must not connect.
bad=$(dock -e HOME=/tmp -e npm_config_prefix=/tmp/npm -e PATH=/tmp/npm/bin:/usr/local/bin:/usr/bin:/bin -e DISABLE_AUTOUPDATER=1 node:22-slim sh -ec "${cmd//$CLRD_TOKEN/clrd_wrong}" 2>&1) || true
grep -q "Connected" <<<"$bad" && fail "connected with a wrong token: $bad"
ok "a wrong token does not connect"
