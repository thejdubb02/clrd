import { randomUUID } from 'node:crypto';
import { lstat, mkdir, readFile, rename, unlink, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';
import { isDeepStrictEqual } from 'node:util';
import { applyEdits, findNodeAtLocation, getNodeValue, modify, parseTree } from 'jsonc-parser';
import { parse as parseToml } from 'smol-toml';

export async function readTarget(path) {
  try {
    const info = await lstat(path);
    if (!info.isFile() || info.nlink !== 1) throw new Error('Expected a regular file at ' + path);
    return { text: await readFile(path, 'utf8'), mode: info.mode & 0o777 };
  } catch (error) {
    if (error.code === 'ENOENT') return { text: null, mode: 0o600 };
    throw error;
  }
}

export async function replaceTarget(path, before, text) {
  if (text === before.text) return;
  if (text === null) {
    if ((await readTarget(path)).text !== before.text) throw new Error('File changed during setup: ' + path);
    await unlink(path);
    return;
  }
  await mkdir(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = path + '.' + randomUUID() + '.tmp';
  try {
    await writeFile(temporary, text, { mode: before.mode, flag: 'wx' });
    if ((await readTarget(path)).text !== before.text) throw new Error('File changed during setup: ' + path);
    await rename(temporary, path);
  } finally {
    await unlink(temporary).catch((error) => {
      if (error.code !== 'ENOENT') throw error;
    });
  }
}

export async function privateDirectory(path) {
  await mkdir(path, { recursive: true, mode: 0o700 });
  const info = await lstat(path);
  if (!info.isDirectory() || info.uid !== process.getuid() || (info.mode & 0o077)) {
    throw new Error('The installer directory must be private and owned by you: ' + path);
  }
}

function conflict(path) {
  return new Error('Existing or edited clrd configuration in ' + path + '. Move that entry first.');
}

function jsonStep(text, { path, key, value, append }, operation) {
  const errors = [];
  const tree = parseTree(text, errors, { allowTrailingComma: true });
  if (errors.length || tree?.type !== 'object') throw new Error('Invalid JSON configuration: ' + path);
  for (let end = 1; end < key.length; end++) {
    const parent = findNodeAtLocation(tree, key.slice(0, end));
    if (parent && parent.type !== 'object') throw conflict(path);
  }
  const node = findNodeAtLocation(tree, key);
  const current = node && JSON.parse(JSON.stringify(getNodeValue(node)));
  let editKey = key;
  if (append) {
    if (node && node.type !== 'array') throw conflict(path);
    const index = current?.findIndex((element) => isDeepStrictEqual(element, value)) ?? -1;
    if (index >= 0 && operation === 'resume') return text;
    if (index >= 0 && operation === 'add') throw conflict(path);
    if (index < 0 && operation === 'remove') return text;
    editKey = [...key, operation === 'remove' ? index : -1];
  } else if (node) {
    if (operation === 'add' || !isDeepStrictEqual(current, value)) throw conflict(path);
    if (operation === 'resume') return text;
  } else if (operation === 'remove') {
    return text;
  }
  return applyEdits(text, modify(text, editKey, operation === 'remove' ? undefined : value, {
    formattingOptions: { insertSpaces: true, tabSize: 2, eol: text.includes('\r\n') ? '\r\n' : '\n' },
  }));
}

function tomlStep(text, { path, value }, operation) {
  const entries = (source) => {
    try { return parseToml(source).mcp_servers; } catch {
      throw new Error('Invalid TOML configuration: ' + path);
    }
  };
  const current = entries(text) ?? {};
  const exists = Object.hasOwn(current, 'clrd');
  if (text.includes(value)) {
    if (!exists || !isDeepStrictEqual(current.clrd, entries(value).clrd)) throw conflict(path);
    if (operation === 'remove') {
      const updated = text.replace(value, '');
      entries(updated); // Verify the remaining document before writing it.
      return updated;
    }
    if (operation === 'resume') return text;
    throw conflict(path);
  }
  if (exists || text.includes('# create-clrd start')) throw conflict(path);
  if (operation === 'remove') return text;
  const updated = text + value;
  if (!Object.hasOwn(entries(updated) ?? {}, 'clrd')) throw conflict(path);
  return updated;
}

export async function prepareEdits(edits, operation) {
  const files = new Map();
  for (const edit of edits) {
    const { path, kind, value } = edit;
    if (!files.has(path)) {
      const before = await readTarget(path);
      files.set(path, { before, text: before.text });
    }
    const file = files.get(path);
    if (file.text === null && operation === 'remove') continue;
    if (kind === 'json') {
      file.text = jsonStep(file.text ?? edit.seed ?? '{}\n', edit, operation);
    } else if (kind === 'toml') {
      file.text = tomlStep(file.text ?? '', edit, operation);
      if (!file.text && operation === 'remove') file.text = null;
    } else {
      if (file.text !== null && (operation === 'add' || file.text !== value)) throw conflict(path);
      file.text = operation === 'remove' ? null : value;
    }
  }
  return async () => {
    for (const [path, { before, text }] of files) {
      await replaceTarget(path, before, text);
    }
  };
}
