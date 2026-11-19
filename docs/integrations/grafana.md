# Grafana

**Needs on the server (proposed, see the integrations gap issue):**
`POST /{topic}?template=grafana` accepts Grafana's webhook JSON (the same URL
form as ntfy's built-in `grafana` template, ntfy 2.14+). Each alert's
`fingerprint` becomes `external_id`; `"status": "resolved"` resolves it on
every device.

Alerting, Contact points, New, Integration **Webhook**:

- URL: `https://clrd.itsjustin.me/infra?template=grafana`
- HTTP method: POST
- Authorization header: scheme `Bearer`, credentials `clrd_...`
- Leave "Disable resolved message" off: the resolved message is what clears it.

Provisioning:

```yaml
apiVersion: 1
contactPoints:
  - orgId: 1
    name: clrd
    receivers:
      - uid: clrd
        type: webhook
        settings:
          url: https://clrd.itsjustin.me/infra?template=grafana
          httpMethod: POST
          authorization_scheme: Bearer
        secureSettings:
          authorization_credentials: clrd_...
```

Mapping is the same as [Alertmanager](alertmanager.md) (Grafana's payload is a
superset): `fingerprint` to `external_id`, `labels.alertname` to title,
`annotations.summary` to message, plus `dashboardURL` or `panelURL` as the
click URL when present.
