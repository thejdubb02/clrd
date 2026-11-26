import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { parse } from 'jsonc-parser';

const cli = fileURLToPath(new URL('./cli.js', import.meta.url));
const ids = ['claude', 'codex', 'gemini', 'grok', 'opencode', 'cursor', 'vscode'];
const code = (i) => String(i).repeat(22);
const token = (i) => 'clrd_' + String(i).repeat(32);

const claimData = (i, grants = ['agents', 'build'].map((topic) => ({ topic, scopes: ['read', 'publish'] }))) => ({
  token: token(i), name: 'fixture-agent', guide: { grants },
});

function run(argv, env, input = '') {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, argv, { env });
    let out = '', err = '';
    child.stdout.setEncoding('utf8').on('data', (part) => { out += part; });
    child.stderr.setEncoding('utf8').on('data', (part) => { err += part; });
    child.on('error', reject);
    child.stdin.on('error', reject);
    child.on('close', (status) => resolve({ status, out, err }));
    child.stdin.end(input);
  });
}

async function fixture(t) {
  const home = await mkdtemp(join(tmpdir(), 'create-clrd-test-'));
  const env = { HOME: home, PATH: '', XDG_CONFIG_HOME: join(home, '.config'), XDG_DATA_HOME: join(home, 'data') };
  const root = join(env.XDG_DATA_HOME, 'create-clrd');
  const state = (agent) => join(root, agent + '.json');
  const calls = [];
  const responders = new Map();
  const server = createServer(async (req, res) => {
    let text = '';
    for await (const part of req) text += part;
    const call = { path: req.url, method: req.method, auth: req.headers.authorization, body: text ? JSON.parse(text) : null };
    calls.push(call);
    const response = await responders.get(req.url)?.(call);
    if (!response) {
      res.writeHead(404).end();
      return;
    }
    res.writeHead(response.status ?? 200, { 'Content-Type': 'application/json' });
    res.end(response.data === undefined ? '' : JSON.stringify(response.data));
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const url = `http://127.0.0.1:${server.address().port}/clrd`;
  t.after(async () => {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
    await rm(home, { recursive: true, force: true });
  });
  responders.set('/clrd/api/v1/agents/claim', ({ body }) => ({
    data: claimData(body.code[0]),
  }));
  responders.set('/clrd/api/v1/devices/this', () => ({ status: 204 }));
  return {
    home, env, root, state, calls, responders, url,
    async put(path, content) {
      const file = join(home, path);
      await mkdir(dirname(file), { recursive: true });
      await writeFile(file, content);
      return file;
    },
    async setup(args = [], input = '') {
      return run([cli, url, '--no-browser', ...args], env, input);
    },
    async remove(...args) {
      return run([cli, '--remove', ...args], env);
    },
  };
}

test('detects seven agents, claims separate invites, and removes only its own entries', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  for (const folder of ['.claude', '.codex', '.gemini', '.grok', '.config/opencode', '.cursor', '.config/Code', 'Library/Application Support/Code']) {
    await mkdir(join(f.home, folder), { recursive: true });
  }
  const cursor = await f.put('.cursor/mcp.json', '{\n  // Keep this comment\n  "mcpServers": { "custom": { "command": "custom" }, },\n}\n');
  const codex = await f.put('.codex/config.toml', '# Keep TOML\nmodel = "example"\n');
  const nameOrder = ids.map((_, i) => code(i));
  nameOrder[0] = f.url + '/agent#' + code(0);
  nameOrder[1] = 'clrd://agent?' + new URLSearchParams({ server: f.url, code: code(1) });
  const installed = await f.setup(['--topic', 'build', '--phone-approvals'], nameOrder.join('\n') + '\n');
  assert.equal(installed.status, 0, installed.err);
  assert.deepEqual(f.calls.map((call) => call.body.code), ids.map((_, i) => code(i)));

  const configs = [
    ['.claude.json', 'mcpServers'], ['.gemini/settings.json', 'mcpServers'],
    ['.cursor/mcp.json', 'mcpServers'], ['.copilot/mcp-config.json', 'mcpServers'],
  ];
  for (const [file, key] of configs) {
    const cfg = parse(await readFile(join(f.home, file), 'utf8'));
    assert.equal(cfg[key].clrd.args[1], 'mcp');
  }
  assert.match(await readFile(codex, 'utf8'), /\[mcp_servers\.clrd\]/);
  assert.match(await readFile(join(f.home, '.grok/config.toml'), 'utf8'), /\[mcp_servers\.clrd\]/);
  const open = parse(await readFile(join(f.home, '.config/opencode/opencode.jsonc'), 'utf8'));
  assert.equal(open.mcp.clrd.type, 'local');
  const openHook = join(f.home, '.config/opencode/plugins/create-clrd.js');
  assert.match(await readFile(openHook, 'utf8'), /session\.idle/);
  for (const [i, agent] of ids.entries()) {
    const state = await readFile(f.state(agent), 'utf8');
    assert.equal(JSON.parse(state).token, token(i));
    assert.equal((await stat(f.state(agent))).mode & 0o777, 0o600);
    assert.doesNotMatch(installed.out + installed.err, new RegExp(token(i)));
  }
  const claudeSettings = join(f.home, '.claude/settings.json');
  const hooks = JSON.parse(await readFile(claudeSettings, 'utf8')).hooks;
  assert.equal(hooks.PermissionRequest[0].hooks[0].timeout, 600);
  hooks.Stop.push({ hooks: [{ type: 'command', command: 'keep-me' }] });
  await writeFile(claudeSettings, JSON.stringify({ hooks }));
  const rerun = await f.setup();
  assert.equal(rerun.status, 0, rerun.err);
  assert.equal(f.calls.length, 7);

  const removed = await f.remove();
  assert.equal(removed.status, 0, removed.err);
  assert.deepEqual(f.calls.slice(7).map(({ path, auth }) => [path, auth]),
    ids.map((_, i) => ['/clrd/api/v1/devices/this', 'Bearer ' + token(i)]));
  assert.equal((await stat(f.root)).mode & 0o777, 0o700);
  assert.equal(existsSync(f.state('claude')), false);
  assert.equal(existsSync(openHook), false);
  assert.equal(existsSync(join(f.root, 'runtime.mjs')), false);
  assert.match(await readFile(cursor, 'utf8'), /Keep this comment/);
  assert.deepEqual(parse(await readFile(cursor, 'utf8')).mcpServers, { custom: { command: 'custom' } });
  assert.equal(await readFile(codex, 'utf8'), '# Keep TOML\nmodel = "example"\n');
  assert.equal(JSON.parse(await readFile(claudeSettings, 'utf8')).hooks.Stop[0].hooks[0].command, 'keep-me');
});

test('rejects conflicting settings and foreign invite URLs before claiming anything', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  const original = '{"mcpServers":{"clrd":{"command":"keep-me"}}}';
  const file = await f.put('.cursor/mcp.json', original);
  const conflict = await f.setup(['--agents', 'cursor'], code(0) + '\n');
  assert.equal(conflict.status, 1);
  assert.match(conflict.err, /Existing or edited clrd configuration/);
  assert.equal(await readFile(file, 'utf8'), original);

  const foreign = await f.setup(['--agents', 'claude'], 'https://clrd.example.com/agent#' + code(1) + '\n');
  assert.equal(foreign.status, 1);
  assert.match(foreign.err, /Invite belongs to another server/);
  assert.deepEqual(f.calls, []);
});

