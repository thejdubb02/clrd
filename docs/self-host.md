# Self-hosting clrd

clrd is one binary with one SQLite file. Run it anywhere that can keep a
port open, put HTTPS in front of it, and point your phones and scripts at it.

## Run it

With Docker Compose (the `docker-compose.yml` in the repo root):

```sh
git clone https://github.com/thejdubb02/clrd && cd clrd
# set CLRD_BASE_URL in docker-compose.yml to your public https URL
docker compose up -d
docker compose logs clrd    # shows the one-time setup link
```

Or plain Docker:

```sh
docker build -t clrd .
docker run -d --name clrd -p 127.0.0.1:8080:8080 -v clrd-data:/data \
  -e CLRD_BASE_URL=https://clrd.example.com clrd
```

Both publish the port on localhost only (Docker 28 or newer keeps that
from other machines on the same network), so tokens reach clrd through your
HTTPS proxy and never cross the network in plain HTTP. If the proxy runs on
another machine, publish the port on a private interface instead.

Or the binary (Go 1.24+, no C compiler needed):

```sh
CGO_ENABLED=0 go build -o clrd ./server/cmd/clrd
./clrd
```

No settings are needed. The first log line says where the database went.

## First run

With no admin token in the database, clrd logs a one-time link:

```
level=INFO msg="no admin token yet: open this link once to create one (or run `clrd token create`)" url=https://clrd.example.com/setup?code=...
```

Open it, name the device, and keep the token it shows: it is shown once.
The link stops working as soon as any admin token exists. Anyone who can read
the server log before you finish setup can use that link, so finish setup
right after the first start.

On a headless box you can skip the link and make the token from the CLI
instead (same database, so run it where the server runs):

```sh
docker compose exec clrd clrd token create --name me --scope admin --approve allowed
```

`--approve allowed` lets this token, and the phones and browsers you link
with it, answer approval requests such as an agent's Allow or Deny prompt.
Without it the token is denied, like every new token except the one the
setup link makes.

## Tokens

Every phone, browser and script gets its own token, so a lost phone is one
revoke and every dismissal shows which device did it.

```sh
clrd token create --name pixel-9 --scope read,publish
clrd token create --name backup-job --scope publish --topics backups
clrd token list
```

Scopes: `read` (see and act on notifications), `publish` (send), `admin`
(manage tokens). `--topics` limits a token to those topics. Revoke with
`DELETE /api/v1/tokens/{id}` (ids are in `GET /api/v1/tokens`); open streams for that token close
at once.

`POST /api/v1/tokens` with an admin token makes one over the API. Instead of
`topics` it takes per-topic `grants`, first match wins, for example
`{"name":"cam","scopes":["read","publish"],"grants":[{"topic":"home-*","scopes":["publish"]},{"topic":"alerts","scopes":["read"]}]}`.

### Users: one person, many devices

Optional. A user is one person who owns several tokens, so you set their
topics once and every phone, browser and computer they use follows. With an
admin token:

```sh
# make a user who may read and publish on family and home-*
curl -H "Authorization: Bearer $ADMIN" -d '{"name":"Ann","grants":[{"topic":"family","scopes":["read","publish"]},{"topic":"home-*","scopes":["read"]}]}' https://clrd.example.com/api/v1/users
# give her a new phone token, or attach one she already has
curl -H "Authorization: Bearer $ADMIN" -d '{"name":"ann-phone","scopes":["read","publish"],"user":"<user id>"}' https://clrd.example.com/api/v1/tokens
curl -X PUT -H "Authorization: Bearer $ADMIN" -d '{"user":"<user id>"}' https://clrd.example.com/api/v1/tokens/<token id>/user
```

Change her topics later with `PUT /api/v1/users/{id}` and `{"grants":[...]}`:
every device she has, including browsers already signed in, follows at once.
To share a topic with someone else, give their user a grant on it (read,
publish or both). A token's own scopes stay its limit, a user grant never
gives admin, and an admin token keeps full admin. `{"grants":null}` means
the user limits nothing and each device keeps its own access. Tokens with no
user work exactly as before. The same tools are in MCP: `users`,
`create_user`, `set_user_grants`, `set_token_user`, `delete_user`.

