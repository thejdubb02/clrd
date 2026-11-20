# clrd spec (v0, 2026-11-19)

The shared contract for every part of clrd. Server, web, Android and SDKs all
build against this file. Change it in its own commit when the contract changes,
and say so in the PR.

Tagline: **Dismiss once. Gone everywhere.**
Public instance: `https://clrd.itsjustin.me` (docs and examples use this host).

## 1. Repo layout

| Dir | Owner thread | What |
|---|---|---|
| `server/` | server | Go package `github.com/thejdubb02/clrd/server`, binary in `server/cmd/clrd`. |
| `web/` | web | Inbox app and landing/docs pages, embedded in the binary via `embed`. |
| `android/` | android | Kotlin app, Gradle, F-Droid ready. |
| `sdk/python`, `sdk/js`, `sdk/go` | integrations | Thin clients over the REST API. |
| `server/mcp/` | integrations | MCP server mounted at `/mcp`. |
| `docs/` | everyone | This spec, `api.md`, `self-host.md`, `integrations/*.md`. |
| `assets/` | web | `logo.svg`, `mark.svg`, icons. One source for every surface. |

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
  "attachment": {"name": "report.pdf", "url": "/api/v1/files/9f2c...", "type": "application/pdf", "size": 48213}, // or null
  "actions": [{"label": "Open draft", "url": "https://..."}, {"label": "Yes", "reply": "yes"}],
  "reply": "",                 // the reply action tapped, "" until someone answers
  "state": "active",
  "state_by": "pixel-9",       // device name that made the last change
  "state_by_id": "k3j9x2m1q8wz", // that device's token id ("" for the server); names can repeat, ids cannot
  "snooze_until": null,        // RFC3339 when state = snoozed
  "expires_at": null,          // RFC3339 when the server resolves it on its own, or null
  "created_at": "2026-11-19T05:00:00Z",
  "updated_at": "2026-11-19T05:00:00Z",
  "seq": 48211                 // seq of the last event that touched it
}
```

**States:** `active`, `acknowledged`, `snoozed`, `dismissed`, `resolved`.

- `dismissed` (a user cleared it) and `resolved` (the sender cleared it) are
  final. Both remove it from every inbox and every phone tray.
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

**Actions:** each action is a link (`url`) or an answer button (`reply`),
exactly one of the two. `reply` is `[A-Za-z0-9_-]{1,64}`, unique within the
notification, and there are at most 3 reply actions (an Android tray entry
shows 3). Tapping one posts it to `/api/v1/notifications/{id}/reply`, which
sets `reply`, sets the state to `acknowledged` with `state_by` = the device,
and logs one `state` event. A device sends the `seq` it showed with the tap;
if the notification changed since (a new question under the same key), the tap
gets 409. An `acknowledged` notification with a non-empty `reply` is answered,
and `state_by` is who tapped. The first answer wins: a second one gets 409.
Any later state change except dismiss and resolve (reopen, snooze, a plain
ack) clears `reply`. Reply actions are not sent to ntfy subscribers.

**Icons:** `icon` is an absolute `http` or `https` URL with a host (at most
2048 characters); anything else is a 400. Clients load it straight from that
URL, so the image host sees the viewer's IP address. Each topic can have its
own icon (`PUT /api/v1/topics/{topic}`): a publish that gives no `icon` gets
the topic's icon copied in at publish time, so changing the topic's icon later
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
 "topic_previews": {"news": true}, "snooze_intervals": ["1h", "4h"], "snooze_default": "1h"}
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
without the current default makes its first entry the default. Every change goes to every open
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

## 3. Events and sync

Every change is an append-only **event** with a global, gap-free, monotonic
`seq` (SQLite `INTEGER PRIMARY KEY AUTOINCREMENT` in the same transaction as
the row change):

```json
{"seq": 48212, "type": "state", "notification": { ...full notification... }}
```

`type` is `created`, `updated` or `state`. Clients only need the full
notification in the event; they never merge diffs.

Clients store the highest `seq` they applied. On (re)connect they ask for
everything after it. That is the whole sync model: no client clocks, no
conflict resolution, last write on the server wins.

### WebSocket `GET /api/v1/ws?since=<seq>&topics=a,b`

1. Server sends every event with `seq > since` (replay), then the display
   settings `{"type":"settings","settings":{...}}` (section 2.1), then
   `{"type":"ready","seq":<current>,"epoch":"<id>"}`.
2. Then live events as they happen, and a new `settings` message whenever a
   device changes the settings. Clients ignore message types they do not know.
3. Server pings every 30 s; client reconnects with backoff and its last seq.
4. If `since` is older than retention, server sends `{"type":"reset"}` and the
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
from a backup keeps the epoch it had, so this alone does not catch a restore
(issue #63 tracks that).

Also `GET /api/v1/events?since=<seq>&limit=500` for clients that poll: returns
`{"events":[...],"seq":<current>,"epoch":"<id>"}`, or HTTP 410 with code `reset`. SSE at
`GET /api/v1/events/sse?since=<seq>` sends the same JSON objects as the
WebSocket (events, `settings`, `ready`, `reset`), one per `data:` line, and
honours `Last-Event-ID` (`settings` and `reset` carry no `id:`).

Browsers cannot set headers on a WebSocket, so the WebSocket and SSE accept
the session cookie (same-origin only) as well as a Bearer token.

## 4. Auth

- **Tokens:** `Authorization: Bearer clrd_<32 random base62>`. Stored hashed (SHA-256).
  Scopes: `publish`, `read`, `admin`. Optional topic allow-list per token.
  Tokens imported from ntfy keep their `tk_...` value (also stored hashed) and
  work everywhere a `clrd_` token does.
- **Per-topic grants:** a token may instead carry an ordered list of grants,
  each a topic pattern (`*` matches any run of characters; matching ignores
  case, as in ntfy) and the scopes it
  gives there (`read`, `publish`, both, or none, which denies). For a topic the
  first grant whose pattern matches decides; no match means no access. The
  token's own scopes are the ceiling: a grant never adds a scope the token
  lacks, and a grant never holds `admin`. A write-only grant (`publish` only)
  lets the token publish to the topic but not list, get, stream, download
  files from or act on its notifications, on the native API and on the ntfy
  routes alike. A token with grants never manages tokens and only sees itself
  in the device list. A browser signed in with it gets the same grants.
  Grants come from `clrd import ntfy` (ntfy's access control list, rules in
  ntfy's own order).
- Bootstrap: first start with no users prints a one-time admin setup URL to the log.
  `clrd token create --name pixel-9 --scope read,publish` on the CLI.
- Basic auth (`user:token`) and `?auth=` query param accepted for ntfy-client compatibility.
- **Passwords, ntfy routes only:** users imported from ntfy keep their bcrypt
  password hash, so `Basic user:password` (header or `?auth=`) works on the
  ntfy-compatible routes (section 5) with that user's grants. The native API
  (`/api/v1/*`, `/mcp`, the web session) never accepts a password: tokens only.
- **Anonymous:** off unless imported. `clrd import ntfy` turns ntfy's
  `everyone` rules and `auth-default-access` into a token named `anonymous`
  with grants. It applies only on the ntfy-compatible routes, only to a request
  that sends no credentials at all (a wrong credential is still a 401), and it
  can never sign a browser in. Revoke that token to turn anonymous access off.
- Web UI uses a session cookie (HttpOnly, SameSite=Lax) after login.
- Every token has a **device name**; it is what shows as `state_by`. Names need not be
  unique, so a client that asks "did I make this change?" compares `state_by_id` with its own token id.
- What each scope allows: `read` = list, get, events, WebSocket, plus the
  user's own actions (ack, reply, snooze, dismiss, reopen, bulk clear), because a
  phone that can see a notification must be able to clear it. `publish` =
  publish, resolve (by id or by external id), ack by external id. `admin` =
  everything, including tokens. A token with a topic allow-list sees and
  touches only those topics; one with grants follows them, as above.
- Session: `POST /api/v1/session` with `{"token":"clrd_..."}` (JSON or a form
  post, optional `next` redirect for forms, optional `name`) mints a new token
  for this browser, with the same scopes, topics and grants, and sets it as the
  `clrd_session` cookie. It is named `name`, else from the User-Agent ("Firefox
  on Linux"), so each browser is its own device. Revoking the token typed in
  also revokes the browsers signed in with it. One token keeps at most 20
  browsers signed in: the 21st sign-in revokes the one used least recently.
  `DELETE /api/v1/session` revokes
  the browser's token and clears the cookie. The cookie works on `/api/v1/*`
  only, never on the ntfy-style GET publish webhooks.
- Setup: with no admin token in the database the server logs
  `<CLRD_BASE_URL>/setup?code=<one-time code>`. Opening it shows a form; posting
  it creates an admin token, shows it once and signs the browser in.

## 5. HTTP API

Native (JSON in, JSON out, errors as `{"error":"...","code":"..."}`).
Codes: `bad_request` 400, `unauthorized` 401, `forbidden` 403, `not_found`
404, `conflict` 409, `reset` 410, `rate_limited` 429 (with `Retry-After` in
seconds), `internal` 500.

Notifications always carry every field: `""` for unset strings, `[]` for
`tags` and `actions`, `null` for `snooze_until`. List returns
`{"notifications":[...],"seq":<current>,"epoch":"<id>"}`, newest first; page with
`before=<last id>`; `limit` defaults to 100, max 500. Fetch the list, then open
the WebSocket with `since=<seq>` from that response. Setting the state a
notification already has returns it unchanged with no new event (two phones
dismissing at once is normal). Reopening a dismissed row whose `external_id`
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
| POST | `/api/v1/notifications/{id}/ack` | Acknowledge. |
| POST | `/api/v1/notifications/{id}/reply` | Body `{"reply":"yes"}`: answer with one of its reply actions (section 2). 400 if not one of them, 409 if already answered or final. |
| POST | `/api/v1/notifications/{id}/snooze` | Body `{"for":"1h"}` or `{"until":"RFC3339"}`. |
| POST | `/api/v1/notifications/{id}/dismiss` | User clears it. |
| POST | `/api/v1/notifications/{id}/resolve` | Sender clears it. |
| POST | `/api/v1/notifications/{id}/reopen` | Undo a dismiss (powers the Undo toast, 30 s window not enforced by server). |
| POST | `/api/v1/notifications/clear` | Bulk dismiss, body `{"topic":"infra"}` or `{"ids":[...]}`. |
| GET | `/api/v1/topics` | Topics with open counts and icon: `{"topics":[{"topic":"infra","open":2,"icon":""}]}`. |
| PUT | `/api/v1/topics/{topic}` | Body `{"icon":"https://..."}` sets the topic's icon; `""` removes it. `{"checkin":"25h"}` makes the topic expect a message at least that often (1m to 8784h, `""` for off): when one is late the server publishes an urgent notification with external_id `clrd-checkin`, and the next message to the topic resolves it. A body with neither key is a 400. Scope publish on the topic. |
| GET | `/api/v1/topics/{topic}/templates` | The topic's saved webhook templates (secret hidden, `has_secret` shown) and the built-in names. Scope publish on the topic. |
| PUT | `/api/v1/topics/{topic}/templates/{name}` | Save a webhook template (section 5). Replaces the whole template; under a built-in name it overrides only the fields given. Scope publish on the topic. |
| DELETE | `/api/v1/topics/{topic}/templates/{name}` | Remove a saved template. Scope publish on the topic. |
| GET/PUT | `/api/v1/settings` | Display settings (section 2.1). PUT changes only the fields given. |
| GET | `/api/v1/favicon?host=` | A site's icon, or a fallback (section 2.1). |
| GET | `/api/v1/preview?url=&topic=` | A link's preview card; 403 while previews are off (section 2.1). |
| GET | `/api/v1/devices` | Devices (tokens) seen in the last 10 min = "In sync on N devices". |
| GET/POST/DELETE | `/api/v1/tokens` | Manage tokens (admin). |
| GET | `/healthz` | `{"ok":true,"seq":N}`; 503 with `"ok":false,"failing":["snooze wake"]` while a background job (snooze wake, prune) has failed 3 times in a row. |
| POST/DELETE | `/api/v1/session` | Web sign-in and sign-out (section 4). |
| GET | `/openapi.json` | OpenAPI 3.1 spec of all of the above. |

By external id (what scripts and monitors use):

| Method | Path | Does |
|---|---|---|
| POST | `/{topic}/{external_id}/resolve` | Resolve the open row with that key. 404 if none. |
| POST | `/{topic}/{external_id}/ack` | Same, ack. |

### ntfy compatibility (must pass ntfy's own client behaviour)

- `PUT|POST /{topic}` with the body as the message. Headers `X-Title`/`Title`/`t`,
  `X-Priority`/`Priority`/`p` (1-5 or min/low/default/high/urgent),
  `X-Tags`/`Tags`, `X-Click`, `X-Actions`, `X-Delay`, `X-Icon` (sets `icon`; a
  bad URL is dropped, not refused), `X-Attach` (external attachment; a bad URL
  is dropped), `X-Filename`/`Filename`/`File`/`f` (with it, the body is the
  file and the message comes from `X-Message`/`Message`/`m`, default "You
  received a file: <name>"), `X-Markdown`/`Markdown`/`md` (`no`/`false`/`0` publishes
  plain text, section 2.1), `X-Email` (ignored, documented),
  plus clrd's own `X-Id` = `external_id`. A body over 4096 bytes without a file
  name is still a 413; clrd does not turn it into an attachment. ntfy output
  carries `icon` and `attachment` (`name`, `url`, `type`, `size`, with `url`
  made absolute from `CLRD_BASE_URL`). Unlike ntfy, clrd's file URLs need a
  token, so a third-party ntfy app that fetches files without one gets 401.
- `POST /` with ntfy JSON body (`topic`, `message`, `title`, `priority`, `tags`,
  `icon`, `attach`, `filename`, ...).
- `GET /{topic}/json`, `/{topic}/sse`, `/{topic}/ws`, `/{topic}/raw` with `since=`
  (`all`, unix time, duration, message id) and `poll=1`, and ntfy's filters
  `id`, `message`, `title`, `priority` (any of a list) and `tags` (all of a
  list). Output in ntfy's message format so the ntfy Android app and ntfy CLI
  work unchanged. The ntfy routes answer any origin (CORS); no cookie is read
  there, so this grants nothing a token holder could not already do.
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
  Docs: `docs/integrations/custom-webhooks.md`.

Full ntfy parity checklist lives in issue #2; tick it off there.

## 6. MCP (`/mcp`, streamable HTTP, Bearer auth)

Tools: `send(topic, message, title?, format?, priority?, id?, actions?)`, `list(topic?, state?)`,
`ack(id)`, `resolve(id | topic+external_id)`, `snooze(id, for)`,
`wait_for_reply(id, timeout_s)` (blocks until the notification is acked,
dismissed or an action is tapped; returns which, so an agent can ask a human
"yes?" and wait). `actions` takes `[{"label":"Yes","reply":"yes"}]`; when one
is tapped, `wait_for_reply` returns `answer` (the `reply` value) next to
`reply` (the state).

## 7. Server internals

- Go 1.23 (`GOTOOLCHAIN=local`; `modernc.org/sqlite` v1.38 is the last line that builds on 1.23), stdlib `net/http` router, `modernc.org/sqlite` (pure Go, no cgo),
  WAL mode, one writer goroutine or a mutex around writes.
- Config by env: `CLRD_LISTEN` (`:8080`), `CLRD_DB` (`/data/clrd.db`),
  `CLRD_BASE_URL`, `CLRD_RETENTION` (`720h` for final rows and events),
  `CLRD_ATTACHMENTS` (folder for uploaded files, default `attachments` next to
  the database), `CLRD_ATTACHMENT_MAX_MB` (15, per file),
  `CLRD_ATTACHMENT_TOTAL_MB` (5000, all files of notifications that still
  exist; open notifications are never pruned, so their files keep counting),
  `CLRD_PUBLIC_PAGES` (`on`; `off` for a private server: `/` redirects to
  `/app`, `/docs` and `/sitemap.xml` are 404, `robots.txt` disallows
  everything, and the web pages send `X-Robots-Tag: noindex, nofollow`).
  `CLRD_RATE_TOKEN` and `CLRD_RATE_TOPIC` (60 each, publishes a minute per
  token and per topic, with a burst of the same size; `0` turns one off).
  Every publish counts, on every route; over a cap is a 429 `rate_limited`.
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
  `setDeleteIntent` -> POST `/dismiss`. Buttons ACK, SNOOZE 1H, CLEAR.
- Incoming `dismissed`/`resolved` event -> `NotificationManager.cancel(tag)`. That is the headline feature.
- Inbox screen per the brand board: filter pills by topic, swipe to clear with green reveal, tabs Inbox, Snoozed, Cleared, Settings.
- Settings: server URL + token (QR scan of a pairing URL later), per-topic mute.
- `fastlane/metadata/android/` for F-Droid. Reproducible build, no proprietary deps.
- UnifiedPush distributor support is a later issue, not v0.

## 9. Web

- Served by the Go binary. Server renders the HTML (Go `html/template`);
  JS only adds live updates over the WebSocket. No SPA shell: pages must show
  real content with JavaScript off, and AI crawlers must be able to read the
  landing and docs pages.
- Routes: `/` landing (public), `/docs/*` (public, from `docs/*.md`),
  `/app` inbox (login), `/app/snoozed`, `/app/cleared`, `/app/settings`,
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
- Web Push (VAPID) for browser notifications is a later issue.

## 10. Brand tokens

Fonts: Geist (UI), Geist Mono (meta, ids, code). Self-host the font files; no Google Fonts call from the app.

| Token | Light | Use |
|---|---|---|
| ink | `#0F1114` | text, dark bg |
| paper | `#F5F3EE` | light bg |
| surface | `#FFFFFF` | cards |
| line | `#DEDAD1` | 1px hairlines |
| slate | `#5B6470` | quiet text |
| body | `#3D444D` | body copy |
| clear | `#0B7A55` (dark mode `#3DDC97`) | done, Clear button |
| signal | `#C2410C` | urgent only |
| snooze | `#B7791F` | waiting |

State chips (bg / text): active `#FCE9DF`/`#9A3412`, acknowledged
`#E9E6DE`/`#3D444D`, resolved `#DDF1E8`/`#065F42`, snoozed
`#F7EBD3`/`#7A4D0B`, dismissed outline `#DEDAD1`/`#5B6470`.

Rules: no gradients, flat fills, 1px hairlines, radius 6 (chips, inputs), 8
(buttons, menus) and 12 (cards, dialogs), square-cornered chips for state, orange only when something needs you, green means it went away. Dark
mode swaps paper and ink, same accents. Wordmark `clrd`, Geist 600, letter-spacing -0.05em.

Logo ("A, refined", picked 2026-11-19; it lives in `assets/logo.svg` so every surface uses one file). The three lines end on one circle around the disc, so every gap to the disc is the same:

```svg
<svg viewBox="0 0 70 40" fill="none" xmlns="http://www.w3.org/2000/svg">
  <path d="M2.5 10H24.38M7.5 20H22.5M12.5 30H24.38" stroke="#0F1114" stroke-width="5" stroke-linecap="round"/>
  <circle cx="50" cy="20" r="20" fill="#0B7A55"/>
  <path d="M41 20.5l6 6l12-12" stroke="#F5F3EE" stroke-width="5" stroke-linecap="round" stroke-linejoin="round"/>
</svg>
```

## 11. Rules for every thread

- Public repo: no secrets, no internal hostnames or IPs, no em dashes, en dashes or the ellipsis character in anything committed.
- Stay in your own directories. A spec change goes in its own commit with a reason.
- Work on your branch, open a PR to `main`, tests green. Never force-push `main`.
- CI only on `runs-on: [self-hosted, cezar1]`.
- File follow-ups as GitHub issues on `thejdubb02/clrd`.
