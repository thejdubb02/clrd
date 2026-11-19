# clrd spec (v0, 2026-11-19)

The shared contract for every part of clrd. Server, web, Android and SDKs all
build against this file. Change it in its own commit when the contract changes,
and say so in the PR.

Tagline: **Dismiss once. Gone everywhere.**
Public instance: `https://clrd.itsjustin.me` (docs and examples use this host).

## 1. Repo layout

| Dir | Owner thread | What |
|---|---|---|
| `server/` | server | Go module `github.com/thejdubb02/clrd/server`. Single binary. |
| `web/` | web | Inbox app and landing/docs pages, embedded in the binary via `embed`. |
| `android/` | android | Kotlin app, Gradle, F-Droid ready. |
| `sdk/python`, `sdk/js`, `sdk/go` | integrations | Thin clients over the REST API. |
| `server/mcp/` | integrations | MCP server mounted at `/mcp`. |
| `docs/` | everyone | This spec, `api.md`, `self-host.md`, `integrations/*.md`. |
| `assets/` | web | `logo.svg`, `mark.svg`, icons. One source for every surface. |

## 2. Data model

A **notification**:

```json
{
  "id": "01JD8...",            // server ULID
  "topic": "infra",
  "external_id": "bk-nightly", // optional, sender's key, unique per topic among non-final rows
  "title": "Nightly backup failed",
  "message": "restic exited 1 after 4m 12s.",
  "priority": 4,               // 1..5, ntfy scale. 5 = urgent (Signal orange)
  "tags": ["backup"],
  "click": "https://...",      // optional
  "actions": [{"label": "Open draft", "url": "https://..."}],
  "state": "active",
  "state_by": "pixel-9",       // device name that made the last change
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
  updates that row (title/message/priority) instead of making a new one.
  After it is final, a new publish makes a new row.

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

Also `GET /api/v1/events?since=<seq>&limit=500` (JSON, same events) for
clients that poll, and SSE at `GET /api/v1/events/sse?since=<seq>`.

## 4. Auth

- **Tokens:** `Authorization: Bearer clrd_<32 random base62>`. Stored hashed (SHA-256).
  Scopes: `publish`, `read`, `admin`. Optional topic allow-list per token.
- Bootstrap: first start with no users prints a one-time admin setup URL to the log.
  `clrd token create --name pixel-9 --scope read,publish` on the CLI.
- Basic auth (`user:token`) and `?auth=` query param accepted for ntfy-client compatibility.
- Anonymous publish/read is off by default, can be enabled per topic.
- Web UI uses a session cookie (HttpOnly, SameSite=Lax) after login.
- Every token has a **device name**; it is what shows as `state_by`.

## 5. HTTP API

Native (JSON in, JSON out, errors as `{"error":"...","code":"..."}`):

| Method | Path | Does |
|---|---|---|
| POST | `/api/v1/notifications` | Publish. Body = notification fields above (topic required). Returns the notification. |
| GET | `/api/v1/notifications?topic=&state=open\|all\|<state>&q=&limit=&before=` | List. `open` = active+acknowledged+snoozed. |
| GET | `/api/v1/notifications/{id}` | One. |
| POST | `/api/v1/notifications/{id}/ack` | Acknowledge. |
| POST | `/api/v1/notifications/{id}/snooze` | Body `{"for":"1h"}` or `{"until":"RFC3339"}`. |
| POST | `/api/v1/notifications/{id}/dismiss` | User clears it. |
| POST | `/api/v1/notifications/{id}/resolve` | Sender clears it. |
| POST | `/api/v1/notifications/{id}/reopen` | Undo a dismiss (powers the Undo toast, 30 s window not enforced by server). |
| POST | `/api/v1/notifications/clear` | Bulk dismiss, body `{"topic":"infra"}` or `{"ids":[...]}`. |
| GET | `/api/v1/topics` | Topics with open counts. |
| GET | `/api/v1/devices` | Devices (tokens) seen in the last 10 min = "In sync on N devices". |
| GET/POST/DELETE | `/api/v1/tokens` | Manage tokens (admin). |
| GET | `/healthz` | `{"ok":true,"seq":N}` |
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
  (`all`, unix time, duration, message id) and `poll=1`. Output in ntfy's
  message format so the ntfy Android app and ntfy CLI work unchanged.
- Webhook-friendly: `GET /{topic}/publish?message=...` and `/{topic}/trigger`.
- Alertmanager/Grafana/Gatus: a payload with `status: resolved` and a stable
  key (alert fingerprint / `X-Id`) resolves instead of creating. Mapping in `docs/integrations/`.

Full ntfy parity checklist lives in issue #2; tick it off there.

## 6. MCP (`/mcp`, streamable HTTP, Bearer auth)

Tools: `send(topic, message, title?, priority?, id?)`, `list(topic?, state?)`,
`ack(id)`, `resolve(id | topic+external_id)`, `snooze(id, for)`,
`wait_for_reply(id, timeout_s)` (blocks until the notification is acked,
dismissed or an action is tapped; returns which, so an agent can ask a human
"yes?" and wait).

## 7. Server internals

- Go 1.23+, stdlib `net/http` router, `modernc.org/sqlite` (pure Go, no cgo),
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
