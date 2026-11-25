"""Runs action.yml's script against a fake server. Needs bash, curl and jq.

    python3 test_action.py
"""
import http.server
import os
import pathlib
import subprocess
import textwrap
import threading
import unittest

yml = pathlib.Path(__file__).with_name("action.yml").read_text()
SCRIPT = textwrap.dedent(yml.split("run: |\n", 1)[1])


class Fake(http.server.BaseHTTPRequestHandler):
    status = 200
    hits = []

    def do_POST(self):
        Fake.hits.append(self.path)
        self.rfile.read(int(self.headers.get("Content-Length") or 0))
        self.send_response(Fake.status)
        if Fake.status == 302:
            self.send_header("Location", "https://elsewhere.example/")
        self.end_headers()
        self.wfile.write(b'{"id":"01J","state":"active"}')

    def log_message(self, *_):
        pass


class ActionTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Fake)
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()

    def run_action(self, status, **inputs):
        Fake.status, Fake.hits = status, []
        env = dict(os.environ, CLRD_URL=f"http://127.0.0.1:{self.srv.server_port}", CLRD_TOKEN="clrd_test",
                   TOPIC="ci", KEY="build-1", MESSAGE="failed", TITLE="", PRIORITY="3", RESOLVE="false")
        env.update(inputs)
        return subprocess.run(["bash", "-c", SCRIPT], env=env, capture_output=True, text=True)

    def test_send(self):
        for status, ok in [(200, True), (201, True), (302, False), (403, False), (500, False)]:
            r = self.run_action(status)
            self.assertEqual(r.returncode == 0, ok, f"{status}: {r.stdout}{r.stderr}")
            self.assertEqual(Fake.hits, ["/api/v1/notifications"])

    def test_no_url(self):
        r = self.run_action(200, CLRD_URL="")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("set url", r.stdout)
        self.assertEqual(Fake.hits, [])

    def test_resolve(self):
        for status, ok in [(200, True), (404, True), (302, False), (500, False)]:
            r = self.run_action(status, RESOLVE="true")
            self.assertEqual(r.returncode == 0, ok, f"{status}: {r.stdout}{r.stderr}")
            self.assertEqual(Fake.hits, ["/ci/build-1/resolve"])

    def test_readme_links_repo(self):
        readme = pathlib.Path(__file__).with_name("README.md").read_text()
        self.assertIn("https://github.com/thejdubb02/clrd", readme)


if __name__ == "__main__":
    unittest.main()
