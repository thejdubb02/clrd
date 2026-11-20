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
  const page = await (await browser.newContext({ timezoneId: 'America/Chicago' })).newPage();
  const card = title => page.locator('article.card', { hasText: title });
  // Width the snooze date box has, and the width its own date and time fields need.
  const dateFit = scope => scope.getByLabel('Snooze until').evaluate(i => {
    const c = i.cloneNode(); c.style.cssText = 'position:absolute;visibility:hidden;width:auto;min-width:0;flex:none';
    i.parentNode.append(c); const need = c.offsetWidth; c.remove(); return { has: i.offsetWidth, need };
  });
  const toast = page.locator('[data-toast]');
  const announce = page.locator('[data-announce]');

  // Sign-in link: the token leaves the URL, the cookie stays.
  const link = Buffer.from(`Bearer ${laptop}`).toString('base64url');
  await page.goto(`${base}/app?auth=${link}`);
  assert.equal(new URL(page.url()).search, '', 'token still in the URL');
  // The browser gets its own token, named from its User-Agent (HeadlessChrome on Linux).
  assert.equal(await page.locator('[data-me]').getAttribute('data-me'), 'Chrome on Linux');

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

  // A second browser gets the same device name. It is still another device: no Undo here.
  const twin = await (await browser.newContext()).newPage();
  await twin.goto(`${base}/app?auth=${link}`);
  assert.equal(await twin.locator('[data-me]').getAttribute('data-me'), 'Chrome on Linux');
  const h = await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Load high on db-1', message: 'load 14' });
  await page.reload();
  await twin.reload();
  await twin.locator('article.card', { hasText: 'Load high on db-1' }).locator('[data-act="dismiss"]').click();
  await twin.locator('[data-toast]').getByRole('button', { name: 'Undo' }).waitFor();
  await card('Load high on db-1').locator('[data-act="dismiss"]').click();
  await toast.filter({ hasText: 'Cleared on Chrome on Linux' }).waitFor();
  assert.equal(await toast.getByRole('button', { name: 'Undo' }).count(), 0, 'Undo offered for a same-named device');
  assert.equal((await get(laptop, h.id)).state, 'dismissed');
  await twin.context().close();
  console.log('ok: no Undo for another device with the same name');

  // Server out of reach for 3 minutes: a notice says the page may be stale,
  // and it goes once live sync is back.
  const cut = await browser.newContext();
  const slow = await cut.newPage();
  await slow.clock.install();
  let down = true;
  await slow.routeWebSocket(/\/api\/v1\/ws/, ws => { if (down) ws.close(); else ws.connectToServer(); });
  await slow.goto(`${base}/app?auth=${link}`);
  const notice = slow.locator('[data-offline]');
  await slow.clock.runFor('02:50');
  assert.equal(await notice.isVisible(), false, 'notice before 3 minutes');
  await slow.clock.runFor('00:20');
  await notice.waitFor();
  down = false;
  await slow.clock.runFor('00:31');
  await notice.waitFor({ state: 'hidden' });
  await cut.close();
  console.log('ok: offline notice after 3 minutes, gone on reconnect');

  // The server came back on another database: its epoch differs from the one
  // this page was rendered with, so the page reloads once, like a reset.
  const moved = await (await browser.newContext()).newPage();
  let swapped = false, loads = 0, loaded;
  const firstLoad = new Promise(r => { loaded = r; });
  moved.on('load', () => loads++);
  await moved.routeWebSocket(/\/api\/v1\/ws/, ws => {
    const srv = ws.connectToServer();
    srv.onMessage(msg => {
      const m = JSON.parse(msg);
      // Held until the first load, so the reload cannot race it.
      if (m.type === 'ready' && !swapped) { swapped = true; m.epoch = 'another-db'; firstLoad.then(() => ws.send(JSON.stringify(m))); }
      else ws.send(msg);
    });
  });
  await moved.goto(`${base}/app?auth=${link}`);
  loaded();
  for (let i = 0; loads < 2 && i < 50; i++) await moved.waitForTimeout(100);
  assert.equal(loads, 2, 'page kept its old seq after the epoch changed');
  await moved.locator('.is-live').waitFor();
  await moved.waitForTimeout(500);
  assert.equal(loads, 2, 'page reloaded again with the same epoch');
  await moved.context().close();
  console.log('ok: a new server epoch reloads the page once');

  // Snooze menu: Tomorrow 09:00 means 09:00 where the reader is, not on the server.
  const d = await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Cert renews soon', message: '7 days left' });
  await page.reload();
  const want = await page.evaluate(() => { const t = new Date(); t.setDate(t.getDate() + 1); t.setHours(9, 0, 0, 0); return t.toISOString(); });
  await card('Cert renews soon').getByLabel('More snooze times').click();
  await card('Cert renews soon').getByRole('button', { name: 'Tomorrow 09:00' }).click();
  await announce.filter({ hasText: 'Snoozed until' }).waitFor({ state: 'attached' });
  const got = await get(laptop, d.id);
  assert.equal(got.state, 'snoozed');
  assert.equal(new Date(got.snooze_until).toISOString(), want);
  console.log('ok: snooze until tomorrow 09:00 local');

  // A custom time, confirmed with Enter, snoozes until that time (and does not Ack).
  const g = await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Renew domain', message: 'expires in 30 days' });
  await page.reload();
  await card('Renew domain').getByLabel('More snooze times').click();
  // The custom time box and its Set button fit inside the menu, not clipped by it.
  const box = await card('Renew domain').locator('[data-popover]').boundingBox();
  const set = await card('Renew domain').getByRole('button', { name: 'Set' }).boundingBox();
  assert.ok(set.x >= box.x && set.x + set.width <= box.x + box.width, 'Set button clipped by the snooze menu');
  const deskFit = await dateFit(card('Renew domain'));
  assert.ok(deskFit.has >= deskFit.need, `snooze date box squeezed to ${deskFit.has} px, needs ${deskFit.need} px`);
  assert.ok(Math.abs(set.y - (await card('Renew domain').getByLabel('Snooze until').boundingBox()).y) < 8, 'Set button wrapped under the date box on a wide screen');
  // On a wide screen the menu opens inside its card, not over the sidebar.
  const cardBox = await card('Renew domain').boundingBox();
  assert.ok(box.x >= cardBox.x, `snooze menu starts at ${box.x}, left of its card at ${cardBox.x}`);
  await card('Renew domain').getByLabel('Snooze until').fill('2031-06-01T08:30');
  await card('Renew domain').getByLabel('Snooze until').press('Enter');
  await announce.filter({ hasText: 'Snoozed until' }).waitFor({ state: 'attached' });
  const custom = await get(laptop, g.id);
  assert.equal(custom.state, 'snoozed');
  assert.equal(new Date(custom.snooze_until).toISOString(), '2031-06-01T13:30:00.000Z'); // 08:30 in Chicago (CDT)
  console.log('ok: custom snooze time with Enter');

  // Snooze times from Settings: a tap uses the default, a long press or right
  // click opens the list instead of snoozing, and an open page picks up a change.
  const putSettings = async body => {
    const r = await fetch(`${base}/api/v1/settings`, { method: 'PUT', headers: { Authorization: `Bearer ${phone}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    assert.equal(r.status, 200, `PUT settings: ${r.status}`);
  };
  const tap = await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Rotate keys', message: 'due' });
  const hold = await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Prune images', message: 'disk 80%' });
  const sp = await page.context().newPage(); // page no longer hears live events (above)
  await sp.goto(`${base}/app`);
  const sc = t => sp.locator('article.card', { hasText: t });
  await putSettings({ snooze_intervals: ['15m', '2h', '48h'], snooze_default: '2h' });
  await sp.locator('[data-snooze="2h,15m,2h,48h"]').waitFor({ state: 'attached' }); // the open page reloaded itself
  await sc('Prune images').getByText('Snooze', { exact: true }).click({ delay: 700 });
  assert.equal(await sc('Prune images').locator('details[open] [data-popover]').isVisible(), true, 'long press did not open the snooze times');
  assert.deepEqual(await sc('Prune images').locator('[data-popover] [data-for]').allTextContents(), ['15 min', '2 hours', '2 days']);
  await sc('Prune images').locator('details').evaluate(d => { d.open = false; });
  await sc('Prune images').getByText('Snooze', { exact: true }).click({ button: 'right' });
  assert.equal(await sc('Prune images').locator('details[open] [data-popover]').isVisible(), true, 'right click did not open the snooze times');
  await sp.waitForTimeout(300);
  assert.equal((await get(laptop, hold.id)).state, 'active', 'long press or right click snoozed');
  await sc('Prune images').locator('details').evaluate(d => { d.open = false; });
  const before = Date.now();
  await sc('Rotate keys').getByText('Snooze', { exact: true }).click();
  await sc('Rotate keys').waitFor({ state: 'detached' });
  const tapped = await get(laptop, tap.id);
  assert.equal(tapped.state, 'snoozed');
  assert.ok(Math.abs(new Date(tapped.snooze_until) - before - 2 * 3600e3) < 60e3, `tap snoozed until ${tapped.snooze_until}, not the 2 hour default`);
  // The Settings form saves the list; a default left out of it falls back to its first entry.
  await sp.goto(`${base}/app/settings`);
  await sp.getByLabel('Times').fill('30m, 1d');
  await sp.getByRole('button', { name: 'Save' }).click();
  await sp.locator('[data-snooze="30m,30m,24h"]').waitFor({ state: 'attached' });
  assert.equal(await sp.getByLabel('Default').inputValue(), '30m');
  await sp.getByLabel('Times').fill('1h, soon');
  await sp.getByRole('button', { name: 'Save' }).click();
  await sp.locator('[data-snooze-status]').filter({ hasText: 'soon' }).waitFor();
  await putSettings({ snooze_intervals: ['1h', '4h'], snooze_default: '1h' });
  await sp.locator('[data-snooze="1h,1h,4h"]').waitFor({ state: 'attached' });
  await sp.close();
  console.log('ok: snooze default, long press, right click and Settings');

  // The tab shows the open count and follows a change on another device.
  const titleIs = async (p, t) => { // the page's CSP refuses waitForFunction
    for (let i = 0; i < 50 && await p.title() !== t; i++) await p.waitForTimeout(100);
    assert.equal(await p.title(), t);
  };
  const tp = await page.context().newPage();
  await tp.goto(`${base}/app`);
  const open = Number((await tp.title()).match(/^\((\d+)\) Inbox: clrd$/)?.[1]);
  assert.ok(open > 0, `no open count in the tab title: ${await tp.title()}`);
  const tc = await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Tab count', message: 'one more' });
  await titleIs(tp, `(${open + 1}) Inbox: clrd`);
  await api(phone, `/api/v1/notifications/${tc.id}/dismiss`);
  await titleIs(tp, `(${open}) Inbox: clrd`);
  await tp.close();
  // Two days on, a card shows its date and time, not "2 days ago".
  const later = await page.context().newPage();
  await later.clock.setFixedTime(Date.now() + 2 * 86400e3);
  await later.goto(`${base}/app`);
  const when = await later.locator('article.card time').first().textContent();
  assert.match(when, /\d{1,2}:\d{2}/, `old card time has no clock time: ${when}`);
  assert.doesNotMatch(when, /ago|yesterday|UTC/i, `old card time is relative or not localized: ${when}`);
  await later.close();
  console.log('ok: open count in the tab, full time on older cards');

  // A reply button stores the answer for whoever waits on it; the card then shows it.
  const q = await api(phone, '/api/v1/notifications', { topic: 'agents', title: 'Merge 212?', message: 'tests pass', actions: [{ label: 'Yes', reply: 'yes' }, { label: 'No', reply: 'no' }] });
  await page.reload();
  await card('Merge 212?').getByRole('button', { name: 'Yes' }).click();
  await announce.filter({ hasText: 'Answered Yes' }).waitFor({ state: 'attached' });
  const answered = await get(laptop, q.id);
  assert.equal(answered.reply, 'yes');
  assert.equal(answered.state, 'acknowledged');
  await card('Merge 212?').getByText('Answered Yes').waitFor();
  assert.equal(await card('Merge 212?').getByRole('button', { name: 'No', exact: true }).count(), 0, 'answered card still offers replies');
  console.log('ok: reply button stores the answer');

  // On a phone, the open Snooze menu stays on screen, even when Snooze ends the first row of buttons.
  const narrow = await (await browser.newContext({ viewport: { width: 360, height: 740 } })).newPage();
  await api(phone, '/api/v1/notifications', { topic: 'agents', title: 'Deploy 213?', message: 'staging green', actions: [{ label: 'Go', reply: 'go' }, { label: 'No', reply: 'no' }] });
  await narrow.goto(`${base}/app?auth=${link}`);
  const wide = narrow.locator('article.card', { hasText: 'Deploy 213?' });
  await wide.getByLabel('More snooze times').click();
  const menu = await wide.locator('[data-popover]').boundingBox();
  assert.ok(menu.x >= 0 && menu.x + menu.width <= 360, `snooze menu spans ${menu.x} to ${menu.x + menu.width} on a 360 px screen`);
  assert.equal(await narrow.evaluate(() => document.documentElement.scrollWidth), 360, 'page scrolls sideways with the menu open');
  // A very small phone too.
  await narrow.setViewportSize({ width: 300, height: 740 });
  const small = await wide.locator('[data-popover]').boundingBox();
  assert.ok(small.x >= 0 && small.x + small.width <= 300, `snooze menu spans ${small.x} to ${small.x + small.width} on a 300 px screen`);
  const smallSet = await wide.getByRole('button', { name: 'Set' }).boundingBox();
  assert.ok(smallSet.x >= small.x && smallSet.x + smallSet.width <= small.x + small.width, 'Set button clipped by the snooze menu on a 300 px screen');
  // The date box keeps the width its own fields need (a squeezed one hides part of the time).
  const fit = await dateFit(wide);
  assert.ok(fit.has >= fit.need, `snooze date box squeezed to ${fit.has} px, needs ${fit.need} px on a 300 px screen`);
  await narrow.context().close();
  console.log('ok: snooze menu fits a phone screen');

  // With JavaScript off the buttons still work: a form post and back to the same page.
  const nojs = await (await browser.newContext({ javaScriptEnabled: false })).newPage();
  await nojs.goto(`${base}/app?auth=${link}`);
  const e = await api(phone, '/api/v1/notifications', { topic: 'home', title: 'Garage open', message: '20 minutes' });
  const f = await api(phone, '/api/v1/notifications', { topic: 'home', title: 'Washer done', message: 'cycle finished' });
  await nojs.goto(`${base}/app?topic=home`);
  const plain = title => nojs.locator('article.card', { hasText: title });
  await plain('Garage open').getByLabel('More snooze times').click();
  assert.equal(await plain('Garage open').getByRole('button', { name: '4 hours' }).isVisible(), true, 'snooze menu did not open without script');
  assert.equal(await plain('Garage open').getByRole('button', { name: 'Tomorrow 09:00' }).isVisible(), false, 'script-only snooze shown without script');
  await plain('Garage open').getByLabel('More snooze times').click();
  await plain('Garage open').getByRole('button', { name: 'Clear' }).click();
  await plain('Garage open').waitFor({ state: 'detached' });
  assert.equal(new URL(nojs.url()).search, '?topic=home');
  assert.equal((await get(laptop, e.id)).state, 'dismissed');
  await plain('Washer done').getByLabel('More snooze times').click();
  await plain('Washer done').getByRole('button', { name: '4 hours' }).click();
  await plain('Washer done').waitFor({ state: 'detached' });
  assert.equal((await get(laptop, f.id)).state, 'snoozed');
  const r = await api(phone, '/api/v1/notifications', { topic: 'home', title: 'Lock the door?', message: 'left open', actions: [{ label: 'Lock', reply: 'lock' }] });
  await nojs.goto(`${base}/app?topic=home`);
  await plain('Lock the door?').getByRole('button', { name: 'Lock' }).click();
  await plain('Lock the door?').getByText('Answered Lock').waitFor();
  assert.equal((await get(laptop, r.id)).reply, 'lock');
  console.log('ok: Clear, Snooze and reply work without JavaScript');

  // QR sign-in (#74): a signed-out browser shows a code, a signed-in phone
  // opens the link and taps Approve, and the waiting browser signs itself in.
  const pc = await (await browser.newContext()).newPage();
  await pc.goto(base + '/app');
  const [res] = await Promise.all([
    pc.waitForResponse(r => r.url().endsWith('/api/v1/pair')),
    pc.getByRole('button', { name: 'Show sign-in code' }).click(),
  ]);
  const { code } = await res.json();
  await pc.locator('[data-pair-qr]').waitFor({ state: 'visible' });
  const phoneCtx = await browser.newContext();
  assert.ok((await phoneCtx.request.post(base + '/api/v1/session', { data: { token: phone } })).ok(), 'phone sign-in');
  const phonePage = await phoneCtx.newPage();
  await phonePage.goto(`${base}/pair?c=${code}`);
  await phonePage.getByRole('button', { name: 'Approve' }).click();
  await phonePage.getByText('Approved.').waitFor();
  await pc.locator('[data-inbox]').waitFor({ timeout: 5000 });
  console.log('ok: sign in a browser by approving its QR code from a phone');

  // Link a phone (#74): the signed-in browser shows a code in Settings, the
  // app claims it, the browser approves it by name, and the app gets a token.
  {
    await page.goto(base + '/app/settings');
    const [made] = await Promise.all([
      page.waitForResponse(r => r.url().endsWith('/api/v1/link')),
      page.getByRole('button', { name: 'Link a phone' }).click(),
    ]);
    const { code: lc } = await made.json();
    await page.locator('[data-link] [data-pair-qr]').waitFor({ state: 'visible' });
    const claim = () => fetch(base + '/api/v1/link/claim', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: lc, secret: 'p'.repeat(32), name: 'Pixel 9' }) });
    assert.equal((await claim()).status, 202, 'claim before approve');
    await page.getByText('Pixel 9 wants to connect').waitFor();
    // The match number both screens show: SHA-256 of the secret, first 4 bytes, mod 10000.
    const h = (await import('node:crypto')).createHash('sha256').update('p'.repeat(32)).digest();
    const check = String(h.readUInt32BE(0) % 10000).padStart(4, '0');
    assert.equal(await page.locator('[data-link-check]').textContent(), check, 'browser shows the match number');
    await page.getByRole('button', { name: 'Approve' }).click();
    await page.getByText('Connected: Pixel 9.').waitFor();
    const got = await claim();
    assert.equal(got.status, 200, 'claim after approve');
    const { token: linked } = await got.json();
    const list = await fetch(base + '/api/v1/notifications', { headers: { Authorization: `Bearer ${linked}` } });
    assert.equal(list.status, 200, 'linked token lists notifications');
    console.log('ok: link a phone from Settings by approving it by name');
  }
} finally {
  await browser?.close();
  server.kill();
}
