# ntfy clients (CLI and apps)

clrd speaks ntfy's publish and subscribe protocol, so ntfy's own tools work:
`PUT|POST /{topic}`, `POST /` JSON, `GET /{topic}/json|sse|ws|raw` in ntfy's
message format, and Basic auth with a token as the password.

## ntfy CLI

```sh
ntfy publish --token clrd_... --title "Disk 91%" --priority high \
  clrd.example.com/infra "/var on build is 91% full"

ntfy subscribe --token clrd_... clrd.example.com/infra
```

## Publish options

For `POST /` with a JSON body, specify `topic` in JSON. You can mix JSON
fields with ntfy headers or query parameters, for example `Actions: view,
Open, https://example.com/report` and `?delay=30m`. For options accepted in
more than one place, populated JSON fields take precedence, followed by
headers and then query parameters. An empty JSON array clears out header
or query tags and actions, and `"markdown": false` keeps the message plain.
For a raw publish, `Content-Type: text/markdown` selects Markdown.

## ntfy Android / iOS app

Add a subscription with "Use another server" set to
`https://clrd.example.com`. For auth, add a user in Settings, Manage users:
any username, password = your `clrd_...` token (clrd accepts Basic `user:token`).

The ntfy app only receives. Swiping there does not clear the notification on
other devices; for that, use the clrd app.

## ntfy web app and other web pages

Out of the box, the ntfy and Gotify routes refuse a page on another web site
(403), so a site you visit cannot post to or read your topics with your
browser. Phones, scripts and the ntfy CLI send no Origin header and are not
affected. To let a web app on another site in, such as ntfy's hosted app, an
admin adds its origin:

```sh
curl -X PUT -H "Authorization: Bearer clrd_..." \
  -d '{"origins":["https://ntfy.sh"]}' https://clrd.example.com/api/v1/ntfy/origins
```

`["*"]` lets every site in, as ntfy does. `GET` the same path shows the list.
The MCP tools are `ntfy_origins` and `set_ntfy_origins`.
Allowed origins can read the `Retry-After` response header on time-based
rate limits to decide when to retry.

## Request and stream limits

Each ntfy subscription can name up to 150 distinct topics. Repeat names
count once. Use another subscription for any remaining topics.

Operators can [set per-address limits](../self-host.md#server-environment)
for ntfy API requests and open subscriptions. Both are off by default.
Clients that hit the request rate get HTTP 429 and a `Retry-After` header.
If a new live subscription gets HTTP 429 without that header, a slot frees
when an existing stream disconnects. A poll uses the request budget only.

## Auto-resolve

ntfy has no resolved state. Send with clrd's `X-Id` header, then resolve by key:

```sh
curl -H "Authorization: Bearer clrd_..." -H "X-Id: disk-build" -d "Disk 91%" https://clrd.example.com/infra
curl -fsS -X POST -H "Authorization: Bearer clrd_..." https://clrd.example.com/infra/disk-build/resolve
```

A resolve removes it from the clrd inbox and the clrd app. ntfy subscribers get
a `message_delete` for it (a `message_clear` for an ack or snooze); whether a
given ntfy app acts on those depends on the app version.
