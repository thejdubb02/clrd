# web

The landing page, the docs and the inbox, served by the clrd binary. Every page
is rendered on the server, so it reads fine without JavaScript (and to crawlers).
`static/inbox.js` only adds the action buttons, live updates over the WebSocket
and the Undo toast. No framework, no build step.

## The seam with the server

The server mounts one handler as its `GET` fallback and implements one interface:

```go
import "github.com/thejdubb02/clrd/web"

mux.Handle("GET /", web.Handler(api, os.Getenv("CLRD_BASE_URL")))
```

`server/go.mod` needs the root module:

```
require github.com/thejdubb02/clrd v0.0.0
replace github.com/thejdubb02/clrd => ../
```

`api` implements:

```go
type API interface {
	Snapshot(r *http.Request, q web.Query) (web.Snapshot, error)
}
```

- `q.States` lists the states the view shows (inbox: active, acknowledged;
  snoozed: snoozed; cleared: dismissed, resolved; settings: none). `q.Topic`
  and `q.Q` filter by topic and free text.
- Read `Snapshot.Seq` in the same transaction as the items (or before them), so
  the WebSocket replay from that seq cannot miss an event.
- `Counts` is by state, `Topics` carries each topic's open count, `Devices` the
  devices seen in the last 10 minutes.
- Return `web.ErrUnauthorized` when the request has no valid session. The page
  then answers 401 with a sign-in notice and nothing else from the snapshot.

Specific server routes (`/api/v1/...`, `POST /{topic}`, `GET /{topic}/json` and
the rest of the ntfy routes) win over `GET /`, so the two coexist on one mux.
`web_test.go` checks this.

## Routes

`/`, `/docs/`, `/docs/{name}` (from `docs/*.md`), `/app`, `/app/snoozed`,
`/app/cleared`, `/app/settings`, `/static/*`, `/assets/*`, `/favicon.svg`,
`/robots.txt`, `/sitemap.xml`.

So topic names `app`, `docs`, `static`, `assets`, `favicon.svg`, `robots.txt`
and `sitemap.xml` must be reserved: `GET /{topic}` style routes for them would
shadow these pages.

## What the browser calls

From `docs/SPEC.md`: `POST /api/v1/notifications/{id}/{ack|snooze|dismiss|reopen}`,
`POST /api/v1/notifications/clear` with `{"ids": [...]}`, and the WebSocket at
`/api/v1/ws?since={seq}`. The page sends `{"for": "1h"}` with snooze. On a
`reset` message it reloads.

## Run it without the server

```
go run ./web/cmd/clrd-web-dev -addr 127.0.0.1:8080
```

This serves every page against `Fake`, an in-memory stand-in with seeded
notifications and the REST and WebSocket routes above. Play a phone with curl:

```
curl -X POST -H 'X-Device: pixel-9' localhost:8080/api/v1/notifications/{id}/dismiss
```

`Fake` has no auth and no persistence. Not for production.

## Tests

`go test ./web/` renders every page and checks: key content, every internal
link, the 401 page leaks nothing, hostile notification fields are escaped, the
mount beside the ntfy routes, robots.txt names the AI crawlers, the CSP header,
and no em dash, en dash or ellipsis character anywhere we ship.
