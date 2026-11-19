# ntfy clients (CLI and apps)

clrd speaks ntfy's publish and subscribe protocol, so ntfy's own tools work.
**All of this needs ntfy compat** ([issue 2](https://github.com/thejdubb02/clrd/issues/2)): `PUT|POST /{topic}`, `POST /`
JSON, `GET /{topic}/json|sse|ws|raw` in ntfy's message format, and Basic auth
with a token as the password.

## ntfy CLI

```sh
ntfy publish --token clrd_... --title "Disk 91%" --priority high \
  clrd.itsjustin.me/infra "/var on build is 91% full"

ntfy subscribe --token clrd_... clrd.itsjustin.me/infra
```

## ntfy Android / iOS app

Add a subscription with "Use another server" set to
`https://clrd.itsjustin.me`. For auth, add a user in Settings, Manage users:
any username, password = your `clrd_...` token (clrd accepts Basic `user:token`).

The ntfy app only receives. Swiping there does not clear the notification on
other devices; for that, use the clrd app.

## Auto-resolve

ntfy has no resolved state. Send with clrd's `X-Id` header, then resolve by key:

```sh
curl -H "Authorization: Bearer clrd_..." -H "X-Id: disk-build" -d "Disk 91%" https://clrd.itsjustin.me/infra
curl -X POST -H "Authorization: Bearer clrd_..." https://clrd.itsjustin.me/infra/disk-build/resolve
```

A resolve removes it from the clrd inbox and the clrd app. Whether a resolve
should also reach ntfy-app subscribers (ntfy has no delete event they act on)
is open; see [issue 7](https://github.com/thejdubb02/clrd/issues/7).
