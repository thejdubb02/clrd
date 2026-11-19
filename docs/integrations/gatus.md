# Gatus

Use Gatus's `custom` alert provider, not its `ntfy` provider: the ntfy provider
only accepts tokens starting with `tk_` and sends no stable key, so a recovery
cannot clear the original alert.

The native publish `POST /api/v1/notifications` treats `"status": "resolved"` plus
`topic` and `external_id` as a resolve of that key instead of a new
notification (the `status: resolved` rule in SPEC section 5). Any other
`status` value, such as `firing`, is a normal publish.

```yaml
alerting:
  custom:
    url: https://clrd.itsjustin.me/api/v1/notifications
    method: POST
    headers:
      Authorization: Bearer clrd_...
      Content-Type: application/json
    body: |
      {
        "topic": "infra",
        "external_id": "gatus-[ENDPOINT_GROUP]-[ENDPOINT_NAME]",
        "title": "[ENDPOINT_NAME] is down",
        "message": "[ALERT_DESCRIPTION] [RESULT_ERRORS]",
        "priority": 4,
        "status": "[ALERT_TRIGGERED_OR_RESOLVED]"
      }
    placeholders:
      ALERT_TRIGGERED_OR_RESOLVED:
        TRIGGERED: "firing"
        RESOLVED: "resolved"

endpoints:
  - name: website
    url: https://example.com
    conditions: ["[STATUS] == 200"]
    alerts:
      - type: custom
        description: "health check failed"
        send-on-resolved: true
```

`send-on-resolved: true` is what makes the alert disappear from every device
when the endpoint recovers. Placeholders work in `url` and `body` only, not in
`headers`. Gatus does not JSON-escape endpoint names or descriptions, so keep
quotes out of them.
