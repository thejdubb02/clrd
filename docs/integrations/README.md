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
| [Gatus](gatus.md) | today | yes | publish honours `status: resolved` |
| [Uptime Kuma](uptime-kuma.md) | today | yes | publish honours `status: resolved` |
| [Alertmanager](alertmanager.md) | today | yes | `?template=alertmanager` |
| [Grafana](grafana.md) | today | yes | `?template=grafana` |
| [Apprise](apprise.md) | today | no | ntfy JSON publish to `POST /` |
| [ntfy clients](ntfy-clients.md) | today | by key, via `X-Id` | ntfy publish/subscribe, Basic auth |
| Home Assistant ntfy integration | today | no | ntfy publish |

Everything in the last column is in the server. Snippets follow each tool's
upstream docs; none has been run against a live clrd yet.
