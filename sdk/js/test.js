import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import { Client, ClrdError } from "./index.js";

const seen = [];
const srv = createServer((req, res) => {
  let raw = "";
  req.on("data", (d) => (raw += d));
  req.on("end", () => {
    seen.push({ method: req.method, url: req.url, body: raw ? JSON.parse(raw) : null, auth: req.headers.authorization });
    const missing = req.url.startsWith("/missing");
    res.writeHead(missing ? 404 : 200, { "Content-Type": "application/json" });
    const n = { id: "01J", state: "active" };
    const list = req.method === "GET" && req.url.startsWith("/api/v1/notifications?");
    res.end(JSON.stringify(missing ? { error: "no open notification", code: "not_found" } : list ? { notifications: [n] } : n));
  });
});
let c, url;
const last = () => seen.at(-1);
const call = () => `${last().method} ${last().url}`;

before(async () => {
  await new Promise((r) => srv.listen(0, "127.0.0.1", r));
  url = `http://127.0.0.1:${srv.address().port}`;
  c = new Client({ url, token: "clrd_test" });
});
after(() => srv.close());

test("send maps id to external_id", async () => {
  assert.equal((await c.send("infra", "Disk 91%", { id: "disk-build", priority: 4 })).id, "01J");
  assert.deepEqual(last(), {
    method: "POST",
    url: "/api/v1/notifications",
    body: { topic: "infra", message: "Disk 91%", priority: 4, external_id: "disk-build" },
    auth: "Bearer clrd_test",
  });
});

test("resolve and ack by key or id", async () => {
  await c.resolve("infra", "disk/build");
  assert.equal(call(), "POST /infra/disk%2Fbuild/resolve");
  await c.resolve("01J");
  assert.equal(call(), "POST /api/v1/notifications/01J/resolve");
  await c.ack("infra", "k");
  assert.equal(call(), "POST /infra/k/ack");
  await c.ack("01J");
  assert.equal(call(), "POST /api/v1/notifications/01J/ack");
});

test("snooze, dismiss, list", async () => {
  await c.snooze("01J", "30m");
  assert.deepEqual([call(), last().body], ["POST /api/v1/notifications/01J/snooze", { for: "30m" }]);
  await c.dismiss("01J");
  assert.equal(call(), "POST /api/v1/notifications/01J/dismiss");
  assert.deepEqual(await c.list({ topic: "infra" }), { notifications: [{ id: "01J", state: "active" }] });
  assert.equal(call(), "GET /api/v1/notifications?state=open&topic=infra");
});

test("mark and unmark", async () => {
  await c.mark("01J", "actioned", { note: "filed #5" });
  assert.deepEqual([call(), last().body], ["POST /api/v1/notifications/01J/mark", { mark: "actioned", note: "filed #5" }]);
  await c.unmark("01J");
  assert.equal(call(), "DELETE /api/v1/notifications/01J/mark");
});

test("no default server: a call without a url says to set CLRD_URL", async () => {
  const saved = process.env.CLRD_URL;
  delete process.env.CLRD_URL;
  try {
    await assert.rejects(new Client({ token: "clrd_test" }).send("infra", "hi"), /set CLRD_URL/);
  } finally {
    if (saved !== undefined) process.env.CLRD_URL = saved;
  }
});

test("errors carry status and code", async () => {
  const bad = new Client({ url: url + "/missing", token: "clrd_test" });
  await assert.rejects(bad.resolve("infra", "nope"), (e) => e instanceof ClrdError && e.status === 404 && e.code === "not_found");
});

test("default fetch is called unbound-safe", async () => {
  const real = globalThis.fetch;
  globalThis.fetch = function (u) {
    if (this !== undefined && this !== globalThis) throw new TypeError("Illegal invocation");
    return Promise.resolve(new Response('{"id":"01J"}'));
  };
  try {
    const n = await new Client({ url: "http://x", token: "t" }).ack("01J");
    assert.equal(n.id, "01J");
  } finally {
    globalThis.fetch = real;
  }
});

test("README links to the repo", () => {
  const readme = readFileSync(new URL("./README.md", import.meta.url), "utf8");
  assert.match(readme, /https:\/\/github\.com\/thejdubb02\/clrd/);
});
