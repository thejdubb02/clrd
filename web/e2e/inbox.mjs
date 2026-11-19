// Browser test for the inbox against the real binary.
//
//   CLRD_BIN=/path/to/clrd node web/e2e/inbox.mjs
//
// Needs the playwright package (1.48) and its Chromium; .github/workflows/web-e2e.yml
// runs it in the official Playwright image.
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import assert from 'node:assert/strict';
import { chromium } from 'playwright';

const bin = process.env.CLRD_BIN || 'clrd';
const port = await new Promise(ok => { const s = createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => ok(p)); }); });
const base = `http://127.0.0.1:${port}`;
const env = { ...process.env, CLRD_DB: join(mkdtempSync(join(tmpdir(), 'clrd-e2e-')), 'clrd.db'), CLRD_LISTEN: `127.0.0.1:${port}` };
const token = name => execFileSync(bin, ['token', 'create', '--name', name, '--scope', 'admin'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
const laptop = token('laptop');
const phone = token('pixel-9');

const server = spawn(bin, [], { env, stdio: 'inherit' });
const api = async (tok, path, body) => {
  const r = await fetch(base + path, { method: 'POST', headers: { Authorization: `Bearer ${tok}`, 'Content-Type': 'application/json' }, body: body && JSON.stringify(body) });
  assert.equal(r.status, 200, `${path}: ${r.status}`);
  return r.json();
};
const get = async (tok, id) => (await fetch(`${base}/api/v1/notifications/${id}`, { headers: { Authorization: `Bearer ${tok}` } })).json();

let browser;
try {
  for (let i = 0; ; i++) {
    try { if ((await fetch(base + '/healthz')).ok) break; } catch {}
    if (i > 50) throw new Error('server did not start');
    await new Promise(r => setTimeout(r, 100));
  }
  browser = await chromium.launch();
  const page = await (await browser.newContext()).newPage();
  const card = title => page.locator('article.card', { hasText: title });
  const toast = page.locator('[data-toast]');

  // Sign-in link: the token leaves the URL, the cookie stays.
  const link = Buffer.from(`Bearer ${laptop}`).toString('base64url');
  await page.goto(`${base}/app?auth=${link}`);
  assert.equal(new URL(page.url()).search, '', 'token still in the URL');
  assert.equal(await page.locator('[data-me]').getAttribute('data-me'), 'laptop');

  // Clearing it here offers Undo, and Undo brings it back.
  const a = await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Disk full on web-1', message: '/var at 99%' });
  await page.reload();
  await card('Disk full on web-1').locator('[data-act="dismiss"]').click();
  await toast.getByRole('button', { name: 'Undo' }).waitFor();
  assert.equal((await get(laptop, a.id)).state, 'dismissed');
  await toast.getByRole('button', { name: 'Undo' }).click();
  await page.waitForFunction(async id => (await (await fetch(`/api/v1/notifications/${id}`)).json()).state === 'active', a.id);
  console.log('ok: Clear offers Undo, Undo reopens');

  // Another device resolved it before this page heard: Clear is a no-op on the
  // server, so Undo must not be offered (it would reopen the resolved alert).
  const b = await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Backup failed', message: 'restic exited 1' });
  await page.routeWebSocket(/\/api\/v1\/ws/, () => {}); // this page stops hearing live events
  await page.reload();
  await api(phone, `/api/v1/notifications/${b.id}/resolve`);
  await card('Backup failed').locator('[data-act="dismiss"]').click();
  await toast.filter({ hasText: 'Resolved by pixel-9' }).waitFor();
  assert.equal(await toast.getByRole('button', { name: 'Undo' }).count(), 0, 'Undo offered after a no-op clear');
  assert.equal((await get(laptop, b.id)).state, 'resolved');
  console.log('ok: no Undo when another device got there first');
} finally {
  await browser?.close();
  server.kill();
}
