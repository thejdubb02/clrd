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
docker run -d --name clrd -p 8080:8080 -v clrd-data:/data \
  -e CLRD_BASE_URL=https://clrd.example.com clrd
```

Or the binary (Go 1.23+, no C compiler needed):

```sh
CGO_ENABLED=0 go build -o clrd ./server/cmd/clrd
CLRD_DB=./clrd.db ./clrd serve
```

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
docker compose exec clrd clrd token create --name me --scope admin
```

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

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `CLRD_LISTEN` | `:8080` | Address to listen on. |
| `CLRD_DB` | `/data/clrd.db` | SQLite file. Its folder must be writable (WAL files live next to it). |
| `CLRD_BASE_URL` | `http://localhost:<port>` | Public URL, used in the setup link. Set it to your https URL. |
| `CLRD_RETENTION` | `720h` | How long dismissed and resolved rows, and the event log, are kept. |

## HTTPS and the reverse proxy

Phones need HTTPS. The proxy must pass WebSocket upgrades and must not buffer
streams. clrd sends a keepalive every 30 seconds, so idle timeouts of a
minute or more are fine.

Caddy (handles certificates and WebSockets on its own):

```
clrd.example.com {
	reverse_proxy localhost:8080
}
```

nginx:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;  # map $http_upgrade in the http block
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_read_timeout 1h;
}
```

## Health

`GET /healthz` returns `{"ok":true,"seq":N}`. The Docker image runs
`clrd health` as its health check. Point your uptime monitor at `/healthz`.

## Backups

All state is the SQLite file. Stop clrd, copy `clrd.db` together with any
`clrd.db-wal` and `clrd.db-shm` next to it, start clrd. Or, with the `sqlite3`
tool on the host, take a live copy:

```sh
sqlite3 /path/to/clrd.db ".backup /path/to/clrd-backup.db"
```

Phones keep their last sequence number. If you restore an older backup, they
notice the server is behind them, drop their local copy and resync.

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

Not supported yet: attachments, icons, email and phone call forwarding,
anonymous topics, and `http`/`broadcast` action buttons (view actions work).

## Upgrading

Pull, rebuild, restart. Take a backup first.

The full API is at `/openapi.json` on your server.
