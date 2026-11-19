# curl

Works today against the native API. No ntfy compatibility needed.

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

The ntfy-style form also works (needs ntfy compat, [issue 2](https://github.com/thejdubb02/clrd/issues/2)):

```sh
curl -sS -H "Authorization: Bearer $CLRD_TOKEN" \
  -H "Title: Disk 91%" -H "Priority: high" -H "X-Id: disk-build" \
  -d "/var on build is 91% full" "$CLRD/infra"
```

## Auto-resolve

When the problem clears, resolve by the same key. It disappears from every
inbox and every phone.

```sh
curl -sS -X POST -H "Authorization: Bearer $CLRD_TOKEN" "$CLRD/infra/disk-build/resolve"
```

A 404 means nothing open has that key (already cleared).

## Pattern: a cron check that cleans up after itself

```sh
#!/bin/sh
use=$(df --output=pcent /var | tail -1 | tr -dc 0-9)
if [ "$use" -ge 90 ]; then
  curl -sS -X POST "$CLRD/api/v1/notifications" -H "Authorization: Bearer $CLRD_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"topic\":\"infra\",\"external_id\":\"disk-$(hostname)\",\"title\":\"Disk ${use}%\",\"message\":\"/var on $(hostname)\",\"priority\":4}"
else
  curl -sS -o /dev/null -X POST -H "Authorization: Bearer $CLRD_TOKEN" "$CLRD/infra/disk-$(hostname)/resolve"
fi
```

Prefer a library? See [`sdk/python`](../../sdk/python), [`sdk/js`](../../sdk/js), [`sdk/go`](../../sdk/go).
The Python package also installs a `clrd` command: `clrd send infra "Disk 91%" --id disk-build`, `clrd resolve infra disk-build`.
