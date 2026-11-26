#!/usr/bin/env node
import { spawn } from 'node:child_process';
import { mkdir, readFile, rmdir } from 'node:fs/promises';
import { homedir } from 'node:os';
import { isAbsolute, join } from 'node:path';
import { createInterface } from 'node:readline';
import { Writable } from 'node:stream';
import { parseArgs } from 'node:util';
import { agents, agentEdits, detectAgents } from './agents.js';
import { prepareEdits, privateDirectory, readTarget, replaceTarget } from './config.js';
import { request, serverURL } from './connection.mjs';

const usage = `Usage:
  create-clrd <server-url> [--agents claude,codex] [--topic agents]
      [--no-browser] [--no-notifications] [--phone-approvals]
  create-clrd --remove [--agents claude,codex]

Connects agents after you create a separate invite for each in clrd.
Run on Linux or macOS with Node.js 22 or later.`;

const dataHome = isAbsolute(process.env.XDG_DATA_HOME ?? '')
  ? process.env.XDG_DATA_HOME : join(homedir(), '.local', 'share');
const root = join(dataHome, 'create-clrd');
const runtime = join(root, 'runtime.mjs');
const runtimeFiles = ['runtime.mjs', 'connection.mjs'];
const reservedTopics = new Set(['api', 'app', 'assets', 'docs', 'healthz', 'login', 'logout', 'mcp', 'setup', 'static', 'v1']);

function openBrowser(url) {
  const opener = spawn(process.platform === 'darwin' ? 'open' : 'xdg-open', [url], {
    stdio: 'ignore',
    detached: true,
  });
  opener.on('error', () => {});
  opener.unref();
}

function invitePrompt(cancel) {
  const silent = new Writable({ write(_chunk, _encoding, callback) { callback(); } });
  const input = createInterface({
    input: process.stdin,
    output: silent,
    terminal: Boolean(process.stdin.isTTY),
    crlfDelay: Infinity,
  });
  input.on('SIGINT', cancel);
  const lines = input[Symbol.asyncIterator]();
  return {
    async read(label, topic) {
      process.stderr.write(`Create an invite for ${label} with topic "${topic}". Paste it here (hidden), or Enter to skip: `);
      const line = await lines.next();
      process.stderr.write('\n');
      return line.value?.trim();
    },
    close: () => input.close(),
  };
}

function inviteCode(value, server) {
  let code = value;
  if (value.includes(':')) {
    let url;
    try { url = new URL(value); } catch { throw new Error('Invalid agent invite'); }
    if (url.protocol === 'clrd:' && url.hostname === 'agent') {
      if (serverURL(url.searchParams.get('server')) !== server) throw new Error('Invite belongs to another server');
      code = url.searchParams.get('code');
    } else {
      if (url.href.split('#')[0] !== server + '/agent') throw new Error('Invite belongs to another server');
      code = url.hash.slice(1);
    }
  }
  if (!/^[A-Za-z0-9_-]{22}$/.test(code ?? '')) throw new Error('Invalid agent invite code');
  return code;
}

async function claim(server, value) {
  const response = await request(server, '/api/v1/agents/claim', {
    body: JSON.stringify({ code: inviteCode(value, server) }),
  });
  if (!response.ok) throw new Error(`clrd could not claim the invite (HTTP ${response.status}). Check it or ask for a fresh invite.`);
  const { token, name, guide } = await response.json();
  if (!/^clrd_[A-Za-z0-9]{32}$/.test(token ?? '')) throw new Error('clrd returned an invalid token');
  return { token, name, grants: Array.isArray(guide?.grants) ? guide.grants : [] };
}

function topicMatches(pattern, topic) {
  const [start, ...parts] = pattern.split('*');
  if (!parts.length) return start === topic;
  const end = parts.pop();
  if (!topic.startsWith(start) || !topic.endsWith(end) || topic.length < start.length + end.length) return false;
  let middle = topic.slice(start.length, topic.length - end.length);
  return parts.every((part) => {
    const index = middle.indexOf(part);
    if (index < 0) return false;
    middle = middle.slice(index + part.length);
    return true;
  });
}

function permits(grants, topic, scope) {
  for (const grant of grants) {
    if (typeof grant?.topic !== 'string' || !topicMatches(grant.topic.toLowerCase(), topic.toLowerCase())) continue;
    const has = Array.isArray(grant.scopes) && grant.scopes.includes(scope);
    if (topicMatches(grant.topic, topic)) return has;
    if (!has) return false;
  }
  return false;
}

async function installRuntime() {
  for (const name of runtimeFiles) {
    const path = join(root, name);
    const source = new URL('./' + name, import.meta.url);
    await replaceTarget(path, await readTarget(path), await readFile(source, 'utf8'));
  }
}

async function localState(path) {
  const file = await readTarget(path);
  if (file.text === null) return { file };
  if (file.mode & 0o077) throw new Error('The installer state file is not private: ' + path);
  return { file, state: JSON.parse(file.text) };
}

