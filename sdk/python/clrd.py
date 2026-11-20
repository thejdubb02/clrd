"""Tiny client for clrd (https://github.com/thejdubb02/clrd). Stdlib only.

    import clrd
    clrd.send("infra", "Disk 91%", id="disk-build")
    clrd.resolve("infra", "disk-build")

Reads CLRD_URL and CLRD_TOKEN from the environment, or use
clrd.Client(url, token). There is no default server.
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

__version__ = "0.1.0"
__all__ = ["Client", "ClrdError", "send", "resolve", "ack", "snooze", "dismiss", "list", "get"]

_q = urllib.parse.quote


class ClrdError(Exception):
    def __init__(self, status, message, code=None):
        super().__init__(f"{status}: {message}")
        self.status, self.message, self.code = status, message, code


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    # Never follow redirects: urllib would resend the Bearer token to the new host.
    def redirect_request(self, *_):
        return None


_open = urllib.request.build_opener(_NoRedirect).open


class Client:
    def __init__(self, url=None, token=None, timeout=10):
        self.url = (url or os.environ.get("CLRD_URL") or "").rstrip("/")
        self.token = token if token is not None else os.environ.get("CLRD_TOKEN")
        self.timeout = timeout

    def _req(self, method, path, body=None):
        if not self.url:
            raise ClrdError(0, "no server URL: set CLRD_URL or pass url")
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.url + path, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        req.add_header("User-Agent", "clrd-python/" + __version__)
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with _open(req, timeout=self.timeout) as r:
                raw = r.read()
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                err = json.loads(raw)
                raise ClrdError(e.code, err.get("error", raw.decode()), err.get("code")) from None
            except (ValueError, AttributeError):
                raise ClrdError(e.code, raw.decode(errors="replace")) from None
        return json.loads(raw) if raw else None

    def send(self, topic, message, title=None, priority=None, id=None, tags=None, click=None, actions=None):
        """Publish. id is your stable key (external_id): sending again with it
        updates the open notification, resolve(topic, id) clears it."""
        body = {"topic": topic, "message": message}
        for k, v in (("title", title), ("priority", priority), ("external_id", id),
                     ("tags", tags), ("click", click), ("actions", actions)):
            if v is not None:
                body[k] = v
        return self._req("POST", "/api/v1/notifications", body)

    def resolve(self, topic_or_id, external_id=None):
        """resolve(topic, external_id) or resolve(server_id)."""
        if external_id is not None:
            return self._req("POST", f"/{_q(topic_or_id, safe='')}/{_q(external_id, safe='')}/resolve")
        return self._state(topic_or_id, "resolve")

    def ack(self, topic_or_id, external_id=None):
        """ack(topic, external_id) or ack(server_id)."""
        if external_id is not None:
            return self._req("POST", f"/{_q(topic_or_id, safe='')}/{_q(external_id, safe='')}/ack")
        return self._state(topic_or_id, "ack")

    def dismiss(self, id):
        return self._state(id, "dismiss")

    def snooze(self, id, duration="1h"):
        return self._req("POST", f"/api/v1/notifications/{_q(id, safe='')}/snooze", {"for": duration})

    def get(self, id):
        return self._req("GET", f"/api/v1/notifications/{_q(id, safe='')}")

    def list(self, topic=None, state="open", limit=None):
        q = {"state": state}
        if topic:
            q["topic"] = topic
        if limit:
            q["limit"] = limit
        return self._req("GET", "/api/v1/notifications?" + urllib.parse.urlencode(q))

    def _state(self, id, action):
        return self._req("POST", f"/api/v1/notifications/{_q(id, safe='')}/{action}")


_default = None


def _client():
    global _default
    if _default is None:
        _default = Client()
    return _default


def send(*a, **kw): return _client().send(*a, **kw)
def resolve(*a, **kw): return _client().resolve(*a, **kw)
def ack(*a, **kw): return _client().ack(*a, **kw)
def dismiss(*a, **kw): return _client().dismiss(*a, **kw)
def snooze(*a, **kw): return _client().snooze(*a, **kw)
def get(*a, **kw): return _client().get(*a, **kw)
def list(*a, **kw): return _client().list(*a, **kw)  # noqa: A001 (shadows builtin by design: clrd.list)


def main(argv=None):
    p = argparse.ArgumentParser(prog="clrd", description="Send and clear clrd notifications. Uses CLRD_URL and CLRD_TOKEN.")
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("send", help="send a notification")
    s.add_argument("topic")
    s.add_argument("message")
    s.add_argument("-t", "--title")
    s.add_argument("-p", "--priority", type=int, choices=range(1, 6))
    s.add_argument("--id", help="stable key, so resolve can clear it later")
    for name in ("resolve", "ack"):
        r = sub.add_parser(name, help=f"{name} by server id, or by topic and key")
        r.add_argument("topic_or_id")
        r.add_argument("key", nargs="?")
    d = sub.add_parser("dismiss", help="dismiss by server id")
    d.add_argument("id")
    z = sub.add_parser("snooze", help="snooze by server id")
    z.add_argument("id")
    z.add_argument("duration", nargs="?", default="1h")
    ls = sub.add_parser("list", help="list notifications")
    ls.add_argument("topic", nargs="?")
    ls.add_argument("--state", default="open")
    a = p.parse_args(argv)
    c = Client()
    try:
        if a.cmd == "send":
            out = c.send(a.topic, a.message, title=a.title, priority=a.priority, id=a.id)
        elif a.cmd in ("resolve", "ack"):
            out = getattr(c, a.cmd)(a.topic_or_id, a.key)
        elif a.cmd == "dismiss":
            out = c.dismiss(a.id)
        elif a.cmd == "snooze":
            out = c.snooze(a.id, a.duration)
        else:
            out = c.list(a.topic, a.state)
    except (ClrdError, urllib.error.URLError) as e:
        print(f"clrd: {e}", file=sys.stderr)
        return 1
    print(json.dumps(out, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
