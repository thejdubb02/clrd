# Outgoing webhooks

clrd can tell another service when a human answers. Save a webhook on a
topic and clrd POSTs to its URL every time a notification there is
dismissed, acknowledged, resolved, snoozed, or has a reply button tapped. A
service with no WebSocket client still learns what happened.

## Saving one

Webhooks are managed with an admin token, because a webhook sends a topic's
answers to any URL you give it.

```sh
curl -X PUT https://clrd.example.com/api/v1/topics/alerts/hooks/github \
  -H "Authorization: Bearer clrd_..." -H "Content-Type: application/json" \
  -d '{"url": "https://hooks.example.com/clrd", "events": ["resolved"], "secret": "a long random string"}'
```

- `url`: http or https. clrd refuses private, loopback and link-local
  addresses and ports other than 80 and 443, the same guard it uses to fetch
  favicons. Put a receiver on your LAN behind a public reverse proxy.
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

## Retries and turning off

Any 2xx answer is a success. Anything else, a redirect or no answer within 10
seconds is a failure, and clrd tries again after 30 seconds, then doubling,
8 tries in all (about an hour). Deliveries can arrive out of order; use `seq`
to order them.

If a webhook keeps failing for 24 hours, clrd turns it off and posts a
notification to the topic saying so. Save it again to turn it back on.

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