test('recovers a claimed invite when an editor changes configuration during setup', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  const file = await f.put('.gemini/settings.json', '{ "editor": "before" }');
  f.responders.set('/clrd/api/v1/agents/claim', async () => {
    await writeFile(file, '{ "editor": "after" }');
    return { data: claimData(0) };
  });
  const first = await f.setup(['--agents', 'gemini'], code(0) + '\n');
  assert.equal(first.status, 1);
  assert.match(first.err, /File changed during setup/);
  assert.equal(JSON.parse(await readFile(f.state('gemini'), 'utf8')).phase, 'pending');
  const next = await f.setup(['--agents', 'gemini']);
  assert.equal(next.status, 0, next.err);
  assert.equal(JSON.parse(await readFile(f.state('gemini'), 'utf8')).phase, 'active');
  const current = JSON.parse(await readFile(file, 'utf8'));
  assert.equal(current.editor, 'after');
  assert.ok(current.mcpServers.clrd);
  assert.equal(f.calls.length, 1);
});

test('runtime keeps tokens off command lines and handles MCP, notifications and explicit approvals', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  const installed = await f.setup(['--agents', 'claude', '--phone-approvals'], code(2) + '\n');
  assert.equal(installed.status, 0, installed.err);
  const { args } = JSON.parse(await readFile(join(f.home, '.claude.json'), 'utf8')).mcpServers.clrd;
  assert.deepEqual(args, [join(f.root, 'runtime.mjs'), 'mcp', f.state('claude')]);
  f.responders.set('/clrd/mcp', ({ body }) => Object.hasOwn(body, 'id')
    ? { data: { jsonrpc: '2.0', id: body.id, result: { tools: [{ name: 'send' }] } } }
    : { status: 202 });
  const mcp = await run(args, f.env, '{"jsonrpc":"2.0","method":"notifications/initialized"}\n{"jsonrpc":"2.0","id":"request","method":"tools/list"}\n');
  assert.deepEqual(JSON.parse(mcp.out), { jsonrpc: '2.0', id: 'request', result: { tools: [{ name: 'send' }] } });
  assert.ok(f.calls.filter((call) => call.path === '/clrd/mcp').every((call) => call.auth === 'Bearer ' + token(2)));

  const send = {
    jsonrpc: '2.0', id: 'send', method: 'tools/call',
    params: { name: 'send', arguments: {
      topic: 'agents', message: 'Finished', id: 'build-42',
      icon: 'https://example.com/build.png',
      attachment: { url: 'https://example.com/report.pdf', name: 'Report.pdf' },
      actions: [{ label: 'Copy ID', copy: 'build-42' }],
    } },
  };
  const io = await run(args, f.env, JSON.stringify(send) + '\n');
  assert.equal(io.status, 0, io.err);
  assert.equal(JSON.parse(io.out).id, 'send');
  assert.deepEqual(f.calls.at(-1).body, send);

  f.responders.set('/clrd/api/v1/notifications', () => ({ data: {} }));
  const hookInput = '{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"example"}}\n';
  await run([args[0], 'notify', args[2]], f.env, hookInput);
  assert.deepEqual(f.calls.at(-1).body, {
    topic: 'agents', title: 'Claude Code', message: 'Agent turn finished or session ended.',
  });
  f.responders.set('/clrd/mcp/approve?topic=agents', () => ({ data: { decision: 'allow' } }));
  assert.equal((await run([args[0], 'approve', args[2]], f.env, hookInput)).out, '');
  const answer = { hookSpecificOutput: { hookEventName: 'PermissionRequest', decision: { behavior: 'deny' } } };
  f.responders.set('/clrd/mcp/approve?topic=agents', () => ({ data: answer }));
  assert.deepEqual(JSON.parse((await run([args[0], 'approve', args[2]], f.env, hookInput)).out), answer);
  assert.deepEqual(f.calls.at(-1).body, JSON.parse(hookInput));
});

