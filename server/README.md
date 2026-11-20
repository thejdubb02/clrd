# server

The clrd server: one Go package (`github.com/thejdubb02/clrd/server`) and one
binary (`server/cmd/clrd`). SQLite store, event log, native API, ntfy-compatible
API, WebSocket and SSE sync. The contract is `docs/SPEC.md`.

## Run it

```sh
GOTOOLCHAIN=local go run ./server/cmd/clrd                 # serve on :8080
CLRD_DB=./clrd.db go run ./server/cmd/clrd token create --name laptop --scope admin
GOTOOLCHAIN=local go test ./...
```

Config is env only and all optional: `CLRD_LISTEN` (`:8080`), `CLRD_DB`
(`/data/clrd.db` if it exists, else `~/.local/share/clrd/clrd.db`),
`CLRD_BASE_URL` (public URL; unset, links use the request's address),
`CLRD_RETENTION` (`720h`).

## Mounting pages: the one seam for web and MCP

Other packages never touch the server's routes. They hand it an `http.Handler`
and a path prefix, in `server/cmd/clrd/main.go`:

```go
s, _ := server.New(opt)
s.Mount("/", web.Handler(s))     // web thread: landing page at exactly "/"
s.Mount("/docs", web.Docs(s))    // gets /docs and /docs/...
s.Mount("/app", web.App(s))      // gets /app and /app/...
s.Mount("/mcp", mcp.New(s))
s.Run(ctx)
```

Rules:

- `"/"` matches only the exact root, and only GET and HEAD (`POST /` is the
  ntfy JSON publish). Any other prefix matches itself and
  everything under it (`/docs` gets `/docs/x`, not `/docsx`).
- Longest prefix wins. Mounts are checked before the server's own routes.
- `/api`, `/healthz`, `/openapi.json`, `/setup` and `/v1` (ntfy health) belong to the server;
  mounting there panics at startup.
- Call `Mount` before `Run`. The handler sees the full, unstripped path; use
  `http.StripPrefix` if you want it stripped.

What a mounted handler can call on `*server.Server`:

| Call | For |
|---|---|
| `Authenticate(r) *Token` | Who is calling: Bearer, Basic, `?auth=` or the `clrd_session` cookie. `nil` if nobody. Cookie writes from another origin return `nil`. |
| `tok.Has(scope)`, `tok.Allows(topic)`, `tok.Name` | Scope and topic checks; `Name` is the device name shown as `state_by`. |
| `Publish(ctx, tok, PublishInput)` | Same as `POST /api/v1/notifications`. |
| `List(ctx, tok, ListQuery)`, `Get(ctx, tok, id)`, `Topics(ctx, tok)` | Reads, already filtered to what `tok` may see. |
| `Act(ctx, tok, id, action, until)` | `ack`, `snooze` (needs `until`), `dismiss`, `resolve`, `reopen`. |
| `ActByKey(ctx, tok, topic, externalID, action)` | `resolve` or `ack` by external id. |
| `Clear(ctx, tok, topic, ids)` | Bulk dismiss. |
| `Subscribe() (<-chan Event, cancel)` | Every event committed from now on (for `wait_for_reply`). The channel closes if you fall behind. |
| `CurrentSeq()` | The last committed event seq. |

Errors from these are the same ones the API returns (status and code); a
mounted handler can just answer with its own message.

The browser flow: the web app signs in with `POST /api/v1/session` (JSON or a
form post with `token`, optional `next` and optional `name`), which mints a
token for that browser with the same scopes and topics and sets it as the
`clrd_session` cookie, so each browser is its own device. After that every `/api/v1/*` call, the WebSocket and SSE work from the
browser with no header. `DELETE /api/v1/session` signs out and revokes that browser's token. On a fresh server
with no admin token, the log prints `<CLRD_BASE_URL>/setup?code=...`; that page
creates the first admin token and signs the browser in.

## Files

| File | What |
|---|---|
| `server.go` | Options, `New`, `Mount`, `Run`, native routes |
| `store.go` | Schema, notifications, the event log (`write`), state rules (`transition`) |
| `auth.go` | Tokens, scopes, session cookie, setup page |
| `stream.go` | Live fan-out, WebSocket, SSE and polling, all on one `follow` loop |
| `ntfy.go` | ntfy-compatible publish and subscribe |
