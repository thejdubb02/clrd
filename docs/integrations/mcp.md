# MCP (Claude Code, Claude Desktop, Cursor)

clrd serves MCP at `https://clrd.itsjustin.me/mcp` (streamable HTTP, stateless).
Auth is the same Bearer token as the REST API; the token's scopes and topic
allow-list apply, and actions show up as made by that token's device.

Tools:

| Tool | Arguments | Does |
|---|---|---|
| `send` | `topic, message, title?, format?, priority?, id?, actions?, icon?, attachment?` | Publish. `format` is `markdown` (default) or `text`. `icon` is an http(s) image URL. `attachment` links a file, `{"url":"https://...","name":"report.pdf"}`. `id` is your stable key (`external_id`). `actions` adds up to 3 answer buttons, `[{"label":"Yes","reply":"yes"}]`. |
| `list` | `topic?, state?` | List, default `open`. |
| `ack` | `id` | Acknowledge by server id. |
| `resolve` | `id` or `topic + external_id` | Clear it everywhere. |
| `snooze` | `id, for` | Snooze, e.g. `for: "1h"`. |
| `wait_for_reply` | `id, timeout_s?` | Block until a human acks or dismisses it, or it is resolved. Snooze keeps it waiting. Returns `reply` = the new state, or `timeout`, plus `answer` when a button from `actions` was tapped. Default 60 s, max 300 s. |

`wait_for_reply` returns at once if the notification is already acknowledged,
dismissed or resolved. Otherwise it watches the server's committed events, so
an ack that is quickly followed by a snooze is still reported as an ack.

`wait_for_reply` is how an agent asks a human "OK to deploy?" and waits:

```json
send {"topic": "agents", "message": "OK to deploy?", "id": "deploy-1",
      "actions": [{"label": "Yes", "reply": "yes"}, {"label": "No", "reply": "no"}]}
wait_for_reply {"id": "<id from send>", "timeout_s": 300}
-> {"reply": "acknowledged", "answer": "yes", "by": "pixel-9", "notification": {...}}
```

The first tap wins; a second phone gets told it is already answered. A plain
ack (no button) has no `answer`, and dismiss means no. The web inbox and the
Android app show the buttons once they support them
([issue 8](https://github.com/thejdubb02/clrd/issues/8)); until then a human
can still ack or dismiss.

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
