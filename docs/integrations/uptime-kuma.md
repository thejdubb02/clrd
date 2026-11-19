# Uptime Kuma

Use the **Webhook** notification type with a custom body. Kuma's ntfy type
works for plain alerts but sends no stable key, so "Up" arrives as a second
notification instead of clearing the "Down" one. The native publish below
resolves by key when `status` is `resolved`.

Settings, Notifications, Setup Notification:

- Notification Type: Webhook
- Post URL: `https://clrd.itsjustin.me/api/v1/notifications`
- Request Body: Custom Body
- Additional Headers: `{"Authorization": "Bearer clrd_..."}`
- Custom Body (Liquid template, Kuma 1.23 and 2.x):

```liquid
{
  "topic": "infra",
  "external_id": "kuma-{{ monitorJSON.id }}",
  "title": "{{ monitorJSON.name }} is {% if heartbeatJSON.status == 1 %}up{% else %}down{% endif %}",
  "message": {{ msg | json }},
  "priority": 4,
  "status": "{% if heartbeatJSON.status == 1 %}resolved{% else %}firing{% endif %}"
}
```

`heartbeatJSON.status` is `0` down and `1` up. On the test button and on
certificate-expiry notices `heartbeatJSON` is empty, which sends a firing
notification; resolve it from the inbox.
