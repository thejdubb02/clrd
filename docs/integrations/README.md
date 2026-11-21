# Integrations

Every tool below can send to clrd. "Auto-resolve" means a recovery clears the
notification on every device without anyone touching it.

| Tool | Send | Auto-resolve | Needs on the server | Checked in CI |
|---|---|---|---|---|
| [curl](curl.md) | today | yes | native API only | every snippet, including the cron script |
| [SDKs](../../sdk/) (Python, JS, Go) | today | yes | native API only | each README usage example (Python, JS, Go); the install lines are not run, as they fetch from GitHub |
| [GitHub Actions](github-actions.md) | today | yes | native API only | the action on a real runner (send, resolve, resolve with nothing open), checked against the guide's YAML; the steps without the action, as written |
| [MCP](mcp.md) (Claude Code, Claude Desktop, Cursor, VS Code, Windsurf, Zed, Codex CLI, Gemini CLI) | today | yes (agent calls `resolve`) | `/mcp` mounted | Claude Code: `claude mcp add`, then `claude mcp list` connects. Every tool (send, list, topics, get, update, ack, snooze, resolve, and `wait_for_reply` with an answer) is called through the real `mcp-remote` bridge that Claude Desktop uses. The other clients: setup in the guide, not run |
| [Home Assistant](home-assistant.md) `rest_command` | today | yes | native API only | the `rest_command` templates rendered with Jinja2 and posted; Home Assistant itself not run |
| [Gatus](gatus.md) | today | yes | publish honours `status: resolved` | Gatus 5.17: down, then up |
| [Uptime Kuma](uptime-kuma.md) | today | yes | publish honours `status: resolved` | Uptime Kuma 1.23: down, then up |
| [Alertmanager](alertmanager.md) | today | yes | `?template=alertmanager` | Alertmanager 0.28: firing, then resolved |
| [Grafana](grafana.md) | today | yes | `?template=grafana` | Grafana 11.6 with the provisioning file: firing, then resolved |
| [GitHub](github.md) | today | issue or PR closed | `?template=github` | the guide's URL and token form, with a real GitHub webhook body: sends, then resolves |
| [Sentry](sentry.md) | today | yes | `?template=sentry` | the guide's URL and token form, with a real Sentry webhook body: sends, then resolves |
| [Stripe](stripe.md) | today | no | `?template=stripe` | the guide's URL and token form, with a real Stripe webhook body: sends |
| [Jira](jira.md) | today | issue done | `?template=jira` | the guide's URL and token form, with a real Jira webhook body: sends, then resolves |
| [Healthchecks.io](healthchecks.md) | today | yes | `?template=healthchecks` | the guide's URL and token form, with a real Healthchecks.io webhook body: sends, then resolves |
| [Proxmox VE](proxmox.md) | today | no | `?template=proxmox` | the guide's body template, filled in as Proxmox VE would; Proxmox itself not run |
| [Zabbix](zabbix.md) | today | yes | `?template=zabbix` | the guide's media type script, run with Zabbix's parameters: problem, then recovery; Zabbix itself not run |
| [Netdata](netdata.md) | today | yes | `?template=netdata` | the guide's URL and token form, with a real Netdata webhook body: sends, then resolves |
| [Jellyfin](jellyfin.md) | today | no | `?template=jellyfin` | the guide's URL and token form, with a real Jellyfin webhook body: sends |
| [Sonarr, Radarr, Lidarr, Readarr](arr.md) | today | health only | `?template=arr` | the guide's URL and token form, with a real Sonarr webhook body: sends, then resolves |
| [Watchtower](watchtower.md) | today | no | ntfy publish | Watchtower 1.7 with the guide's URL: a run report arrives |
| [Any other service](custom-webhooks.md) | today | if you map it | a saved template | the guide's template saved and fed events (send, resolve, secret), both token forms, a saved GitHub secret rejecting unsigned requests |
| [Apprise](apprise.md) | today | no | ntfy JSON publish to `POST /` | Apprise 1.9: both URL forms, `?priority=` included |
| [ntfy clients](ntfy-clients.md) | today | by key, via `X-Id` | ntfy publish/subscribe, Basic auth | ntfy CLI 2.14: publish, subscribe, Basic auth; then resolve by key |
| [Gotify senders](gotify.md) | today | no | Gotify publish, `POST /message` with a one-topic token | the guide's token call, curl `-F` and Apprise 1.9 lines; gotify CLI 2.4 push |
| Home Assistant ntfy integration | today | no | ntfy publish | not run |
| [Outgoing webhooks](webhooks-out.md): clrd tells your service when someone dismisses, acks, resolves, snoozes or replies | today | not applicable | a saved outgoing webhook (admin) | server tests: delivery, signature, retry, turn-off; the guide's receiver not run |

Everything in "Needs on the server" is in the server. "Checked in CI" is what
[`test/integrations`](../../test/integrations/) runs against a live clrd on
every change to the server, the guides or the SDKs, using the guide's own text
and, where it can, the real tool at the version named.
