# Gotify senders

clrd answers Gotify's publish call, `POST /message`, so a script or tool
that sends to Gotify keeps working when you point it at clrd.

## Make an app token

A Gotify app is a clrd token that may publish to exactly one topic. The token
decides the topic, the same way a Gotify app token decides the app.

```sh
clrd token create --name "Sonarr TV" --scope publish --topics tv
```

Or through the API, with an admin token (the reply's `token` is the secret,
shown once):

```sh
curl -fsS https://clrd.example.com/api/v1/tokens \
  -H "Authorization: Bearer clrd_..." \
  -d '{"name":"Sonarr TV","scopes":["publish"],"topics":["tv"]}'
```

The MCP tool `create_token` does the same for an agent. A token for every
topic, or for more than one, gets a 400 on `/message`, since Gotify has no
way to say which topic.

## Send

Swap your Gotify URL for your clrd server and the app token for the clrd one:

```sh
curl -fsS "https://clrd.example.com/message?token=clrd_..." -F "title=Backup done" -F "message=nightly backup finished" -F "priority=5"
```

Every form Gotify takes works: the token in `?token=`, the `X-Gotify-Key`
header or `Authorization: Bearer`, and the body as JSON, a form or multipart.

- `title` is optional. Left out, it is the token's name, as Gotify uses the
  app name.
- `priority` maps the way the Gotify app treats it: 0 is min (1), 1 to 3 low
  (2), 4 to 7 default (3), 8 to 10 high (4). Left out, it is clrd's default.
  Nothing from Gotify becomes urgent (5).
- `extras`: `client::display` `contentType: text/markdown` sends Markdown
  (otherwise plain text, as in Gotify), `client::notification` `click.url`
  sets the click link and `bigImageUrl` becomes an image attachment.
- The reply is Gotify's message shape (`id`, `appid`, `title`, `message`,
  `priority`, `date`, `extras`), and errors use Gotify's
  `error`, `errorCode`, `errorDescription`.

## Tools

- Apprise: `gotifys://clrd.example.com/clrd_...` (`gotify://` for plain HTTP).
- The gotify CLI: set `url` to your clrd server and `token` to the clrd token.
- Anything else that posts to `/message` with an app token.

```sh
apprise -t "Backup done" -b "nightly backup finished" "gotifys://clrd.example.com/clrd_..."
```

## Gotify-shaped keys (shoutrrr, Watchtower)

shoutrrr, and so Watchtower's `gotify://` URLs, refuses any token that does
not look like Gotify's (`A` and 14 characters). For those, make a Gotify app
key with an admin token:

```sh
curl -fsS https://clrd.example.com/api/v1/gotify/apps \
  -H "Authorization: Bearer clrd_..." \
  -d '{"name":"Watchtower","topic":"docker"}'
```

The reply's `token` is the key, shown once; `id` is the `appid` messages
report. Add `"priority": 5` for a default when a message sends none. The key
publishes only to that topic and only on `POST /message`; revoke it like any
token, with the reply's `token_id`. The MCP tool `create_gotify_app` does the
same for an agent.

```sh
WATCHTOWER_NOTIFICATION_URL=gotify://clrd.example.com/A...
```

Add `?disabletls=yes` for a server on plain HTTP.

## Without a Gotify token

A `POST /message` with no `?token=`, `X-Gotify-Key` or Gotify key as Bearer
is still ntfy's publish
to the topic named `message`, so nothing that used that topic breaks.

## Auto-resolve

Not supported: Gotify messages have no stable key or resolved state. Use the
native API or an SDK for alerts that should clear themselves.
