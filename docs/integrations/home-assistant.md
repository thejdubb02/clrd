# Home Assistant

Two options.

**Built-in ntfy integration** (HA 2025.5+): add it with server URL
`https://clrd.itsjustin.me` and your `clrd_...` token as the access token.
Publish only. **Needs ntfy compat** ([issue 2](https://github.com/thejdubb02/clrd/issues/2)). No resolve.

**`rest_command`** (works with the native API today, and can resolve):

`secrets.yaml`:

```yaml
clrd_bearer: "Bearer clrd_..."
```

`configuration.yaml`:

```yaml
rest_command:
  clrd_send:
    url: https://clrd.itsjustin.me/api/v1/notifications
    method: post
    headers:
      authorization: !secret clrd_bearer
    content_type: application/json
    payload: >
      {"topic": "{{ topic }}", "external_id": "{{ id }}", "title": "{{ title }}",
       "message": "{{ message }}", "priority": {{ priority | default(3) }}}
  clrd_resolve:
    url: "https://clrd.itsjustin.me/{{ topic }}/{{ id }}/resolve"
    method: post
    headers:
      authorization: !secret clrd_bearer
```

## Auto-resolve: garage door left open

```yaml
automation:
  - alias: Garage open too long
    triggers:
      - trigger: state
        entity_id: cover.garage_door
        to: open
        for: "00:10:00"
    actions:
      - action: rest_command.clrd_send
        data: {topic: home, id: garage-open, title: Garage open, message: Open for 10 minutes, priority: 4}
  - alias: Garage closed
    triggers:
      - trigger: state
        entity_id: cover.garage_door
        to: closed
    actions:
      - action: rest_command.clrd_resolve
        data: {topic: home, id: garage-open}
```

Close the door and the alert disappears from every phone.
