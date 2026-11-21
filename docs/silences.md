# Silences

A silence mutes a topic, a tag, or a tag within one topic until a time you
pick. Notifications still arrive, sync to every device and show in every
inbox. They just make no sound and do not vibrate. When the silence ends,
new notifications alert again; nothing is held back and replayed.

Use one for a maintenance window, a noisy backup job overnight, or a weekend
away from a work topic.

## Start one

In the web inbox, open Settings, then Silences: give a topic, a tag or both,
pick how long, and press Start silence. The list above the form shows each
running silence with the time left, and End now ends it early. Both work
without JavaScript, except "Until a time", which needs the browser's clock.
A device whose token cannot publish sees the list but not the controls.

On a card, the snooze menu also has "Silence <topic> for 1 hour" when your
device may silence that topic. It starts the same silence in one tap.

"Until a time" is read in the browser's time zone. On a daylight saving
change, a time inside the skipped hour moves forward by an hour, and a time
in the repeated hour means its first pass.

Over the API:

```sh
# Mute the infra topic for 8 hours
curl -H "Authorization: Bearer $CLRD_TOKEN" \
  -d '{"topic":"infra","for":"8h","comment":"maintenance"}' \
  https://clrd.example.com/api/v1/silences

# Mute anything tagged backup, in every topic, until Monday morning
curl -H "Authorization: Bearer $CLRD_TOKEN" \
  -d '{"tag":"backup","until":"2026-11-23T08:00:00Z"}' \
  https://clrd.example.com/api/v1/silences
```

- Give `topic`, `tag` or both. With both, only that tag in that topic is muted.
- Give exactly one of `for` (a duration like `30m`, `8h` or `7d`) or `until`
  (an RFC3339 time). At most 8784 hours (a year) ahead.
- `comment` is optional, up to 256 characters, shown to whoever lists silences.

The answer is the silence, with its `id`.

Agents use the MCP tools `silence` (same fields), `silences` and
`end_silence`.

## See and end them

```sh
curl -H "Authorization: Bearer $CLRD_TOKEN" https://clrd.example.com/api/v1/silences
curl -X DELETE -H "Authorization: Bearer $CLRD_TOKEN" https://clrd.example.com/api/v1/silences/<id>
```

The list shows only silences that are still running. A silence ends by
itself at its time; `DELETE` ends it early.

## Who can set one

A token that can publish to a topic can silence it. A silence with no topic
(a tag across every topic) needs a token that is not limited to a list of
topics.

## What it mutes

Every priority, urgent (5) included. If a topic must always ring, do not
silence it; silence a tag instead.

Each notification carries `silenced`: `true` when a silence matched at the
moment it was published, updated, or woke from a snooze. Apps that speak the
ntfy protocol get a silenced message at priority 1 (min), which shows it
without a sound. The web inbox plays no sound for a silenced notification, and the clrd
Android app shows it in the shade without a sound or vibration. A silence
started or ended on another device counts from the next notification.

Quiet hours on a single phone (no sound from 22:00 to 07:00 on this device
only) will be a setting in the Android app, not a server silence.
