# Claude Code plugin

The clrd plugin for Claude Code gives Claude the clrd MCP server, pings your
phone when a task finishes or Claude is waiting on you, and sends permission
prompts to your phone so you can tap Allow or Deny.

## Install

In Claude Code:

```text
/plugin marketplace add thejdubb02/clrd
/plugin install clrd@clrd
```

Claude Code asks two questions: your server URL and a token. That is all.

- **Server URL**: for example `https://clrd.example.com`. It must be https,
  unless the server is on the same machine (`http://localhost`,
  `http://127.0.0.1` or `http://[::1]`). With any other http URL the plugin
  sends nothing: `/mcp` shows "server URL must be https".
- **Token**: make one for this agent, never reuse your phone's. It needs
  `read` and `publish`:

  ```sh
  clrd token create --name claude-code --scope read,publish --topics agents
  ```

  Claude Code keeps it in its secure storage: the Keychain on macOS, and on
  Linux a file only your user can read (`~/.claude/.credentials.json`). The
  plugin never puts it in a settings file, a URL, a log line or a command
  line.

A third, optional setting is the topic (default `agents`). Change any of them
with `/plugin configure clrd@clrd`.

## What it does

| When | What you get |
|---|---|
| Claude finishes a task | "Claude finished: *project*" with Claude's last message |
| Claude waits for your input | "Claude is waiting: *project*", priority 4 |
| Claude asks permission to run a tool | "Allow Bash? (*project*)" with Allow and Deny buttons |
| Always | The clrd MCP server, so Claude can send, list, clear and `wait_for_reply` |

The project is the folder Claude Code runs in. All of it goes to one topic,
`agents` unless you set another.

## Approving from your phone

A permission prompt shows on screen and on your phone at the same time.
Answer either one.

- **Allow** on the phone runs the tool. **Deny** stops it and tells Claude it
  was denied from clrd.
- No answer in about 9.5 minutes: Claude Code stops waiting for the phone and
  the prompt stays on screen. Nothing is ever allowed without a tap.
- clrd down, bad token or URL: the hook gives up and you get the normal
  prompt on screen.
- Only a device allowed to approve can tap Allow (see
  [who may approve](mcp.md#approve-agent-actions-from-your-phone)). The
  plugin's token starts denied, so Claude can never answer its own prompt.
  The plugin also blocks Claude from calling `request_approve`, so it never
  asks for that setting to be raised. A person changes it, in clrd.

## Limits

- Answering on screen clears the phone's prompt: on Allow once the tool runs,
  on Deny at once. If the same session runs the same tool while a prompt is
  waiting, the phone's prompt can clear early; the prompt on screen stays.
- The hooks and the MCP connection need `bash` and `curl`. On Windows that means Git Bash or WSL;
  only Linux has been tested.

## Without the plugin

The same pieces by hand: [MCP setup](mcp.md#claude-code) and the
[PermissionRequest hook](mcp.md#approve-agent-actions-from-your-phone).
