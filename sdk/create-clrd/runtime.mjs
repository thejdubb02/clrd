import { readFile } from 'node:fs/promises';
import { createInterface } from 'node:readline';
import { request } from './connection.mjs';

const [mode, configPath] = process.argv.slice(2);
let input;

try {
  const { server, token, topic, label, phase } = JSON.parse(await readFile(configPath, 'utf8'));
  if (phase !== 'active') throw new Error('clrd connection is unavailable');
  input = createInterface({ input: process.stdin, crlfDelay: Infinity });

  if (mode === 'mcp') {
    async function handle(line) {
      let message;
      try { message = JSON.parse(line); } catch { return; }
      if (!message || message.jsonrpc !== '2.0') return;
      const hasId = Object.hasOwn(message, 'id');
      // clrd's streamable HTTP MCP endpoint is stateless and returns JSON.
      try {
        const response = await request(server, '/mcp', { token, body: line, timeout: 660_000 });
        if (!response.ok) throw new Error('MCP request failed');
        if (hasId) process.stdout.write(JSON.stringify(await response.json()) + '\n');
      } catch {
        if (hasId) {
          process.stdout.write(JSON.stringify({
            jsonrpc: '2.0', id: message.id, error: { code: -32000, message: 'clrd MCP request failed' },
          }) + '\n');
        }
      }
    }
    const pending = new Set();
    for await (const line of input) {
      const task = handle(line).finally(() => pending.delete(task));
      pending.add(task);
    }
    await Promise.allSettled(pending);
  } else if (mode === 'notify') {
    // Wait for the hook input to finish. Send only a fixed message.
    for await (const _line of input) { /* discard */ }
    await request(server, '/api/v1/notifications', {
      token,
      body: JSON.stringify({ topic, title: label, message: 'Agent turn finished or session ended.' }),
    });
  } else if (mode === 'approve') {
    let body = '';
    for await (const line of input) {
      body += line + '\n';
      if (Buffer.byteLength(body) > 1_048_576) throw new Error('Approval input is too large');
    }
    const hook = JSON.parse(body);
    if (hook.hook_event_name !== 'PermissionRequest' || typeof hook.tool_name !== 'string' ||
        !hook.tool_input || typeof hook.tool_input !== 'object') throw new Error('Invalid permission hook');
    const response = await request(server, '/mcp/approve?topic=' + encodeURIComponent(topic), {
      token, body, timeout: 595_000,
    });
    if (response.ok) {
      const value = (await response.json())?.hookSpecificOutput;
      if (value?.hookEventName === 'PermissionRequest' && ['allow', 'deny'].includes(value.decision?.behavior)) {
        const { behavior, message } = value.decision;
        process.stdout.write(JSON.stringify({
          hookSpecificOutput: { hookEventName: 'PermissionRequest', decision: {
            behavior,
            ...(typeof message === 'string' ? { message } : {}),
          } },
        }) + '\n');
      }
    }
  }
} catch {
  // Empty hook output leaves the agent's own permission prompt in charge.
  if (mode === 'mcp') process.stderr.write('clrd connection is unavailable\n');
} finally {
  input?.close();
}