test('retains the connection for retry when the server cannot revoke it', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  const installed = await f.setup(['--agents', 'codex', '--no-notifications'], code(0) + '\n');
  assert.equal(installed.status, 0, installed.err);
  f.responders.set('/clrd/api/v1/devices/this', () => ({ status: 503 }));
  const first = await f.remove('--agents', 'codex');
  assert.equal(first.status, 1);
  assert.match(first.err, /could not unlink.*503/);
  assert.ok(existsSync(f.state('codex')));
  assert.match(await readFile(join(f.home, '.codex/config.toml'), 'utf8'), /mcp_servers\.clrd/);
  f.responders.set('/clrd/api/v1/devices/this', () => ({ status: 401, data: { code: 'unauthorized' } }));
  const next = await f.remove('--agents', 'codex');
  assert.equal(next.status, 0, next.err);
  assert.equal(existsSync(f.state('codex')), false);
  assert.equal(existsSync(join(f.home, '.codex/config.toml')), false);
});

test('uses the selected Claude profile without touching the default', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  const original = '{"mcpServers":{"clrd":{"command":"keep-default"}}}';
  const defaultFile = await f.put('.claude.json', original);
  f.env.CLAUDE_CONFIG_DIR = join(f.home, 'claude-alternate');
  const installed = await f.setup(['--agents', 'claude'], code(0) + '\n');
  assert.equal(installed.status, 0, installed.err);
  assert.equal(await readFile(defaultFile, 'utf8'), original);
  const selected = JSON.parse(await readFile(join(f.env.CLAUDE_CONFIG_DIR, '.claude.json'), 'utf8'));
  assert.equal(selected.mcpServers.clrd.args[2], f.state('claude'));
});

test('refuses to move a key from an edited TOML block into another table', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  const config = await f.put('.codex/config.toml', '[mcp_servers.other]\ncommand = "other"\n');
  const installed = await f.setup(['--agents', 'codex'], code(0) + '\n');
  assert.equal(installed.status, 0, installed.err);
  const edited = await readFile(config, 'utf8') + 'enabled = false\n';
  await writeFile(config, edited);
  const removed = await f.remove('--agents', 'codex');
  assert.equal(removed.status, 1);
  assert.match(removed.err, /Existing or edited clrd configuration/);
  assert.equal(await readFile(config, 'utf8'), edited);
  assert.equal(f.calls.length, 1);
});