### Users and tokens in a config file

Optional. Set `CLRD_CONFIG` to a JSON file and every start makes the users
and tokens in it match the file, so a server can be rebuilt from config:

```json
{
  "users": [{"name": "Ann", "grants": [{"topic": "family", "scopes": ["read", "publish"]}]}],
  "tokens": [
    {"name": "admin", "secret": "$CLRD_ADMIN_SECRET", "scopes": ["admin"], "approve": "allowed"},
    {"name": "ann-phone", "secret": "$ANN_PHONE_SECRET", "scopes": ["read", "publish"], "user": "Ann"}
  ]
}
```

A token is its secret: the value a device sends, at least 20 characters
(`openssl rand -base64 30` makes one). `"$NAME"` reads it from the
environment variable `NAME`, so the file itself holds no secret. Each token
takes the same fields as `POST /api/v1/tokens`, plus `approve` and `user`
(a user name from the file). A token already made on the API is taken over
when the file names its secret. Tokens and users the file made and no
longer lists are removed at the next start, with every browser signed in
with them; ones made on the API are left alone. The file wins over API edits
to its rows at every start. A file with a mistake (an unknown field, a short
secret, a user it does not list) stops the server with the reason, and
changes nothing. With an admin token in the file, no setup link is printed.

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `CLRD_LISTEN` | `:8080` | Address to listen on. |
| `CLRD_DB` | `/data/clrd.db` if it exists, else `~/.local/share/clrd/clrd.db` (or under `$XDG_DATA_HOME`) | SQLite file. Its folder must be writable (WAL files live next to it). |
| `CLRD_BASE_URL` | unset: links use the address the browser opened clrd on | Public URL, used in the setup and phone pairing links. Set it to your https URL behind a proxy. A path in it (`https://example.com/clrd`) serves everything under that path. |
| `CLRD_RETENTION` | `720h` | How long dismissed and resolved rows, and the event log, are kept. This is the starting value: once someone sets "Keep cleared messages" in Settings (or `keep_cleared_days` over the API or MCP), that setting wins, and a topic can have its own value (in Settings under "By topic", or `topic_keep_cleared_days`). Open and snoozed messages are never deleted. |
| `CLRD_ATTACHMENTS` | `attachments` next to the database | Folder for uploaded files. |
| `CLRD_PUBLIC_PAGES` | `on` | `off` hides the landing page and docs on a private server: `/` goes to the inbox, `/docs` is 404, and `robots.txt` asks every crawler to stay out. |
| `CLRD_ATTACHMENT_MAX_MB` | `15` | Largest file one notification can carry, in MB. |
| `CLRD_ATTACHMENT_TOTAL_MB` | `5000` | Space all stored files may use together, in MB. Uploads past it get 507. |
| `CLRD_RATE_TOKEN` | `60` | Publishes a minute one token may send, with a burst of the same size. Past it gets 429 with `Retry-After`. `0` turns it off. |
| `CLRD_RATE_TOPIC` | `60` | Publishes a minute one topic may take from all tokens together. `0` turns it off. |
| `CLRD_TRUSTED_PROXIES` | unset | Comma-separated CIDRs or addresses of your reverse proxies, for example `127.0.0.1,172.16.0.0/12`. Only these may set the client address with `X-Forwarded-For`, and the link scheme with `X-Forwarded-Proto`. Unset, any loopback or private-network peer may. |
| `CLRD_CONFIG` | unset | A JSON file of users and tokens applied at every start. See [Users and tokens in a config file](#users-and-tokens-in-a-config-file). |

`GET /api/v1/server` with an admin token returns every one of these as in
effect, defaults filled in, so an agent can check a setup. They change only by
setting the variable and restarting.

## HTTPS and the reverse proxy

Phones need HTTPS. The proxy must pass WebSocket upgrades and must not buffer
streams. clrd sends a keepalive every 30 seconds, so idle timeouts of a
minute or more are fine.

Using your own certificate authority? Install its CA certificate on the phone
(Settings, Security, Encryption and credentials, Install a certificate, CA
certificate). The Android app trusts CAs installed that way.

Caddy (handles certificates and WebSockets on its own):

```
clrd.example.com {
	reverse_proxy localhost:8080
}
```

clrd limits wrong passwords per address. When the request comes from
loopback or a private network it takes the address from the last
`X-Forwarded-For` hop, which Caddy sends by default and the nginx block below
sets. If other machines on your private network can reach clrd directly, set
`CLRD_TRUSTED_PROXIES` to your proxy's address so they cannot fake theirs.

nginx (the `map` goes in the `http` block):

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_buffering off;
    proxy_read_timeout 1h;
}
```

### Under a sub-path

To serve clrd at `https://example.com/clrd/` instead of its own host name, put
the path in `CLRD_BASE_URL` (`https://example.com/clrd`). Every page, link,
asset, API route and the WebSocket then live under `/clrd/`, and the sign-in
cookie is scoped to it. The proxy can pass the path through or strip it; both
work. The path cannot start with `/api`. In Caddy, `handle /clrd/*` with the same `reverse_proxy` line; in nginx,
the same block as `location /clrd/`. In the Android app, enter the full
address with the path.

