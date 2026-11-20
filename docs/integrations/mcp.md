# MCP (Claude Code, Claude Desktop, Cursor)

clrd serves MCP at `https://clrd.itsjustin.me/mcp` (streamable HTTP, stateless).
Auth is the same Bearer token as the REST API; the token's scopes and topic
allow-list apply, and actions show up as made by that token's device.

Tools:

| Tool | Arguments | Does |
|---|---|---|
| `send` | `topic, message, title?, priority?, id?, actions?` | Publish. `id` is your stable key (`external_id`). `actions` adds up to 3 answer buttons, `[{"label":"Yes","reply":"yes"}]`. |
| `list` | `topic?, state?` | List, default `open`. |
| `ack` | `id` | Acknowledge by server id. |
| `resolve` | `id` or `topic + external_id` | Clear it everywhere. |
| `snooze` | `id, for` | Snooze, e.g. `for: "1h"`. |
| `wait_for_reply` | `id, timeout_s?` | Block until a human acks or dismisses it, or it is resolved. Snooze keeps it waiting. Returns `reply` = the new state, or `timeout`, plus `answer` when a button from `actions` was tapped. Default 60 s, max 600 s. |

`wait_for_reply` returns at once if the notification is already acknowledged,
dismissed or resolved. Otherwise it watches the server's committed events, so
an ack that is quickly followed by a snooze is still reported as an ack.

`wait_for_reply` is how an agent asks a human "OK to deploy?" and waits:

```json
send {"topic": "agents", "message": "OK to deploy?", "id": "deploy-1",
      "actions": [{"label": "Yes", "reply": "yes"}, {"label": "No", "reply": "no"}]}
wait_for_reply {"id": "<id from send>", "timeout_s": 600}
-> {"reply": "acknowledged", "answer": "yes", "by": "pixel-9", "notification": {...}}
```

The first tap wins; a second phone gets told it is already answered. A plain
ack (no button) has no `answer`, and dismiss means no. The web inbox and the
Android app show the buttons once they support them
([issue 8](https://github.com/thejdubb02/clrd/issues/8)); until then a human
can still ack or dismiss.

## Approve agent actions from your phone

`POST /mcp/approve` answers a coding agent's permission prompt. Point Claude
Code's or Codex's `PermissionRequest` hook at it: clrd sends a notification
with **Allow** and **Deny** buttons to the `agents` topic, waits for a tap, and
answers the agent. The tap clears the notification on every other device.

- Only an **Allow** tap allows. Deny, dismiss, resolve or no answer in time
  all deny, and the agent is told why.
- It waits 590 s by default, just under the agents' 600 s hook limit. Set
  `?timeout=` (seconds, max 590) and `?topic=` to change either.
- If clrd cannot send the notification (bad token, server down), or the
  request is too long to show in full (over 4000 characters), it answers
  with an error and the agent shows its normal prompt on screen.
- The token needs `read` and `publish`. Behind a reverse proxy, allow requests
  to run 10 minutes (nginx: `proxy_read_timeout 600s;`).

Claude Code, in `~/.claude/settings.json` (with `CLRD_TOKEN` set in your
environment):

```json
{
  "hooks": {
    "PermissionRequest": [{
      "hooks": [{
        "type": "http",
        "url": "https://clrd.itsjustin.me/mcp/approve",
        "timeout": 600,
        "headers": {"Authorization": "Bearer $CLRD_TOKEN"},
        "allowedEnvVars": ["CLRD_TOKEN"]
      }]
    }]
  }
}
```

Codex, in its `hooks.json` (Codex hooks run a command, so curl does the call):

```json
{
  "hooks": {
    "PermissionRequest": [{
      "hooks": [{
        "type": "command",
        "command": "curl -sf --max-time 600 -H \"Authorization: Bearer $CLRD_TOKEN\" -H \"Content-Type: application/json\" --data-binary @- https://clrd.itsjustin.me/mcp/approve"
      }]
    }]
  }
}
```

Add a `"matcher"` (for example `"Bash"`) to ask only about some tools.

## Claude Code

```sh
claude mcp add --transport http clrd https://clrd.itsjustin.me/mcp \
  --header "Authorization: Bearer clrd_..."
```

## Claude Desktop

`claude_desktop_config.json`, through the `mcp-remote` bridge:

```json
{
  "mcpServers": {
    "clrd": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://clrd.itsjustin.me/mcp", "--header", "Authorization:${AUTH_HEADER}"],
      "env": { "AUTH_HEADER": "Bearer clrd_..." }
    }
  }
}
```

(No spaces around the colon in `Authorization:${AUTH_HEADER}`; that is an
`mcp-remote` workaround for Windows argument escaping.)

## Cursor

`.cursor/mcp.json` (or `~/.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "clrd": {
      "url": "https://clrd.itsjustin.me/mcp",
      "headers": { "Authorization": "Bearer ${env:CLRD_TOKEN}" }
    }
  }
}
```

## Auto-resolve

Have the agent call `send` with an `id` when it starts something risky, and
`resolve` with `topic` and `external_id` when it is done. CI runs the Claude
Code and Claude Desktop snippets above against a live clrd on every change.
The Cursor snippet follows Cursor's docs and is not run.
