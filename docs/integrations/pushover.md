# Pushover senders

clrd answers Pushover's publish call, `POST /1/messages.json`, so a script
written for Pushover keeps working when you point it at clrd.

Most apps with a built-in Pushover option (Apprise's `pover://`, Uptime Kuma,
Home Assistant) send to Pushover's own host and cannot be pointed elsewhere.
This is for scripts, curl and any sender that lets you set the Pushover URL.
For those apps, use their ntfy or webhook option instead.

## Make an app key

A Pushover app is a clrd key that may publish to exactly one topic. The key
decides the topic, the same way a Pushover app token decides the app. Make
one with an admin token:

```sh
curl -fsS https://clrd.example.com/api/v1/pushover/apps \
  -H "Authorization: Bearer clrd_..." \
  -d '{"name":"Backups","topic":"backups"}'
```

The reply's `token` is the key, shown once, shaped like a Pushover app token
(30 letters and digits). `user` is a value for senders that insist on a user
key: clrd never checks it, so any non-empty value works. The key publishes
only to that topic and only on the Pushover routes; revoke it like any token,
with the reply's `token_id`. The MCP tool `create_pushover_app` does the same
for an agent.

A clrd token limited to one topic (`clrd token create --scope publish
--topics backups`) works as the `token` too.

## Send

Swap `https://api.pushover.net` for your clrd server:

```sh
curl -fsS https://clrd.example.com/1/messages.json \
  --form-string "token=..." --form-string "user=..." \
  --form-string "title=Backup done" --form-string "message=nightly backup finished"
```

The body can be a form, multipart or JSON, as Pushover takes it.

- `title` is optional. Left out, it is the app's name, as Pushover uses it.
- `priority` -2 to 2 becomes clrd's 1 to 5. Priority 2 (emergency) needs
  `retry` (30 seconds or more) and `expire` (up to 10800 seconds): the alert
  rings again every `retry` seconds (60 at least, clrd's shortest repeat)
  until someone acts on it or `expire` runs out, 50 times at most. The reply
  carries a `receipt`, but clrd has no receipt endpoint to poll yet.
- `url` is the click link. `ttl` (seconds) makes the message expire.
- `html=1`: bold, italic, links and line breaks become Markdown; other tags
  are dropped. `monospace=1` shows the message as a code block. Otherwise
  the message is plain text.
- `attachment` (multipart) or `attachment_base64` with `attachment_type`
  adds an image, up to 5 MB as Pushover allows, within clrd's attachment cap.
- `sound`, `device`, `timestamp` and `url_title` are accepted and ignored.
- The reply is Pushover's `{"status":1,"request":"..."}`. An error is a 4xx
  with `status` 0, `errors`, and the bad parameter set to `invalid`, as
  Pushover's clients read it.

`POST /1/users/validate.json` answers `status` 1 for a valid key and any
non-empty `user`, for senders that check before they send.

## Auto-resolve

Not supported: Pushover messages have no stable key or resolved state. Use
the native API or an SDK for alerts that should clear themselves.
