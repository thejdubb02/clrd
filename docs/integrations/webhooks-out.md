# Outgoing webhooks

clrd can tell another service when a human answers. Save a webhook on a
topic and clrd POSTs to its URL every time a notification there is
dismissed, acknowledged, resolved, snoozed, or has a reply button tapped. A
service with no WebSocket client still learns what happened.

## Saving one

Webhooks are managed with an admin token, because a webhook sends a topic's
answers to any URL you give it. In the web inbox, signed in with an admin
token, Settings > Webhooks adds, edits and deletes them and shows each one's
last 10 deliveries. There, leaving Secret empty on Edit keeps the saved one.

An agent with an admin token uses the MCP tools `hooks`, `set_hook`,
`delete_hook` and `hook_deliveries`. With the API:

```sh
curl -X PUT https://clrd.example.com/api/v1/topics/alerts/hooks/github \
  -H "Authorization: Bearer clrd_..." -H "Content-Type: application/json" \
  -d '{"url": "https://hooks.example.com/clrd", "events": ["resolved"], "secret": "a long random string"}'
```

- `url`: http or https. By default clrd refuses private, loopback and
  link-local addresses and ports other than 80 and 443, the same guard it
  uses to fetch favicons. For a receiver on your LAN, see
  [Receivers on your LAN](#receivers-on-your-lan).
- `events`: any of `dismissed`, `acknowledged`, `resolved`, `snoozed` and
  `action` (a tapped reply button). Leave it out for all five. A tapped reply
  also acknowledges, so a webhook on `acknowledged` gets it too.
- `secret`: optional. When set, every request carries
  `X-Clrd-Signature: sha256=<hex HMAC-SHA256 of the body>`.

Saving again replaces the webhook (send the secret again too) and turns it
back on. `GET /api/v1/topics/alerts/hooks` lists them,
`DELETE /api/v1/topics/alerts/hooks/github` removes one.

## What arrives

The body is the event exactly as the sync stream carries it:

```json
{
  "seq": 1042,
  "type": "state",
  "notification": {
    "id": "01JD3K8W2Q6V9X0YB4N5M7P1RS",
    "topic": "alerts",
    "external_id": "issue-42",
    "state": "resolved",
    "state_by": "pixel-9",
    "reply": "",
    "message": "Disk full on backup"
  }
}
```

The notification has every field the API returns; this shows a few. Headers:

- `X-Clrd-Event`: `dismissed`, `acknowledged`, `resolved`, `snoozed` or `action`.
- `X-Clrd-Delivery`: an id, the same on every retry of one delivery.
- `X-Clrd-Signature`: when the webhook has a secret.

## Your own body shape

Some receivers, like Slack and Discord, want their own JSON instead of the
event. Give the webhook a `body` template and clrd sends what it renders. It
uses the same template language as [incoming webhooks](custom-webhooks.md),
run against the event above, plus `json`, which quotes a value so a title
with a quote or a line break still makes valid JSON:

```json
{"url": "https://hooks.slack.com/services/...",
 "body": "{\"text\": {{json .notification.title}}}"}
```

For Discord, use `{"content": {{json .notification.title}}}`. The output must
be valid JSON of at most 16 KB. If it is not, nothing is sent and the
delivery log shows why; fix the template and the waiting retries use it. The
signature covers the body as sent. Leave `body` empty to send the event.
The web inbox has a Body template field on each webhook under Settings >
Webhooks; empty there sends the event too.

Try a template before saving it. This sends nothing and returns what a
delivery would POST as a string, exactly as sent, or the error it would log:

```sh
curl -X POST https://clrd.example.com/api/v1/topics/alerts/hooks/preview \
  -H "Authorization: Bearer clrd_..." -H "Content-Type: application/json" \
  -d '{"body": "{\"text\": {{json .notification.title}}}"}'
```

Without `event` it uses a sample resolved notification on the topic; pass an
event shaped like the one above to try your own. Agents use the MCP tool
`preview_hook`.

## Receivers on your LAN

To let webhooks reach a receiver like `http://192.168.1.20:8080/hook`, an
admin lists its network and its port. A dial passes when the address is
public or in a listed network, and the port is 80, 443 or listed: a LAN
receiver on port 80 needs only its network, a public one on 8080 only its
port.

```sh
curl -X PUT https://clrd.example.com/api/v1/hooks/allow \
  -H "Authorization: Bearer clrd_..." -H "Content-Type: application/json" \
  -d '{"networks": ["192.168.1.0/24"], "ports": [8080]}'
```

A single address works too, like `192.168.1.20`. Send both fields every
time; `{"networks": [], "ports": []}` turns it off again, and webhooks no
longer reach those addresses from that moment. `GET /api/v1/hooks/allow`
shows the list. Agents use the MCP tools `hook_allow` and `set_hook_allow`.
The favicon and link preview fetcher is not affected.

List only what you need. A full admin token can then make clrd POST to
those addresses, with a body it chooses, and read the status code in the
delivery log, so a leaked admin token reaches whatever you list. Never
list `127.0.0.0/8` or `169.254.0.0/16` unless the receiver lives there.

If the box runs other services on loopback or a private network and no
webhook needs them, lock the list: start clrd with `CLRD_HOOK_ALLOW=off`.
Webhooks then reach only public addresses on ports 80 and 443, any saved
list is ignored, `GET /api/v1/hooks/allow` shows `"locked": true`, and
saving one answers 403 `locked`. Only the operator can undo it, by unsetting
the variable (or setting it to `on`) and restarting.

## Retries and turning off

Any 2xx answer is a success. Anything else, a redirect or no answer within 10
seconds is a failure, and clrd tries again after 30 seconds, then doubling,
8 tries in all (about an hour). Deliveries can arrive out of order; use `seq`
to order them.

If a webhook keeps failing for 24 hours, clrd turns it off and posts a
notification to the topic saying so. Save it again to turn it back on.
Deliveries still waiting then go out once it is back on. Each try uses the
URL, secret and body template saved at that moment, so saving a fix also
fixes the retries already queued. A webhook with nothing to send stays on;
the next failure after 24 hours of failing turns it off at once.

`GET /api/v1/topics/alerts/hooks/github/deliveries` shows the last 100
deliveries: status (`pending`, `ok` or `failed`), tries, the last HTTP code
or error, and when the next try is due.

## Example: close a GitHub issue when the alert resolves

Send alerts with `external_id` set to `issue-` and the number of the GitHub
issue that tracks them, say `issue-42`. This receiver checks the signature
and closes the issue when clrd resolves the notification. It needs Python 3
only.

```python
#!/usr/bin/env python3
# Closes GitHub issue N when clrd resolves the notification with id issue-N.
import hashlib, hmac, json, os, re, urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

SECRET = os.environ["CLRD_HOOK_SECRET"].encode()
REPO = os.environ["GITHUB_REPO"]  # owner/name
TOKEN = os.environ["GITHUB_TOKEN"]  # fine-grained, Issues: read and write


class Hook(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        want = "sha256=" + hmac.new(SECRET, body, hashlib.sha256).hexdigest()
        if not hmac.compare_digest(want, self.headers.get("X-Clrd-Signature", "")):
            self.send_response(401)
            self.end_headers()
            return
        n = json.loads(body)["notification"]
        m = re.fullmatch(r"issue-(\d+)", n["external_id"])
        if n["state"] == "resolved" and m:
            req = urllib.request.Request(
                f"https://api.github.com/repos/{REPO}/issues/{m[1]}",
                method="PATCH",
                data=json.dumps({"state": "closed", "state_reason": "completed"}).encode(),
                headers={"Authorization": f"Bearer {TOKEN}", "Accept": "application/vnd.github+json"},
            )
            urllib.request.urlopen(req, timeout=5)  # an error here makes clrd retry
        self.send_response(204)
        self.end_headers()


HTTPServer(("", 8080), Hook).serve_forever()
```

Run it behind your reverse proxy at `https://hooks.example.com/clrd`, then
save the webhook as above with the same secret.
