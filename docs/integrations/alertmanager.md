# Prometheus Alertmanager

**Needs on the server (proposed, see the integrations gap issue):**
`POST /{topic}?template=alertmanager` accepts Alertmanager's webhook JSON. The
URL form is the same as ntfy's built-in `alertmanager` template (ntfy 2.14+),
so a config that works with ntfy works with clrd. clrd goes further: each alert
uses its `fingerprint` as `external_id`, and an alert with `"status":
"resolved"` resolves that row on every device instead of posting "resolved".

```yaml
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
| `priority` | `labels.severity`: critical 5, warning 4, info 3, else 3 |
| resolve | `status: resolved` |
| `click` | `generatorURL` |
