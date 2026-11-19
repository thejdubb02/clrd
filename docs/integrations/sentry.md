# Sentry

`POST /{topic}?template=sentry` takes webhooks from a Sentry internal
integration: issues, issue alerts and metric alerts. A resolved issue or
metric alert resolves its notification. A critical metric alert is urgent, a
warning is high.

1. Settings, Developer Settings, Custom Integrations, Create New Integration,
   Internal Integration.
2. Webhook URL: `https://clrd.itsjustin.me/errors?template=sentry&auth=<token>`
   ([how to make `<token>`](custom-webhooks.md#sending-the-token)).
3. Turn on Alert Rule Action, and under Webhooks tick `issue`.
4. Copy the integration's Client Secret and save it in clrd so it checks
   `Sentry-Hook-Signature`:
   `curl -X PUT .../api/v1/topics/errors/templates/sentry -H "Authorization: Bearer clrd_..." -d '{"secret":"..."}'`
5. For alerts, add the integration as an action on an alert rule.

| clrd | Sentry |
|---|---|
| `title` | `data.issue.title`, `data.event.title` or the metric alert rule name |
| `message` | the culprit, or the metric alert's `description_text` |
| `click` | `web_url` |
| `external_id` | `sentry-<issue id>`, or `sentry-metric-<id>` for metric alerts |
| resolve | `action: resolved` |
| `priority` | urgent for `critical`, high for `warning`, else 3 |

Source: [Sentry integration webhooks](https://docs.sentry.io/organization/integrations/integration-platform/webhooks/).
