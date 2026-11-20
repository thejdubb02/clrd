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

## ntfy Android / iOS app

Add a subscription with "Use another server" set to
`https://clrd.example.com`. For auth, add a user in Settings, Manage users:
any username, password = your `clrd_...` token (clrd accepts Basic `user:token`).

The ntfy app only receives. Swiping there does not clear the notification on
other devices; for that, use the clrd app.

## Auto-resolve

ntfy has no resolved state. Send with clrd's `X-Id` header, then resolve by key:

```sh
curl -H "Authorization: Bearer clrd_..." -H "X-Id: disk-build" -d "Disk 91%" https://clrd.example.com/infra
curl -fsS -X POST -H "Authorization: Bearer clrd_..." https://clrd.example.com/infra/disk-build/resolve
```

A resolve removes it from the clrd inbox and the clrd app. ntfy subscribers get
a `message_delete` for it (a `message_clear` for an ack or snooze); whether a
given ntfy app acts on those depends on the app version.
