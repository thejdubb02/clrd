import contextlib
import io
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

import clrd

seen = []


class Fake(BaseHTTPRequestHandler):
    def _do(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(n)) if n else None
        seen.append((self.command, self.path, body, self.headers.get("Authorization")))
        if self.path.startswith("/redirect"):
            self.send_response(302)
            self.send_header("Location", "/elsewhere")
            out = {}
        elif self.path.startswith("/missing"):
            self.send_response(404)
            out = {"error": "no open notification", "code": "not_found"}
        else:
            self.send_response(200)
            out = {"id": "01J", "state": "active"}
            if self.command == "GET" and self.path.startswith("/api/v1/notifications?"):
                out = {"notifications": [out]}
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(out).encode())

    do_GET = do_POST = _do

    def log_message(self, *_):
        pass


class T(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv = HTTPServer(("127.0.0.1", 0), Fake)
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()
        cls.url = f"http://127.0.0.1:{cls.srv.server_port}"
        clrd._default = clrd.Client(cls.url, "clrd_test")

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()

    def last(self):
        return seen[-1]

    def test_send_uses_external_id(self):
        self.assertEqual(clrd.send("infra", "Disk 91%", id="disk-build", priority=4)["id"], "01J")
        self.assertEqual(self.last(), ("POST", "/api/v1/notifications",
                                       {"topic": "infra", "message": "Disk 91%", "priority": 4, "external_id": "disk-build"},
                                       "Bearer clrd_test"))

    def test_resolve_and_ack(self):
        clrd.resolve("infra", "disk/build")
        self.assertEqual(self.last()[:2], ("POST", "/infra/disk%2Fbuild/resolve"))
        clrd.resolve("01J")
        self.assertEqual(self.last()[:2], ("POST", "/api/v1/notifications/01J/resolve"))
        clrd.ack("infra", "disk-build")
        self.assertEqual(self.last()[:2], ("POST", "/infra/disk-build/ack"))
        clrd.ack("01J")
        self.assertEqual(self.last()[:2], ("POST", "/api/v1/notifications/01J/ack"))

    def test_snooze_dismiss_list(self):
        clrd.snooze("01J", "30m")
        self.assertEqual(self.last()[:3], ("POST", "/api/v1/notifications/01J/snooze", {"for": "30m"}))
        clrd.dismiss("01J")
        self.assertEqual(self.last()[:2], ("POST", "/api/v1/notifications/01J/dismiss"))
        self.assertEqual(clrd.list("infra"), {"notifications": [{"id": "01J", "state": "active"}]})
        self.assertEqual(self.last()[:2], ("GET", "/api/v1/notifications?state=open&topic=infra"))

    def test_error(self):
        c = clrd.Client(self.url + "/missing", "clrd_test")
        with self.assertRaises(clrd.ClrdError) as e:
            c.resolve("infra", "nope")
        self.assertEqual((e.exception.status, e.exception.code), (404, "not_found"))

    def test_no_redirect(self):
        c = clrd.Client(self.url + "/redirect", "clrd_test")
        with self.assertRaises(clrd.ClrdError) as e:
            c.resolve("infra", "x")
        self.assertEqual(e.exception.status, 302)
        self.assertFalse(any(p == "/elsewhere" for _, p, _, _ in seen))

    def test_cli(self):
        import os
        os.environ.update(CLRD_URL=self.url, CLRD_TOKEN="clrd_cli")
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(clrd.main(["send", "infra", "hi", "--id", "k", "-p", "5"]), 0)
        self.assertEqual(self.last(), ("POST", "/api/v1/notifications",
                                       {"topic": "infra", "message": "hi", "priority": 5, "external_id": "k"}, "Bearer clrd_cli"))
        with contextlib.redirect_stdout(io.StringIO()):
            clrd.main(["resolve", "infra", "k"])
        self.assertEqual(self.last()[:2], ("POST", "/infra/k/resolve"))
        os.environ["CLRD_URL"] = self.url + "/missing"
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(clrd.main(["resolve", "infra", "k"]), 1)


if __name__ == "__main__":
    unittest.main()
