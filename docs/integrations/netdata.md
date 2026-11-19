# Netdata

`?template=netdata` takes Netdata Cloud's webhook notifications. An alert is
one notification per alert and chart, resolved when it goes `clear`. A node
that becomes unreachable is one notification, resolved when it is back.
Critical is priority high.

1. Space settings, Alerts & Notifications, Notification Methods, Webhook.
2. Webhook URL: `https://clrd.itsjustin.me/netdata?template=netdata`.
3. Authentication: Bearer, with your clrd token.
4. Use the Test button to check it arrives.

| clrd | Netdata |
|---|---|
| `title` | `alert` and `chart`, or `host` for reachability |
| `message` | `message` |
| `click` | `alert_url`, or `url` |
| `external_id` | `netdata-<alert>-<chart>`, or `netdata-reach-<host>` |
| resolve | `severity: clear`, or `status.reachable: true` |
| `priority` | high for `critical`, else 3 |

Source: [Netdata webhook notifications](https://learn.netdata.cloud/docs/alerts-&-notifications/notifications/centralized-cloud-notifications/webhook).
