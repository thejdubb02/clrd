# Switch from Gotify

`clrd import gotify` copies a Gotify server into clrd: every app, its token
and default priority, and the message history. Then you point Gotify's
hostname at clrd. Senders keep the URL and token they already have, so no
script, Sonarr, Watchtower or Apprise setup needs touching on the day you
switch.

Tested against Gotify 3.1.1. Databases from Gotify before 2.2 (no default
priority) import too.

## What carries over

| Gotify | clrd |
|---|---|
| Apps | One topic per app, named from the app name in lower case (`Sonarr TV` becomes `sonarr-tv`; two apps that would share a name get `-2`, `-3`). |
| App tokens | Keep working on `POST /message`, both Gotify 3's `gtfya.` keys and the older `A...` ones. Each one publishes only to its app's topic. |
| Default priority | Kept. A message sent without `priority` takes it, as in Gotify. |
| Messages | Notifications on the app's topic, with their original times, title, priority, Markdown, click link and big image. An untitled message takes the app name. |

What does not carry over, and the dry run lists each one with a count:
users (make clrd tokens for the people who read), client tokens (sign each
phone and browser in to clrd), and plugins.

## 1. Look first

Stop Gotify so its database is not changing, then run a dry run. It reads
Gotify's database, prints what maps and what does not, and writes nothing.

```sh
clrd import gotify --dry-run --db /var/gotify/data/gotify.db
```

The database is `data/gotify.db` in Gotify's working directory (in the Docker
image, `/app/data/gotify.db`). The import never writes to it. It reads SQLite
only; for a Gotify on MySQL or Postgres, there is no import yet.

## 2. Import

Run the same command without `--dry-run`, on a clrd database, while clrd is
not running (`CLRD_DB` says which database). An import runs once per
database; a second one is refused.

Add `--clear-older-than 720h` to bring older messages in as dismissed, so
phones do not start with a pile of old alerts. Without it every message
arrives open.

Then start clrd and sign in to the web inbox with an admin token
(`clrd token create --name me --scope admin`).

## 3. Swap the hostname

Point Gotify's hostname (DNS, or your reverse proxy) at clrd. Senders carry
on: each app's token posts to its topic, with its default priority.

Keep Gotify stopped but do not delete it or its files. To roll back, point
the hostname back at Gotify and start it again.

## 4. Phones

The Gotify app reads through Gotify's own API, which clrd does not serve.
Install the clrd app on each phone, or use the web inbox, and subscribe to
the app topics.

More on what `POST /message` takes: [Gotify senders](integrations/gotify.md).
