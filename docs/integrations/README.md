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
| [GitHub](github.md) | today | issue or PR closed | `?template=github` |
| [Sentry](sentry.md) | today | yes | `?template=sentry` |
| [Stripe](stripe.md) | today | no | `?template=stripe` |
| [Jira](jira.md) | today | issue done | `?template=jira` |
| [Healthchecks.io](healthchecks.md) | today | yes | `?template=healthchecks` |
| [Proxmox VE](proxmox.md) | today | no | `?template=proxmox` |
| [Zabbix](zabbix.md) | today | yes | `?template=zabbix` |
| [Netdata](netdata.md) | today | yes | `?template=netdata` |
| [Jellyfin](jellyfin.md) | today | no | `?template=jellyfin` |
| [Sonarr, Radarr, Lidarr, Readarr](arr.md) | today | health only | `?template=arr` |
| [Watchtower](watchtower.md) | today | no | ntfy publish |
| [Any other service](custom-webhooks.md) | today | if you map it | a saved template |
| [Apprise](apprise.md) | today | no | ntfy JSON publish to `POST /` |
| [ntfy clients](ntfy-clients.md) | today | by key, via `X-Id` | ntfy publish/subscribe, Basic auth |
| Home Assistant ntfy integration | today | no | ntfy publish |

Everything in the last column is in the server. Snippets follow each tool's
upstream docs; none has been run against a live clrd yet.
