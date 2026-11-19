# clrd

Self-hosted push notifications where **dismiss once means gone everywhere**.

Send an alert with one HTTP call. It shows up on your Android phone, your tablet and the web inbox. Swipe it away on any one of them and it clears on all the others.

clrd is a notification server, not a chat app. No rooms, no messages back and forth, no accounts on someone else's service.

> Dismiss once. Gone everywhere.
>
> Status: being built. The contract every part builds against is [docs/SPEC.md](docs/SPEC.md).

## Why

[ntfy](https://github.com/binwiederhier/ntfy) is great, and clrd aims to cover everything it does. The one gap: in ntfy the *sender* can clear a notification everywhere, but when a *person* swipes it away on one device, the other devices keep it. clrd makes the server the source of truth for every notification's state, so a dismissal anywhere syncs to every device.

## Planned

- Single Go binary or one Docker container, SQLite, no Redis, no broker
- HTTP publish API, plus an ntfy-compatible endpoint so existing scripts keep working
- Android app (Kotlin), distributed through F-Droid, no Google services required
- Web inbox built into the server, with browser push
- Topics, priorities, tags, click URLs, action buttons, attachments, scheduled sends
- Separate states that all sync: **dismiss** (hide it), **acknowledge** (someone is on it), **resolve** (the problem is fixed), **snooze**
- Senders can resolve an alert by their own ID when the fault clears, so monitoring tools close their own alerts
- Offline devices catch up on reconnect from an event sequence number, so cleared alerts never come back

## Sending (planned API)

```bash
curl -H "Authorization: Bearer $TOKEN" \
  -d '{"topic":"infra","title":"Disk full","message":"vps1 at 95%","priority":4,"external_id":"disk-vps1"}' \
  https://clrd.itsjustin.me/api/v1/notifications

# ntfy style also works
curl -d "Disk full" https://clrd.itsjustin.me/infra
```

## License

Apache-2.0
