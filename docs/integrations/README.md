# Integrations

Every tool below can send to clrd. "Auto-resolve" means a recovery clears the
notification on every device without anyone touching it.

| Tool | Send | Auto-resolve | Needs on the server |
|---|---|---|---|
| [curl](curl.md) | today | yes | native API only |
| [SDKs](../../sdk/) (Python, JS, Go) | today | yes | native API only |
| [GitHub Actions](github-actions.md) | today | yes | native API only |
| [MCP](mcp.md) (Claude Code, Claude Desktop, Cursor) | today | yes (agent calls `resolve`) | `/mcp` mounted |
| [Home Assistant](home-assistant.md) `rest_command` | today | yes | native API only |
| [Gatus](gatus.md) | today | yes | publish honours `status: resolved` (proposed) |
| [Uptime Kuma](uptime-kuma.md) | today | yes | publish honours `status: resolved` (proposed) |
| [Alertmanager](alertmanager.md) | needs compat | yes | `?template=alertmanager` (proposed) |
| [Grafana](grafana.md) | needs compat | yes | `?template=grafana` (proposed) |
| [Apprise](apprise.md) | needs compat | no | ntfy JSON publish to `POST /` (#2) |
| [ntfy clients](ntfy-clients.md) | needs compat | by key, via `X-Id` | ntfy publish/subscribe, Basic auth (#2) |
| Home Assistant ntfy integration | needs compat | no | ntfy publish (#2) |

"Proposed" items are tracked in the integrations gap issue. Snippets follow
each tool's upstream docs; none has been run against a live clrd yet.