## Health

`GET /healthz` returns `{"ok":true,"seq":N}`. The Docker image runs
`clrd health` as its health check. Point your uptime monitor at `/healthz`.

## Backups

All state is the SQLite file plus the `attachments` folder next to it. Stop
clrd, copy `clrd.db` together with any `clrd.db-wal` and `clrd.db-shm` next to
it and the `attachments` folder, start clrd. Or, with the `sqlite3`
tool on the host, take a live copy:

```sh
sqlite3 /path/to/clrd.db ".backup /path/to/clrd-backup.db"
cp -r /path/to/attachments /path/to/attachments-backup
```

A file replaced between the two copies can be missing from the backup; stop
clrd first if you need an exact copy.

Phones and browsers keep their last sequence number. If you restore an older
backup, stop clrd first and restore over the old `clrd.db`, leaving the small
`clrd.db.epoch` file next to it in place. clrd then sees the database went back
in time and tells every client to drop its local copy and resync.

## Coming from ntfy

clrd speaks ntfy's publish and subscribe API, so existing scripts, the `ntfy`
CLI and webhook senders keep working. Change the server URL and use a clrd
token:

```sh
curl -u :$CLRD_TOKEN -H "Title: Backup failed" -d "nas-1: exit 2" https://clrd.example.com/backups
ntfy publish --token $CLRD_TOKEN https://clrd.example.com/backups "nas-1: exit 2"
ntfy subscribe --token $CLRD_TOKEN https://clrd.example.com/backups
```

Send `X-Id: <key>` (or publish to `/{topic}/{key}`) to update one
notification instead of stacking new ones, and `Status: resolved` with the
same key to clear it everywhere when the problem goes away. Alertmanager and
Grafana can post straight to `https://clrd.example.com/<topic>?template=alertmanager`:
firing alerts appear, resolved alerts clear.

Files work: send one with `X-Filename` (or `PUT` it as the body), or link one
with `X-Attach`. Icons work with `X-Icon`.

Anonymous topics are off until an admin sets them, with
`PUT /api/v1/ntfy/anonymous` or the MCP tool `set_ntfy_anonymous`, or
`clrd import ntfy` brings over ntfy's `everyone` rules
([Switch from ntfy](migrate-from-ntfy.md)).

Not supported yet: email and phone call forwarding, `Cache: no` (refused, since clrd stores every notification
to sync it), and `http`/`broadcast` action buttons (view actions work).

## Upgrading

Pull, rebuild, restart. Take a backup first.

The full API is at `/openapi.json` on your server.
