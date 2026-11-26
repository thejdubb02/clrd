# create-clrd

This installer is built from source and is not yet published to npm. It
connects Claude Code, Codex CLI, Gemini CLI, Grok Build, OpenCode, Cursor and
VS Code to a self-hosted clrd server. It requires Node.js 22 or later on
Linux or macOS and a clrd server that supports agent invites.

## Connect from a checkout

```sh
cd sdk/create-clrd
npm ci --ignore-scripts
node cli.js https://clrd.example.com
```

The installer detects agents from their commands or user configuration
directories. Use `--agents claude,codex` to choose a subset or configure a
client it missed. The supported names are `claude`, `codex`, `gemini`,
`grok`, `opencode`, `cursor` and `vscode`.

In the clrd browser tab, open Settings, Linked devices, Add agent. Make one
invite per agent, granting read and publish on the topic shown in the
terminal (`agents` by default). Paste each invite into its prompt. Use a
different name for every invite so you can identify the agent in Linked
devices. Use `--topic` to choose a different notification topic and
`--no-browser` to open the printed URL yourself. Press Ctrl+C to stop setup.
Connections already completed stay in place.

The installer uses each invite once. It keeps the agent's token in a private
local file and writes a stdio MCP entry to the agent's user configuration.
Agent config files and hook commands contain only the path to that file.
Restart the clients and approve any MCP or hook trust prompts in the client.
For VS Code, this configures the Copilot session target on the local machine.

Turn completion hooks send a fixed message to the selected topic and need
publish access. Permission prompts need both read and publish. Use
`--no-notifications` for MCP only. Use `--phone-approvals` to also wire Claude
Code and Codex permission prompts to clrd. This sends the prompt's tool
details to your server. The hook uses clrd's Allow or Deny response and
falls back to the client's prompt if clrd cannot accept the request.
OpenCode 1.x uses a local notification plugin. An existing OpenCode 2.x MCP
configuration gets an MCP entry alone.

## Remove

```sh
node cli.js --remove
```

This revokes each installed agent's clrd token and removes the entries the
installer wrote. Use `--agents` to remove specific agents. If the server is
temporarily unreachable, retry after it returns. If you edited an installed
MCP entry, remove or restore that entry before retrying. Existing MCP
servers, unrelated hooks and comments are kept. Some files may retain empty
configuration sections.

If an editor saves a configuration while setup is running, rerun the same
command to finish with the saved invite claim. To change the topic or hooks
of an existing connection, remove it and connect again with the new options.

Read about [agent invites](../../docs/integrations/agent-invite.md) and the
[clrd MCP endpoint](../../docs/integrations/mcp.md).

Source: [github.com/thejdubb02/clrd](https://github.com/thejdubb02/clrd)
