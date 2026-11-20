# Watchtower

Watchtower sends through shoutrrr, which speaks ntfy, so no template is
needed:

```sh
WATCHTOWER_NOTIFICATION_URL=ntfy://anything:clrd_...@clrd.example.com/docker
```

The token goes in the password. Each update report is one notification.
Use shoutrrr's `ntfy://` URL, not its generic webhook: the generic one keeps
`?template=` for itself.

Source: [shoutrrr ntfy service](https://containrrr.dev/shoutrrr/v0.8/services/ntfy/).
