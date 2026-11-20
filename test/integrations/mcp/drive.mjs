// Drives the MCP server from a Claude Desktop style config (argv[2]) the way
// Claude Desktop does: spawn command + args with env, speak JSON-RPC on stdio.
// Prints one JSON line per tool call result for the case to check.
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { createInterface } from "node:readline";

const srv = JSON.parse(readFileSync(process.argv[2], "utf8")).mcpServers.clrd;
const child = spawn(srv.command, srv.args, { env: { ...process.env, ...srv.env }, stdio: ["pipe", "pipe", "inherit"] });
const waiting = new Map();
let next = 1;
createInterface({ input: child.stdout }).on("line", (l) => {
  const m = JSON.parse(l);
  if (m.id && waiting.has(m.id)) { waiting.get(m.id)(m); waiting.delete(m.id); }
});
const rpc = (method, params) => new Promise((ok) => {
  const id = next++;
  waiting.set(id, ok);
  child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
});
const call = async (name, args) => {
  const m = await rpc("tools/call", { name, arguments: args });
  if (m.error || m.result.isError) throw new Error(`${name}: ${JSON.stringify(m)}`);
  const out = JSON.parse(m.result.content[0].text);
  console.log(JSON.stringify({ tool: name, out }));
  return out;
};
const api = (path, body) => fetch(process.env.CLRD + path, { method: "POST", body: JSON.stringify(body),
  headers: { authorization: "Bearer " + process.env.CLRD_TOKEN, "content-type": "application/json" } });

await rpc("initialize", { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "clrd-it", version: "1" } });
child.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n");
const tools = (await rpc("tools/list", {})).result.tools.map((t) => t.name).sort();
console.log(JSON.stringify({ tool: "tools/list", out: tools }));

// The guide's example: ask, a human taps Yes while the agent waits.
const sent = await call("send", JSON.parse(process.env.SEND));
await call("list", { topic: "agents" });
const waited = call("wait_for_reply", { id: sent.id, timeout_s: 30 });
setTimeout(() => api(`/api/v1/notifications/${sent.id}/reply`, { reply: "yes" }), 1000);
await waited;

// Auto-resolve: send with an id, resolve by topic + external_id.
await call("send", { topic: "agents", message: "Migrating", id: "job-2" });
await call("resolve", { topic: "agents", external_id: "job-2" });

// ack and snooze by server id.
const a = await call("send", { topic: "agents", message: "Disk full", id: "job-3" });
await call("ack", { id: a.id });
const z = await call("send", { topic: "agents", message: "Later", id: "job-4" });
await call("snooze", { id: z.id, for: "1h" });

// A progress line edited in place, then read back.
const p = await call("send", { topic: "agents", message: "Step 1 of 3", id: "job-5" });
await call("update", { id: p.id, message: "Step 3 of 3" });
await call("get", { id: p.id });
await call("topics", {});
child.kill();
