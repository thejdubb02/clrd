# curl

Works today against the native API.

```sh
export CLRD=https://clrd.itsjustin.me
export CLRD_TOKEN=clrd_...
```

## Send

```sh
curl -sS -X POST "$CLRD/api/v1/notifications" \
  -H "Authorization: Bearer $CLRD_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"topic":"infra","external_id":"disk-build","title":"Disk 91%","message":"/var on build is 91% full","priority":4}'
```

`external_id` is your stable key. Sending again with the same key while the
notification is still open updates it instead of making a second one.

The ntfy-style form also works:

```sh
curl -sS -H "Authorization: Bearer $CLRD_TOKEN" \
  -H "Title: Disk 91%" -H "Priority: high" -H "X-Id: disk-build" \
  -d "/var on build is 91% full" "$CLRD/infra"
```

## Auto-resolve

When the problem clears, resolve by the same key. It disappears from every
inbox and every phone.

```sh
curl -fsS -X POST -H "Authorization: Bearer $CLRD_TOKEN" "$CLRD/infra/disk-build/resolve"
```

`-f` makes curl exit non-zero on any HTTP error, 404 included. A 404 means
nothing open has that key (already cleared); the cron example below treats it
as fine. Keys with spaces or slashes must be URL-encoded in the path.

## Pattern: a cron check that cleans up after itself

```sh
#!/bin/sh
use=$(df --output=pcent /var | tail -1 | tr -dc 0-9)
if [ "$use" -ge 90 ]; then
  # Header form: no JSON to build, so nothing to escape.
  curl -fsS -o /dev/null -H "Authorization: Bearer $CLRD_TOKEN" -H "X-Id: disk-$(hostname)" \
    -H "Title: Disk ${use}%" -H "Priority: high" -d "/var on $(hostname)" "$CLRD/infra"
else
  code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $CLRD_TOKEN" \
    "$CLRD/infra/disk-$(hostname)/resolve")
  case "$code" in 2??|404) ;; *) echo "clrd resolve failed: $code" >&2; exit 1 ;; esac
fi
```

Prefer a library? See [`sdk/python`](../../sdk/python), [`sdk/js`](../../sdk/js), [`sdk/go`](../../sdk/go).
The Python package also installs a `clrd` command: `clrd send infra "Disk 91%" --id disk-build`, `clrd resolve infra disk-build`.
