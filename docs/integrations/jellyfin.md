# Jellyfin

`?template=jellyfin` takes a body you set in the Jellyfin Webhook plugin's
Generic destination.

1. Dashboard, Plugins, Catalog: install Webhook, restart Jellyfin.
2. Plugins, Webhook, Add Generic Destination.
3. Webhook URL: `https://clrd.example.com/media?template=jellyfin&auth=<token>`
   ([how to make `<token>`](custom-webhooks.md#sending-the-token)).
4. Pick the notification types you want, and add header
   `Content-Type: application/json`.
5. Template:

   ```
   {"type": "{{NotificationType}}", "server": "{{ServerName}}", "name": "{{Name}}", "user": "{{NotificationUsername}}", "url": "{{ServerUrl}}"}
   ```

A title containing a double quote breaks the JSON.

| clrd | body |
|---|---|
| `title` | `name`, else `type` |
| `message` | `type`, server and user, e.g. `ItemAdded on Living Room` |
| `click` | `url` |

Source: [jellyfin-plugin-webhook](https://github.com/jellyfin/jellyfin-plugin-webhook).
