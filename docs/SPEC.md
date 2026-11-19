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
  "priority": 4,               // 1..5, ntfy scale. 5 = urgent (Signal orange)
  "tags": ["backup"],
  "click": "https://...",      // optional
  "actions": [{"label": "Open draft", "url": "https://..."}, {"label": "Yes", "reply": "yes"}],
  "reply": "",                 // the reply action tapped, "" until someone answers
  "state": "active",
  "state_by": "pixel-9",       // device name that made the last change
  "state_by_id": "k3j9x2m1q8wz", // that device's token id ("" for the server); names can repeat, ids cannot
  "snooze_until": null,        // RFC3339 when state = snoozed
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

1. Server sends every event with `seq > since` (replay), then
   `{"type":"ready","seq":<current>}`.
2. Then live events as they happen.
3. Server pings every 30 s; client reconnects with backoff and its last seq.
4. If `since` is older than retention, server sends `{"type":"reset"}` and the
   client fetches `GET /api/v1/notifications?state=open` fresh.

After `reset` the server sends `ready` with the current seq and carries on
with live events, so the client drops its local state, fetches the open list,
and keeps the connection. The server also sends `reset` when `since` is ahead
of the server (a wiped server).

Also `GET /api/v1/events?since=<seq>&limit=500` for clients that poll: returns
`{"events":[...],"seq":<current>}`, or HTTP 410 with code `reset`. SSE at
`GET /api/v1/events/sse?since=<seq>` sends the same JSON objects as the
WebSocket (events, `ready`, `reset`), one per `data:` line, and honours
`Last-Event-ID`.

Browsers cannot set headers on a WebSocket, so the WebSocket and SSE accept
the session cookie (same-origin only) as well as a Bearer token.

## 4. Auth

- **Tokens:** `Authorization: Bearer clrd_<32 random base62>`. Stored hashed (SHA-256).
  Scopes: `publish`, `read`, `admin`. Optional topic allow-list per token.
- Bootstrap: first start with no users prints a one-time admin setup URL to the log.
  `clrd token create --name pixel-9 --scope read,publish` on the CLI.
- Basic auth (`user:token`) and `?auth=` query param accepted for ntfy-client compatibility.
- Anonymous publish/read is off by default, can be enabled per topic.
- Web UI uses a session cookie (HttpOnly, SameSite=Lax) after login.
- Every token has a **device name**; it is what shows as `state_by`. Names need not be
  unique, so a client that asks "did I make this change?" compares `state_by_id` with its own token id.
- What each scope allows: `read` = list, get, events, WebSocket, plus the
  user's own actions (ack, reply, snooze, dismiss, reopen, bulk clear), because a
  phone that can see a notification must be able to clear it. `publish` =
  publish, resolve (by id or by external id), ack by external id. `admin` =
  everything, including tokens. A token with a topic allow-list sees and
  touches only those topics.
- Session: `POST /api/v1/session` with `{"token":"clrd_..."}` (JSON or a form
  post, optional `next` redirect for forms, optional `name`) mints a new token
  for this browser, with the same scopes and topics, and sets it as the
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
404, `conflict` 409, `reset` 410, `internal` 500.

Notifications always carry every field: `""` for unset strings, `[]` for
`tags` and `actions`, `null` for `snooze_until`. List returns
`{"notifications":[...],"seq":<current>}`, newest first; page with
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
| POST | `/api/v1/notifications` | Publish. Body = notification fields above (topic required). Returns the notification. |
| GET | `/api/v1/notifications?topic=&state=open\|all\|<state>&q=&limit=&before=` | List. `open` = active+acknowledged+snoozed. |
| GET | `/api/v1/notifications/{id}` | One. |
| POST | `/api/v1/notifications/{id}/ack` | Acknowledge. |
| POST | `/api/v1/notifications/{id}/reply` | Body `{"reply":"yes"}`: answer with one of its reply actions (section 2). 400 if not one of them, 409 if already answered or final. |
| POST | `/api/v1/notifications/{id}/snooze` | Body `{"for":"1h"}` or `{"until":"RFC3339"}`. |
| POST | `/api/v1/notifications/{id}/dismiss` | User clears it. |
| POST | `/api/v1/notifications/{id}/resolve` | Sender clears it. |
| POST | `/api/v1/notifications/{id}/reopen` | Undo a dismiss (powers the Undo toast, 30 s window not enforced by server). |
| POST | `/api/v1/notifications/clear` | Bulk dismiss, body `{"topic":"infra"}` or `{"ids":[...]}`. |
| GET | `/api/v1/topics` | Topics with open counts. |
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
  `X-Tags`/`Tags`, `X-Click`, `X-Actions`, `X-Delay`, `X-Markdown`, `X-Icon`,
  `X-Attach`, `X-Email` (ignored, documented), plus clrd's own `X-Id` = `external_id`.