async function install(agent, server, flags, getInvite) {
  const { label } = agents[agent];
  const path = join(root, agent + '.json');
  const { file, state } = await localState(path);
  if (state && state.server !== server) throw new Error('Already connected to a different clrd server. Remove the connection first.');
  if (state?.phase === 'active') {
    await installRuntime();
    console.log(label + ' is already connected.');
    if ((flags.topic !== undefined && flags.topic !== state.topic) ||
        (flags['no-notifications'] && state.options?.notifications !== false) ||
        (flags['phone-approvals'] && state.options?.phoneApprovals !== true)) {
      console.warn('Existing settings were kept. Remove and reconnect ' + label + ' to change its setup.');
    }
    return;
  }

  const topic = state?.topic ?? flags.topic ?? 'agents';
  const options = {
    phoneApprovals: Boolean(flags['phone-approvals'] && (agent === 'claude' || agent === 'codex')),
    notifications: !flags['no-notifications'],
  };
  const edits = state?.edits ?? await agentEdits(agent, runtime, path, options.phoneApprovals, options.notifications);
  const apply = await prepareEdits(edits, state ? 'resume' : 'add');
  let pending = state;
  if (!pending) {
    const value = await getInvite(label, topic);
    if (!value) {
      console.log('Skipped ' + label + '.');
      return;
    }
    const { grants, ...identity } = await claim(server, value);
    pending = { ...identity, server, label, topic, options, edits, phase: 'pending' };
    const required = options.phoneApprovals ? ['read', 'publish'] : edits.length > 1 ? ['publish'] : [];
    const missing = required.filter((scope) => !permits(grants, topic, scope));
    if (missing.length) {
      try { await unlinkAgent(pending); } catch {
        console.error('Could not unlink the rejected agent. Unlink it under clrd Settings, Linked devices.');
      }
      throw new Error(`The invite needs ${missing.join(' and ')} on "${topic}". Create a new invite for this topic.`);
    }
    try {
      await replaceTarget(path, file, JSON.stringify(pending) + '\n');
    } catch (error) {
      // There is no local token to retry with if it could not be saved.
      try { await unlinkAgent(pending); } catch {
        console.error('Could not save or unlink the new agent. Unlink it under clrd Settings, Linked devices.');
      }
      throw error;
    }
  }
  await installRuntime();
  await apply();
  await replaceTarget(path, await readTarget(path), JSON.stringify({ ...pending, phase: 'active' }) + '\n');
  console.log('Connected ' + label + '.');
}

async function unlinkAgent({ server, token }) {
  const response = await request(server, '/api/v1/devices/this', { method: 'DELETE', token });
  if (response.status === 204) return;
  if (response.status === 401 && (await response.json().catch(() => null))?.code === 'unauthorized') return;
  throw new Error(`clrd could not unlink this agent (HTTP ${response.status})`);
}

async function remove(agent) {
  const path = join(root, agent + '.json');
  const { file, state } = await localState(path);
  if (!state) return;
  const apply = await prepareEdits(state.edits, 'remove');
  await unlinkAgent(state);
  await apply();
  await replaceTarget(path, file, null);
  console.log('Removed ' + agents[agent].label + '.');
}

async function main() {
  const { values: flags, positionals } = parseArgs({
    allowPositionals: true,
    options: {
      help: { type: 'boolean', short: 'h' },
      remove: { type: 'boolean' },
      agents: { type: 'string' },
      topic: { type: 'string' },
      'no-browser': { type: 'boolean' },
      'no-notifications': { type: 'boolean' },
      'phone-approvals': { type: 'boolean' },
    },
  });
  if (flags.help) {
    console.log(usage);
    return;
  }
  if (!['linux', 'darwin'].includes(process.platform)) throw new Error('Use create-clrd on Linux or macOS');
  if (positionals.length !== (flags.remove ? 0 : 1)) throw new Error(usage);
  if (flags.topic !== undefined) {
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(flags.topic)) throw new Error('Invalid topic name');
    if (reservedTopics.has(flags.topic.toLowerCase())) throw new Error('This topic name is reserved by clrd');
  }
  const names = flags.agents === undefined
    ? (flags.remove ? Object.keys(agents) : detectAgents())
    : [...new Set(flags.agents.split(',').map((name) => name.trim()))];
  if (names.some((name) => !Object.hasOwn(agents, name))) throw new Error('Unknown agent. Choose from ' + Object.keys(agents).join(', '));
  if (!names.length) throw new Error('No supported agents found. Use --agents to choose explicitly.');
  const server = flags.remove ? undefined : serverURL(positionals[0]);
  await privateDirectory(root);
  const lock = join(root, 'install.lock');
  await mkdir(lock, { mode: 0o700 }).catch((error) => {
    if (error.code === 'EEXIST') throw new Error('An installer lock exists. If no installer is running, remove the empty directory ' + lock);
    throw error;
  });
  let prompt;
  let interrupted = false;
  const cancel = () => {
    interrupted = true;
    prompt?.close();
  };
  process.on('SIGINT', cancel);
  try {
    for (const agent of names) {
      if (interrupted) break;
      try {
        if (flags.remove) await remove(agent);
        else await install(agent, server, flags, async (label, topic) => {
          if (interrupted) throw new Error('Setup cancelled');
          if (!prompt) {
            const settings = server + '/app/settings#add-agent';
            process.stderr.write('In clrd Settings, open Linked devices and select Add agent.\n' + settings + '\n');
            if (!flags['no-browser']) openBrowser(settings);
            if (flags['phone-approvals']) {
              process.stderr.write('Claude Code and Codex permission prompts will send tool details to this clrd server.\n');
            }
            prompt = invitePrompt(cancel);
          }
          const value = await prompt.read(label, topic);
          if (interrupted) throw new Error('Setup cancelled');
          return value;
        });
      } catch (error) {
        if (interrupted) break;
        console.error(agents[agent].label + ': ' + error.message);
        process.exitCode = 1;
      }
    }
    if (flags.remove) {
      const states = await Promise.all(Object.keys(agents).map((name) => readTarget(join(root, name + '.json'))));
      if (states.every(({ text }) => text === null)) {
        for (const name of runtimeFiles) {
          const path = join(root, name);
          await replaceTarget(path, await readTarget(path), null);
        }
      }
    }
  } finally {
    prompt?.close();
    await rmdir(lock);
    process.off('SIGINT', cancel);
  }
  if (interrupted) {
    console.error('Setup cancelled.');
    process.exitCode = 130;
  }
}

main().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
