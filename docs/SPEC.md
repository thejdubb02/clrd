# clrd spec (v0, 2026-11-19)

The shared contract for every part of clrd. Server, web, Android and SDKs all
build against this file. Change it in its own commit when the contract changes,
and say so in the PR.

Tagline: **Dismiss once. Gone everywhere.**
Docs and examples use `https://clrd.example.com` as a placeholder for your
own server. There is no public instance and no default server.

## 1. Repo layout

| Dir | Owner thread | What |
|---|---|---|
| `server/` | server | Go package `github.com/thejdubb02/clrd/server`, binary in `server/cmd/clrd`. |
| `web/` | web | Inbox app and landing/docs pages, embedded in the binary via `embed`. |
| `android/` | android | Kotlin app, Gradle, F-Droid ready. |
| `desktop/` | desktop | Tray app for Windows and Linux (#162): Rust, Tauri 2. Section 8.1. |
| `sdk/python`, `sdk/js`, `sdk/go` | integrations | Thin clients over the REST API. |
| `server/mcp/` | integrations | MCP server mounted at `/mcp`. |
| `plugins/`, `.claude-plugin/` | integrations | Agent plugins: `plugins/claude-code`, listed by the repo's Claude Code marketplace. |
| `docs/` | everyone | This spec, `api.md`, `self-host.md`, `integrations/*.md`. |
| `assets/` | web | `logo.svg`, `mark.svg`, icons, notification sounds (`sounds/`). One source for every surface. |

One Go module at the repo root, `github.com/thejdubb02/clrd` (`go.mod`
owned by the server thread). Reason: `go:embed` cannot reach outside its
module, and the binary must embed `web/`. So `web/` is a plain package in that
module (no own `go.mod`); `sdk/go` gets its own nested `go.mod` so SDK users do
not pull in SQLite. How `web` and `server/mcp` plug into the binary:
`server/README.md`.

## 2. Data model

A **notification**:

```json
{
  "id": "01JD8...",            // server ULID
  "topic": "infra",
  "external_id": "bk-nightly", // optional, sender's key, unique per topic among non-final rows; max 256 chars, not . or ..
  "title": "Nightly backup failed",
  "message": "restic exited 1 after 4m 12s.",
  "format": "markdown",        // or "text"; section 2.1. Missing in old events = markdown
  "priority": 4,               // 1..5, ntfy scale. 5 = urgent (Signal orange)
  "tags": ["backup"],
  "click": "https://...",      // optional
  "icon": "https://.../disk.png", // optional http(s) image URL, max 2048; "" if none
  "color": "#0EA5E9",          // optional sender color, #RRGGBB; "" if none (and in old events). Color rules win over it (section 2.1)
  "attachment": {"name": "report.pdf", "url": "/api/v1/files/9f2c...", "type": "application/pdf", "size": 48213}, // or null
  "actions": [{"label": "Open draft", "url": "https://..."}, {"label": "Yes", "reply": "yes"}],
  "reply": "",                 // the reply action tapped, "" until someone answers
  "reply_text": "",            // what was typed with a text reply action, plain text; "" for none
  "state": "active",
  "state_by": "pixel-9",       // device name that made the last change
  "state_by_id": "k3j9x2m1q8wz", // that device's token id ("" for the server); names can repeat, ids cannot
  "snooze_until": null,        // RFC3339 when state = snoozed
  "expires_at": null,          // RFC3339 when the server resolves it on its own, or null
  "created_at": "2026-11-19T05:00:00Z",
  "updated_at": "2026-11-19T05:00:00Z",
  "seq": 48211,                // seq of the last event that touched it
  "silenced": false,           // a silence matched when that event was written: show, do not alert
  "repeat": null,              // or {"every": "5m", "left": 8, "count": 2, "next": "<RFC3339>" or null}
  "marks": [],                 // who saw or actioned it, below; never null
  "devices": [],               // token ids it alerts on, [] for every device; never null. Missing in old events = []
  "requested_topic": "up",     // only when a sender route moved it: the topic it was sent to (section 5.1)
  "changed": true              // only in the answer to ack, snooze, dismiss, resolve or reopen: false when it already had that state (#693)
}
```

**Marks:** anyone who may read a topic (a person, or an agent with its own
token) can mark one of its notifications `seen`, or `actioned` with an
optional `note` (at most 200 characters) and `link` (http or https), with
`POST /api/v1/notifications/{id}/mark`. Each mark is
`{"by":"stew","by_id":"...","mark":"actioned","note":"filed relay#512","link":"https://...","at":"<RFC3339>"}`.
A mark never changes the state, so it clears and hides nothing for anyone;
everyone who reads the topic sees it on the card. There is one mark per
author: a token attached to a person marks as that person (`by_id` is the
person's id), any other token as the token its device tree starts from.
Setting it again replaces it; only the author changes or removes it. A mark
writes a `mark` event (section 3) and moves `updated_at`. A publish that
updates the row by `external_id` keeps its marks.

**Emoji (#306):** `title` and `message` are sent with common GitHub and
Slack shortcodes shown as the emoji (`:warning:` becomes ⚠️), and a tag that
names an emoji (`rotating_light`, `warning`) goes in front of the title, or of
the message when there is no title, as ntfy apps show it. Unknown names, text
inside backticks and strings like `10:30:15` or `a:b:c` stay as typed. The
stored row keeps the text as sent in. The ntfy endpoints send the shortcodes
as emoji but leave the tag emoji out, since ntfy apps add those themselves.

**States:** `active`, `acknowledged`, `snoozed`, `dismissed`, `resolved`.

- `dismissed` (a user cleared it) and `resolved` (the sender cleared it) are
  final. Both remove it from every inbox and every phone tray, except a
  dismiss from a device whose "Clears everywhere" is off (below).
- **Clears apply everywhere (#767):** a per-device option, on by default.
  Off, a dismiss (single or `/api/v1/notifications/clear`) made with that
  device's token hides the notification on that device only: the shared
  state stays, no other device gets an event, and no hook or push fires.
  That device gets one `state` event carrying `"only":"<its token id>"`
  with the notification shown `dismissed` by it (its `seq` stays the row's);
  every other token never receives such an event. On that device lists,
  counts and topic open counts leave it out (it is in no state there, not
  even `dismissed`), `GET` shows it `dismissed`, and its stream sends no
  more events for it; it stays hidden
  there through edits and repeats until that device reopens it, which sends
  it alone the real notification again (a reopen changes the shared state
  only when that is final). A real clear made elsewhere still reaches it and
  ends the hide, so a later reopen by anyone shows it there again.
  Changing the option follows the rules of `PUT /api/v1/devices/{id}/approve`,
  and as with approve a browser or phone signed in or linked with a token
  starts with that token's option (and keeps the option of a row it replaces).
  The web Devices page sets it for any device; the Android app shows a
  switch for its own phone under Settings, Devices, and a refusal from the
  server (`approve_denied`) shows beside it in plain words.
- `acknowledged` keeps it in the inbox, stops repeat alerting, shows "Acknowledged on <device>".
- `snoozed` hides it until `snooze_until`; then the server moves it back to
  `active` with a new event (and re-notifies).
- `expires` on publish (a duration like `30m`, or an RFC3339 time, in the
  future) sets `expires_at`. When it passes, the server moves a non-final row
  to `resolved` (`state_by` "server") with a normal event and clears
  `expires_at`, so a reopen does not expire it again. A publish that updates
  the row replaces `expires_at` with its own (none if it gave none).
- Publishing again with the same `external_id` while the row is non-final
  updates that row (title/message/priority) instead of making a new one,
  and clears `reply`. With `"reopen": true` an acknowledged row also goes
  back to `active` in the same write. After it is final, a new publish
  makes a new row.

**Silences:** a silence mutes a topic, a tag, or a tag within a topic until a
set time (`POST /api/v1/silences`). Notifications still arrive, sync and show;
the server sets `silenced: true` on every event it writes while a silence
matches (publish, update, snooze wake, check-in), and on every publish from a
muted sender (section 5.1), and clients must show those
without sound or vibration (the web inbox and the Android app do). A silence mutes every priority, 5 included. It
ends by itself; `DELETE` ends it early. Events logged before a server upgrade
may omit `silenced`: treat missing as `false`. ntfy subscribers get a silenced
message at priority 1. Quiet hours on one device are a client setting, not a
silence.

**Chosen devices (#82):** `"devices": ["pixel-9", "k3j9x2m1q8wz"]` on publish
alerts only those devices. Each entry is a device name (any case) or token
id, as `GET /api/v1/devices` and MCP `devices` list them, and must match at
least one device that may read the topic, else 400 (so a typo never silences
everyone); a name on several devices picks them all; at most 20 entries.
The server stores the matching token ids in `devices`. Every other reader
still gets the notification, lists it and syncs its state as usual, but the
copy sent to it carries `silenced: true`, so it shows without sound (ntfy
subscribers get priority 1). `GET /api/v1/notifications` and
`GET /api/v1/notifications/{id}` answer the same way for the token asking, so
a client rebuilding from the list after a reset alerts exactly where the
stream would have (#588). Only that copy changes: the row, the event log
and outgoing webhooks keep the stored value. The list holds through state
changes, and a re-alert to a device not on it is silenced like the first
(clients already never alert on a silenced `repeat`); an update by `external_id` replaces it,
and an update without `devices` alerts everyone again. Live publishes take
it too. An entry equal to a token id picks only that token, never a device
named like it. The reply's `devices` shows a publisher the ids of the readers
it picked; ids are labels, not credentials (`state_by_id` shows them too).
Native JSON publishes (with or without an upload) and MCP `send` take it;
the ntfy, Gotify, Pushover and incoming webhook doors have no such field.

**Live:** `"live": true` on publish (ntfy's `Cache: no`) sends the message to
the ntfy streams open right now and stores nothing: no row, no event, no seq,
no hook. It is never replayed, so a reconnect sees nothing, and native and
MCP streams never get it. The reply carries `live: true` and a 12 character
`id` starting with `l`; ntfy's `since=<that id>` replays what was stored after
it. A live publish with `snooze_until`, `repeat`, `expires`, `external_id`,
`status` or an uploaded file is a 400.

**Repeat:** `"repeat": "5m"` on publish (1m to 24h) re-alerts while the row
stays `active`, up to `repeat_times` (1 to 100, default 10). Each re-alert is
a `repeat` event with the full notification; `repeat.count` goes up and
`repeat.next` says when the next one is due (null once it stopped). Clients
alert again on a `repeat` event exactly as on `created`, unless `silenced`.
Acknowledge, snooze, dismiss, resolve or a tapped reply stops it for good on
every device; a reopen or snooze wake does not restart it. A scheduled send
(`snooze_until`) counts from its delivery. A publish that updates the row
replaces `repeat` with its own (none if it gave none). ntfy subscribers get
each re-alert as a new message. The web inbox plays its sound again on a
`repeat` event, and the Android app alerts again even while it is showing.

**Actions:** each action sets exactly one of `url` (link), `reply` (answer),
`copy` (clipboard text), `http` or `broadcast`. For example,
`{"label":"Copy code","copy":"123456"}` supplies text to copy. Copy text
is limited to 4096 bytes and works on any topic. The web inbox, Android
inbox and Android notification shade copy the value and clear the card
after a successful copy if the action sets `clear: true`. Android hides
the clipboard preview of the value. `reply` is `[A-Za-z0-9_-]{1,64}`, unique within the
notification, and there are at most 3 reply actions (an Android tray entry
shows 3). Tapping one posts it to `/api/v1/notifications/{id}/reply`, which
sets `reply`, sets the state to `acknowledged` with `state_by` = the device,
and logs one `state` event. A device sends the `seq` it showed with the tap;
if the notification changed since (a new question under the same key), the tap
gets 409. A mark or a re-alert does not change the question, so it never
refuses a tap, even after older events are pruned (#452). An `acknowledged` notification with a non-empty `reply` is answered,
and `state_by` is who tapped. The first answer wins: a second one gets 409.
Any later state change except dismiss and resolve (reopen, snooze, a plain
ack) clears `reply`. Reply actions are not sent to ntfy subscribers.

**Typed answers** (#153): a reply action with `"text": true`, as in
`{"label":"Other","reply":"other","text":true}`, is answered with typed text
instead of a plain tap. At most one reply action per notification has it, and
only a reply action may (400 otherwise). Senders that never set `text` see no
change. The client contract:

- Show a text box for the action with `text: true`; its other reply actions
  stay plain buttons. Send `{"reply":"other","text":"...","seq":N}` to the
  reply route. The server trims spaces and line breaks at both ends and keeps
  the ones inside. The text must be 1 to 1000 characters (Unicode code
  points, not bytes) after trimming.
- 400 `bad_request`: no text for a text action, text for a plain one, or over
  1000 characters. Keep what was typed and show the error. A client that
  does not know `text` and taps the action gets this 400 too, so it answers
  nothing by mistake.
- 403 `approve_denied` (this device may not answer) and 403 `own_request`
  (the token tree that sent it): the same rules as a tap (section 4). Show
  why; nothing changed.
- 409 `conflict`: already answered, cleared, or the question changed since
  `seq`. Drop the text box and show the current row.
- 202: this device is ask. The body is the confirm prompt; the question stays
  unanswered until an allowed device confirms. The prompt's message ends with
  `Answer: <text>` and its `format` is `text`, so the typed text is never
  rendered as markdown. The same text again returns the same prompt; other
  text edits that same prompt (an `updated` event, new `seq`), so a confirm
  sent with the `seq` it showed before the edit gets 409.
- 200: answered. The row has `reply`, `reply_text` and state `acknowledged`,
  in one `state` event, like a tap.

Every device shows `reply_text` on an answered row as plain text, never
markdown or HTML. It is cleared together with `reply` (a new question,
reopen, snooze, a plain ack) and kept on dismiss and resolve. Hooks get it in
the event body like any other field. `reply_text` is in every notification
JSON, `""` when there is none.

**Device actions** (#401, ntfy's `http` and `broadcast` buttons) are a third
and fourth kind: `{"label":"Off","http":{"method":"PUT","url":"https://...",
"headers":{...},"body":"..."},"clear":true}` is a request the reader's device
sends when tapped (method one of GET, POST, PUT, PATCH, DELETE, HEAD, POST if
empty; an http(s) URL of at most 2048 characters; at most 10 headers of at most
1024 characters each, no line breaks; a body of at most 4096 bytes), and
`{"label":"Ping","broadcast":{"intent":"...","extras":{...}}}` is an Android
broadcast (at most 10 extras). `clear` asks the device to clear the
notification after the button runs. Because any publisher could make every
reader's device call any address, a topic takes them only after an admin turns
`device_actions` on (`PUT /api/v1/topics/{topic}`); off by default. With it off
the native API answers 403 and a ntfy publish drops them silently. Turning it
off keeps what was already published, and once the switch answers, no publish
still in flight keeps one. Readers, ntfy subscribers and outgoing
webhooks get the whole action, headers and body included, so do not put a
secret there that a reader of the topic must not see. ntfy apps run device
actions, and so does clrd's Android app, as tray buttons (a broadcast with no
intent sends ntfy's `io.heckel.ntfy.USER_ACTION`, and `clear` applies only when
the button worked). The web inbox never runs them: it shows each one under the
card as text (the label and the method and URL, or the broadcast intent),
without headers or body (#450).

**Icons:** `icon` is an absolute `http` or `https` URL with a host (at most
2048 characters); anything else is a 400. Clients load it straight from that
URL, so the image host sees the viewer's IP address. Each topic can have its
own icon (`PUT /api/v1/topics/{topic}`), and so can each sender (its `icon`
rule, section 5.1). A publish that gives no `icon` gets its sender's icon,
else the topic's, copied in at publish time, so changing either later
leaves notifications already sent as they were. Fields added after a client
shipped (`icon` today) may be missing from events logged before the server
upgrade: clients treat a missing field as empty (`attachment`: null).

**Attachments:** a notification carries at most one file, `attachment`
(`null` if none). Two kinds:

- **Uploaded** to clrd: `url` is a path on this server,
  `/api/v1/files/<id>`. Clients fetch it with the same credentials as the rest
  of the API (session cookie on the web, Bearer token on Android). `type` is
  sniffed from the content by the server, never taken from the sender; `size`
  is in bytes.
- **External**: `url` is an absolute `http`/`https` URL (same rule as `icon`)
  that the server never fetches. `type` is `""` and `size` is 0. Clients open
  it as a plain link and never send the clrd token to it.

So: a `url` starting with `/` is ours, send credentials; anything else is
not. `name` is the sender's file name cut to its last path segment, control
characters removed, at most 255 bytes (`attachment` if empty). Publishing
again under the same `external_id` replaces the attachment, or clears it if
the new publish has none, and the replaced file is deleted. Older events may
still point at a deleted file: clients treat a 404 as "file gone". A file
stays while its notification exists: when retention prunes a dismissed or
resolved notification, its file goes with it. Downloads serve png, jpeg, gif
and webp inline and everything else as a download (`Content-Disposition:
attachment`), with `X-Content-Type-Options: nosniff` and
`Content-Security-Policy: sandbox`.

**Share and copy (client guidance):** each card offers Share and Copy. Share
sends the title, the message and `click` (if set) to the platform's share
sheet (Web Share API, Android `ACTION_SEND`). Copy puts the same text on the
clipboard: the title, a blank line, the message, then `click` on its own line.

## 2.1 Message formatting

**Format.** Each notification has `format`: `markdown` (default) or `text`.
Publish sets it with `"format"`, or ntfy style with `"markdown": false` (JSON)
or `X-Markdown: no` (header, also `Markdown`, `md`, and `?markdown=` on a
GET publish). `format` wins when both are given. A re-publish under the same
`external_id` sets it again like every other field. Clients show `text`
exactly as sent. `markdown` is a subset: bold, italic, underline (`<u>` or
`++text++`, the only raw HTML allowed; all other HTML is escaped),
strikethrough, code, code blocks, lists, quotes, headings (shown small) and
links. Never images inside the body, scripts, iframes or styles. ntfy output carries
`content_type: "text/markdown"` for `markdown` and leaves it out for `text`,
so ntfy apps render the same way. Events logged before this field existed
have no `format`: clients treat that as `markdown`.

**Display settings.** One set per server, stored on the server so every device
shows messages the same way:

```json
{"render_formatting": true, "autolink": true, "favicons": true, "link_previews": false,
 "topic_previews": {"news": true}, "snooze_intervals": ["1h", "4h"], "snooze_default": "1h",
 "keep_cleared_days": 30, "topic_keep_cleared_days": {"ci": 2}}
```

`render_formatting` off shows Markdown as plain text. `autolink` turns bare
http(s) URLs into links. `favicons` shows a site icon next to links.
`link_previews` shows a card for links, and `topic_previews` overrides it per
topic. `GET /api/v1/settings` reads them and `PUT` changes only the fields
given (a topic mapped to `null` drops its override); both need scope read. A
token limited to some topics sees and sets only those topics' overrides and
cannot change the four switches or the snooze times (403). `snooze_intervals`
is the list of snooze times clients offer (1 to 8 Go durations such as `15m`
or `48h`, each above 0 and at most 31 days, no two the same length) and
`snooze_default` is the one a plain tap uses; it must be in the list. A list
without the current default makes its first entry the default.
`keep_cleared_days` is how many days a dismissed or resolved notification is
kept after it was cleared (its `updated_at`); `0` keeps them forever. Open
notifications stay by default. With `CLRD_NTFY_CACHE_TOPICS` set to exact
topic names separated by commas, the hourly prune also deletes active and
acknowledged rows on those topics after `CLRD_RETENTION` since `updated_at`.
Snoozed rows and delayed sends survive the prune and start a new retention
window when they wake. A removed row emits an identifying `state` event
with state `resolved`, via `cache-expiry`, and no original notification body,
so live clients can clear it. In ntfy streams this is a `message_delete`
by sequence ID, including on filtered streams. Until `keep_cleared_days` is set it
follows `CLRD_RETENTION` (30 days by default); once set, the setting wins.
Changing it needs scope admin and takes 0 to 36500. The hourly prune applies
it, so a shorter value deletes older cleared rows within the hour, and the
event log is cut to the shorter of the two periods: a client whose last
`seq` is older gets 410 and resyncs. `GET /api/v1/counts?keep_cleared_days=N`
previews it: `would_remove` is how many cleared notifications N would delete
now (in the topics without their own value). `topic_keep_cleared_days` sets
it for one topic, like `{"alerts": 7}`, and overrides the global value there;
`null` drops a topic's value. It needs scope admin on that topic, reads show
only the topics the token can read, and the event log is cut to the shortest
keep in use. With `&topic=T`, `would_remove` counts only topic T, to preview
its own value (the state counts stay over every readable topic).
`keep_cleared_days=default` previews dropping a topic's value: it counts
against the global keep exactly, `CLRD_RETENTION` to the hour when unset.
`unlink_idle_browsers_days` (default 30, 0 never, at most 36500; setting it
needs an admin token with no topic list) unlinks, in the hourly sweep, every
signed-in browser not used for that many days, each with a device log line by
"unused for N days". It never touches a phone, a token typed in or an agent,
nor a browser with a stream open, web push, or a browser signed in under it
(#765).

Swipe buttons (#161): `swipe_start` and `swipe_end` choose the
buttons a swipe on a card reveals, on every client, like
`{"swipe_start": ["clear", "snooze"], "swipe_end": ["ack"]}` (those are the
defaults, and what the Android app shows today). `swipe_start` is what a
swipe toward the start of the line reveals (a swipe to the left, or to the
right in a right-to-left language) and `swipe_end` is the opposite swipe (to
the right, or to the left in a right-to-left language). The buttons appear in
the space the card slides away from, and the first entry sits nearest the
screen edge on that side. Each value is a JSON array of 0 to 3 strings from
`clear`, `snooze`, `ack` and `resolve`; an empty array means that swipe opens
nothing. A string may appear once across both lists, checked against the pair
the PUT would leave (a side not in the body keeps its stored list). Anything
else gets 400 naming the key: `null`, a value that is not an array, an entry
that is not a string, an unknown name, more than 3 entries or a repeat; a
rejected PUT changes no field. A client skips a button the card cannot take
(Ack on a card already acknowledged, Snooze outside the Inbox, Resolve without
scope publish on the card's topic) and any action it has no control for, and
the device's own "Full swipe acts" switch runs the first button left on that
side. Clear stays undoable from either side. Reads need scope read; a token
limited to some topics or carrying per-topic grants cannot change them (403),
as with the four switches. The web inbox shows them on a touch screen only (mouse and keyboard keep
the card's own buttons) and has no Resolve control, so it skips Resolve.

Color rules (#165): `color_rules` is a list of up to 50 rules that color
cards on every client, like `{"color_rules": [{"topic": "gatus",
"priority_min": 4, "color": "#DC2626", "style": "tint"}]}`. Default `[]`, so a
new server shows no colors. Each rule has `topic` (the topic the card landed
in, after any reroute), `tag` (one of its tags, exact), `priority_min` and
`priority_max` (the stored priority, 1 to 5, inclusive; 0 means no limit),
`title` (text found anywhere in the title, any case), `color` (`#RRGGBB`,
nothing else, since clients put it in CSS) and `style` (`bar`, a stripe on
the card's edge; `tint`, a light wash of the color; `solid`, a filled card
with text in a readable contrast; `gradient`, the color fading across the
card). An empty field matches anything. Every client runs the same match:
the first rule whose set fields all match wins; with no match a card with a
sender `color` (section 2) shows it as `bar`; otherwise it has no color. A
client that does not know a style draws `bar`. `server/testdata/colorrules.json`
holds cases every client's matcher must pass. A PUT replaces the whole list
(read first, send every rule to keep; `[]` clears it, `null` is a 400); a bad rule, or a field of the wrong type, is a 400
naming its index and field and changes nothing. Only a token with no topic
list and no per-topic grants may change it (403 otherwise); a limited token reads only rules with no
topic or a topic it may read or publish to. Presets are client shortcuts that write
ordinary rules: Severity is priority 5 `#DC2626`, 4 `#EA580C`, 3 `#CA8A04`,
all `bar`; By topic writes one `bar` rule per topic. Not built yet: matching
by sender (a notification carries no public sender field) and ntfy-style
per-topic display defaults.

Every change goes to every open
WebSocket and SSE stream as a `settings` message (section 3).

**Fetchers.** Clients never contact a linked site; the server does, so the
viewer's IP stays private.

- `GET /api/v1/favicon?host=<hostname>`: tries `https://host/favicon.ico`,
  then the page's `<link rel="icon">`. Only raster images (PNG, JPEG, GIF,
  WebP, ICO, BMP) are served; anything else, and every failure, returns the
  same fallback SVG. Cached on disk 7 days (a failure 1 hour).
- `GET /api/v1/preview?url=<url>&topic=<topic>`: 403 unless previews are on
  for that topic (or globally with no topic). Returns `{"url","title",
  "description","site_name","image"}` from Open Graph tags (falling back to
  `<title>` and `<meta name="description">`); `image` is a `data:` URL (raster,
  at most 512 KB). A page that cannot be fetched returns only `url`. Cached 24 h.

Both are scope read and share one guard: http and https only, ports 80 and
443 only, no user info in the URL, no proxy. Every connection, including each
redirect hop (at most 5), is checked after DNS resolves, and is refused if the
address is loopback, private (RFC 1918, `fc00::/7`), link-local (including
`169.254.169.254`), multicast, unspecified, CGNAT `100.64.0.0/10`, or another
special-use range (`0.0.0.0/8`, `192.0.0.0/24`, documentation nets,
`198.18.0.0/15`, `240.0.0.0/4`, `64:ff9b::/96`, `64:ff9b:1::/48`,
`2001::/32`, `2002::/16`). IPv4-mapped IPv6 addresses are checked as IPv4.
Each fetch has a 5 s timeout and a 1 MB cap, sends `User-Agent: clrd/1
(+https://github.com/thejdubb02/clrd)`, and at most 8 run at once. Cache files
live in `cache/` next to the database and are swept hourly.

## 2.2 Sounds

One set of built-in sounds, with the same names on every surface, so a choice
means the same sound on a phone, the web inbox and a desktop:

| Name | Sound |
|---|---|
| `default` | The platform's own notification sound. **Unset means this.** A browser has none, so the web inbox plays `chime`. |
| `none` | No sound. |
| `chime` | Two rising notes. |
| `ding` | One bright note. |
| `pop` | A short drop in pitch. |
| `bell` | A small bell with a long ring. |
| `alert` | Two tones back and forth, for urgent alerts. |
| `soft` | One low, pure note that fades slowly. |

The six built-in sounds are `assets/sounds/<name>.wav` (16-bit mono PCM at
8000 to 22050 Hz, the lowest rate that holds the sound's tones, under 40 KB
each), rendered by the binary from the same code (`internal/sounds`, not embedded) and served at `/assets/sounds/<name>.wav`. The
Android build packs the same files. They are generated from sine waves by
`go run ./internal/sounds/gen` (no samples, so they ship under Apache-2.0); to
change a sound, change the generator and commit both. New names may be added;
a name is never renamed or reused for a different sound. A client that gets a
name it does not know plays `default`.

**Per device.** Each device keeps its own choice (a phone on `alert`, a desktop
on `pop`); unset means `default`, so sound works with no setup. The choice is
stored on the server per device, so an agent can read the valid names and read
and set any device's choice through the API and MCP (`GET /api/v1/sounds` and
`PUT /api/v1/devices/{id}` in section 5, the `sound` tool in section 6),
and the device learns of a change over its WebSocket without a restart. A
per-topic override is a later issue.

What still wins over the choice: an event with `silenced: true` (section 2),
quiet hours on the device, a muted topic, and the platform's own settings (on
Android the user can change a channel's sound in system settings, and the app
says so when that overrides its choice). Browsers block sound until the page
has had a click or key press; the web inbox then shows a hint instead of an
error.

## 2.3 Subscriptions (#617)

A person chooses which topics reach their devices. An unsubscribed topic
shows no card, rings no tray or system notification and adds to no count or
badge on any of that person's devices. It is a view filter on the server, not
a state: unsubscribing clears, dismisses or deletes nothing, and nobody else
sees a difference (other users, ntfy subscribers, scripts on their own
token, outgoing webhooks, silences and the sender all work as before). Access still wins:
subscribing to a topic you cannot read is a 403 and never shows it.

**Whose.** Subscriptions belong to an owner: the user, for a token attached to
a user (section 4), and otherwise the typed-in token at the top of the tree
(the browsers signed in with it and the phones and desktops linked from it
share its set). Attaching or detaching a token moves its devices to the new
owner's set and counts as a change of that set (see Sync); the old rows stay
with the old owner.

**Who is filtered.** The filter applies to devices: browsers signed in through
`/api/v1/session` or by a phone, phones and desktops linked by a code, and
any token attached to a user. A typed-in token with no user is never
filtered, because it is usually a script and is often also the token a
browser signed in with. It reads `"filtered":false` with no topics, and a
PUT on it is a 409, since no user holds its choices; the browsers and
phones made from it read and change their tree's set. An app holding such
a token asks to follow that set by sending `subscribed=mine` on every
request (list, settings, subscriptions, WebSocket, SSE and events); it is
then filtered like a linked device, gets the same resets, and may change
the set. A request that does not ask stays unfiltered, so scripts and SDKs
are untouched, and asking only narrows: it never shows a topic the token
cannot read. The Android app always asks (#764), and reloads what it holds
the first time a settings message says it is filtered. A script on a token
attached to a user follows that user's subscriptions; give it its own token
with no user, or have it send `subscribed=all`, to see every topic.

**Defaults.** With no setup every topic is subscribed, so nothing changes for
anyone until they unsubscribe. `new_topics` (default `true`) says what a
topic with no row for this owner is. Setting it to `false` first writes a
`true` row for every topic that has no row yet, has stored notifications or
topic settings, and any of the owner's tokens can read today; existing rows
are kept as they are, so no existing topic changes. After that, a topic
seen for the first time, by a new publish or a new pattern grant, starts
unsubscribed. Setting it back to `true` changes no row.

**What the filter covers.** For a filtered token, these leave out unsubscribed
topics unless the request asks for `subscribed=all`: the notification list
and search, `GET /api/v1/counts`, `GET /api/v1/topics`, the WebSocket, SSE
and `GET /api/v1/events` (replay and live; a WebSocket `?topics=` list is
narrowed further, never widened), Web Push, and the server-rendered inbox
pages. `subscribed=all` on the list, counts, topics and streams shows every
readable topic, which is how a client browses an unsubscribed topic without
subscribing; clients that know nothing of subscriptions never send it, so
they get the filtered view. Getting one notification by id and every state
route (ack, snooze, dismiss, resolve, reply, mark) work on an unsubscribed
topic as on any other; each keeps its own scope checks, so subscriptions
never grant a route anything. An approval request, a check-in alarm or a quiet alarm on an
unsubscribed topic is hidden like any other message, so unsubscribing from
the approval topic (`agents` by default) also hides approval prompts; the
Topics page says so on that topic. Events the filter leaves out still move a
stream's `synced_seq` and the `ready` seq, as for topics a token cannot
read.

**Sync.** The owner's set is part of the per-device settings view
(section 2.1): `"subscriptions":{"filtered":true,"new_topics":true,"topics":{"chat":false}}`
in `GET /api/v1/settings` and in every `settings` stream message, with only
the topics that have a row and that this token can read. `filtered` is
false for a token that is never filtered (see Who is filtered); then
`topics` is empty and `new_topics` is left out. A topic is
subscribed when `topics[t]` is true, or it has no row and `new_topics` is
true. Every change appends an event of type `subscriptions` that only the
owner's filtered devices receive (it moves the seq like any event; clients
ignore the type, as section 3 says for unknown types). Each open stream of
those devices then gets the new `settings` message, then `{"type":"reset"}`
and `ready` (section 3), so every client, old ones included, reloads what
it shows: the open list, or for a view browsing an unsubscribed topic, that
topic with `subscribed=all`. Cards of an unsubscribed topic leave that
device and its Android tray, and the still open ones of a resubscribed
topic come back, quietly, as after an offline catch-up. A browser system
notification already shown stays until it is closed, as it does today
after a clear. A filtered device that connects with a `since` older than
the seq of its owner's last `subscriptions` event gets `reset` (and
`GET /api/v1/events` a 410 `reset`), so a device that was offline during
the change catches up the same way. A typed-in token with no user is not
filtered, so only an attach or detach resets it, never its browsers
changing the set.

**API and MCP.** `GET /api/v1/subscriptions` returns
`{"filtered":true,"new_topics":true,"topics":[{"topic":"chat","subscribed":false,"open":2,"last_at":"RFC3339"|null,"approvals":false}]}`,
every topic the token can read, subscribed or not, sorted by name. With
`"filtered":false` (a typed-in token with no user) `topics` is empty,
`new_topics` is left out and a PUT answers 409 `conflict`: the token has no
user to store topic choices for.
`PUT /api/v1/subscriptions` takes `{"new_topics"?:bool,"topics"?:{"chat":false,"infra":true}}`
(`new_topics` or `topics` or both, at most 500 topics in one call; a name outside the
topic pattern is a 400 naming it, a topic the token cannot read is a 403
naming it, and nothing is changed when any entry fails) and returns the same
shape as GET. Scope read. An optional `"request_id"` (up to 64 letters,
digits, `-` or `_`) tags the change: GET and PUT answers carry the
`request_id` of the set's last change, left out when it had none or its
event has been pruned from the log, so a tab
that reloads mid-request can tell its own change from another device's
before offering Undo (#702). An optional `"if_request_id"` (same form)
applies the change only while the set's last change is still that request;
otherwise it is a 409 and nothing changes, so an Undo cannot reverse a
change another device made in between (#716). Any token of the owner may change the owner's set,
and a full admin may add `?for=<user id or token id>` to read or set another
owner's set (an invited agent is its own user, so this is how an admin agent
sets a person's topics). A user id, or the id of any typed-in token,
names its owner; for a typed-in token with no user that is the token
itself, which is not filtered, so to set the set its browsers and phones
share, name one of them. The MCP tool is `subscriptions(topics?,
new_topics?, for?)`: no arguments reads, arguments change those fields.
`GET /api/v1/topics` also gains `subscribed` and `last_at` on each topic.
Both also carry `approvals` on each topic (#623): true once any message
that waits for an answer (a reply action, as `/mcp/approve` sends, or a
confirm prompt) has been sent to it, and it stays true after that message
is gone. Clients warn before unsubscribing from a topic with `approvals`
(or `agents`), since an agent's request there would go unseen.

**Clients.** The web and Android apps add a Topics page (a switch per topic,
last message time, open count, search), Unsubscribe in a card's menu and on
a stack header, and a way to open an unsubscribed topic (`subscribed=all`
with `topic=`) without subscribing. When `filtered` is false they show the
Topics page as not available on this token. Android's device-only muted topics stay
as they are: muted still shows the card and only stops the pop-up. Per-device
overrides are a later issue.

## 3. Events and sync

Every change is an append-only **event** with a global, gap-free, monotonic
`seq` (SQLite `INTEGER PRIMARY KEY AUTOINCREMENT` in the same transaction as
the row change):

```json
{"seq": 48212, "type": "state", "notification": { ...full notification... }}
```

`type` is `created`, `updated`, `state`, `repeat` or `mark` (section 2), or
`subscriptions` (section 2.3, only to the devices it concerns).
A `mark` event never alerts: clients update the card quietly, and the ntfy
stream and hooks leave it out. Clients ignore types they do not know. A `state` event also carries `"via"`, how
the change was made (section 5), when the server knows it. Clients only
need the full
notification in the event; they never merge diffs.

Clients store the highest `seq` they applied. On (re)connect they ask for
everything after it. That is the whole sync model: no client clocks, no
conflict resolution, last write on the server wins.

**Alerts on catch-up (#589).** A client may keep only the last event per
notification while it replays, so a phone that was offline does not buzz for
things already cleared elsewhere. It must still remember, per notification,
whether any replayed event hid it (`snoozed`, `dismissed` or `resolved`). When
the last one shows it again (a snooze that ended, a reopen), that is a fresh
show and alerts as it did on the devices that were online, even if the card
was showing on this device before it went offline. The client keeps the
sequence of the event that showed it again, so it can tell a reopen made
elsewhere from this device's own action (an Undo it sent, matched against its
pending actions and the answers it got back), which stays quiet. A replayed
`repeat` or `mark` may carry an earlier show forward but never makes one: on
its own it never alerts, and it never brings back a card this phone's tray hid
by a swipe. A replay the client saves quietly (too long, or no `ready` in
time) stays quiet. This applies to clients that alert
on events (the Android and desktop trays); the web inbox plays nothing on
catch-up (section 10.1).

### WebSocket `GET /api/v1/ws?since=<seq>&topics=a,b`

1. Server sends every event with `seq > since` (replay), then the display
   settings `{"type":"settings","settings":{...}}` (section 2.1), then
   `{"type":"ready","seq":<current>,"epoch":"<id>","mark_id":"<by_id>"}`.
   `mark_id` is the `by_id` this token's marks get (section 2, "Marks"), so
   a client can tell its own mark on a card.
2. Then live events as they happen, and a new `settings` message whenever a
   device changes the settings. When a device in this token's list (the one
   `GET /api/v1/devices?all=1` shows, section 6) connects, finishes its replay
   or drops, the server sends that list again as
   `{"type":"devices","devices":[...]}`, so a client can show which devices
   are live without polling. A stream does not hear its own connect: read the
   list once after `ready`, then apply these. Clients ignore
   message types they do not know.
3. Server pings every 30 s; client reconnects with backoff and its last seq.
4. If `since` is older than retention, or older than the owner's last
   subscription change (section 2.3), server sends `{"type":"reset"}` and the
   client fetches `GET /api/v1/notifications?state=open` fresh.

After `reset` the server sends `ready` with the current seq and carries on
with live events, so the client drops its local state, fetches the open list,
and keeps the connection. The server also sends `reset` when `since` is ahead
of the server (a wiped server).

**Epoch.** The server picks a random `epoch` (a short string) when it creates
its database (or first opens one from before epochs) and keeps it for the life
of that database. It comes with `ready` and with the two responses that hand
out a seq, `GET /api/v1/events` and `GET /api/v1/notifications`; single events
do not carry it. Clients save it next to their seq and check it there, before
trusting anything they replayed. When the epoch they see differs from the
saved one, the seq numbers belong to another database (a server reinstalled or
started on a new file), so they treat it exactly like `reset`: drop the cursor
and local state, fetch the open list fresh, and carry on from the new seq. A
client with no saved epoch just saves the one it sees. A database restored
from a backup keeps the epoch it had, so on a clean stop the server also writes
the epoch and its head seq to `<db>.epoch` next to the database. At startup, a
database whose head seq is below the saved one, or whose epoch differs from
the saved one, was restored or swapped: the server picks a new epoch, and
clients start over.

Also `GET /api/v1/events?since=<seq>&limit=500` for clients that poll. Use
`subscribed=all` to poll all readable topics without restarting the cursor
when subscriptions are edited. The endpoint returns
`{"events":[...],"seq":<current>,"epoch":"<id>"}`, or HTTP 410 with code `reset`. SSE at
`GET /api/v1/events/sse?since=<seq>` sends the same JSON objects as the
WebSocket (events, `settings`, `ready`, `reset`), one per `data:` line, and
honours `Last-Event-ID` (`settings` and `reset` carry no `id:`).

Browsers cannot set headers on a WebSocket, so the WebSocket and SSE accept
the session cookie (same-origin only) as well as a Bearer token. Same origin
means the page's scheme and host equal `CLRD_BASE_URL`'s, or this request's
host and, when clrd knows it (TLS, or a trusted proxy's X-Forwarded-Proto),
scheme.

## 4. Auth

- **Tokens:** `Authorization: Bearer clrd_<32 random base62>`. Stored hashed (SHA-256).
  Scopes: `publish`, `read`, `admin`. Optional topic allow-list per token.
  Tokens imported from ntfy keep their `tk_...` value (also stored hashed) and
  work everywhere a `clrd_` token does.
- **Per-topic grants:** a token may instead carry an ordered list of grants,
  each a topic pattern (`*` matches any run of characters) and the scopes it
  gives there (`read`, `publish`, both, or none, which denies). For a topic the
  first grant whose pattern matches decides; no match means no access.
  Matching ignores case, as in ntfy, but topics are case-sensitive, so a grant
  that matches only in another case never allows: a deny on `secret` also
  denies `SECRET`, while a grant for `home` is skipped for `HOME` and a later
  grant decides. The
  token's own scopes are the ceiling: a grant never adds a scope the token
  lacks, and a grant never holds `admin`. A write-only grant (`publish` only)
  lets the token publish to the topic but not list, get, stream, download
  files from or act on its notifications, on the native API and on the ntfy
  routes alike. A token with grants never manages tokens and sees only its
  own devices in the device list (itself and what was linked from it). A browser signed in with it gets the same grants.
  Grants come from `clrd import ntfy` (ntfy's access control list, rules in
  ntfy's own order) or from `grants` on `POST /api/v1/tokens`.
- **Users (people):** a user is one person and owns many device tokens. Off
  until a full admin makes one: a token with no user works exactly as above.
  A user has a name and, optionally, grants (same rules as a token's). A
  token belongs to a user when it is attached (`user` on `POST
  /api/v1/tokens`, or `PUT /api/v1/tokens/{id}/user` with `{"user":"<id>"}`,
  `""` to detach); every browser or phone signed in or linked from it belongs
  to the same user. Access is worked out from the user on every request, so
  a new or existing device follows a later change to the user's grants. When
  the user has grants, they replace the device's own topics and grants (an
  empty list is no topic), and the device's own scopes stay the ceiling. A
  grant never gives admin, and a token holding `admin` keeps its own access
  whatever its user's grants. A user with no grants (`null`) limits nothing:
  devices keep their own access. Topics are shared between users by giving
  the other user a grant (read, publish or both); there is no other sharing.
  All of a user's devices are "mine" for the device list, the device log and
  sender names in the format check; the #187 `own_request` rule stays with
  the token tree, so a person can still answer a request their own agent
  sent. Only a full admin uses `GET`/`POST /api/v1/users`, `PUT
  /api/v1/users/{id}` (`{"grants":[...]}` or `{"grants":null}`), `DELETE
  /api/v1/users/{id}` (409 while tokens are attached) and the attach route.
  A change to a user's grants, or attaching or detaching a token, closes the
  open streams of that user's devices so they reconnect under the new rules.
  A stream reads its access again once it is subscribed, so a change made
  while it is still starting is never missed.
  A device whose user cannot be read is refused (401), never let through.
  These are separate from the ntfy password users below.
- Bootstrap: first start with no users prints a one-time admin setup URL to the log.
  `clrd token create --name pixel-9 --scope read,publish` on the CLI.
- Basic auth (`user:token`) and `?auth=` query param accepted for ntfy-client compatibility.
- **Passwords, ntfy routes only:** users imported from ntfy keep their bcrypt
  password hash, so `Basic user:password` (header or `?auth=`) works on the
  ntfy-compatible routes (section 5) with that user's grants. The native API
  (`/api/v1/*`, `/mcp`, the web session) never accepts a password: tokens only.
- **Anonymous:** off unless set. A full admin sets it with
  `PUT /api/v1/ntfy/anonymous {"grants":[{"topic":"up*","scopes":["publish"]}]}`
  (MCP `set_ntfy_anonymous`; `GET` or `ntfy_anonymous` shows it), and
  `clrd import ntfy` sets it from ntfy's `everyone` rules and
  `auth-default-access`. It is a token named `anonymous` with grants, read and
  publish only, never admin. It applies only on the ntfy-compatible routes,
  only to a request that sends no credentials at all (a wrong credential is
  still a 401), and it can never sign a browser in. An empty grants list (or
  revoking the token) turns it off; any change closes open anonymous streams
  so they reconnect under the new rules.
- Web UI uses a session cookie (HttpOnly, SameSite=Lax) after login.
- Every token has a **device name**; it is what shows as `state_by`. Names need not be
  unique, so a client that asks "did I make this change?" compares `state_by_id` with its own token id.
- What each scope allows: `read` = list, get, events, WebSocket, plus the
  user's own actions (ack, reply, snooze, dismiss, reopen, bulk clear), because a
  phone that can see a notification must be able to clear it. `publish` =
  publish, edit the message, resolve (by id or by external id), ack by
  external id. `admin` =
  everything, including tokens. A token with a topic allow-list sees and
  touches only those topics; one with grants follows them, as above.
- Session: `POST /api/v1/session` with `{"token":"clrd_..."}` (JSON or a form
  post, optional `next` redirect for forms, optional `name`) mints a new token
  for this browser, with the same scopes, topics and grants, and sets it as the
  `clrd_session` cookie. It is named `name`, else from the User-Agent ("Firefox
  on Linux"), so each browser is its own device. Revoking the token typed in
  also revokes the browsers signed in with it. One token keeps at most 20
  browsers signed in: the 21st sign-in revokes the one used least recently.
  Phones linked under a token have their own cap of 20, so browser sign-ins
  never sign a phone out.
- **A device that links again replaces its own row (#765).** Sign-in, setup,
  the pair claim and the link claim take an optional `install`: an id the
  browser or app keeps for itself (16 to 64 of `A-Z a-z 0-9 _ -`; anything
  else is ignored). When a new row is made, an older row with the same
  `install`, under the same parent, of the same kind (browser or phone) is that
  device linked before: the browsers signed in under it move under the new row,
  its web push moves too, and the old row is revoked with a device log line.
  Never by name. Per the #187 guard rails the new row's `approve` is the lower
  of the token's and the old row's, so a relink never raises it. A per-device
  sound is not carried over. Each row also keeps `detail`, the browser and
  system from the User-Agent ("Firefox 120 on Linux") or the link claim's
  optional `detail` ("Pixel 8, Android 15"), so rows of one name can be told
  apart.
- `DELETE /api/v1/session` revokes
  the browser's token and clears the cookie. The cookie works on `/api/v1/*`
  only, never on the ntfy-style GET publish webhooks.
- **Sign in with a phone (pairing):** a signed-out browser posts
  `POST /api/v1/pair` (same origin, no auth) and gets `{code, url, qr,
  expires_at}`: a 22-character code, the link `<CLRD_BASE_URL>/pair?c=<code>`,
  that link as a QR PNG data URL, and an expiry 2 minutes out. It also gets an
  HttpOnly, SameSite=Strict `clrd_pair` cookie holding a secret only it knows.
  It polls `POST /api/v1/pair/claim` with `{"code":...}`: 202 while waiting,
  410 once denied, expired, used, or when the cookie does not match, and 200
  once approved, which signs it in exactly like `POST /api/v1/session` with the
  approving token as parent (same access, revoked with it). A signed-in device
  reads `GET /api/v1/pair/{code}` (`{name, created_at}`) and posts
  `/api/v1/pair/{code}/approve` (200 `{name}`) or `/deny` (204); unknown or
  spent codes are 404. `GET /pair?c=` is the page the QR opens: Approve and
  Deny when signed in, else an "Open in the clrd app" link to
  `clrd://pair?server=<base>&code=<code>`. Codes are single use, kept in memory
  only, and at most 64 wait at once (429 `busy` past that). The code alone
  approves nothing and cannot be claimed by another browser.
- **Link a phone from a signed-in browser:** the reverse of pairing. A
  signed-in browser (any token or session, any scope) posts
  `POST /api/v1/link` and gets `{code, url, qr, expires_at}` with the link
  `<CLRD_BASE_URL>/link?c=<code>`. The clrd app scans it and polls
  `POST /api/v1/link/claim` (no auth) with `{code, secret, name}`: `secret`
  is 32+ characters the app made, `name` its device name. The first claim
  binds the secret and name to the code; a later claim with another secret
  gets 410. It gets 202 `{"state":"waiting"}` until approved, then 200
  `{"state":"approved", "token", "name"}` once: a new token with the creating
  token's scopes, topics and grants, listed under the token typed in (when
  the creator is a browser session, the server follows its parents up past
  every browser session to the first typed token or linked phone), so signing
  out any browser on the way keeps the phone and revoking that token revokes
  it. 410
  when denied, expired, used or unknown. Only the creating token may read
  `GET /api/v1/link/{code}` (`{name, state, check}`, state `waiting` or
  `claimed`; once claimed, `check` is a 4-digit match number: the first 4
  bytes of SHA-256 of the secret as a big-endian unsigned int, mod 10000,
  zero-padded. The app shows the same number, so the user approves only the
  phone in their hand, not one that photographed the code first)
  and post `/api/v1/link/{code}/approve` (200 `{name}`, 409 before a phone
  claims) or `/deny` (204); anyone else gets 404. `GET /link?c=` is the page a
  phone camera opens: an "Open in the clrd app" link to
  `clrd://link?server=<base>&code=<code>`. Link codes share the pairing
  expiry and single use; they have their own cap of 64, so waiting public
  sign-ins cannot crowd them out.
- **Agent invite (#454):** one short string enrolls an agent as a user of
  kind `agent`. `POST /api/v1/agents/invite` `{name, topics, expires_in?}`
  from a person's browser or phone (a session token, not one of an agent) or
  a full admin token (so agents with admin can provision agents); a typed-in
  non-admin token or anything of an agent gets 403. `topics` is 1 to 64
  names. Each gets the `read` and `publish` the creator holds on it, never
  more; a topic with neither is 403. Only a full admin may name a pattern
  (`ops-*`). `expires_in` is 1m to 1h, default 15m. A name a user already
  has is 409. Answer 201 `{code, invite, url, expires_at, name, grants}`:
  `invite` is `clrd://agent?code=<code>&server=<base>`, `url` is
  `<base>/agent#<code>` (after `#`, so the code never reaches a request
  line or a proxy log, #463). The agent posts `POST /api/v1/agents/claim`
  `{code}` (no auth): 200 `{token, user, name, guide}` once, where `guide`
  is `{server, grants, publish, notification, events, mcp, approve_request,
  approve, limits: {publish_per_minute, attachment_max_bytes}}`. The claim
  makes the user (grants as in the invite) and its own token (`read`,
  `publish`, approve `denied`) in one transaction. The code is spent by the
  first claim, even one that fails (a name taken since: 409); after that, or
  once expired or unknown, 410 `gone`. `GET /agent` is the guide as plain
  text with the claim command, the same for every invite (the server never
  sees the code); it spends nothing and holds no token. Invites share the pairing store: single use,
  their own cap of 64, held in memory, so a restart drops them. The agent
  shows in the creator's device list (kind `agent`, can unlink) and in
  `GET /api/v1/users` (`kind`, `last_seen_at`). Unlinking it revokes the
  token, ends its streams and drops its user. Access is fixed at claim: a
  later change to the creator does not change the agent; an admin changes it
  with `PUT /api/v1/users/{id}`. The agent's own request for approve rights
  goes to a person (`request_approve`); it cannot answer its own requests.
- Setup: with no admin token in the database the server logs
  `<CLRD_BASE_URL>/setup?code=<one-time code>`. Opening it shows a form; posting
  it creates an admin token, shows it once and signs the browser in.
- **Who may approve:** every token has `approve`: `allowed`, `ask` or
  `denied`. Only an allowed or ask token may tap a reply action (403
  `approve_denied` otherwise),
  and never on a notification sent by its own token tree: the token itself,
  a browser or phone made from it, or the token it hangs under (403
  `own_request`). The setup token is allowed; every token made with `POST
  /api/v1/tokens` starts denied, whoever makes it (`approve` other than
  `denied` is 400), and is raised like any device, with the PUT below or
  an approve-request. Only `clrd token create --approve allowed` (or `ask`) on the
  server, the way back when locked out, makes one that is not denied. A
  browser or phone signed in or linked with a token starts with that token's
  setting. `PUT /api/v1/devices/{id}/approve` with `{"approve":...}` changes
  it: the caller must be allowed, not limited to some topics, and see the
  device in its device list; it may lower any of them, itself included, but
  never raise itself or a token it hangs under (denied, ask, allowed is the
  order). An ask device's tap does not count yet: the reply route answers
  202 with a confirm prompt, a new notification on the same topic titled
  "Confirm <device>'s answer: <label>" with reply actions `confirm` and
  `reject`. A repeat tap returns the same prompt. Only an allowed device
  outside the question's token tree may answer it (the asker cannot, being
  ask, but a phone linked from the same token can); `confirm`
  applies the tap to the question (state by the ask device), 409 if the
  question was answered, cleared or changed since. `reject` only clears the
  prompt. Taps on a prompt are not sent to the topic's action hooks. A
  device, an agent's token say, asks to raise its own setting with `POST
  /api/v1/devices/approve-request`: a prompt "Confirm <device> may approve:
  <setting>" on the topic it names. `confirm` applies it only for a device
  that could make that change itself with the PUT above, 409 if the
  device's setting changed since it asked or the confirmer cannot see it. On
  upgrade from a version
  without this setting, browsers and phones already linked are allowed and
  plain tokens are denied.

## 5. HTTP API

Native (JSON in, JSON out, errors as `{"error":"...","code":"..."}`).
Codes: `bad_request` 400, `unauthorized` 401, `forbidden` 403, `not_found`
404, `conflict` 409, `reset` 410, `gone` 410 (pairing, linking),
`rate_limited` 429 (with `Retry-After` in seconds), `busy` 429 (pairing,
linking), `internal` 500.

Notifications always carry every field except `requested_topic` and
`changed`: `""` for unset strings, `[]` for `tags` and `actions`, `null` for
`snooze_until`. List returns
`{"notifications":[...],"seq":<current>,"epoch":"<id>"}`, newest first; page with
`before=<last id>`; `limit` defaults to 100, max 500. Fetch the list, then open
the WebSocket with `since=<seq>` from that response. Setting the state a
notification already has returns it unchanged with no new event (two phones
dismissing at once is normal), with `"changed": false` so a client retrying an
action does not take the row's seq as its own change (#693). Any answer that
wrote an event says `"changed": true`, including a snooze that sets a new
`until`. The ntfy endpoints answer in ntfy's format, without this field. Reopening a dismissed row whose `external_id`
has since been reused returns 409.

Topic names: `[A-Za-z0-9_-]{1,64}`. Reserved (the server and mounted pages
own these paths): `api`, `app`, `assets`, `docs`, `healthz`, `login`,
`logout`, `mcp`, `setup`, `static`, `v1`.

| Method | Path | Does |
|---|---|---|
| POST | `/api/v1/notifications` | Publish. Body = notification fields above (topic required). Returns the notification. `attachment` takes `{"url":"https://...","name":"..."}` (external; `name` optional). To upload a file, send `multipart/form-data` instead: first a part named `notification` holding the JSON, then a part named `file`; its file name becomes `attachment.name`. Over the size cap is a 413. |
| GET | `/api/v1/files/{id}` | Download an uploaded attachment. Scope read on its topic. 404 once the file is gone. |
| GET | `/api/v1/notifications?topic=&state=open\|all\|<state>&q=&limit=&before=` | List. `open` = active+acknowledged+snoozed. |
| GET | `/api/v1/notifications/{id}` | One. |
| PATCH | `/api/v1/notifications/{id}` | Body `{"message":"..."}`: replace the message of an open notification in place (same id and state, an `updated` event). Scope publish on its topic; 409 once dismissed or resolved, or if it has reply buttons (a tap answers the text shown; link buttons are fine). |
| POST | `/api/v1/notifications/{id}/ack` | Acknowledge. |
| POST | `/api/v1/notifications/{id}/reply` | Body `{"reply":"yes"}`, plus `"text"` for a text action and optional `"seq"`, `"via"`: answer with one of its reply actions (section 2). 400 if not one of them, or the text is missing, not wanted or over 1000 characters, 409 if already answered or final, 403 `approve_denied` from a device not allowed to approve, 403 `own_request` from the token tree that sent it, 202 with a confirm prompt from an ask device (section 4). |
| POST | `/api/v1/notifications/{id}/snooze` | Body `{"for":"1h"}` or `{"until":"RFC3339"}`. |
| POST | `/api/v1/notifications/{id}/mark` | Body `{"mark":"seen"\|"actioned","note"?,"link"?}`: set your mark (section 2). Scope read on its topic. Changes no state. |
| DELETE | `/api/v1/notifications/{id}/mark` | Remove your own mark. |
| POST | `/api/v1/notifications/{id}/dismiss` | User clears it. |
| POST | `/api/v1/notifications/{id}/resolve` | Sender clears it. |
| POST | `/api/v1/notifications/{id}/reopen` | Undo a dismiss (powers the Undo toast, 30 s window not enforced by server). Optional `{"until": RFC3339}` in the future lands it snoozed until then in one change, for undoing a Clear on a snoozed row; a past `until` is a plain reopen. |
| POST | `/api/v1/notifications/clear` | Bulk dismiss, body `{"topic":"infra"}` or `{"ids":[...]}`. |

Every state change above (ack, reply, snooze, dismiss, resolve, reopen, clear,
and `POST /{topic}/{external_id}/resolve|ack`) takes an optional `"via"` in its
body: how the change was made, so the server log can answer "I never cleared
that". The body is optional except for snooze, reply and clear; a missing,
empty or malformed one never blocks the action. `via` is 1 to 32 characters of
`a-z`, `0-9` and `_`, starting with a letter; anything else is recorded as
`api`. The server records it on the `state` event (`"via"` next to `"seq"`) and
logs one `state` line per change with id, topic, state, device name, device id
and via. Values clients send today:

| via | Made by |
|---|---|
| `app` | The Android app's inbox (a tap or swipe in the app) |
| `tray_button` | A button on an Android tray notification (Ack, Snooze, Clear, a reply) |
| `tray_swipe` | A lone swipe of an Android tray notification, only with "Swipe clears everywhere" on (section 8) |
| `web` | The web inbox, one notification |
| `web_bulk` | The web inbox's Clear all or a stack's Clear |
| `app_bulk` | The Android app's Clear on a stack |
| `desktop_button` | The Clear button on a desktop system notification (section 8.1) |
| `desktop_close` | Closing a desktop system notification, only with "closing clears everywhere" on (section 8.1) |
| `web_form` | The web inbox's buttons with scripts off |
| `mcp` | An agent through the MCP endpoint |
| `ntfy` | An ntfy-compatible clear or delete |
| `api` | Anything else (scripts, older clients) |

The server's own changes use `snooze_end`, `expired`, `checkin` (a late
check-in resolved by the next message), `quiet` (a sender's quiet alarm
resolved by its next publish or a rule change, section 5.1), `publish` (a publish with `status
resolved`), `ntfy_import` and `cache-expiry` (the cache retention prune).

| Method | Path | Does |
|---|---|---|
| GET | `/api/v1/counts` | Exact counts of what the token can read: `{"active":3,"acknowledged":1,"snoozed":2,"dismissed":640,"resolved":12,"inbox":4,"cleared":652}`. `inbox` is active plus acknowledged, `cleared` is dismissed plus resolved. |
| GET | `/api/v1/topics` | Topics with open counts and icon: `{"topics":[{"topic":"infra","open":2,"icon":"","approvals":false}]}`. `approvals` marks a topic that has carried a message waiting for an answer (section 2.3). |
| GET/PUT | `/api/v1/subscriptions` | Which topics reach this owner's devices (section 2.3). GET lists every readable topic with `subscribed`, `open` and `last_at`, plus `filtered` and `new_topics`; PUT takes `{"new_topics"?,"topics"?:{"chat":false}}`. Scope read; `?for=` is full admin only. |
| GET | `/api/v1/format-check?topic=&by=` | Format check (#308), advice only. Plain string rules over the newest 500 notifications the token can read (in `topic` when given): `markdown_as_text` (format `text` with a `**bold**` pair, `](http`, a code fence or a `# ` heading), `unknown_shortcode` (a `:name:` with a letter that is not in the emoji table, outside code; `12:30:45` never counts), `no_title`, `long_body` (over 2000 characters), `json_dump` (the whole message is a valid JSON object or array). `by=sender` (#320) groups by topic and sending token (the top of its token tree, as kept for #187), adding `sender_id` (`"unknown"` for messages with no known sending token, #391) and `sender` (the token name) to each group and naming the token in the prompt; only a full admin, or the token the sender belongs to (as in Linked devices, #245), gets `sender` and the prompt, anyone else gets both blank (#406); the default `by=topic` leaves both out. Returns `{"checked":N,"problems":N,"topics":[{"topic","sender_id"?,"sender"?,"checked","problems","findings":[{"rule","count","example_id","example","fix"}],"prompt"}],"off":["topic"],"dismissed":[{"topic","rule","sender_id"}]}`, only groups with findings, in topic then sender_id order; topics in `off` are not checked or counted, and a dismissed rule does not count for that topic and sender (`sender_id` "" means every sender); `problems` counts messages with at least one finding; `example` is the newest hit's title and first 200 characters, with anything that looks like a secret (a value after `password=`, `token:`, `Bearer` and the like, the password in `user:pass@host`, a run of 20+ letters and digits) shown as `[hidden]`; `prompt` is text for the user's agent to fix the sender and holds `$CLRD_TOKEN`, never a token. |
| POST | `/api/v1/format-check/dismiss` | Body `{"topic","rule","sender_id"?,"dismissed"?}` (#320): stop `rule` counting in `topic` for that sending token (`"unknown"` for the group with no known sender), or every sender without `sender_id`. `"dismissed":false` undoes it. Unknown rule is a 400. Scope publish on the topic. Nothing is dismissed until set. |
| PUT | `/api/v1/topics/{topic}` | Body `{"icon":"https://..."}` sets the topic's icon; `""` removes it. `{"checkin":"25h"}` makes the topic expect a message at least that often (1m to 8784h, `""` for off): when one is late the server publishes an urgent notification with external_id `clrd-checkin`, and the next message to the topic resolves it. `{"format_check":false}` leaves the topic out of the format check, `true` (the default) puts it back (#320). `{"device_actions":true}` lets senders add `http` and `broadcast` actions (section 2, #401), `false` (the default) refuses them; this key needs scope admin on the topic, and a body with it and a token without admin changes nothing. A body with none of these keys is a 400. Scope publish on the topic. |
| GET | `/api/v1/topics/{topic}/templates` | The topic's saved webhook templates (secret hidden, `has_secret` shown) and the built-in names. Scope publish on the topic. |
| PUT | `/api/v1/topics/{topic}/templates/{name}` | Save a webhook template (section 5). Replaces the whole template; under a built-in name it overrides only the fields given. Scope publish on the topic. |
| DELETE | `/api/v1/topics/{topic}/templates/{name}` | Remove a saved template. Scope publish on the topic. |
| POST | `/api/v1/topics/{topic}/templates/{name}/preview` | Dry run: `{payload, headers?, template?}` returns `{notifications}`, what a post of payload would publish, through the same render path, without storing or sending anything and without the secret check. `template` previews an unsaved one. Scope publish on the topic. |
| GET | `/api/v1/topics/{topic}/hooks` | The topic's outgoing webhooks (secret hidden, `has_secret` shown). Admin. |
| PUT/DELETE | `/api/v1/topics/{topic}/hooks/{name}` | Save (body `{"url","events","secret","body"}`, replaces and turns it back on; `body` is an optional template, #239; `"kind": "discord"` posts and edits Discord messages, #166; `"kind": "slack"` posts and edits Slack messages and adds reactions, #166; `"kind": "github"` opens and closes GitHub issues, #168) or remove an outgoing webhook. Admin. |
| GET | `/api/v1/topics/{topic}/hooks/{name}/deliveries` | The last 100 deliveries, newest first. Admin. |
| POST | `/api/v1/topics/{topic}/hooks/preview` | Dry run a body template (`{"body","event"?}`, a sample event without one): returns `{"body"}` as a delivery would send it, sends nothing. Admin. |
| GET/PUT | `/api/v1/hooks/allow` | Private networks and extra ports outgoing webhooks may reach: `{"networks":["192.168.1.0/24"],"ports":[8080]}`, both empty by default, plus `"locked"` (true under `CLRD_HOOK_ALLOW=off`, when PUT is 403 `locked`). PUT needs both fields (`[]` for none), at most 64 of each; a bare address is a single-address network. Full admin. |
| GET | `/api/v1/silences` | Scope read. Active silences on topics this token may read, plus those with no topic: `{"silences":[{"id","topic","tag","until","comment","created_by","created_at"}]}`. |
| POST | `/api/v1/silences` | Body `{"topic","tag","for":"8h"}` or `"until":"RFC3339"` instead of `for` (exactly one, at most 8784h ahead), optional `comment`. Needs `topic`, `tag` or both. Returns 201 and the silence. Scope publish on the topic; no topic (every topic) needs a token without a topic list. |
| DELETE | `/api/v1/silences/{id}` | End a silence early. Same scope as creating it. 204. |
| GET/PUT | `/api/v1/settings` | Display settings (section 2.1). PUT changes only the fields given. |
| GET | `/api/v1/favicon?host=` | A site's icon, or a fallback (section 2.1). |
| GET | `/api/v1/preview?url=&topic=` | A link's preview card; 403 while previews are off (section 2.1). |
| GET | `/api/v1/devices` | Devices (tokens) seen in the last 10 min, from the same set `?all=1` shows: `{devices:[{id,name,last_seen_at}]}`. With `?all=1`: every linked device, `{devices:[{id,name,detail,kind,via,via_id,linked_at,last_seen_at,this,can_unlink,sound,approve,clears_everywhere,can_approve,connected,caught_up,synced_seq}]}`; `detail` is the browser and system or what the app sent (section 4, absent when unknown); `kind` is `token`, `browser` or `phone`. `via` and `via_id` are the name and id of the token or device it hangs under (absent for a token), `approve` is its setting from "Who may approve" in section 4, `clears_everywhere` its "Clears everywhere" option (section 2, true by default). `can_approve` is true only on the caller's own row (`this`), when the caller may change who approves: `approve` is `allowed` and the token is on every topic. `connected` is true while the device has a stream open, `caught_up` once one of them has sent `ready`, and `synced_seq` is the last event seq that device's stream has passed, sent to it or not visible to it, leaving out another device's own clears (0 when not connected); compare it with the current seq from your own stream. The inbox draws one dot per device from this list, green when `connected` and `caught_up`, and counts the green ones as "In sync on N devices". The server cannot see whether the device applied it, only that it was sent. A full admin sees every token; anyone else sees the token they signed in with, the browsers signed in with it, the phones linked from those and the browsers those phones approved. Never a secret or hash. |
| GET | `/api/v1/devices/log` | The last 200 links, sign-ins and unlinks for the same devices, newest first: `{events:[{at,action,device,device_id,by}]}`. |
| GET | `/api/v1/sounds` | Valid notification sound names: `{sounds:[...],default:"default"}`. |
| PUT | `/api/v1/devices/{id}` | Rename a browser or phone, `{name:"Work laptop"}` (403 for a typed-in token or agent, 400 when empty), set its sound, `{sound:"chime"}`, a name from `/api/v1/sounds`, or both. Same device list and 404 as DELETE; a token limited to some topics gets 403; an unknown name gets 400 listing the valid ones. Returns the device. The device list (`?all=1`) shows each device's `sound`, and `GET /api/v1/settings` and the settings stream message show the asking device's own `sound` (`PUT /api/v1/settings {sound}` sets it). |
| DELETE | `/api/v1/devices/{id}` | Unlink a browser or phone from that list: it is signed out at once and its WebSocket closes. Unlinking a phone also signs out the browsers it approved. A typed-in token gives 403 (revoke it with `/api/v1/tokens`); a device outside the list gives 404. Needs `read`, except that a device may always unlink itself, whatever its scopes (#609); `{id}` = `this` means the asking device, so it need not look up its own id. |
| POST | `/api/v1/devices/tidy` | Body `{"days":7,"dry_run":true}`, both optional (days 1 to 36500, default 7). Unlinks the browsers and phones in that list unused for `days`, as DELETE does, never the caller, a typed-in token or an agent, nor a device with a stream open, web push or a browser signed in under it. `dry_run` only counts. Optional `devices` (ids, as a dry run returned them) limits the unlink to those. Each row is checked again as it is unlinked, so one used since it was counted stays and is left out of the reply. Returns `{count, devices, dry_run}` (`devices` the ids). Needs `read` (#765, #773). |
| POST | `/api/v1/devices/approve-request` | Body `{"approve":"allowed"}` or `ask`, `topic` optional (default `agents`, needs publish there): the calling device asks to raise its own setting (section 4). 202 with a confirm prompt on that topic; a repeat returns the same open prompt; 400 unless it is a raise. |
| PUT | `/api/v1/devices/{id}/approve` | Body `{"approve":"allowed"}`, `ask` or `denied`: who may answer approval requests (section 4). Same device list and 404 as DELETE; 403 `approve_denied` when the caller is not allowed, 403 `forbidden` to raise itself or a token it hangs under or from a token limited to some topics, 400 for any other value. Returns the device; the device list shows each device's `approve`. |
| PUT | `/api/v1/devices/{id}/clears` | Body `{"everywhere":false}` or `true`: whether that device's clears reach every device (section 2, "Clears everywhere"). Same rules and errors as `/approve`: 403 `approve_denied` when the caller is not allowed, 403 `forbidden` from a token limited to some topics, 404 outside the device list, 400 without `everywhere`. Returns the device. |
| GET/POST/DELETE | `/api/v1/tokens` | Manage tokens (admin with no topic limit). POST takes `{name, scopes, topics}` or `{name, scopes, grants:[{topic, scopes}]}` (section 4) and returns the token with its secret once, as `token`. It starts denied for approval requests (section 4); `approve` other than `denied` is 400. DELETE `/api/v1/tokens/{id}` revokes one; a token from `CLRD_CONFIG` gets 409, since the next start would make it again (remove it from that file instead). |
| PUT | `/api/v1/tokens/{id}` | Rename a token, `{name}`, cleaned as a device name is (section 4). Same admin as above. 400 when empty, 404 for an unknown id, 409 for a token from `CLRD_CONFIG` (named in that file). Returns `{id, name}`. |
| POST | `/api/v1/tokens/{id}/rotate` | A new secret for the same token row, returned once as `token` (`{id, token}`). Same admin as above. The id stays, so sender rules, history, grants, its user and stats stay. The old secret stops at once, its open streams close, and every browser and phone signed in with it is revoked, as DELETE revokes them: a rotation usually means a leak. A sign-in, pairing or phone link it approved but not yet claimed fails with 401. Typed-in tokens only: a browser or phone, a Gotify or Pushover key, an ntfy user's token and a `CLRD_CONFIG` token get 409. 404 for an unknown id. |
| GET | `/api/v1/stats` | Traffic numbers for notifications created in `[from, to)` (unix ms; `to` defaults to now, `from` to `days` back, default 7, at most 366): `total`, `answered`, count per `priorities`, count per UTC hour in `hours`, `week_hours` as a 7 by 24 grid indexed by weekday (Sunday = 0) and hour in the server's local time zone, and the top 50 `topics`, `senders` (top token id) and `cleared_by` (who cleared), each with `open`, `cleared` and the median and p90 time from publish to first clear in ms, plus `flappers` (an external id that fired more than once) and `delivery` (failed outgoing webhook deliveries and Web Push sends per topic, worst first, #661; a deleted webhook's failures still count, #794). Each topic and sender has a `previous` count for the equally long period ending at `from`. Set `group=topic`, `sender` or `priority` to add a `by` count map to each UTC hour. Topic and sender breakdowns keep the top five for the whole range and fold the rest into `other`, which also includes any literal `other` topic. Priority keys are `1` through `5`. Unknown groups return 400. `cleared` counts cleared notifications and `self_cleared` counts those cleared by their sending token. `quiet` lists current quiet alarms (`sender_id`, `name`, `topic`) and `connected` counts live client streams (WebSocket, SSE and ntfy). Publish-to-first-device latency is not recorded. `?format=csv` returns the same numbers as CSV (`text/csv`); a text cell starting with `=`, `+`, `-`, `@`, `'` or tab gets one leading `'`, so a value that already starts with `'` stays distinct; a CR in a text cell is written as `\r` and a backslash as `\\`, so a CR LF stays apart from a bare LF in readers that fold them (#804). Read from stored rows, so cleared messages past the keep-cleared setting are not counted. Admin with no topic limit (#120). The web Stats page (`/app/stats`, `days` 1, 7, 30 or 90) shows the same numbers and links to the CSV export. Its "by someone else" count is every other clear: a person, a paired device or the server. |
| GET | `/metrics` | Optional Prometheus text exposition metrics, off by default (404 until `CLRD_METRICS=on`, #661). Admin with no topic limit. |
| GET | `/api/v1/senders` | Every sender, most recently heard first (section 5.1). Admin with no topic limit. |
| GET | `/api/v1/senders/{id}` | One sender with `recent`, its last notifications still stored (section 5.1). 404 for an unknown id. |
| PUT | `/api/v1/senders/{id}` | Change a sender's `rules` (section 5.1). Returns the Sender. |
| GET | `/api/v1/senders/{id}/changes` | `{changes:[SenderChange]}`, newest first (section 5.1). |
| POST | `/api/v1/senders/{id}/changes/{seq}/undo` | Put the rules back as they were before that change (section 5.1). Returns the Sender. |
| GET | `/api/v1/version` | No token. Version, build commit and date, and the recent history (section 5.2). |
| GET | `/api/v1/server` | Admin. The section 7 settings in effect, defaults filled in: `{"env":{"CLRD_LISTEN":":8080",...}}`, each written the way its variable takes it (`CLRD_BASE_URL` empty when unset, a password in it shown as `xxxxx`; `0` for a rate limit that is off). Read only; no secrets. |
| GET | `/healthz` | `{"ok":true,"seq":N}`; 503 with `"ok":false,"failing":["snooze wake"]` while a background job (snooze wake, prune) has failed 3 times in a row. |
| POST/DELETE | `/api/v1/session` | Web sign-in and sign-out (section 4). |
| GET | `/openapi.json` | OpenAPI 3.1 spec of all of the above. |

By external id (what scripts and monitors use):

| Method | Path | Does |
|---|---|---|
| POST | `/{topic}/{external_id}/resolve` | Resolve the open row with that key. 404 if none. |
| POST | `/{topic}/{external_id}/ack` | Same, ack. |

### 5.1 Senders (#119)

A sender is everything that publishes under one token tree: the token typed
in, and every browser or phone signed in under it (the same top token as
`sender_id` in section 2.1). It appears on its first stored notification,
with nothing to set up, so after a move from ntfy or Gotify the list already
shows every service still talking. A person's own web app or phone counts
when it publishes, as kind `person`. clrd's own alerts (missed check-ins,
webhooks turned off) are no sender. A publish that stores nothing (`live`, or
`status: resolved`) is not counted. Anonymous ntfy publishers share one
token, so each client address is its own sender, at most 500 of them; the one
heard from longest ago goes first.

`GET /api/v1/senders` (admin with no topic limit; the list names every token
and its address) returns `{senders:[Sender]}`, last heard first:

| Field | Meaning |
|---|---|
| `id` | The sender id: its top token id, or `anonymous-` and 12 hex digits for an anonymous address |
| `token_id` | The top token id; `""` for anonymous. Rename (`PUT /api/v1/tokens/{id}`), rotate (`POST /api/v1/tokens/{id}/rotate`) and revoke (`DELETE /api/v1/tokens/{id}`) go through `/api/v1/tokens` with it; a rotate keeps the sender, revoking it removes the sender |
| `name` | The token's name, `anonymous` for anonymous |
| `kind` | `token`, `person` (a token owned by a user), `ntfy_user`, `gotify_app`, `pushover_app` or `anonymous` |
| `template` | The webhook template (`?template=`) it last posted through, `""` if none ever |
| `user_agent`, `address` | From its last publish: hints, never checked. `address` is the client IP (behind a trusted proxy, the forwarded one; IPv6 as its /64) |
| `first_seen`, `last_seen` | RFC 3339 |
| `count` | Stored publishes, an update by `external_id` included; it outlives the rows themselves |
| `topics` | `{topic: count}` of the same publishes |
| `config` | `true` for a token from `CLRD_CONFIG`, absent otherwise: its rename, rotate and revoke are 409 (change it in that file) |

`GET /api/v1/senders/{id}` returns one Sender plus `recent`: up to its last
10 notifications, newest first, that are still stored (full notifications,
section 2). A row updated again by `external_id` shows once. 404 for an unknown id.

Every Sender also carries `rules`, `{}` until someone sets one:

| Rule | Effect on its publishes |
|---|---|
| `paused` | Accepted with the usual 2xx and dropped: nothing stored, no file kept, still counted in `count` and `last_seen`. Pause covers publishing only; acks and other changes it makes still apply |
| `muted` | Stored and synced, but `silenced` (section 2), so no sound or tray alert |
| `priority_min`, `priority_max` | Priority clamped into the range, 1 to 5; `0` means no limit. Min above max is 400 |
| `routes` | `{topic: [topics]}`: a publish to the key topic lands in each listed topic instead. Key `*` matches any topic with no route of its own. An empty list drops the publish like `paused` |
| `expect`, `alert_topic` | Nothing on its publishes: an urgent alarm when the sender goes quiet that long, see the silence alarm below |
| `icon` | An `http(s)` image URL (the rule for `icon` in section 2, else 400) that a publish giving no `icon` gets, before the topic's icon; `""` for none. A routed publish takes it to every target. The Senders list and the sender page show it |

A routed notification keeps the topic it was sent to as `requested_topic`
(section 2), and the sender is counted once, under that topic. Each target
gets its own copy, and its own copy of any file. The publisher's answer is the
first target's notification. A route is applied by the server on the
sender's behalf, so its targets need no grant on the sender's token. The
sender's rate limit is spent once; each target topic spends its own topic
limit. An error on a later target (a full topic included) is logged and
skipped. The routes apply to `status:
resolved` too, so a resolve follows the alert to its targets. One gap: change
a route while an alert is open and its resolve goes to the new targets and
finds nothing in the old one.

`PUT /api/v1/senders/{id}` (admin with no topic limit) takes any of the rule
fields and changes only those; `routes`, when present, replaces the whole map.
A bad topic is 400, an unknown id 404. An optional `base_seq` is the `seq`
of the newest change the caller saw (0 for none); if a newer change exists
the request is 409 and changes nothing. Rules can be set on a token that has
not published yet (its id from `/api/v1/tokens`); it then shows with `count`
0 and a zero `first_seen`. Each change is logged: `GET
/api/v1/senders/{id}/changes` returns `{changes:[{seq, before, after, by,
by_id, at}]}`, newest first, with `before` and `after` the full rules. `POST
/api/v1/senders/{id}/changes/{seq}/undo` sets the rules back to that change's
`before`, and is logged as a change itself. Revoking the token removes the
sender, its rules and its log.

The web inbox shows the list at `/app/senders` and one sender at
`/app/senders/{id}`, where a form sets every rule above (an `expect`, `alert_topic` or `icon` the server
refuses comes back with its message by that field) and each change
has an Undo button, for the same admins only; anyone
else gets 403. A sender with a `token_id` also has a Token section: a name
field (rename), Rotate secret (a typed-in token or a person's, after a
confirm; the new secret is shown once on the page that answers, never in a
URL) and Revoke (after a confirm, then back to the list). A `config`
sender shows Revoke only, an anonymous sender none of these. The web page refuses to rotate or revoke the
token the browser itself is signed in under, which would sign it out with
the new secret unseen; the API and MCP allow it. The form sends `base_seq`, so saving a page drawn before a
newer change is refused rather than undoing that change.
`/app/senders/add` (Add a service, only for an admin with no topic limit;
anyone else gets 403) takes a service (a built-in webhook template, curl,
ntfy, Gotify or Pushover), a name and a topic, makes a `publish` token for
that one topic, and shows the secret and a ready-to-paste URL or command once,
with a link to the service's guide. The page then waits for the token's first
message and links its sender page once it arrives (live with JavaScript, a
Check again link without). The Android app
lists them in Settings with their icons, change log and Undo, for the same
admins. It edits the same rules, including the silence alarm, and sends one
field per change. Set the icon from the web, REST or MCP. For anyone else
the section stays hidden.

The silence alarm: `expect` (a duration, 1m to 8784h, like `"25h"`; `""`
for off) makes the server watch the sender. When it has sent nothing,
stored or dropped by its rules, for that long, the server publishes an urgent
notification (priority 5, title "<name> went quiet") with external_id
`clrd-quiet-{id}`, in `alert_topic` if set, else in the first topic its last
publish routes to, else in that topic itself. A sender that has never
published and has no `alert_topic` raises nothing. A live publish (`Cache:
no`) and a `status: resolved` publish are not counted (as for `count`), so
they do not keep the alarm away. The sender shows the alarm's topic as
`alarm` until its next publish resolves it (reason `quiet`); a change to
`expect` or `alert_topic` resolves it too and starts the wait over, and
revoking the sender's token resolves it for good. One
alarm at a time: clearing it by hand raises no new one until the sender
publishes or the rule changes, and the next can only come a full `expect`
after that. Publishers cannot create a notification with an external_id
starting `clrd-quiet-` (400). The alarm counts as a message in its topic, so
it resets that topic's own check-in.

### 5.2 Version and history (#580)

One project version for the server, web, Android and desktop apps: the
newest dated heading in `CHANGELOG.md` (`## 0.1.0 - 2026-11-23`). It is
`0.0.0` until the first version is cut. Cut one with
`go run ./server/cmd/cutversion 0.1.0` (date defaults to today, UTC): it
collects the pending files in `changelog.d/` into a new dated section,
removes the files it consumed, and leaves `## Unreleased` in place. With
`-summary-file F` (before the version), the short hand-written text in `F`
(`###` sections with `-` items, like Unreleased) becomes the version's
section, and the full entries go to `CHANGELOG-archive.md` under the same
heading. A version is cut with each deploy batch, so Unreleased stays small.

The binary shows the pending entries under Unreleased and carries recent
versions from `CHANGELOG.md`. A test fails when the combined changelog is
over 96,000 bytes. When a cut leaves it over 90,000 bytes, the script moves the oldest
versions into `CHANGELOG-archive.md` at the repo root (not embedded), keeping
the newest 10 when they fit under 96,000 and always the newest one.

`GET /api/v1/version` needs no token, on public and private servers alike,
and answers only from the binary (nothing about the server, its config or
its data):

```json
{"version":"0.1.0","commit":"<git commit, 40 hex>","date":"2026-11-23T10:00:00Z",
 "history":[{"version":"Unreleased","date":"","changes":[{"section":"Added","items":["Server: ..."]}]},
            {"version":"0.1.0","date":"2026-11-23","changes":[...]}],
 "full_history":"https://github.com/thejdubb02/clrd/blob/main/CHANGELOG.md"}
```

- `commit` and `date` (RFC 3339, UTC) are when the binary was built, set at
  build time (`docs/self-host.md`); a `go build` inside a git checkout fills
  them from the commit. A build with neither has `""` for both.
- `history` is newest first. `Unreleased` comes first only when it has
  entries (what this binary has beyond the last version); a version's `date`
  is `YYYY-MM-DD`. Each version lists its sections (`Added`, `Changed`,
  `Fixed`, `Removed`, `Security`) in the order they first appear, a section
  repeated under one version merged into the first. Every field is always
  present; arrays are `[]`, never `null`.
- Each item is one line of inline Markdown (code spans, links), wrapped lines
  joined with a space.
- Views of the history (the web, apps, `/docs/changelog`) end with a link to
  `full_history`, where older versions are.
- After an update to a new version, the web inbox shows a small, dismissible
  What's new note on the first open (quiet on a browser's first visit ever),
  linking to Settings, About; each browser can turn the note off in Settings.
  The Android app does the same for its own version: after the app updates,
  a dismissible note at the top of the inbox opens What's new; quiet on the
  first open ever, and off with the switch in Settings, About.

`/docs/changelog` renders the combined changelog; a private server hides it with the
rest of `/docs`, the endpoint stays. MCP: `version()` returns the same JSON,
and `initialize` reports `version` as `serverInfo.version`.

### ntfy compatibility (must pass ntfy's own client behaviour)

- `PUT|POST /{topic}` with the body as the message. Headers `X-Title`/`Title`/`t`,
  `X-Priority`/`Priority`/`p` (1-5 or min/low/default/high/urgent),
  `X-Tags`/`Tags`, `X-Click`, `X-Actions` (JSON or the short form, where
  a quoted value keeps its spaces and `\"` or `\'` is the quote itself;
  `view`, `copy`, `http` and `broadcast`, the last two only on a topic with
  `device_actions` on, else dropped. For copy, use `copy, Label, value`
  or set `value` in JSON), `X-Delay`/`Delay`/`X-At`/`At`/`X-In`/`In` (a unix
  time, a duration such as `30m`, `1d` or `2 days`, an RFC 3339 time, or a
  time of day such as `10am`, `8:30pm`, `noon`, `tomorrow, 3pm` or
  `Tuesday 7am`; a bare time already past today means tomorrow; at most 3
  days ahead; anything else is a 400), `X-Timezone`/`Timezone` (or the
  `timezone` JSON field: the IANA zone a time of day is read in, else the
  server's zone; an unknown one is a 400 only when there is a delay),
  `X-Icon` (sets `icon`; a
  bad URL is dropped, not refused), `X-Attach` (external attachment; a bad URL
  is dropped), `X-Filename`/`Filename`/`File`/`f` (with it, the body is the
  file and the message comes from `X-Message`/`Message`/`m`, default "You
  received a file: <name>"), `X-Markdown`/`Markdown`/`md` (`no`/`false`/`0` publishes
  plain text, section 2.1), `X-Email` (ignored, documented),
  plus clrd's own `X-Id` = `external_id`. Any of these headers may carry
  RFC 2047 encoded words (`=?UTF-8?B?...?=`, `=?UTF-8?Q?...?=`), decoded as
  ntfy does; a value that cannot be decoded is kept as sent. Query values
  are taken as they are. `X-Template`/`Template`/`tpl` set
  to yes/true/1 makes the message, title and priority Go templates over the
  JSON body. Other selectors on a JSON POST or PUT can load `name.yml`
  from `CLRD_NTFY_TEMPLATE_DIR` (unset disables disk lookup). Existing saved
  or built-in webhooks selected by `?template=` take precedence. Disk
  templates need publish access to the topic, use the normal ntfy publish
  response and share the inline renderer limits. See
  [supported file syntax](migrate-from-ntfy.md#named-templates).
  By default, a body over 4096 bytes without a file name is a 413.
  With `CLRD_NTFY_IMPLICIT_UPLOADS=on`, ordinary topic publishes with a body
  over 4096 bytes or invalid UTF-8 become uploaded files named `attachment`.
  The same file limits, access checks and safe download types apply. Templates
  and external attachments keep their message handling. ntfy output
  carries `icon` and `attachment` (`name`, `url`, `type`, `size`, with `url`
  made absolute from `CLRD_BASE_URL`). Unlike ntfy, clrd's file URLs need a
  token, so a third-party ntfy app that fetches files without one gets 401.
- `POST /` with ntfy JSON body (`topic`, `message`, `title`, `priority`, `tags`,
  `icon`, `attach`, `filename`, ...). Headers and query parameters fill
  omitted options, including actions, delay, Markdown, cache, and the message
  ID. Headers win over query parameters. Non-empty JSON fields win over both,
  as do explicit JSON arrays and the Markdown boolean. The topic comes from
  JSON, and the filename describes an external attachment. For a raw publish,
  `Content-Type: text/markdown` selects Markdown even with `Markdown: no`.
- `GET /{topic}/json`, `/{topic}/sse`, `/{topic}/ws`, `/{topic}/raw` with `since=`
  (`all`, unix time, duration, message id) and `poll=1`, and ntfy's filters
  `id`, `message`, `title`, `priority` (any of a list) and `tags` (all of a
  list). `scheduled=1` (or `sched=1`) also lists messages sent with a delay
  that have not fired yet, as a `message` at their delivery time, as ntfy
  does; a notification snoozed until later counts too, since a delay is a
  snooze. Output in ntfy's message format so the ntfy Android app and ntfy CLI
  work unchanged. Pages on other web sites are refused on the ntfy and Gotify
  routes (requests, preflight and WebSocket) unless an admin allows their
  origin with `PUT /api/v1/ntfy/origins` (or `*`, ntfy's own behaviour);
  requests with no Origin (phones, scripts) and clrd's own pages always pass
  (#374).
- Webhook-friendly: `GET /{topic}/publish?message=...` and `/{topic}/trigger`.
- Alertmanager/Grafana/Gatus: a payload with `status: resolved` and a stable
  key (alert fingerprint / `X-Id`) resolves instead of creating. Mapping in `docs/integrations/`.
- Webhooks in: `POST /{topic}?template=<name>` maps a service's JSON to
  notifications with a Go text/template per field (title, message, priority,
  tags, click, id, resolve; `each` splits a list). Built-ins: alertmanager,
  grafana, github, sentry, stripe, jira, healthchecks, proxmox, zabbix,
  netdata, jellyfin, arr. A topic owner saves others, or a built-in's secret,
  under `/api/v1/topics/{topic}/templates/{name}`. With a secret saved, GitHub,
  Jira, Sentry and Stripe signatures are checked on the raw body; other
  services send it in `X-Clrd-Secret` or `?secret=`. A failed check is 401.
  `X-Markdown` / `?markdown=` work as on any ntfy publish (section 2.1).
  Docs: `docs/integrations/custom-webhooks.md`.
- Webhooks out: a topic's outgoing webhook POSTs the sync stream event
  (section 3), or its `body` template rendered against that event (the
  incoming template functions plus `json`; must render valid JSON, 16 KB at
  most, or the try fails unsent), on dismissed, acknowledged, resolved, snoozed and `action` (a
  reply tapped), with `X-Clrd-Event`, `X-Clrd-Delivery` and, with a secret,
  `X-Clrd-Signature: sha256=<hex HMAC>`. Same address guard as the favicon
  fetcher, on a connection pool of its own, unless an admin lists networks
  and ports with `PUT /api/v1/hooks/allow` (#237): a dial then passes when
  the address is public or in a listed network and the port is 80, 443 or
  listed. `CLRD_HOOK_ALLOW=off` (section 7) locks the list empty: saved
  entries are ignored at send time, GET adds `"locked": true` and PUT is 403
  `locked`. Non-2xx is tried again from 30 s, doubling, 8 tries in all; 24 h of failures turns
  it off with a notification on the topic. Each try uses the hook's URL,
  secret and body saved at that moment. A hook with `"kind": "discord"`
  (#166, #673; no body or secret) instead posts each new active notification
  (and each scheduled notification when it wakes) to
  its Discord webhook URL with `?wait=true`, keeps the message id, and on each
  notification update or subscribed state change PATCHes that message: title
  struck through when dismissed or resolved, plus a status line such as
  "Cleared by pixel-9". A
  discord hook sends its deliveries one at a time in order; an edit of a
  message that was never posted, or was deleted (404), is done, not retried.
  A discord hook's URL is saved without `wait` (query in key order), and a
  save that changes only `wait` or the query order keeps its message ids; any other URL or kind change clears them
  (#816). A post whose reply is lost has no id, so a retry or a later wake
  posts again (a webhook cannot list messages to check first).
  A hook with `"kind": "slack"` (#166) follows the Discord post and edit events.
  Its URL is `https://slack.com/api/chat.postMessage?channel=CHANNEL`, its
  secret is a bot token, and it takes no body template. It also reacts with
  eyes on acknowledgment and a check mark on dismissal or resolution.
  A hook with `"kind": "github"` (#168; url
  `https://api.github.com/repos/OWNER/REPO`, secret a token sent as
  `Authorization: Bearer`, no body) opens an issue per new, unscheduled
  notification, except from an anonymous sender or for a question (a confirm
  prompt or a reply button), at most 10 an hour per hook (pending deliveries
  count at any age). The issue body starts with a hidden mark
  `<!-- clrd TOPIC/HOOK/ID -->` (query-escaped); before opening, clrd lists
  the repo's newest 50 issues (GitHub counts pull requests among them) and
  adopts one with that mark, so a lost reply does not open a second unless
  more than 50 issues or pull requests were opened in the repo before the
  retry. The issue title is prefixed with `[clrd <topic>] `,
  and issues carry the `clrd` and `priority:<min|low|default|high|urgent>`
  labels. When `CLRD_BASE_URL` is set, the body links back to the notification
  at `/app/n/{id}`, which finds it in any state until it is pruned. The body puts the sender's text in a fenced block it
  cannot close. Setting `github_hooks_paused: true` in `PUT /api/v1/settings`
  (full admin) pauses every GitHub issue hook server-wide without deleting it
  (#680). Each subscribed state
  change comments with the status line; dismissed and resolved first close
  the issue. A deleted issue (410) is done; any other error is retried.
  Delivery errors never carry the URL. Docs:
  `docs/integrations/webhooks-out.md`.

Full ntfy parity checklist lives in issue #2; tick it off there.

### Gotify compatibility (publish only, #116)

- `POST /message` with the token in `?token=` or `X-Gotify-Key` is Gotify's
  publish. The token must be able to name one topic (`topics` with one entry,
  or exactly one literal grant with `publish`); anything else is a 400. The
  native scope checks apply unchanged, through `Publish`. A wrong token is a
  401 and never falls through to the ntfy or anonymous path.
- Body: JSON, form or multipart with `message` (required), `title` (empty =
  the token's name), `priority` and `extras`. Priority maps 0 to 1, 1-3 to 2,
  4-7 to 3, 8 and up to 4; never 5. Missing = clrd's default. Format is plain
  text unless `extras.client::display.contentType` is `text/markdown`;
  `client::notification.click.url` sets `click`, `bigImageUrl` an external
  attachment.
- Reply: `{id, appid, message, title, priority, date, extras}` with `id` the
  event seq. Errors: `{error, errorCode, errorDescription}`.
- Without a Gotify token, `POST /message` stays ntfy's publish to topic
  `message`. A Bearer counts as a Gotify token when it is not a clrd or ntfy
  token (#371).
- Gotify apps (#370): `POST /api/v1/gotify/apps` `{name, topic, priority?}`
  (full admin; MCP `create_gotify_app`) makes a publish-only token for that
  topic whose secret is `A` plus 14 characters, Gotify's shape, which
  shoutrrr requires. Row in `gotify_apps` (id = appid, nullable default
  priority), deleted with the token. These secrets are looked up only on
  `POST /message`, never by the native or ntfy auth. A message without
  `priority` takes the app's default; the reply's `appid` is the app id, 0
  for a plain clrd token (#372).
- Pushover (#116): `POST /1/messages.json` (form, multipart or JSON) with
  `token` (a Pushover app key or a one-topic clrd token), a non-empty `user`
  (never checked) and `message`. Priority -2..2 is 1..5; 2 needs `retry` >=
  30 and `expire` <= 10800 and repeats every max(retry, 60) s, at most 50
  times, with a `receipt` in the reply (no receipt endpoint). `ttl` sets
  expires, `url` the click, `html=1` turns b, i, a, br into Markdown,
  `attachment` or `attachment_base64` an image (the body may be
  attachment-sized). Reply `{status:1, request}`; errors `{<param>:
  "invalid", errors, status:0, request}`. `POST /1/users/validate.json`
  answers status 1 for a valid token and user. Pushover apps: `POST
  /api/v1/pushover/apps` `{name, topic}` (full admin; MCP
  `create_pushover_app`) makes a publish-only token with id `pushover-...`
  and a 30-character [A-Za-z0-9] key, plus a random `user`. A raw key is
  looked up only among `pushover-` tokens and only on these two routes.
- Gotify import (#116): `clrd import gotify --db gotify.db
  [--clear-older-than D] [--dry-run]` makes each app a topic (slug of its
  name) and a `gotify_apps` key hashed in the form Gotify stores it. Gotify 3
  stores a `gtfy<ident>.<seed>` key by its ed25519 public key, so on lookup a
  `gtfy` key is turned into that form first; the stored form alone cannot
  send. The signed form `gtfy<ident>.<pubkey>.<unixtime>.<sig>` (Ed25519ph
  over `iat=<unixtime>`) is taken within 15 minutes of its time, as Gotify
  does. Messages keep their time, title, priority, Markdown, click and big
  image. Users, client tokens and plugins are counted, not copied. Only into
  a database with no events yet, once (meta `gotify_imported`).

## 6. MCP (`/mcp`, streamable HTTP, Bearer auth)

Tools: `send(topic, message, title?, format?, priority?, id?, actions?, icon?, attachment?, repeat?, repeat_times?, live?, devices?)`, `list(topic?, state?)`,
`topics()`, `counts(keep_cleared_days?, topic?)`, `format_check(topic?, by?)`, `format_dismiss(topic, rule, sender_id?, dismissed?)` (the format
check below, same JSON), `settings(...)` (no arguments reads
the settings, arguments change those fields), `get(id)`, `update(id, message)` (edit the message in place, for progress),
`ack(id)`, `resolve(id | topic+external_id)`, `snooze(id, for)`,
`mark(id, mark?, note?, link?)` (no `mark` removes yours),
`wait_for_reply(id, timeout_s)` (blocks until the notification is acked,
dismissed or an action is tapped; returns which, so an agent can ask a human
"yes?" and wait). `actions` takes `[{"label":"Yes","reply":"yes"}]`; when one
is tapped, `wait_for_reply` returns `answer` (the `reply` value) next to
`reply` (the state). One action may set `"text": true` (see Typed answers);
its answer also returns `text`, what the human typed. A clear the caller's own
token keeps on its device ("Clears everywhere" off, section 2) counts as
`dismissed`, before or during the wait, as `get` shows it; another device's
never does. `wait_for_reply` waits at most 600 s; on a timeout it
returns the notification only while the token can still read it. `devices()`,
`device_log()` and `unlink(id)` are the device list, its log and unlink,
`sound(id?, sound?)` lists the sound names or sets one device's sound,
and `request_approve(approve, topic?)` is `POST /api/v1/devices/approve-request`,
from section 5. Setup tools, each one REST call: path arguments go in the URL,
the rest is the body of a POST or PUT, and state changes add `via: "mcp"`: `dismiss(id)`, `reopen(id)`, `clear(topic | ids)`, `silences()`,
`silence(topic?, tag?, for | until, comment?)`, `end_silence(id)`,
`set_topic(topic, icon?, checkin?, format_check?, device_actions?)`, `templates(topic)`,
`set_template(topic, name, ...)`, `delete_template(topic, name)`,
`preview_template(topic, name, payload, headers?, template?)`, and, admin
only, `hooks(topic)`, `set_hook(topic, name, url, events?, secret?, body?)`,
`preview_hook(topic, body, event?)`, `hook_allow()`, `set_hook_allow(networks, ports)`, `delete_hook(topic, name)`, `hook_deliveries(topic, name)`, `tokens()`,
`create_token(name, scopes, topics? | grants?)`, `revoke_token(id)`,
`rename_token(id, name)`, `rotate_token(id)`,
`create_agent_invite(name, topics, expires_in?)` (`POST /api/v1/agents/invite`, section 4),
`subscriptions(topics?, new_topics?, for?)` (section 2.3), `server_env()` (`GET /api/v1/server`), `version()` (`GET /api/v1/version`, section 5.2, any token), `ntfy_ignored()`, `senders()` and
`sender(id)` (section 5.1). For the caller's read token, `push()`,
`set_push(endpoint, keys)`, and `delete_push()` map to GET, PUT, and DELETE
`/api/v1/push` respectively (section 9).

`POST /mcp/approve?topic=agents&timeout=590` takes a Claude Code or Codex
`PermissionRequest` hook body, sends an Allow / Deny notification, waits, and
returns `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"|"deny","message"?}}}`.
Only an Allow tap allows; a timeout denies and resolves the notification. A
failed send (502) or a request over 4000 characters (413) sends the agent back to its own prompt.

## 7. Server internals

- Go 1.24 (`GOTOOLCHAIN=local`; `modernc.org/sqlite` must stay at v1.40.1 or later, where a query cancelled as its first row arrives no longer leaks an open read transaction, see #207), stdlib `net/http` router, `modernc.org/sqlite` (pure Go, no cgo),
  WAL mode, one writer goroutine or a mutex around writes.
- Every setting is optional: the binary runs, as any user, with none set.
- Config by env: `CLRD_LISTEN` (`:8080`), `CLRD_DB` (`/data/clrd.db` if that
  file exists, else `clrd/clrd.db` under `$XDG_DATA_HOME` or
  `~/.local/share`; the server logs the path it uses),
  `CLRD_BASE_URL` (unset: links handed to a browser, such as the phone
  pairing QR, use the address that browser opened clrd on; a path in it, like `/clrd`, serves every route, page,
  asset and the WebSocket under that path, and scopes the cookies to it;
  requests without the path are still answered, for a proxy that strips it),
  `CLRD_RETENTION` (`720h` for final rows and events; the
  `keep_cleared_days` setting overrides it once set),
  `CLRD_NTFY_TEMPLATE_DIR` (directory of named ntfy templates, unset disables lookup),
  `CLRD_NTFY_CACHE_TOPICS` (unset; exact topics separated by commas where
  active and acknowledged notifications age out under `CLRD_RETENTION`),
  `CLRD_ATTACHMENTS` (folder for uploaded files, default `attachments` next to
  the database), `CLRD_ATTACHMENT_MAX_MB` (15, per file),
  `CLRD_ATTACHMENT_TOTAL_MB` (5000, all files of notifications that still
  exist, including open notifications that are still kept),
  `CLRD_PUBLIC_PAGES` (`on`; `off` for a private server: `/` redirects to
  `/app`, `/docs` and `/sitemap.xml` are 404, `robots.txt` disallows
  everything, and the web pages send `X-Robots-Tag: noindex, nofollow`).
  `CLRD_RATE_TOKEN` and `CLRD_RATE_TOPIC` (60 each, publishes a minute per
  token and per topic, with a burst of the same size; `0` turns one off).
  Every publish counts, on every route; over a cap is a 429 `rate_limited`.
  `CLRD_TRUSTED_PROXIES` (unset; comma-separated CIDRs or addresses): the
  only peers whose rightmost `X-Forwarded-For` hop is taken as the client
  address, and whose `X-Forwarded-Proto` sets the scheme of links built from
  the request when `CLRD_BASE_URL` is unset. Unset, any loopback or
  private-network peer is trusted.
  `CLRD_HOOK_ALLOW` (`on`; `off` locks the webhook allow list of section 5
  empty, whatever is saved, and no token can change it).
  `CLRD_METRICS` (`off`; `on` turns on `GET /metrics` in Prometheus text
  format for a full admin token, #661).
  `CLRD_CONFIG` (unset): a JSON file `{"users": [...], "tokens": [...]}`
  applied at every start (#155). A user is `name` and `grants` as on the
  API; a user named like an agent stops the start, since it would take the
  agent over. A token is `name`, `secret` (`clrd_` and 20 or more characters in all; `"$NAME"` reads
  the environment variable), `scopes`, `topics`, `grants`, `approve` and
  `user` (a user name in the file), and is matched by the hash of its
  secret, so one made on the API is taken over. A user made on the API is
  managed by the file while listed. Rows the file made and no longer lists
  are removed, with their sessions, and a changed token drops its sessions;
  rows made on the API are not. A dropped user made on the API, or one a
  token made on the API still points at, goes back to the API as it is, with
  a warning in the log, instead of stopping the start (#440). Unknown fields
  or a bad entry stop the start and change nothing.
  An upload may take up to 10 minutes; other request bodies get 30 s.
- Snooze wake-ups: a ticker every 15 s.
- `Dockerfile` (distroless or scratch) and `docker-compose.yml` example.
- Tests: `go test ./...` must cover publish, external_id upsert, each state
  change, replay from seq, and the two-client sync proof (issue #1).
- No telemetry. The only outbound calls are the favicon and link preview
  fetches of section 2.1, made when a client asks for them.

## 8. Android

- Kotlin, Jetpack Compose, minSdk 26, no Google Play Services (F-Droid).
- Foreground service holds the WebSocket; reconnect with backoff; stores last seq in Room/DataStore.
- Each open notification = one system notification, `tag` = notification id.
  Swiping a system notification away (one, a stack, or the shade's Clear all) only hides it on
  that phone and sends nothing: the system notification says there is something to see, and
  clearing happens in the app, the web or the API (#265). A new alert for a hidden item shows it
  again. The phone's own "Swipe clears everywhere" setting (off by default, #271) makes a lone
  swipe a clear, sent with `via` `tray_swipe`: Android sends the same delete for a swipe, a stack or
  bundle swipe and Clear all, so the app waits one second and clears only when no other row left the
  shade within one second of it. A stack swipe and Clear all on several rows still only hide. Buttons ACK, SNOOZE (for `snooze_default`), CLEAR are deliberate taps and do sync.
- Incoming `dismissed`/`resolved` event -> `NotificationManager.cancel(tag)`. That is the headline feature.
- Inbox screen per the brand board: filter pills by topic, a swipe that reveals round buttons (by default Clear and Snooze to the left, Ack to the right; the settings `swipe_start` and `swipe_end` in section 2.1 choose them, and Settings sets them on the server, read-only for a token that gets 403; a card skips a button it cannot take, like Ack on an acknowledged card or Resolve, which the app has no button for yet) and changes nothing until one is tapped (#161; a "Full swipe acts" setting, off by default, lets a swipe all the way across run the first button, the one nearest the edge), tabs Inbox, Snoozed, Cleared, Settings.
- Settings: server URL + token (QR scan of a pairing URL later), per-topic mute.
- `fastlane/metadata/android/` for F-Droid. Reproducible build, no proprietary deps.
- UnifiedPush distributor support is a later issue, not v0.

## 8.1 Desktop

The tray app for Windows and Linux (#162), in `desktop/`. A Rust core holds
the WebSocket and follows section 3 like any other client; the window loads
the server's own `/app`. It links through the device link flow in section 4
and is a linked device like any other, under the same Allowed, Ask and Denied
rules. Its token lives in the OS keychain and is sent only as an
`Authorization` header.

- The window is signed in without the token: the core asks
  `POST /api/v1/session` for a session and the app puts that cookie into the
  web view's cookie store itself, for the server's host only, HttpOnly, and
  with no expiry so the web view keeps it in memory and never writes it to
  disk. The token never reaches the page, a URL or a file.
- The server's page gets no native commands. Only the app's own bundled
  "This computer" page (link, unlink, status) may call the core. The window
  stays on the server's origin; any other address opens in the system
  browser.
- Unlinking ends the window session (`DELETE /api/v1/session`), revokes the
  device on the server and removes the token from the
  keychain.
- One copy runs per sign-in session. Starting it again opens the window of
  the one already running, and hands it the `clrd://` link it was started
  with. On Linux the lock is a name on the session bus. On Windows it is a
  named pipe whose name carries the sign-in session's number; a later start
  writes its link only to a pipe served from its own session. If the lock
  cannot be taken or reached, the app starts anyway.
- Each open notification = one system notification.
- Closing a system notification (its close button, or the desktop's own
  clear) only hides it on that computer and sends nothing, the same rule as
  the Android tray (#265). On Linux only `NotificationClosed` with reason 2
  (closed by the user) counts as a close; reason 1 (expired) and reason 3
  (closed by the app itself) never send anything, in any setting. On Windows
  only a toast's `Dismissed` event with reason `UserCanceled` counts;
  `TimedOut` leaves the toast in the notification centre, still open, and
  `ApplicationHidden` is the app's own close.
- On Windows a notification is a toast, shown under the app id on the Start
  menu shortcut the installer makes (`org.clrd.desktop`), with the clrd id as
  its tag. A build with no such shortcut shows none and still syncs. A toast
  holds text only: the sender's `click` address is never put in it, and a
  click only opens the window. A clear from elsewhere removes it from the
  notification centre too. Not handled: a click on a toast while the app is
  not running (a start removes the toasts an earlier run left). Not yet seen
  on a Windows computer: any of this, and whether a click, the Clear button
  or a close on a toast in the notification centre is reported at all.
- The Clear button on a system notification is a deliberate click and does
  sync: it dismisses with `via: "desktop_button"`.
- "Closing clears everywhere" is a setting on that computer, off by default.
  With it on, a close by the user dismisses with `via: "desktop_close"`.
  The switch is on the app's This computer page. It belongs to the computer,
  not the link: it is kept across restarts and across Unlink, and counts from
  the next close on.
- The window follows the desktop's light or dark setting, at start and at
  each change, so the inbox's System appearance is the desktop's. On Linux
  that is the settings portal's `color-scheme`; a desktop with no portal
  keeps the GTK theme's answer. On Windows it is the "app mode" colour
  setting, which the window library reads and follows by itself.
- An incoming `dismissed` or `resolved` event from any device always closes
  the system notification.
- Updates (Windows, #719). The update address and a minisign public key are
  build inputs (`CLRD_UPDATE_URL`, `CLRD_UPDATE_PUBKEY`); a build without
  both never asks and shows no update settings. The address serves the JSON
  feed Tauri's updater uses (`version`, and under `platforms` a
  `windows-x86_64` entry with `url` and `signature`). Official builds use
  this repo's GitHub Releases: the feed is `latest.json` on the newest
  release, and each installer's `url` names its own release (tag `v` and
  the version). An installer runs only
  when its signature verifies against the built-in key, its signed comment
  carries `version:` equal to the feed's version, and that version is newer
  than the running one. The requests carry no token and no session.
  "Update automatically" is a setting on that computer, on by default; off,
  the app asks nothing by itself and Check for updates still works. An empty
  file `no-auto-update` in the app's data folder is the off state, so a setup
  script can set it. A machine policy wins over it: the DWORD `AutoUpdate`
  under `HKLM\Software\Policies\clrd` (0 off, any other number on) is the
  switch for every user while it is there, and the user cannot change it. The app asks at start and every six hours, and does not
  start the installer for the same version twice by itself.

## 9. Web

- Served by the Go binary. Server renders the HTML (Go `html/template`);
  JS only adds live updates over the WebSocket. No SPA shell: pages must show
  real content with JavaScript off, and AI crawlers must be able to read the
  landing and docs pages.
- Routes: `/` landing (public), `/docs/*` (public, from `docs/*.md`),
  `/app` inbox (login), `/app/snoozed`, `/app/cleared`, `/app/settings`,
  `/app/n/{id}` (one notification: a page with its card alone, in any state,
  under the view its state is in now, with a link back to that view; a card
  this device cleared for itself alone shows as cleared; card actions come
  back to this page; not found, 404, when it is gone or in a topic the token
  cannot read; #814, #822),
  `/app/ui` (the component guide: every component in every state, no sign-in).
- Inbox layout per brand: 248 px sidebar (logo, Inbox/Snoozed/Cleared with
  mono counts, TOPICS list, "In sync on N devices" card), cards with priority
  dot, title 17/600, body 15, mono meta 12, Ack / Snooze / Clear (green),
  search, Clear all, Undo toast.
- Vanilla JS. CSS is Tailwind v4 (standalone CLI, no Node) with the Basecoat
  components, mapped to the tokens in section 10. Source in `web/ui/`;
  `web/ui/build.sh` writes `web/static/app.css`, which is committed so a plain
  `go build` still works. CI rebuilds it and fails if it drifted. Icons are
  Lucide, inlined as SVG partials in `web/templates/icons.html`.
- Installable web inbox (#164). The manifest starts at `/app` in standalone
  mode with the clrd icons. The signed-in inbox registers an offline worker
  scoped to `/app`, including under a sub-path. It loads navigation from the
  network and shows "Lost the live connection" with a reload link on network
  failure. It stores no inbox pages, API replies or credentials.
- Web Push (#164). The
  server makes a VAPID key pair on first start and keeps it in the
  database. A signed-in device subscribes with its token:
  `GET /api/v1/push` gives `public_key` (pass it as `applicationServerKey`)
  and `subscribed`; `PUT /api/v1/push` takes the browser's PushSubscription
  JSON (`endpoint` must be https); `DELETE /api/v1/push` stops it. One
  subscription per device, and revoking the device drops it. All three need
  scope read.
- What pushes: exactly what would ring that device in the open inbox. A new
  notification, a repeat, or a snooze ending, while active and not silenced,
  on a topic the device may read, and only to the chosen devices when the
  notification names any. State changes (clear, ack, resolve) do not push.
  Live publishes never push, since they are never stored.
- Payload (encrypted per RFC 8291, `aes128gcm`): JSON
  `{type, via, seq, id, topic, title, message, priority, tags, click, icon}`,
  empty fields left out. A message too long for one 4 KB push is cut to fit;
  if it still does not fit, only `{type, via, seq, id, topic}` is sent and
  the worker fetches the rest. Headers: `TTL: 86400`, `Urgency` high for
  priority 4 and 5, low for 1 and 2, `Topic` the notification id so a
  newer push replaces an older one waiting in the push service.
- The push service answering 404 or 410 drops the subscription. Other
  failures are logged, not retried. Push endpoints get the same guard as
  link fetches (ports 80 and 443, no private addresses), so a subscription
  cannot point the server at the local network.
- The web inbox (Settings, Browser notifications) registers its worker,
  `static/sw.js`, only when the user turns notifications on there, and asks
  for permission only on that tap. Nothing changes for a browser that never
  opts in. A site the browser blocks, or a subscription one side lost, is
  tidied to off on both sides at the next Settings load. The worker shows
  every push (fetching the notification by id when only the short form
  came), and a tap opens or focuses the inbox. The system notification only
  signals: closing it changes nothing on the server. Browsers allow Web Push
  only over HTTPS (or on localhost). A live or replayed clear or resolve
  closes the browser alert with that ID, unless the alert is newer than the
  event. On reconnect the inbox also checks displayed alerts against server
  state. With every inbox closed, alert removal waits for the inbox to open
  again. State changes never wake a background worker. See
  [Install the web inbox](web-app.md).

## 9.1 Settings layout (#575, web and Android)

Settings is one page in six sections. Both clients use these names, in this
order. A client leaves out a setting it does not have; it never renames one or
moves it to another section. Each setting has a plain label, a one-line
description and the keywords below. Its id is the anchor on web
(`/app/settings#sound`) and the key Android uses to scroll to it.

Search sits at the top. It matches when the typed text, trimmed and ignoring
case, appears in the label, the description or a keyword. Matching settings
show under their section name; a section with no match hides. Nothing
matching shows "No settings match" with the typed text and a way to clear it.
Search only ever lists what this device can open: a setting the signed-in
token may not use is left out of the page, not greyed in the results. With
JavaScript off the web page shows every section and no search box.

| Section | Id | Label | Keywords | Where |
|---|---|---|---|---|
| Notifications | `sound` | Sound | sound, chime, ring, audio, volume | both |
| Notifications | `push` | Browser notifications | push, system notification, pop up, desktop, alert | web |
| Notifications | `topics` | Topics | subscribe, unsubscribe, follow, hide, topic | both |
| Notifications | `silences` | Silences | mute, quiet, quiet hours, do not disturb, pause | web |
| Notifications | `subscriptions` | Topics | subscribe, unsubscribe, topic, follow, new topics | Android |
| Notifications | `muted-topics` | Muted topics | mute, topic, pop up, quiet | Android |
| Notifications | `battery` | Battery | battery, background, delayed, killed, optimization | Android |
| Inbox | `stack` | Stack by topic | group, fold, bundle, collapse | both |
| Inbox | `swipe` | Swipe buttons | swipe, gesture, slide, buttons | both |
| Inbox | `full-swipe` | Full swipe acts | swipe, fast, gesture, one move | Android |
| Inbox | `snooze` | Snooze times | later, remind, postpone, default time | both |
| Inbox | `keep-cleared` | Cleared messages | delete, keep, retention, history, days | web |
| Inbox | `drop-cleared` | Remove cleared items | hide, cleared tab, remove | Android |
| Display | `formatting` | Message text | markdown, formatting, links, preview, site icons | web |
| Display | `topic-icons` | Topic icons | icon, image, avatar, picture | web |
| Display | `format` | Message format check | format, markdown check, broken, warning | web, only with findings |
| Display | `appearance` | Appearance | theme, dark mode, light mode, colors, system | both: System, Light or Dark, kept per browser or phone |
| Display | `color-rules` | Color rules | color, colour, highlight, severity, priority, tint, bar, gradient | web, only where the token may change them |
| Devices | `server` | Server | server, address, url, token, sign in, log in | Android |
| Devices | `servers` | Servers | server, add server, another server, switch, remove | Android |
| Devices | `devices` | Linked devices | phone, browser, unlink, sign out, approve, may approve, activity | both |
| Devices | `link-phone` | Link a phone | qr, scan, pair, code, add phone | web shows the code, Android scans it |
| Devices | `client-cert` | Client certificate | certificate, mtls, mutual tls, tls, key | Android: picked from the Android key store, offered only to the server origin (host and port) it was picked for |
| Devices | `add-agent` | Add an agent | agent, ai, bot, invite, mcp, token | web, admin only |
| Integrations | `senders` | Senders | sender, rules, bot, agent, allow | both, only where the token may manage senders |
| Integrations | `test-send` | Send a test notification | test, curl, publish, try, api | web |
| Integrations | `webhooks` | Webhooks | webhook, outgoing, callback, post, integration | web, admin only |
| Integrations | `templates` | Webhook templates | incoming, template, github, grafana, json | web |
| About | `about` | Version | version, what's new, changelog, release, update, build | both. Web: the running version, What's new, and the per-browser switch for the after-update note (section 5.2). Android: this app's version and the shown server's side by side with a plain line when one is newer, What's new from the server or, when it cannot be reached, the released versions built into the app, and the phone's switch for its after-update note |

The Android version line stays at the foot of the page, outside the sections.

## 10. Brand tokens

Fonts: Geist (UI), Geist Mono (meta, ids, code). Self-host the font files; no Google Fonts call from the app.

| Token | Light | Use |
|---|---|---|
| ink | `#0F1114` | text, dark bg |
| paper | `#F5F3EE` | light bg |
| surface | `#FFFFFF` | cards |
| line | `#DEDAD1` | 1px hairlines |
| slate | `#5B6470` (dark mode `#98A1AC`) | quiet text |
| body | `#3D444D` | body copy |
| clear | `#0B7A55` (dark mode `#3DDC97`) | done, Clear button |
| signal | `#C2410C` (dark mode `#F97316`) | urgent only |
| snooze | `#B7791F` | waiting |

State chips (bg / text): active `#FCE9DF`/`#9A3412`, acknowledged
`#E9E6DE`/`#3D444D`, resolved `#DDF1E8`/`#065F42`, snoozed
`#F7EBD3`/`#7A4D0B`, dismissed outline `#DEDAD1`/`#5B6470`.

Rules: no gradients, flat fills, 1px hairlines, radius 6 (chips, inputs), 8
(buttons, menus) and 12 (cards, dialogs), square-cornered chips for state, orange only when something needs you, green means it went away. A user's color rules (section 2.1) color their own cards, gradient included; clrd's own chrome keeps these rules. Dark
mode swaps paper and ink, and accents use the lighter dark mode values above where given. Each app offers System (the default, follows the device), Light and Dark in Display, Appearance. Wordmark `clrd`, Geist 600, letter-spacing -0.05em.

### Color themes (#584)

Web inbox Settings, Appearance offers Default, High contrast, Midnight and
Custom. Default retains the brand tokens above. High contrast has light and
dark palettes with stronger outlines. Midnight has a cool light palette and
a true black dark background. The browser's System, Light or Dark selection
chooses the built-in variant. Custom uses its own background luminance
(below 0.179 is dark).

`GET /api/v1/settings` and the `settings` stream message carry `theme`
(`default`, `high-contrast`, `midnight` or `custom`), `theme_colors` when a
custom palette has been saved, and `device_theme` when the calling device has
an override. `PUT /api/v1/settings` (MCP `settings`) takes `theme` and/or
`theme_colors`. They belong to the user, or to the top token and its linked
devices when it has no user, and need read scope. The saved custom palette
stays available when a built-in is selected.

`PUT /api/v1/devices/{id}` with `{"theme":"midnight"}` (MCP `device_theme`)
sets a theme override for a device the caller can see. An empty theme
follows the owner. Use `this` for the calling device. A custom override uses
the owner's saved colors and falls back to the owner theme if the device
later moves to an owner without custom colors.

The custom palette takes six `#RRGGBB` strings: `bg` (paper), `card` (surface),
`text` (ink), `clear`, `urgent` (signal), and `snooze`. The server refuses a
save if text on background or cards falls below 4.5:1, any state color on
cards falls below 3:1, or the Euclidean distance between urgent and clear
in 8-bit sRGB is less than 80. Each rejection names the failed check.
The web editor previews valid picks and shows the same checks before saving.

The editor can share a custom palette as one line,
`clrd-theme:1:Custom:#bg,#card,#text,#clear,#urgent,#snooze`, with each
color represented by six hex digits after `#`. The name is a label in the
code, up to 64 characters and without a colon or line break. Imports use
the same color and contrast checks, open a draft, and take effect on other
devices when saved.

Derive line, slate and body by mixing 12%, 60% and 85% ink over paper in sRGB.
High contrast uses 65% ink for outlines. Slate and body fall back to ink if
the mix fails 4.5:1. A chip mixes 15% of its state color over surface, with
black or white text when the state color fails 4.5:1. Card color rules still
apply on matching cards. The inbox bootstraps the validated palette in an
escaped attribute and applies it before first paint with the existing
same-origin theme script.

Logo ("A, refined", picked 2026-11-19; it lives in `assets/logo.svg` so every surface uses one file). The three lines end on one circle around the disc, so every gap to the disc is the same:

```svg
<svg viewBox="0 0 70 40" fill="none" xmlns="http://www.w3.org/2000/svg">
  <path d="M2.5 10H24.38M7.5 20H22.5M12.5 30H24.38" stroke="#0F1114" stroke-width="5" stroke-linecap="round"/>
  <circle cx="50" cy="20" r="20" fill="#0B7A55"/>
  <path d="M41 20.5l6 6l12-12" stroke="#F5F3EE" stroke-width="5" stroke-linecap="round" stroke-linejoin="round"/>
</svg>
```

### 10.1 Motion

One set of values for every client, so a clear or an arrival looks the same on
the web, Android and the desktop. Two curves only, nothing that overshoots or
bounces:

- Ease out `cubic-bezier(.2, .7, .2, 1)`: things coming in (the green wash, an
  arriving card).
- Ease in out `cubic-bezier(.4, 0, .2, 1)`: things going away (the slide, the
  gap closing).

**Clearing.** Every way a card leaves plays the same steps: a swipe, the Clear
button, Clear all, a clear or other change from another device, and a snooze or
resolve that takes it out of the view. The clear is sent at once; the motion is
only what the screen shows.

| Step | Value |
|---|---|
| Green wash across the card, from the inline start | 420ms, ease out |
| Check mark draws in, with the reason | 260ms, starts 180ms in |
| Hold, so the reason can be read | 1400ms from the start |
| Slide away, toward the swipe (inline end when not swiped), fading out | 100% of the card width, 200ms, ease in out |
| Cards below close the gap | 200ms, ease in out |
| Clear all: each card's slide starts a little after the one above it | 40ms per card, at most 8 steps (320ms) |

**Arriving.** A new card slides in from just above its place and fades in, then
gives one small wiggle.

| Step | Value |
|---|---|
| Slide in and fade in | 8px, 180ms, ease out |
| Wiggle, once | rotate to 1deg, then -1deg, then rest, 320ms, ease in out, starts when the slide ends |

It plays only for a card that arrives live (a new message, a repeat, or a
snooze ending) while the page or app is in front and the card is on screen.
Nothing plays on first load, on catch-up after a reconnect, or for cards that
arrived while the person was away. In a burst every new card slides in and
only the newest wiggles.

**Reduced motion.** With the system's reduce motion setting on, none of this
plays: a cleared card shows its reason for the same hold, then goes, and a new
card simply appears.

## 11. Rules for every thread

- Public repo: no secrets, no internal hostnames or IPs, no em dashes, en dashes or the ellipsis character in anything committed.
- Stay in your own directories. A spec change goes in its own commit with a reason.
- Work on your branch, open a PR to `main`, tests green. Never force-push `main`.
- CI on `runs-on: ubicloud-standard-2` (`-4` for the desktop job), the Android and F-Droid jobs included.
- File follow-ups as GitHub issues on `thejdubb02/clrd`.
