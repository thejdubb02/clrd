# Netdata

`?template=netdata` takes Netdata Cloud's webhook notifications. An alert is
one notification per alert and chart, resolved when it goes `clear`. A node
that becomes unreachable is one notification, resolved when it is back.
Critical is priority high.

1. Space settings, Alerts & Notifications, Notification Methods, Webhook.
2. Webhook URL: `https://clrd.itsjustin.me/netdata?template=netdata`.
3. Authentication: Bearer, with your clrd token.

Netdata documents the webhook fields but publishes no sample body, so this
template follows its field reference. The Test button's body is not
documented and may arrive with a blank title. Alert payloads name no node,
so the same alert on two nodes shares one notification; use one topic per
node if that matters.

| clrd | Netdata |
|---|---|
| `title` | `alert` and `chart`, or `host` for reachability |
| `message` | `message` |
| `click` | `alert_url`, or `url` |
| `external_id` | `netdata-<alert>-<chart>`, or `netdata-reach-<host>` |
| resolve | `severity: clear`, or `status.reachable: true` |
| `priority` | high for `critical`, else 3 |

Source: [Netdata webhook notifications](https://learn.netdata.cloud/docs/alerts-&-notifications/notifications/centralized-cloud-notifications/webhook).
