# MCP (Claude Code, Claude Desktop, Cursor)

clrd serves MCP at `https://clrd.itsjustin.me/mcp` (streamable HTTP, stateless).
Auth is the same Bearer token as the REST API; the token's scopes and topic
allow-list apply, and actions show up as made by that token's device.

Tools:

| Tool | Arguments | Does |
|---|---|---|
| `send` | `topic, message, title?, priority?, id?` | Publish. `id` is your stable key (`external_id`). |
| `list` | `topic?, state?` | List, default `open`. |
| `ack` | `id` | Acknowledge by server id. |
| `resolve` | `id` or `topic + external_id` | Clear it everywhere. |
| `snooze` | `id, for` | Snooze, e.g. `for: "1h"`. |
| `wait_for_reply` | `id, timeout_s?` | Block until a human acks or dismisses it, or it is resolved. Snooze keeps it waiting. Returns `reply` = the new state, or `timeout`. Default 60 s, max 300 s. |

`wait_for_reply` is how an agent asks a human "OK to deploy?" and waits:
`send`, then `wait_for_reply` with the returned `id`. Ack means yes, dismiss means no.
Real button replies are [issue 8](https://github.com/thejdubb02/clrd/issues/8).

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
`resolve` with `topic` and `external_id` when it is done. Checked with the MCP
Inspector CLI; the Desktop and Cursor snippets follow their upstream docs and
are untested against a live clrd.
