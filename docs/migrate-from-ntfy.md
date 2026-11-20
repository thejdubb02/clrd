# Switch from ntfy

`clrd import ntfy` copies an ntfy server into clrd: users, passwords, access
tokens, per-topic access rules, anonymous access, message history and
attachments. Then you point ntfy's hostname at clrd. Scripts, the ntfy app
and the ntfy CLI keep working with the credentials they already have, so no
service needs touching on the day you switch.

Tested against ntfy v2.38.1.

## What carries over

| ntfy | clrd |
|---|---|
| Access tokens (`tk_...`) | Kept as they are. Bearer, Basic and `?auth=` all work, on the ntfy routes and on clrd's own API. The token's label becomes its device name. |
| Passwords | Kept (bcrypt hash). `user:password` works on the ntfy routes only; clrd's own API and the web inbox take tokens only. Ten wrong passwords from one address block passwords from it (429 with `Retry-After`), freeing one try a minute; tokens are never blocked. |
| Access rules (read-write, read-only, write-only, deny, wildcards) | Per-topic grants, checked in ntfy's order. A write-only topic stays write-only: you can publish to it but not read it. |
| `everyone` rules and `auth-default-access` | A token named `anonymous`, used only on the ntfy routes and only for requests with no credentials at all. |
| Admin users | Admin tokens. |
| Messages | Notifications, with their original times. Sequence updates merge into one notification, a sequence delete arrives dismissed, a delayed message arrives snoozed until its time. |
| Attachments | Uploaded files are copied in. External links are kept as links. |
| Publish URLs with `?tpl=yes` (or `X-Template: yes`, `?template=yes`) | Work as they are: the message, title and priority are Go templates over the JSON body, so `m={{.text}}&p={{if .channel}}2{{else}}4{{end}}` renders. `range`, `printf` and template calls are refused, as in [custom webhooks](integrations/custom-webhooks.md). |

What does not carry over, and the dry run lists each one with a count:
tiers and their limits, topic reservations (the access they give is kept,
the ownership is not), phone numbers, email addresses, billing, web push
subscriptions (browsers subscribe again), and web app sign-ins (tokens that
expire; sign in again).

## 1. Look first

Stop ntfy so its files are not changing, then run a dry run. It reads ntfy's
files, prints what maps and what does not, and writes nothing.

```sh
clrd import ntfy --dry-run \
  --auth-file /var/lib/ntfy/user.db \
  --cache-file /var/cache/ntfy/cache.db \
  --attachment-dir /var/cache/ntfy/attachments \
  --default-access deny-all
```

Take the three paths from ntfy's `server.yml` (`auth-file`, `cache-file`,
`attachment-cache-dir`). Set `--default-access` to ntfy's
`auth-default-access`; if `server.yml` does not set it, ntfy's default is
`read-write`. The import never writes to ntfy's files.

## 2. Import

Run the same command without `--dry-run`, on a fresh clrd database, while
clrd is not running (`CLRD_DB` says which database). An import runs once per
database; a second one is refused.

Add `--clear-older-than 72h` to bring older messages in as dismissed, so
phones do not start with a pile of old alerts. Without it every message
arrives open.

Then start clrd. The ntfy admins' tokens are admin tokens in clrd; for the web
inbox, sign in with one of them, or make a new one with
`clrd token create --name me --scope admin`.

## 3. Swap the hostname

Point ntfy's hostname (DNS, or your reverse proxy) at clrd. Publishers and
subscribers carry on. A phone that was subscribed with `since=<message id>`
picks up from the same message, because clrd remembers ntfy's message ids.

Keep ntfy stopped but do not delete it or its files. To roll back, point the
hostname back at ntfy and start it again.

## 4. Move phones when you like

The ntfy app keeps working against clrd. Install the clrd app on each phone
when it suits you to get dismissals that sync across devices.

## After the switch

clrd ignores some ntfy publish options (email, calls, Firebase,
UnifiedPush, poll requests) and any unknown JSON field. Each one it receives
is counted at `GET /api/v1/ntfy/ignored` (admin token; open it in a signed-in
browser for a table), so you can see which services rely on something clrd
does not do.