- `POST /` with ntfy JSON body (`topic`, `message`, `title`, `priority`, `tags`, ...).
- `GET /{topic}/json`, `/{topic}/sse`, `/{topic}/ws`, `/{topic}/raw` with `since=`
  (`all`, unix time, duration, message id) and `poll=1`, and ntfy's filters
  `id`, `message`, `title`, `priority` (any of a list) and `tags` (all of a
  list). Output in ntfy's message format so the ntfy Android app and ntfy CLI
  work unchanged. The ntfy routes answer any origin (CORS); no cookie is read
  there, so this grants nothing a token holder could not already do.
- Webhook-friendly: `GET /{topic}/publish?message=...` and `/{topic}/trigger`.
- Alertmanager/Grafana/Gatus: a payload with `status: resolved` and a stable
  key (alert fingerprint / `X-Id`) resolves instead of creating. Mapping in `docs/integrations/`.

Full ntfy parity checklist lives in issue #2; tick it off there.

## 6. MCP (`/mcp`, streamable HTTP, Bearer auth)

Tools: `send(topic, message, title?, priority?, id?, actions?)`, `list(topic?, state?)`,
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
  `CLRD_BASE_URL`, `CLRD_RETENTION` (`720h` for final rows and events).
- Snooze wake-ups: a ticker every 15 s.
- `Dockerfile` (distroless or scratch) and `docker-compose.yml` example.
- Tests: `go test ./...` must cover publish, external_id upsert, each state
  change, replay from seq, and the two-client sync proof (issue #1).
- No outbound calls. No telemetry.

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
  `/app` inbox (login), `/app/snoozed`, `/app/cleared`, `/app/settings`.
- Inbox layout per brand: 248 px sidebar (logo, Inbox/Snoozed/Cleared with
  mono counts, TOPICS list, "In sync on N devices" card), cards with priority
  dot, title 17/600, body 15, mono meta 12, Ack / Snooze / Clear (green),
  search, Clear all, Undo toast.
- Vanilla JS + one CSS file. No framework, no build step unless it pays for itself.
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

Rules: no gradients, flat fills, 1px hairlines, radius 12 to 20, pills for
state, orange only when something needs you, green means it went away. Dark
mode swaps paper and ink, same accents. Wordmark `clrd`, Geist 600, letter-spacing -0.05em.

Logo (placeholder "A", final pick pending; keep it in `assets/logo.svg` so a swap is one file):

```svg
<svg viewBox="0 0 96 64" fill="none" xmlns="http://www.w3.org/2000/svg">
  <path d="M8 22h30M16 32h24M24 42h18" stroke="#0F1114" stroke-width="6" stroke-linecap="round"/>
  <circle cx="68" cy="32" r="20" fill="#0B7A55"/>
  <path d="M59 32l6 6 12-12" stroke="#F5F3EE" stroke-width="6" stroke-linecap="round" stroke-linejoin="round"/>
</svg>
```

## 11. Rules for every thread

- Public repo: no secrets, no internal hostnames or IPs, no em dashes, en dashes or the ellipsis character in anything committed.
- Stay in your own directories. A spec change goes in its own commit with a reason.
- Work on your branch, open a PR to `main`, tests green. Never force-push `main`.
- CI only on `runs-on: [self-hosted, cezar1]`.
- File follow-ups as GitHub issues on `thejdubb02/clrd`.
