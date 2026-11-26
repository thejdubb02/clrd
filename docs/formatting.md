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

## Emoji

Common GitHub and Slack shortcodes in a title or message show as the emoji,
in the web inbox, the Android app and the notifications the API and streams
return (a template preview shows the text as typed):
`:warning: Disk full` reads as ⚠️ Disk full. A tag that names an emoji goes
in front of the title (or of the message when there is no title), as in ntfy:

```sh
curl -u ":$TOKEN" -H "Tags: rotating_light" -H "Title: :fire: web-1 down" \
  -d "Check the load balancer" https://clrd.example.com/infra
```

That arrives titled 🚨 🔥 web-1 down. Unknown names, text inside backticks
and times like `10:30:15` stay as typed, and the server stores the text as
it was sent. On the ntfy endpoints the tag emoji is left out, because ntfy
apps add it themselves. The [format check](#format-check) counts the
messages with shortcodes clrd does not know and shows one of them.

## Display settings

Settings live on the server, so a change on one device shows on all of them.
Change the formatting, link, site icon and link preview switches under
Messages on the web inbox's Settings page or under Display, Formatting and
links in the Android app's Settings, each topic's preview choice under Link
previews by topic on the web, or any setting with the API. The snooze times
and default can also be changed in the Android app's Settings. On the phone,
a preview card shows under a message once you open it.

| Setting | Default | What it does |
|---|---|---|
| `render_formatting` | on | Show Markdown formatting. Off shows the raw text. |
| `autolink` | on | Turn bare web addresses into links. A written link, `[words](address)` or `<address>`, stays a link either way. With it off, the Android app shows any Markdown markers inside a bare address as typed. |
| `favicons` | on | Show the site's icon next to a link. |
| `link_previews` | off | Show a card with the page's title, summary and image. |
| `topic_previews` | none | Turn link previews on or off for one topic. |
| `snooze_intervals` | `["1h","4h"]` | Snooze times offered on each card, 1 to 8, each up to 31 days (`15m`, `2h`, `48h`). |
| `snooze_default` | `1h` | The time a plain tap on Snooze uses. Must be one of the snooze times. |
| `swipe_start` | `["clear","snooze"]` | Buttons a swipe to the left reveals on a card (to the right in a right-to-left language), 0 to 3 of `clear`, `snooze`, `ack`, `resolve`. |
| `swipe_end` | `["ack"]` | Buttons the opposite swipe reveals, same choices. An action may appear on one side only. |

```sh
curl -X PUT -H "Authorization: Bearer $TOKEN" -d '{"link_previews":true,"topic_previews":{"alerts":false}}' https://clrd.example.com/api/v1/settings
```

## Format check

Senders written for another tool often send messages that read badly here.
The format check looks at the newest 500 messages you can read and lists, per
topic, what could be better: Markdown sent as plain text, `:name:` shortcodes
clrd does not know, no title, messages over 2000 characters, and raw JSON. Each
topic comes with a prompt to paste into your agent or LLM so it can fix the
program that sends them. The prompt holds no token, and its example message
shows anything that looks like a password or key as `[hidden]`. Read it
before you paste: a short secret with no label can still slip through. The check only gives
advice: it never changes or blocks a message.

In the web inbox, Settings shows the results under Message format check, one
card per topic with a count of its findings, and inside it one group per
sender with a Copy fix prompt button. With more than three topics the cards
start closed. A device that may publish to the topic can dismiss a finding
for that sender or the whole topic from its Dismiss menu, or stop checking the
topic from the card's footer, and undo any of these from the list below the
results. The section only appears when there is something to fix or
something dismissed.

```sh
curl -H "Authorization: Bearer $TOKEN" https://clrd.example.com/api/v1/format-check
```

Agents can run it with the MCP tool `format_check`.

Add `?by=sender` to split each topic by the token that sent the messages,
so two programs posting to one topic each get their own prompt. Only an
admin, or the device that sent them, sees the sender's name and prompt. A finding you
know about and accept can be dismissed, for one sender or the whole topic,
and a topic can be left out of the check. Nothing is dismissed or left out
until you set it:

```sh
# stop counting missing titles in "backups", for every sender
curl -H "Authorization: Bearer $TOKEN" -d '{"topic":"backups","rule":"no_title"}' https://clrd.example.com/api/v1/format-check/dismiss
# leave the topic "chatty" out of the check
curl -X PUT -H "Authorization: Bearer $TOKEN" -d '{"format_check":false}' https://clrd.example.com/api/v1/topics/chatty
```

Send `"dismissed":false` or `"format_check":true` to undo. The check lists
what is dismissed and off, so nothing stays hidden by surprise. Agents use the
MCP tools `format_dismiss` and `set_topic`.

## Privacy

Your devices never contact the linked sites. When a client asks for an icon
or a preview, the clrd server fetches it, so the sites see your server, not
your phone. The fetcher refuses addresses on your own network (private, loopback and
link-local addresses, checked after DNS and on every redirect), only uses
ports 80 and 443, stops after 5 seconds or 1 MB, and keeps icons for 7 days
and previews for 24 hours. Turning off `favicons` stops the built-in clients
from requesting icons automatically. Clients with read access can still
request them directly. To disable previews, turn off `link_previews` and
remove any topic overrides that enable previews.
