import { accessSync, constants, existsSync } from 'node:fs';
import { homedir } from 'node:os';
import { delimiter, isAbsolute, join } from 'node:path';
import { readTarget } from './config.js';
import { parse } from 'jsonc-parser';

const home = homedir();
const directory = (name, fallback) => isAbsolute(process.env[name] ?? '') ? process.env[name] : fallback;
const xdg = directory('XDG_CONFIG_HOME', join(home, '.config'));
const claude = directory('CLAUDE_CONFIG_DIR', join(home, '.claude'));
const codex = directory('CODEX_HOME', join(home, '.codex'));
const gemini = join(home, '.gemini');
const grok = directory('GROK_HOME', join(home, '.grok'));
const opencode = join(xdg, 'opencode');
const cursor = join(home, '.cursor');
const code = process.platform === 'darwin' ? join(home, 'Library', 'Application Support', 'Code') : join(xdg, 'Code');
const copilot = directory('COPILOT_HOME', join(home, '.copilot'));

export const agents = {
  claude: { label: 'Claude Code', commands: ['claude'], directories: [claude] },
  codex: { label: 'Codex CLI', commands: ['codex'], directories: [codex] },
  gemini: { label: 'Gemini CLI', commands: ['gemini'], directories: [gemini] },
  grok: { label: 'Grok Build', commands: ['grok'], directories: [grok] },
  opencode: { label: 'OpenCode', commands: ['opencode'], directories: [opencode] },
  cursor: { label: 'Cursor', commands: ['cursor', 'cursor-agent'], directories: [cursor] },
  vscode: { label: 'VS Code', commands: ['code'], directories: [code] },
};

function executable(command) {
  return (process.env.PATH ?? '').split(delimiter).filter(isAbsolute).some((dir) => {
    try {
      accessSync(join(dir, command), constants.X_OK);
      return true;
    } catch {
      return false;
    }
  });
}

export function detectAgents() {
  return Object.entries(agents)
    .filter(([, { commands, directories }]) => commands.some(executable) || directories.some(existsSync))
    .map(([name]) => name);
}

const quote = (arg) => "'" + arg.replaceAll("'", "'\\''") + "'";
const json = (path, key, value, append = false) => ({ kind: 'json', path, key, value, append });
const whole = (path, value) => ({ kind: 'whole', path, value });
const hookFile = (path, value) => whole(path, JSON.stringify(value, null, 2) + '\n');
const commandHook = (command, timeout = 15) => ({ type: 'command', command, timeout });
const grouped = (hook) => ({ hooks: [hook] });

export async function agentEdits(agent, runtime, state, approvals, notifications) {
  const command = process.execPath;
  const args = [runtime, 'mcp', state];
  const stdio = { command, args };
  const hook = (mode) => [command, runtime, mode, state].map(quote).join(' ');
  const notify = hook('notify');
  const approve = commandHook(hook('approve'), 600);
  const mcp = (path, value = stdio, key = 'mcpServers') => json(path, [key, 'clrd'], value);

  if (agent === 'claude') {
    const hooks = join(claude, 'settings.json');
    const edits = [mcp(join(directory('CLAUDE_CONFIG_DIR', home), '.claude.json'), { type: 'stdio', ...stdio })];
    if (notifications) edits.push(json(hooks, ['hooks', 'Stop'], grouped(commandHook(notify)), true));
    if (approvals) edits.push(json(hooks, ['hooks', 'PermissionRequest'], grouped(approve), true));
    return edits;
  }
  if (agent === 'codex' || agent === 'grok') {
    const directory = agent === 'codex' ? codex : grok;
    const value = '\n# create-clrd start\n[mcp_servers.clrd]\n' +
      'command = ' + JSON.stringify(command) + '\nargs = ' + JSON.stringify(args) + '\n# create-clrd end\n';
    const edits = [{ kind: 'toml', path: join(directory, 'config.toml'), value }];
    if (agent === 'codex') {
      const hooks = join(directory, 'hooks.json');
      if (notifications) edits.push(json(hooks, ['hooks', 'Stop'], grouped(commandHook(notify)), true));
      if (approvals) edits.push(json(hooks, ['hooks', 'PermissionRequest'], grouped(approve), true));
    } else if (notifications) {
      edits.push(hookFile(join(directory, 'hooks', 'create-clrd.json'), {
        hooks: { Stop: [grouped(commandHook(notify))] },
      }));
    }
    return edits;
  }
  if (agent === 'gemini') {
    const path = join(gemini, 'settings.json');
    const edits = [mcp(path)];
    if (notifications) edits.push(json(path, ['hooks', 'AfterAgent'], grouped(commandHook(notify, 15_000)), true));
    return edits;
  }
  if (agent === 'cursor') {
    const hooks = join(cursor, 'hooks.json');
    const edits = [mcp(join(cursor, 'mcp.json'))];
    if (notifications) {
      edits.push({ ...json(hooks, ['hooks', 'stop'], { command: notify }, true), seed: '{"version": 1}\n' });
    }
    return edits;
  }
  if (agent === 'vscode') {
    const edits = [mcp(join(copilot, 'mcp-config.json'), { type: 'stdio', ...stdio })];
    if (notifications) edits.push(hookFile(join(copilot, 'hooks', 'create-clrd.json'), {
      version: 1, hooks: { agentStop: [{ type: 'command', command: notify, timeoutSec: 15 }] },
    }));
    return edits;
  }
  const plain = join(opencode, 'opencode.json');
  const jsonc = join(opencode, 'opencode.jsonc');
  const path = existsSync(plain) && !existsSync(jsonc) ? plain : jsonc;
  const config = parse((await readTarget(path)).text ?? '{}');
  const v2 = Object.hasOwn(config?.mcp ?? {}, 'servers');
  const key = v2 ? ['mcp', 'servers', 'clrd'] : ['mcp', 'clrd'];
  const edits = [json(path, key, { type: 'local', command: [command, ...args] })];
  if (notifications && !v2) {
    const plugin = `import { spawn } from 'node:child_process';

export const ClrdNotifications = async () => ({
  event: async ({ event }) => {
    if (event.type !== 'session.idle') return;
    const child = spawn(${JSON.stringify(command)}, ${JSON.stringify([runtime, 'notify', state])}, {
      stdio: 'ignore',
    });
    child.on('error', () => {});
    child.unref();
  },
});
`;
    edits.push(whole(join(opencode, 'plugins', 'create-clrd.js'), plugin));
  }
  return edits;
}
