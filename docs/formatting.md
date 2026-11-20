# Message formatting

Messages are Markdown by default. Bold, lists, code and links show as you
would expect in the web inbox and the Android app. Send plain text when your
message has characters that should not be read as Markdown.

## Send plain text

```sh
# clrd API
curl -H "Authorization: Bearer $TOKEN" -d '{"topic":"backups","title":"Log","message":"*.tmp files: 3","format":"text"}' https://clrd.example.com/api/v1/notifications

# ntfy style
curl -u ":$TOKEN" -H "X-Markdown: no" -d "*.tmp files: 3" https://clrd.example.com/backups
```

`format` is `markdown` or `text`. ntfy clients can use `X-Markdown` (or
`Markdown`, `md`, or `?markdown=no`); `no`, `false` and `0` mean plain text.
Messages read back over the ntfy API carry `content_type: "text/markdown"`
unless they are plain text, so ntfy apps render them the same way.

## Display settings

Settings live on the server, so a change on one device shows on all of them.

| Setting | Default | What it does |
|---|---|---|
| `render_formatting` | on | Show Markdown formatting. Off shows the raw text. |
| `autolink` | on | Turn bare web addresses into links. |
| `favicons` | on | Show the site's icon next to a link. |
| `link_previews` | off | Show a card with the page's title, summary and image. |
| `topic_previews` | none | Turn link previews on or off for one topic. |
| `snooze_intervals` | `["1h","4h"]` | Snooze times offered on each card, 1 to 8, each up to 31 days (`15m`, `2h`, `48h`). |
| `snooze_default` | `1h` | The time a plain tap on Snooze uses. Must be one of the snooze times. |

```sh
curl -X PUT -H "Authorization: Bearer $TOKEN" -d '{"link_previews":true,"topic_previews":{"alerts":false}}' https://clrd.example.com/api/v1/settings
```

## Privacy

Your devices never contact the linked sites. When a client asks for an icon
or a preview, the clrd server fetches it, so the sites see your server, not
your phone. These are the only requests clrd makes to other servers. The
server refuses to fetch anything on your own network (private, loopback and
link-local addresses, checked after DNS and on every redirect), only uses
ports 80 and 443, stops after 5 seconds or 1 MB, and keeps icons for 7 days
and previews for 24 hours. Leave `link_previews` off if you would rather your
server never load pages people link to.
