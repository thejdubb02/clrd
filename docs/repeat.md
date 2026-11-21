# Repeat until acknowledged

A critical alert can ring again and again until someone deals with it. Add
`repeat` when you publish and the server alerts again on that schedule while
the notification stays in the inbox. The first person to acknowledge,
snooze, dismiss or resolve it stops the repeats on every device.

Off by default: a notification without `repeat` alerts once.

## Turn it on

```sh
# Ring again every 5 minutes, up to 10 more times
curl -H "Authorization: Bearer $CLRD_TOKEN" \
  -d '{"topic":"pager","message":"Disk full on db1","priority":5,"repeat":"5m"}' \
  https://clrd.example.com/api/v1/notifications

# Every 15 minutes, at most 3 more times
curl -H "Authorization: Bearer $CLRD_TOKEN" \
  -d '{"topic":"pager","message":"Backup failed","repeat":"15m","repeat_times":3}' \
  https://clrd.example.com/api/v1/notifications
```

- `repeat` is how long to wait between alerts, from `1m` to `24h`.
- `repeat_times` caps how many extra alerts it sends, 1 to 100. Default 10.
- Agents can set both on the MCP `send` tool.

## What stops it

- Acknowledge, snooze, dismiss or resolve, from any device or the API.
- Tapping a reply action, since that acknowledges it.
- Running out of `repeat_times`.

It stops for good. Reopening the notification, or a snooze ending, does not
start the repeats again; publish it again with `repeat` if you want them.

A scheduled send (`snooze_until`) starts counting when it is delivered, not
when you sent it. A silence on the topic or tag still applies: repeats during
a silence arrive quietly.

## What clients see

Each re-alert is a `repeat` event on the sync stream carrying the full
notification. Its `repeat` field shows `count` (alerts sent so far), `left`,
and `next` (when the next one is due, or null once it stopped). Apps that
speak the ntfy protocol get each re-alert as a new message. The web inbox
plays its sound on each one (unless silenced) and shows on the card how many
times it alerted and when the next one is due. The clrd Android app is
learning to alert on `repeat` events; until it does, it updates the card
without a sound.
