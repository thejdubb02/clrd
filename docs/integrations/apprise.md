# Apprise

Apprise's ntfy plugin talks to clrd through its ntfy-compatible publish.

```
ntfys://clrd_...@clrd.itsjustin.me/infra?auth=token
```

- `ntfys://` is HTTPS, `ntfy://` is HTTP.
- `?auth=token` is required: Apprise only treats the user part as a token on
  its own when it starts with `tk_`. Equivalent: `ntfys://clrd.itsjustin.me/infra?token=clrd_...`.
- Title and message arrive. Apprise's priority, tags and click URL options
  (`?priority=high` etc.) are currently ignored by clrd: priority arrives as 3
  (bug [#130](https://github.com/thejdubb02/clrd/issues/130)).

```sh
apprise -t "Disk 91%" -b "/var on build is 91% full" "ntfys://clrd_...@clrd.itsjustin.me/infra?auth=token"
```

## Auto-resolve

Not supported: Apprise is fire-and-forget and has no resolved state or stable
key. Resolve separately with `curl -fsS -X POST .../infra/<key>/resolve` (see
[curl](curl.md)) if your script knows when the problem clears, or use the
native API or an SDK instead of Apprise for alerts that should clear themselves.
