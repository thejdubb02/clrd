# Prometheus Alertmanager

`POST /{topic}?template=alertmanager` accepts Alertmanager's webhook JSON. The
URL form is the same as ntfy's built-in `alertmanager` template (ntfy 2.14+),
so a config that works with ntfy works with clrd. clrd goes further: each alert
uses its `fingerprint` as `external_id`, and an alert with `"status":
"resolved"` resolves that row on every device instead of posting "resolved".

```yaml
route:
  receiver: clrd   # or add a child route that matches the alerts you want

receivers:
  - name: clrd
    webhook_configs:
      - url: https://clrd.itsjustin.me/infra?template=alertmanager
        send_resolved: true   # the default for webhooks, shown for clarity
        http_config:
          authorization:
            type: Bearer
            credentials: clrd_...   # or credentials_file: /etc/alertmanager/clrd-token
```

Mapping per alert in `alerts[]`:

| clrd | Alertmanager |
|---|---|
| `external_id` | `fingerprint` |
| `title` | `labels.alertname` |
| `message` | `annotations.summary`, else `annotations.description` |
| `priority` | not from the alert: add `&priority=1..5` (or `high`, `urgent`, ...) to the URL, default 3 |
| resolve | `status: resolved` |
| `click` | `generatorURL` |
