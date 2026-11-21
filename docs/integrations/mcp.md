# MCP (AI agents and coding tools)

clrd serves MCP at `https://clrd.example.com/mcp` (your server's address plus
`/mcp`; streamable HTTP, stateless). Auth is the same Bearer token as the REST
API; the token's scopes and topic allow-list apply, and actions show up as
made by that token's device. Give the agent its own token with `read` and
`publish`, limited to the topics it should use:

```sh
clrd token create --name claude-code --scope read,publish --topics agents,build
```

Tools:

| Tool | Arguments | Does |
|---|---|---|
| `send` | `topic, message, title?, format?, priority?, id?, actions?, icon?, attachment?` | Publish. `format` is `markdown` (default) or `text`. `icon` is an http(s) image URL. `attachment` links a file, `{"url":"https://...","name":"report.pdf"}`. `id` is your stable key (`external_id`). `actions` adds up to 3 answer buttons, `[{"label":"Yes","reply":"yes"}]`. |
| `list` | `topic?, state?` | List, default `open`. |
| `topics` | none | Topics the token can read, with how many are open in each. |
| `counts` | `keep_cleared_days?`, `topic?` | Exact number of notifications in each state, plus `inbox` and `cleared` totals, over the topics the token can read. With `keep_cleared_days`, also `would_remove`: how many cleared ones that setting would delete. With `topic`, `would_remove` counts only that topic, to preview its own value. |
| `settings` | any settings field, e.g. `keep_cleared_days?` | No arguments reads the server settings. With arguments, changes only those fields. `keep_cleared_days` (0 is forever) needs an admin token. `topic_keep_cleared_days`, like `{"alerts": 7}`, sets it for one topic (`null` drops it) and needs admin on that topic. |
| `get` | `id` | One notification by server id, with its current state. |
| `update` | `id, message` | Replace the message of an open notification. The id and state stay, so a progress line ("step 3 of 10") is one notification, not ten. |
| `ack` | `id` | Acknowledge by server id. |
| `resolve` | `id` or `topic + external_id` | Clear it everywhere. |
| `snooze` | `id, for` | Snooze, e.g. `for: "1h"`. |
| `wait_for_reply` | `id, timeout_s?` | Block until a human acks or dismisses it, or it is resolved. Snooze keeps it waiting. Returns `reply` = the new state, or `timeout`, plus `answer` when a button from `actions` was tapped. Default 60 s, max 600 s. |
| `devices` | none | Every linked device this token can see: name, kind (token, browser or phone), how it was linked, last seen. Never a secret. |
| `device_log` | none | The last 200 links, sign-ins and unlinks, newest first. |
| `unlink` | `id` | Sign out a browser or phone from `devices` at once. Unlinking a phone also signs out the browsers it approved. |
| `sound` | `id?, sound?` | Notification sounds, one choice per device. No arguments lists the valid names and the default (`default`, the device's own sound). With `id` from `devices` and `sound`, sets that device's sound; an unknown name is refused with the list. `devices` shows each device's current sound. |
| `request_approve` | `approve, topic?` | Ask the user to let this token answer approval requests (`allowed`, or `ask`). Posts a Confirm prompt on `topic` (default `agents`); the setting changes only when an allowed device taps Confirm. `wait_for_reply` on the returned id gives `confirm` or `reject`. |
| `dismiss` | `id` | Clear it on every device. `reopen` undoes it. |
| `reopen` | `id` | Make it active again. |
| `clear` | `topic` or `ids` | Dismiss every open notification in a topic, or the listed ones. Returns `cleared`. |
| `silences` | none | Silences that have not ended. |
| `silence` | `topic?, tag?, for? or until?, comment?` | Mute a topic, a tag, or a tag in one topic until a set time (`for: "2h"`, or `until` in RFC 3339). Notifications still arrive and sync, without a sound. |
| `end_silence` | `id` | End a silence now. |
| `set_topic` | `topic, icon?, checkin?` | Set the topic icon (an image URL), or a check-in timer (`"25h"`): the topic alerts when nothing arrives for that long. `""` turns either off. |
| `templates` | `topic` | Incoming webhook templates, saved and built in. A service posts to `/{topic}?template={name}`. |
| `set_template` | `topic, name, title?, message?, priority?, tags?, click?, id?, resolve?, each?, secret?` | Save how a service's JSON becomes a notification. Fields are Go templates over the posted JSON, e.g. `{{.alert.name}}`; `each` is a plain dotted path and `secret` a plain value. |
| `delete_template` | `topic, name` | Delete a saved template. |
| `preview_template` | `topic, name, payload, headers?, template?` | Dry run a template over a sample payload: returns the notifications a post would make, publishes nothing. `template` tries an unsaved one. |
| `hooks` | `topic` | Outgoing webhooks: URLs clrd calls when a notification is dismissed, acknowledged, resolved, snoozed or a button tapped. Admin. |
| `set_hook` | `topic, name, url, events?, secret?` | Add or replace one. `events` defaults to all; `secret` signs each call. Saving again turns a failing hook back on. Admin. |
| `delete_hook` | `topic, name` | Delete one. Admin. |
| `hook_deliveries` | `topic, name` | Its recent calls, with status and error. Admin. |
| `tokens` | none | API tokens, never their secrets. Admin. |
| `create_token` | `name, scopes, topics? or grants?` | New token for a phone, script or agent; the secret is in the result once. `grants` sets scopes per topic, first match wins: `[{"topic":"home-*","scopes":["publish"]}]`. Admin. |
| `revoke_token` | `id` | Revoke a token; its streams close at once. Admin. |
| `server_env` | none | Every `CLRD_*` setting in effect, defaults filled in. Read only: those change by setting the variable and restarting. Admin. |
| `ntfy_ignored` | none | ntfy publish options senders used that clrd accepts but ignores, with counts. Admin. |

With an admin token an agent can set up every feature from this list; only
the `CLRD_*` environment settings need a restart, so `server_env` just reads
them. Tools marked Admin need the `admin` scope, and the three token tools
also need a token with no topic limit. The others follow the token's scopes and topics like the REST API, and their
errors are the API's own message (for example `url must be an http(s) URL`).

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
ack (no button) has no `answer`, and dismiss means no.

`update` needs `publish` on the topic and works until the notification is
dismissed or resolved. It refuses a notification with answer buttons
(`actions` with a `reply`), because a tap answers the text that was shown.
Link buttons are fine. It counts against the publish rate limit like a send.

## Add clrd to your client

In every snippet, replace `clrd.example.com` with your server and `clrd_...`
with your token. Where a snippet reads `CLRD_TOKEN`, set it in your
environment instead, so the token stays out of the file. The test suite drives `/mcp` the way these clients do
(initialize, tools/list, tool calls); the snippets follow each client's own
docs.

### Claude Code

```sh
claude mcp add --transport http clrd https://clrd.example.com/mcp \
  --header "Authorization: Bearer clrd_..."
```

### Claude Desktop

`claude_desktop_config.json`, through the `mcp-remote` bridge:

```json
{
  "mcpServers": {
    "clrd": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://clrd.example.com/mcp", "--header", "Authorization:${AUTH_HEADER}"],
      "env": { "AUTH_HEADER": "Bearer clrd_..." }
    }
  }
}
```

(No spaces around the colon in `Authorization:${AUTH_HEADER}`; that is an
`mcp-remote` workaround for Windows argument escaping.)

### Cursor

`.cursor/mcp.json` (or `~/.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "clrd": {
      "url": "https://clrd.example.com/mcp",
      "headers": { "Authorization": "Bearer ${env:CLRD_TOKEN}" }
    }
  }
}
```

### VS Code

`.mcp.json` in your workspace, or `~/.copilot/mcp-config.json` for every
workspace:

```json
{
  "mcpServers": {
    "clrd": {
      "type": "http",
      "url": "https://clrd.example.com/mcp",
      "headers": { "Authorization": "Bearer ${CLRD_TOKEN}" }
    }
  }
}
```

### Windsurf

`~/.codeium/windsurf/mcp_config.json`:

```json
{
  "mcpServers": {
    "clrd": {
      "serverUrl": "https://clrd.example.com/mcp",
      "headers": { "Authorization": "Bearer ${env:CLRD_TOKEN}" }
    }
  }
}
```

### Zed

In Zed's `settings.json`:

```json
{
  "context_servers": {
    "clrd": {
      "url": "https://clrd.example.com/mcp",
      "headers": { "Authorization": "Bearer clrd_..." }
    }
  }
}
```

### Codex CLI

`~/.codex/config.toml`:

```toml
[mcp_servers.clrd]
url = "https://clrd.example.com/mcp"
bearer_token_env_var = "CLRD_TOKEN"
```

### Gemini CLI

`~/.gemini/settings.json` (or `.gemini/settings.json` in a project):

```json
{
  "mcpServers": {
    "clrd": {
      "httpUrl": "https://clrd.example.com/mcp",
      "headers": { "Authorization": "Bearer clrd_..." }
    }
  }
}
```

### ChatGPT connectors

Not yet. ChatGPT's custom connectors sign in with OAuth or no auth at all,
and clrd's `/mcp` takes a Bearer token only. The same goes for custom
connectors on claude.ai. Use one of the clients above until clrd speaks OAuth.

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
- Give the hook its own token, never your phone's. A token cannot answer a
  request it sent, nor can a phone or browser linked from it.
- Only a device allowed to approve can tap Allow or Deny. The setup token and
  the phones and browsers you link are allowed; a new token is denied, so an
  agent's token cannot approve its own prompts. Change it per device with
  `PUT /api/v1/devices/{id}/approve`. Set a device to `ask` and its taps
  wait for an allowed device to confirm them: clrd posts a "Confirm" prompt
  on the same topic. An agent can ask for a higher setting itself with the
  `request_approve` tool; it lands when you tap Confirm. Locked out? `clrd token create
  --approve allowed` on the server makes an allowed token.

Claude Code, in `~/.claude/settings.json` (with `CLRD_TOKEN` set in your
environment):

```json
{
  "hooks": {
    "PermissionRequest": [{
      "hooks": [{
        "type": "http",
        "url": "https://clrd.example.com/mcp/approve",
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
        "command": "curl -sf --max-time 600 -H \"Authorization: Bearer $CLRD_TOKEN\" -H \"Content-Type: application/json\" --data-binary @- https://clrd.example.com/mcp/approve"
      }]
    }]
  }
}
```

Add a `"matcher"` (for example `"Bash"`) to ask only about some tools.

## Auto-resolve

Have the agent call `send` with an `id` when it starts something risky,
`update` as it makes progress, and `resolve` with `topic` and `external_id`
when it is done.
