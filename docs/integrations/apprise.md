# Apprise

Apprise's ntfy plugin talks to clrd through its ntfy-compatible publish.

```
ntfys://clrd_...@clrd.example.com/infra?auth=token
```

- `ntfys://` is HTTPS, `ntfy://` is HTTP.
- `?auth=token` is required: Apprise only treats the user part as a token on
  its own when it starts with `tk_`. Equivalent: `ntfys://clrd.example.com/infra?token=clrd_...`.
- Title, message and Apprise's priority, tags and click URL options
  (`?priority=high` etc.) all arrive.

```sh
apprise -t "Disk 91%" -b "/var on build is 91% full" "ntfys://clrd_...@clrd.example.com/infra?auth=token"
```

## Auto-resolve

Apprise sends alerts without a stable key. To clear an alert when a problem
ends, use the [native API](curl.md) or an SDK to send it with an external ID
and resolve it by that ID.
