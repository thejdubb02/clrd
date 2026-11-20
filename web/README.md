# web

The landing page, the docs and the inbox, served by the clrd binary. Every page
is rendered on the server, so it reads fine without JavaScript (and to crawlers).
`static/inbox.js` only adds the action buttons, live updates over the WebSocket
and the Undo toast. No framework.

## Styles

`ui/app.css` (Tailwind v4 plus the Basecoat components in `ui/basecoat/`, with
the clrd tokens in `ui/tokens.css`) builds to `static/app.css`:

```sh
web/ui/build.sh          # once
web/ui/build.sh --watch  # while you edit
```

The script fetches the pinned Tailwind standalone binary and checks its hash;
there is no Node step. Commit the built `static/app.css` with your change: CI
rebuilds it and fails if it differs. `/app/ui` shows every component in every
state, light and dark.

## The seam with the server

One handler serves every page. The server mounts it at each path it owns
(`server/README.md` on the server branch, `Mount` matches a prefix and
everything under it, `"/"` matches only the root):

```go
h := web.Handler(api, os.Getenv("CLRD_BASE_URL"))
for _, p := range []string{"/", "/docs", "/app", "/static", "/assets", "/favicon.svg", "/robots.txt", "/sitemap.xml"} {
	s.Mount(p, h)
}
```

`web` is a plain package in the root module; it adds `github.com/yuin/goldmark`
to the root `go.mod`.

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

The real `api` lives in `server/cmd/clrd/web.go` (`Authenticate`, `List`,
`Topics`, `CurrentSeq`). `Fake` in `fake_test.go` stands in for it in the page
tests.

## Routes

`/`, `/docs/`, `/docs/{name}` (from `docs/*.md`), `/app`, `/app/snoozed`,
`/app/cleared`, `/app/settings`, `/static/*`, `/assets/*`, `/favicon.svg`,
`/robots.txt`, `/sitemap.xml`.

`app`, `docs`, `static` and `assets` are in the spec's reserved topic names.
The other paths contain a dot, which a topic name cannot.

## What the browser calls

From `docs/SPEC.md`: `POST /api/v1/notifications/{id}/{ack|snooze|dismiss|reopen}`,
`POST /api/v1/notifications/clear` with `{"ids": [...]}`, and the WebSocket at
`/api/v1/ws?since={seq}`. The page sends `{"for": "<snooze_default>"}` with snooze and reads and
changes the snooze times with `GET/PUT /api/v1/settings`. The Send box posts to
`POST /api/v1/notifications` with the session cookie. On a `reset`
message, or a `settings` message that changes the snooze times, it reloads.

## Run it locally

The real binary serves every page. With a throwaway database:

```
export CLRD_DB=/tmp/clrd-dev.db CLRD_LISTEN=127.0.0.1:8080
go run ./server/cmd/clrd token create --name laptop --scope admin   # prints a token
go run ./server/cmd/clrd
```

Open `http://127.0.0.1:8080/app`, paste the token, then play a phone with curl:

```
curl -H "Authorization: Bearer $TOKEN" -d '{"topic":"test","title":"Hello"}' 127.0.0.1:8080/api/v1/notifications
curl -X POST -H "Authorization: Bearer $TOKEN" "127.0.0.1:8080/api/v1/notifications/$ID/dismiss"
```

## Tests

`go test ./web/` renders every page and checks: key content, every internal
link, the 401 page leaks nothing, hostile notification fields are escaped, the
mount beside the ntfy routes, robots.txt names the AI crawlers, the CSP header,
and no em dash, en dash or ellipsis character anywhere we ship.
