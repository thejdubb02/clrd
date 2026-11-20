# Silences

A silence mutes a topic, a tag, or a tag within one topic until a time you
pick. Notifications still arrive, sync to every device and show in every
inbox. They just make no sound and do not vibrate. When the silence ends,
new notifications alert again; nothing is held back and replayed.

Use one for a maintenance window, a noisy backup job overnight, or a weekend
away from a work topic.

## Start one

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
without a sound. The clrd Android app and web inbox are learning to read the
flag; until they do, they still alert.

Quiet hours on a single phone (no sound from 22:00 to 07:00 on this device
only) will be a setting in the Android app, not a server silence.
