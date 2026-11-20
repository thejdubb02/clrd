# Sonarr, Radarr, Lidarr, Readarr

`?template=arr` takes the *arr apps' own Webhook connection. Every event
(grab, import, health, update) is a notification. A health warning is one
notification per check, resolved when the check passes again. Health errors
and manual interaction requests are priority high.

1. Settings, Connect, add a Webhook.
2. URL: `https://clrd.example.com/media?template=arr`, method `POST`.
3. Username: anything. Password: your clrd token.
4. Tick the events you want, including On Health Issue and On Health Restored.

| clrd | *arr |
|---|---|
| `title` | `instanceName` and `eventType`, e.g. `Sonarr: Health` |
| `message` | `message`, or the series, movie, artist or author |
| `click` | `applicationUrl` |
| `external_id` | `arr-<instanceName>-<type>` for health events |
| resolve | `eventType: HealthRestored` |

Source: [Sonarr webhook payloads](https://github.com/Sonarr/Sonarr/tree/develop/src/NzbDrone.Core/Notifications/Webhook).
Radarr, Lidarr and Readarr share the format.