test('rejects reserved topic names before prompting for invites', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  const result = await f.setup(['--agents', 'claude', '--topic', 'ApI'], code(0) + '\n');
  assert.equal(result.status, 1);
  assert.match(result.err, /reserved by clrd/);
  assert.deepEqual(f.calls, []);
});

test('validates the invite grant before adding hooks', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  let grants = [{ topic: 'other', scopes: ['read', 'publish'] }];
  f.responders.set('/clrd/api/v1/agents/claim', () => ({ data: claimData(0, grants) }));
  const setup = ['--agents', 'claude', '--topic', 'build-daily'];
  const wrongTopic = await f.setup(setup, code(0) + '\n');
  assert.equal(wrongTopic.status, 1);
  assert.match(wrongTopic.err, /needs publish on "build-daily"/);

  grants = [{ topic: 'build-*', scopes: ['publish'] }];
  const publishOnly = await f.setup([...setup, '--phone-approvals'], code(1) + '\n');
  assert.equal(publishOnly.status, 1);
  assert.match(publishOnly.err, /needs read on "build-daily"/);
  assert.equal(existsSync(join(f.home, '.claude.json')), false);
  assert.equal(existsSync(join(f.root, 'runtime.mjs')), false);
  assert.deepEqual(f.calls.map(({ method }) => method), ['POST', 'DELETE', 'POST', 'DELETE']);

  const accepted = await f.setup(setup, code(2) + '\n');
  assert.equal(accepted.status, 0, accepted.err);
  assert.ok(existsSync(f.state('claude')));
});

test('skipping or conflicting with every agent leaves no runtime files', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  await f.put('.claude.json', '{"mcpServers":{"clrd":{"command":"keep"}}}');
  const result = await f.setup(['--agents', 'claude,codex'], '\n');
  assert.equal(result.status, 1);
  assert.match(result.out, /Skipped Codex CLI/);
  assert.deepEqual(f.calls, []);
  assert.equal(existsSync(join(f.root, 'runtime.mjs')), false);
  assert.equal(existsSync(join(f.root, 'connection.mjs')), false);
});

test('signal cancellation stops prompts and releases the lock', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  const child = spawn(process.execPath, [cli, f.url, '--no-browser', '--agents', 'claude,codex'], { env: f.env });
  t.after(() => child.kill());
  let stderr = '';
  child.stderr.setEncoding('utf8').on('data', (part) => {
    const wasPrompted = stderr.includes('Create an invite for Claude Code');
    stderr += part;
    if (!wasPrompted && stderr.includes('Create an invite for Claude Code')) child.kill('SIGINT');
  });
  const [status, signal] = await once(child, 'close');
  assert.equal(signal, null);
  assert.equal(status, 130);
  assert.match(stderr, /Setup cancelled/);
  assert.doesNotMatch(stderr, /Create an invite for Codex/);
  assert.equal(existsSync(join(f.root, 'install.lock')), false);
  assert.deepEqual(f.calls, []);
});

test('warns about changed options for a connected agent', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  const args = ['--agents', 'claude'];
  assert.equal((await f.setup(args, code(0) + '\n')).status, 0);
  const initial = await readFile(f.state('claude'), 'utf8');
  const runtimePath = join(f.root, 'runtime.mjs');
  const runtime = await readFile(runtimePath, 'utf8');
  await writeFile(runtimePath, '// old helper');
  const rerun = await f.setup([...args, '--topic', 'build', '--no-notifications']);
  assert.equal(rerun.status, 0, rerun.err);
  assert.match(rerun.err, /Existing settings were kept/);
  assert.equal(await readFile(f.state('claude'), 'utf8'), initial);
  assert.equal(await readFile(runtimePath, 'utf8'), runtime);
  assert.equal(f.calls.length, 1);
});

test('MCP startup exits with failure when the connection is missing', { timeout: 10000 }, async (t) => {
  const f = await fixture(t);
  const runtime = fileURLToPath(new URL('./runtime.mjs', import.meta.url));
  const result = await run([runtime, 'mcp', f.state('claude')], f.env);
  assert.equal(result.status, 1);
  assert.match(result.err, /clrd connection is unavailable/);
  assert.equal(result.out, '');
});
