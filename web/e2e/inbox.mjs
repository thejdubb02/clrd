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
// The user's own devices: allowed to answer approval requests (#187).
const token = name => execFileSync(bin, ['token', 'create', '--name', name, '--scope', 'admin', '--approve', 'allowed'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
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
  // A fresh install has nothing for the format check to show (#320).
  const fresh = await (await page.request.get(base + '/app/settings')).text();
  assert.ok(fresh.includes('id="snz-h"') && !fresh.includes('data-format-check'), 'a fresh install shows the format check');

  // Empty first run: one tip card, send-a-test first; Next moves on, and Copy
  // holds this instance's own address.
  const tip = page.locator('[data-tip]:visible');
  assert.equal(await tip.count(), 1, 'one tip shows');
  assert.equal(await tip.locator('h4').textContent(), 'Send your first notification');
  await page.evaluate(() => { window.copied = []; navigator.clipboard.writeText = async t => { window.copied.push(t); }; });
  await tip.getByRole('button', { name: 'Copy' }).click();
  assert.ok((await page.evaluate(() => window.copied[0])).includes(`${base}/api/v1/notifications`), 'copy holds the base URL');
  const next = page.getByRole('button', { name: 'Next tip' });
  const at = await next.boundingBox();
  await next.click();
  assert.deepEqual(await next.boundingBox(), at, 'Next stays put when the tip height changes (#302)');
  assert.notEqual(await tip.locator('h4').textContent(), 'Send your first notification', 'Next changes the tip');
  assert.equal(await page.locator('[data-tip-count]').textContent(), '2 of 9', 'no topic icon tip before there is a topic (#258)');
  assert.equal(await announce.textContent(), await tip.locator('h4').textContent(), 'Next announces the new tip');
  console.log('ok: tip card shows, Next changes it, Copy has the base URL');

  // Tips move on by themselves, quietly, and hold still under the pointer (#292).
  {
    const tp = await page.context().newPage();
    await tp.clock.install();
    await tp.goto(`${base}/app`);
    const title = () => tp.locator('[data-tip]:visible h4').textContent();
    const first = await title();
    await tp.clock.runFor(10000);
    const second = await title();
    assert.notEqual(second, first, 'the tip changes on its own');
    assert.equal(await tp.locator('[data-announce]').textContent(), '', 'an automatic change is not announced');
    await tp.locator('[data-tips]').hover();
    await tp.clock.runFor(30000);
    assert.equal(await title(), second, 'the tip stays put while hovered');
    // Pause stops the rotation with the pointer and focus gone (a screen
    // reader's virtual cursor triggers neither); Play starts it again (#352).
    const away = async () => { await tp.mouse.move(0, 0); await tp.evaluate(() => document.activeElement.blur()); };
    await tp.getByRole('button', { name: 'Pause tips' }).click();
    await away();
    await tp.clock.runFor(30000);
    assert.equal(await title(), second, 'the tip stays put once paused');
    // Play from the keyboard: focus stays on the button, and it still rotates.
    await tp.getByRole('button', { name: 'Play tips' }).focus();
    await tp.keyboard.press('Enter');
    await tp.clock.runFor(10000);
    assert.notEqual(await title(), second, 'Play starts the rotation again, focus or not');
    await tp.close();
  }
  console.log('ok: tips advance on their own, pause on hover, and have Pause and Play');

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

  // One dot per device (#295): another browser connects and its dot turns
  // green here, with no reload; it closes and the dot turns red.
  const watch = await (await browser.newContext()).newPage();
  await watch.goto(`${base}/app?auth=${link}`);
  await watch.locator('#sync.is-live').waitFor();
  const others = watch.locator('#sync .sync-dot.is-on:not([data-this])');
  const dots0 = await others.count();
  const peer = await (await browser.newContext()).newPage();
  await peer.goto(`${base}/app?auth=${link}`);
  await peer.locator('#sync.is-live').waitFor();
  await others.nth(dots0).waitFor({ state: 'attached' });
  assert.match(await watch.locator('#sync').textContent(), new RegExp(`In sync on ${dots0 + 2} devices`));
  const all = watch.locator('#sync .sync-dot:not([data-this])');
  const dotsUp = await all.count();
  await peer.context().close();
  await others.nth(dots0).waitFor({ state: 'detached' });
  assert.equal(await all.count(), dotsUp, 'the closed browser lost its dot instead of turning red');
  assert.equal(await watch.locator('#sync').getAttribute('href'), '/app/settings#dev-h');
  await watch.context().close();
  console.log('ok: a device dot turns green on connect and red on close, live');

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
  await sp.getByLabel('Default').selectOption('24h'); // a time just typed can be the default before saving
  // While the save is on its way, the forms are locked: the reload after it would drop an edit (#214).
  let release;
  const held = new Promise(ok => { release = ok; });
  await sp.route('**/api/v1/settings', async r => { if (r.request().method() === 'PUT') await held; await r.continue(); });
  await sp.locator('[data-snooze-settings]').getByRole('button', { name: 'Save' }).click();
  for (const l of ['Times', 'Default']) assert.equal(await sp.getByLabel(l).isDisabled(), true, `${l} editable during a save`);
  assert.equal(await sp.locator('[data-display-settings] input').first().isDisabled(), true, 'display switch usable during a snooze save');
  release();
  await sp.unroute('**/api/v1/settings');
  await sp.locator('[data-snooze="24h,30m,24h"]').waitFor({ state: 'attached' });
  assert.equal(await sp.getByLabel('Default').inputValue(), '24h');
  await sp.getByLabel('Times').fill('30m');
  await sp.getByLabel('Times').fill('30m, 1d'); // retyped: the default picked comes back with its time
  assert.equal(await sp.getByLabel('Default').inputValue(), '24h');
  await sp.getByLabel('Times').fill('1h, soon');
  await sp.locator('[data-snooze-settings]').getByRole('button', { name: 'Save' }).click();
  await sp.locator('[data-snooze-status]').filter({ hasText: 'soon' }).waitFor();
  // A change from another device keeps the edit not yet saved here, and says so.
  await putSettings({ snooze_intervals: ['1h', '4h'], snooze_default: '1h' });
  await sp.locator('[data-snooze-status]').filter({ hasText: 'another device' }).waitFor();
  assert.equal(await sp.getByLabel('Times').inputValue(), '1h, soon', 'unsaved snooze edit dropped');
  await sp.reload();
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

  // Send from the inbox: the note lands on the server with its fields and shows
  // live; a bad topic keeps the dialog open with the server's reason.
  const sendPage = await page.context().newPage();
  await sendPage.goto(`${base}/app`);
  const sendBox = sendPage.getByRole('dialog', { name: 'Send a notification' });
  await sendPage.getByRole('button', { name: 'Send', exact: true }).click();
  await sendBox.getByLabel('Topic').fill('bad topic!');
  await sendBox.getByLabel('Message').fill('nope');
  await sendBox.getByRole('button', { name: 'Send' }).click();
  await sendBox.locator('[data-send-status]').filter({ hasText: /topic/i }).waitFor();
  assert.ok(await sendBox.getByLabel('Message').isVisible(), 'dialog closed on an error');
  await sendBox.getByRole('button', { name: 'Cancel' }).click();
  await sendBox.getByLabel('Message').waitFor({ state: 'hidden' }); // the dialog fades out
  await sendPage.getByRole('button', { name: 'Send', exact: true }).click();
  assert.equal(await sendBox.locator('[data-send-status]').textContent(), '', 'old error still shown');
  await sendBox.getByLabel('Topic').fill('notes');
  await sendBox.getByLabel('Title').fill('From the laptop');
  await sendBox.getByLabel('Message').fill('https://example.com/read-later');
  await sendBox.getByLabel('Priority').selectOption('4');
  await sendBox.getByRole('button', { name: 'Send' }).click();
  await sendBox.getByLabel('Message').waitFor({ state: 'hidden' });
  await sendPage.locator('article.card', { hasText: 'From the laptop' }).waitFor();
  const sent = (await (await fetch(`${base}/api/v1/notifications?topic=notes`, { headers: { Authorization: `Bearer ${phone}` } })).json()).notifications;
  assert.equal(sent.length, 1, 'sent note not on the server');
  assert.equal(sent[0].message, 'https://example.com/read-later');
  assert.equal(sent[0].priority, 4);
  await sendPage.getByRole('button', { name: 'Send', exact: true }).click();
  assert.equal(await sendBox.getByLabel('Message').inputValue(), '', 'message kept after sending');
  // Closed while a slow send is in flight, then a new note: the first send's success leaves the new note alone.
  await sendPage.route('**/api/v1/notifications', async r => { if (r.request().method() === 'POST') await new Promise(ok => setTimeout(ok, 800)); await r.continue(); });
  await sendBox.getByLabel('Topic').fill('notes');
  await sendBox.getByLabel('Message').fill('slow one');
  await sendBox.getByRole('button', { name: 'Send' }).click();
  await sendBox.getByRole('button', { name: 'Cancel' }).click();
  await sendBox.getByLabel('Message').waitFor({ state: 'hidden' });
  await sendPage.getByRole('button', { name: 'Send', exact: true }).click();
  await sendBox.getByLabel('Title').fill('Second note');
  await sendPage.locator('article.card', { hasText: 'slow one' }).waitFor();
  await sendPage.waitForTimeout(200);
  assert.equal(await sendBox.getByLabel('Title').inputValue(), 'Second note', 'a finished send cleared the new note');
  assert.ok(await sendBox.getByLabel('Title').isVisible(), 'a finished send closed the new note');
  await sendPage.keyboard.press('Escape');
  await sendPage.close();
  console.log('ok: send a notification from the inbox');

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

  // Share and Copy hand over the title, the message and the link; the icon and attachments show on the card.
  const sharePage = await page.context().newPage();
  await sharePage.addInitScript(() => {
    window.shared = [];
    navigator.share = d => { window.shared.push(d); return Promise.resolve(); };
    navigator.clipboard.writeText = t => { window.shared.push(t); return Promise.resolve(); };
  });
  await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Share me', message: 'line one', click: 'https://example.com/run/7', icon: 'https://example.com/i.png', attachment: { url: 'https://example.com/r.pdf', name: 'r.pdf' } });
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');
  const form = new FormData();
  form.append('notification', JSON.stringify({ topic: 'infra', title: 'Graph attached', message: 'see image' }));
  form.append('file', new Blob([png], { type: 'image/png' }), 'graph.png');
  assert.equal((await fetch(`${base}/api/v1/notifications`, { method: 'POST', headers: { Authorization: `Bearer ${phone}` }, body: form })).status, 200);
  await sharePage.goto(`${base}/app`);
  const shareCard = sharePage.locator('article.card', { hasText: 'Share me' });
  await shareCard.getByRole('button', { name: 'Share' }).click();
  await shareCard.getByRole('button', { name: 'Copy' }).click();
  await sharePage.locator('[data-toast]').filter({ hasText: 'Copied.' }).waitFor();
  assert.deepEqual(await sharePage.evaluate(() => window.shared), [
    { title: 'Share me', text: 'line one', url: 'https://example.com/run/7' },
    'Share me\n\nline one\nhttps://example.com/run/7',
  ]);
  assert.equal(await shareCard.locator('[data-icon]').getAttribute('src'), 'https://example.com/i.png');
  assert.equal(await shareCard.getByRole('link', { name: 'r.pdf' }).getAttribute('href'), 'https://example.com/r.pdf');
  const img = sharePage.locator('article.card', { hasText: 'Graph attached' }).getByRole('img', { name: 'graph.png' });
  await img.scrollIntoViewIfNeeded(); // lazy: loads once on screen
  for (let i = 0; i < 50 && !await img.evaluate(i => i.complete); i++) await sharePage.waitForTimeout(100);
  assert.equal(await img.evaluate(i => i.naturalWidth), 1, 'uploaded image did not load on the card');
  await sharePage.close();
  // On a plain http LAN address there is no navigator.clipboard: Copy still works.
  const lan = await page.context().newPage();
  await lan.addInitScript(() => {
    delete Navigator.prototype.clipboard;
    window.copied = [];
    const exec = document.execCommand.bind(document);
    document.execCommand = c => { if (c === 'copy') window.copied.push(document.activeElement.value); return c === 'copy' || exec(c); };
  });
  await lan.goto(`${base}/app`);
  assert.equal(await lan.evaluate(() => !!navigator.clipboard), false);
  await lan.locator('article.card', { hasText: 'Share me' }).getByRole('button', { name: 'Copy' }).click();
  await lan.locator('[data-toast]').filter({ hasText: 'Copied.' }).waitFor();
  assert.deepEqual(await lan.evaluate(() => window.copied), ['Share me\n\nline one\nhttps://example.com/run/7']);
  // A topic's icon from Settings lands on its next notification; an icon that
  // fails to load is hidden; an external image attachment shows inline.
  await api(phone, '/api/v1/notifications', { topic: 'pics', title: 'First pic', message: 'x' });
  await lan.goto(`${base}/app/settings`);
  await lan.locator('#ti-pics').fill(`${base}/no-such-icon.png`);
  await lan.locator('#ti-pics').press('Enter');
  await lan.locator('[data-topic-icon-status]').filter({ hasText: 'Saved' }).waitFor();
  const pic = await api(phone, '/api/v1/notifications', { topic: 'pics', title: 'Broken icon', message: 'x', attachment: { url: `${base}/static/vendor/none.png` } });
  assert.equal((await get(laptop, pic.id)).icon, `${base}/no-such-icon.png`);
  await lan.goto(`${base}/app`);
  const bc = lan.locator('article.card', { hasText: 'Broken icon' });
  for (let i = 0; i < 50 && await bc.locator('[data-icon]').isVisible(); i++) await lan.waitForTimeout(100);
  assert.equal(await bc.locator('[data-icon]').isVisible(), false, 'icon that failed to load still shown');
  assert.equal(await bc.locator('[data-attachment] img').count(), 1, 'external image attachment not inline');
  await lan.close();
  console.log('ok: share, copy, icon and attachments on the card');

  // Markdown bodies: the subset renders, other HTML shows as typed, no images,
  // line breaks kept; plain text stays raw; turning formatting off reloads open pages.
  await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Formatted', message: '**Disk** 91% on ++web1++\nsee https://example.com/d\n- one\n- two\n<script>window.pwned=1</script> <img src=x onerror="window.pwned=1"> ![px](https://example.com/p.png)' });
  await api(phone, '/api/v1/notifications', { topic: 'infra', title: 'Raw text', message: '**not bold**', format: 'text' });
  const mp = await page.context().newPage();
  await mp.goto(`${base}/app`);
  const fbody = mp.locator('article.card', { hasText: 'Formatted' }).locator('[data-body]');
  assert.equal(await fbody.locator('strong').textContent(), 'Disk');
  assert.equal(await fbody.locator('u').textContent(), 'web1');
  assert.equal(await fbody.locator('li').count(), 2);
  assert.equal(await fbody.locator('br').count(), 1, 'line break lost');
  assert.equal(await fbody.getByRole('link', { name: 'https://example.com/d' }).getAttribute('rel'), 'noopener noreferrer');
  assert.equal(await fbody.locator('img:not(.favicon), script, iframe').count(), 0, 'raw HTML or an image got through');
  assert.deepEqual(await fbody.locator('img.favicon').evaluateAll(i => i.map(e => e.getAttribute('src'))), ['/api/v1/favicon?host=example.com'], 'site icon missing');
  assert.ok(await fbody.locator('img.favicon').evaluate(i => i.complete && i.naturalWidth > 0), 'site icon did not load');
  await mp.emulateMedia({ colorScheme: 'dark' });
  assert.notEqual(await fbody.locator('img.favicon').evaluate(i => getComputedStyle(i).backgroundColor), 'rgba(0, 0, 0, 0)', 'no tile behind the site icon in dark mode (#309)');
  await mp.emulateMedia({ colorScheme: null });
  assert.ok((await fbody.textContent()).includes('<script>window.pwned=1</script>'), 'raw HTML not shown as typed');
  assert.equal(await mp.evaluate(() => window.pwned), undefined);
  const rbody = mp.locator('article.card', { hasText: 'Raw text' }).locator('[data-body]');
  assert.equal(await rbody.textContent(), '**not bold**');
  await putSettings({ render_formatting: false });
  await mp.locator('[data-fmt="false,true,true,false"]').waitFor({ state: 'attached' }); // the open page reloaded itself
  assert.equal(await fbody.locator('strong, li').count(), 0, 'formatting off still renders Markdown');
  assert.ok((await fbody.textContent()).startsWith('**Disk** 91% on ++web1++'));
  await putSettings({ render_formatting: true });
  // The Settings switch saves for every device: site icons off on the open inbox.
  const setp = await page.context().newPage();
  await setp.goto(`${base}/app/settings`);
  const iconSw = setp.getByRole('switch', { name: 'Show site icons next to links' });
  assert.ok(await iconSw.isChecked());
  await Promise.all([setp.waitForEvent('load'), iconSw.click()]); // saved, then the page reloads
  assert.equal(await setp.getByRole('switch', { name: 'Show site icons next to links' }).isChecked(), false, 'switch not saved');
  await mp.locator('[data-fmt="true,true,false,false"]').waitFor({ state: 'attached' });
  assert.equal(await fbody.locator('img.favicon').count(), 0, 'site icon still shown with the setting off');
  await putSettings({ favicons: true });
  await mp.locator('[data-fmt="true,true,true,false"]').waitFor({ state: 'attached' }); // its own reload is done
  // Link previews: off by default; on for one topic from Settings, the card
  // shows what the server returned (the server's fetch is stubbed here).
  const asked = [];
  await mp.route('**/api/v1/preview?*', route => {
    asked.push(new URL(route.request().url()).searchParams);
    route.fulfill({ json: { url: 'https://example.com/d', title: 'Disk <b>dash</b>', description: 'Grafana', site_name: 'Example', image: 'data:image/gif;base64,R0lGODlhAQABAAAAACw=' } });
  });
  await mp.reload();
  assert.equal(asked.length, 0, 'preview fetched with previews off');
  await setp.locator('#tp-infra').selectOption('on');
  await setp.waitForEvent('load');
  assert.equal(await setp.locator('#tp-infra').inputValue(), 'on', 'topic preview not saved');
  await mp.locator('[data-tprev="infra=true;"]').waitFor({ state: 'attached' });
  const prev = mp.locator('article.card', { hasText: 'Formatted' }).locator('a.preview');
  await prev.waitFor();
  assert.equal(await prev.locator('.title').textContent(), 'Disk <b>dash</b>', 'preview title not shown as text');
  assert.equal(await prev.getAttribute('href'), 'https://example.com/d');
  assert.equal(await prev.locator('.desc').textContent(), 'Grafana');
  assert.equal(await prev.locator('.site').textContent(), 'Example');
  assert.ok((await prev.locator('img').getAttribute('src')).startsWith('data:image/gif;'), 'preview image not shown');
  assert.equal(asked.at(-1).get('url'), 'https://example.com/d');
  assert.equal(asked.at(-1).get('topic'), 'infra');
  assert.equal(await mp.locator('a.preview').count(), 1, 'preview on a topic left at the default');
  await putSettings({ topic_previews: { infra: null } });
  await mp.locator('[data-tprev=""]').waitFor({ state: 'attached' });
  assert.equal(await mp.locator('[data-preview]').count(), 0, 'card still asks for a preview after the topic went back to default');
  assert.equal(await mp.locator('a.preview').count(), 0, 'preview still shown after the topic went back to default');
  await setp.close();
  await mp.close();
  console.log('ok: Markdown bodies follow the formatting setting');

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

  // Stack by topic (#108): off by default; on, a topic with two or more
  // notifications shows as one card that opens in place and clears together.
  {
    const sp = await (await browser.newContext()).newPage();
    await sp.goto(`${base}/app?auth=${link}`);
    const ids = [];
    for (const t of ['Build 1 failed', 'Build 2 failed', 'Build 3 failed']) ids.push((await api(phone, '/api/v1/notifications', { topic: 'ci-stack', title: t, message: 'red' })).id);
    await sp.reload();
    const stack = sp.locator('[data-stack="ci-stack"]');
    const item = t => sp.locator('article.card[data-id]', { hasText: t });
    assert.equal(await stack.count(), 0, 'stacked while the setting is off');
    assert.equal(await item('Build 1 failed').isVisible(), true);
    await sp.goto(base + '/app/settings');
    await sp.getByRole('switch', { name: 'Stack by topic' }).check();
    await sp.goto(base + '/app');
    await stack.waitFor();
    assert.equal(await stack.locator('[data-stack-count]').textContent(), '3 notifications');
    assert.equal(await stack.locator('[data-stack-latest]').textContent(), 'Build 3 failed');
    assert.equal(await item('Build 1 failed').isVisible(), false, 'stacked card still shown');
    // A new one only bumps the count, live.
    ids.push((await api(phone, '/api/v1/notifications', { topic: 'ci-stack', title: 'Build 4 failed', message: 'red' })).id);
    await stack.locator('[data-stack-count]', { hasText: '4 notifications' }).waitFor();
    assert.equal(await stack.locator('[data-stack-latest]').textContent(), 'Build 4 failed');
    assert.equal(await item('Build 4 failed').isVisible(), false, 'new card shown outside its stack');
    await stack.getByRole('button', { name: 'Show all' }).click();
    for (const n of [1, 2, 3, 4]) await item(`Build ${n} failed`).waitFor({ state: 'visible' });
    assert.equal(await stack.getByRole('button', { name: 'Hide' }).getAttribute('aria-expanded'), 'true');
    // A live refresh keeps keyboard focus on the stack, and a topic named like
    // an Object property does not break the page.
    await stack.getByRole('button', { name: 'Hide' }).focus();
    const odd = (await api(phone, '/api/v1/notifications', { topic: 'constructor', title: 'Odd topic name', message: 'x' })).id;
    await item('Odd topic name').waitFor();
    assert.equal(await stack.getByRole('button', { name: 'Hide' }).evaluate(b => b === document.activeElement), true, 'focus lost from the stack on refresh');
    // Clear all on the stack clears all four on the server, and Undo reopens them.
    await stack.getByRole('button', { name: 'Clear all' }).click();
    await sp.locator('[data-toast]').filter({ hasText: 'Cleared 4 notifications.' }).waitFor();
    for (const id of ids) assert.equal((await get(laptop, id)).state, 'dismissed');
    await sp.locator('[data-toast]').getByRole('button', { name: 'Undo' }).click();
    for (const id of ids) await sp.waitForFunction(async i => (await (await fetch(`/api/v1/notifications/${i}`)).json()).state === 'active', id);
    await stack.locator('[data-stack-count]', { hasText: '4 notifications' }).waitFor();
    // J lands on the closed stack and Enter opens it (#227).
    await sp.reload(); // nothing selected, so J starts from the top
    await stack.waitFor();
    for (let i = 0; i < 200 && !(await stack.evaluate(s => s.classList.contains('is-selected'))); i++) await sp.keyboard.press('j');
    assert.equal(await stack.evaluate(s => s === document.activeElement && s.classList.contains('is-selected')), true, 'J skipped the closed stack');
    await sp.keyboard.press('c');
    await sp.waitForFunction(() => document.querySelector('[data-announce]').textContent === 'Open the stack to act on one notification.');
    for (const id of ids) assert.equal((await get(laptop, id)).state, 'active', 'C on a stack cleared a card');
    await sp.keyboard.press('Enter');
    await item('Build 1 failed').waitFor({ state: 'visible' });
    await stack.getByRole('button', { name: 'Hide' }).click();
    await item('Build 1 failed').waitFor({ state: 'hidden' });
    // A page fetch already on its way when another change lands must not put
    // the cleared card back (#402). Hold one fetch, made before the clear, and
    // hand it over after the clear.
    let release;
    const onApp = u => u.pathname === '/app';
    await sp.route(onApp, async route => {
      if (route.request().resourceType() !== 'fetch' || release) return route.continue();
      const stale = await route.fetch();
      await new Promise(ok => { release = ok; });
      await route.fulfill({ response: stale });
    });
    await api(phone, `/api/v1/notifications/${odd}/dismiss`);
    for (let i = 0; i < 200 && !release; i++) await new Promise(ok => setTimeout(ok, 25));
    assert.ok(release, 'no page fetch after a change');
    // Another device clears one inside the closed stack: the count and newest
    // title change at once, not after the hidden card's exit timer (about 1.7 s).
    await api(phone, `/api/v1/notifications/${ids[3]}/dismiss`);
    await stack.locator('[data-stack-count]', { hasText: '3 notifications' }).waitFor({ timeout: 1000 });
    await sp.evaluate(() => { window.backAgain = false; new MutationObserver(() => { if (document.querySelector('[data-stack="ci-stack"] [data-stack-count]')?.textContent === '4 notifications') window.backAgain = true; }).observe(document.querySelector('[data-list]'), { childList: true, subtree: true, characterData: true }); });
    release();
    await sp.waitForTimeout(1000);
    await sp.unroute(onApp);
    assert.equal(await sp.evaluate(() => window.backAgain), false, 'a stale page put the cleared card back');
    assert.equal(await stack.locator('[data-stack-latest]').textContent(), 'Build 3 failed');
    // A single topic's page never stacks.
    await sp.goto(`${base}/app?topic=ci-stack`);
    assert.equal(await stack.count(), 0, 'stacked on the topic page');
    await sp.context().close();
    console.log('ok: Stack by topic folds a topic into one card that opens and clears');
  }

  // Stack edge cases (#387), in a browser that sees only its own topics, so
  // its Clear all touches nothing else in this run.
  {
    const own = execFileSync(bin, ['token', 'create', '--name', 'stacker', '--scope', 'read,publish', '--topics', 'pumps,tanks,late'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const sp = await (await browser.newContext()).newPage();
    await sp.goto(`${base}/app?auth=${Buffer.from(`Bearer ${own}`).toString('base64url')}`);
    await sp.evaluate(() => localStorage.setItem('clrd.stack', '1'));
    const send = async (topic, title) => (await api(phone, '/api/v1/notifications', { topic, title, message: 'x' })).id;
    for (const t of ['Pump 1 down', 'Pump 2 down']) await send('pumps', t);
    const tanks = [];
    for (const t of ['Tank 1 low', 'Tank 2 low', 'Tank 3 low']) tanks.push(await send('tanks', t));
    await sp.reload();
    const stackOf = t => sp.locator(`[data-stack="${t}"]`);
    await stackOf('tanks').waitFor();
    // Another device clears one inside a closed stack while a different stack
    // fades out: the count changes at once, not after the fade (about 1.7 s).
    await stackOf('pumps').getByRole('button', { name: 'Clear all' }).click();
    await sp.locator('[data-stack="pumps"].is-leaving').waitFor();
    await api(phone, `/api/v1/notifications/${tanks[2]}/dismiss`);
    await stackOf('tanks').locator('[data-stack-count]', { hasText: '2 notifications' }).waitFor({ timeout: 1000 });
    await stackOf('pumps').waitFor({ state: 'detached' });
    // Clear all with its reply held back: the live events for the clear land
    // first, and a new topic arrives meanwhile. The stack still fades as one,
    // and the new topic is not marked Cleared.
    let release;
    const held = new Promise(r => { release = r; });
    await sp.route('**/api/v1/notifications/clear', async route => { const res = await route.fetch(); await held; await route.fulfill({ response: res }); });
    await sp.locator('[data-act="clear-all"]').click();
    for (const t of ['Late 1', 'Late 2']) await send('late', t);
    await sp.waitForTimeout(1000); // nothing to wait on: the events must change nothing
    assert.equal(await stackOf('tanks').locator('[data-stack-count]').textContent(), '2 notifications', 'the stack shrank card by card');
    release();
    await sp.locator('[data-stack="tanks"].is-leaving').waitFor();
    assert.equal(await sp.locator('[data-stack].is-leaving').count(), 1, 'a stack that came in while clearing was marked Cleared');
    await stackOf('late').locator('[data-stack-count]', { hasText: '2 notifications' }).waitFor();
    await sp.context().close();
    console.log('ok: stacks stay right while Clear all waits and while one fades');
  }

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

  // Unlink (#246): browser a unlinks browser b from Settings; b's open inbox
  // goes straight to sign-in with a plain message, and the log says who did it.
  {
    const open = async name => {
      const ctx = await browser.newContext();
      assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name } })).ok(), name + ' sign-in');
      return ctx.newPage();
    };
    const a = await open('browser a'), b = await open('browser b');
    await b.goto(base + '/app');
    await b.locator('.is-live').waitFor({ timeout: 5000 });
    await a.goto(base + '/app/settings');
    const row = a.locator('tr', { hasText: 'browser b' });
    assert.match(await row.textContent(), /Browser, signed in with laptop/, 'row says how it was linked');
    assert.equal(await a.locator('tr', { hasText: 'browser a' }).getByRole('button', { name: 'Unlink' }).count(), 0, 'no unlink on this browser');
    await row.getByRole('button', { name: 'Unlink' }).click();
    await a.getByRole('dialog').getByRole('button', { name: 'Unlink' }).click();
    await b.locator('[data-signed-out]').waitFor({ timeout: 5000 });
    await a.locator('tr', { hasText: 'browser b' }).waitFor({ state: 'detached' });
    await a.getByText('Device activity').click();
    await a.getByText('browser b unlinked by browser a').waitFor();
    await a.context().close(); await b.context().close();
    console.log('ok: unlink a browser from another; it goes to sign-in at once');
  }

  // Sound (#282): a live new notification plays the chosen sound, chime with no
  // setup; silenced or None plays nothing; a blocked browser gets a hint, not an error.
  {
    const ctx = await browser.newContext();
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'sound' } })).ok(), 'sound sign-in');
    await ctx.addInitScript(() => {
      window.played = [];
      HTMLMediaElement.prototype.play = function () {
        window.played.push(new URL(this.src).pathname);
        return window.blockSound ? Promise.reject(new DOMException('blocked', 'NotAllowedError')) : Promise.resolve();
      };
    });
    const sp = await ctx.newPage();
    const played = () => sp.evaluate(() => window.played);
    let n = 0;
    const arrive = async (topic = 'sound') => {
      const title = `Sound ${++n}`;
      await api(phone, '/api/v1/notifications', { topic, title });
      await sp.locator('article.card', { hasText: title }).waitFor();
    };
    const inbox = async () => { await sp.goto(base + '/app'); await sp.locator('.is-live').waitFor({ timeout: 5000 }); };
    const pick = async name => { await sp.goto(base + '/app/settings'); await sp.getByRole('combobox', { name: 'Sound', exact: true }).selectOption(name); };

    const wav = await ctx.request.get(base + '/assets/sounds/chime.wav');
    assert.ok(wav.ok() && (await wav.body()).subarray(0, 4).toString() === 'RIFF', 'chime.wav is served');
    await inbox();
    assert.deepEqual(await played(), [], 'the catch-up replay plays nothing');
    await arrive();
    assert.deepEqual(await played(), ['/assets/sounds/chime.wav'], 'no setup plays chime');
    const r = await fetch(base + '/api/v1/silences', { method: 'POST', headers: { Authorization: `Bearer ${phone}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ topic: 'hush', for: '1h' }) });
    assert.ok(r.ok, `silence: ${r.status}`);
    await arrive('hush');
    assert.deepEqual(await played(), ['/assets/sounds/chime.wav'], 'a silenced one plays nothing');
    await api(phone, '/api/v1/notifications', { topic: 'sound', title: 'Later', snooze_until: new Date(Date.now() + 2000).toISOString() });
    await sp.waitForTimeout(1000);
    assert.deepEqual(await played(), ['/assets/sounds/chime.wav'], 'a scheduled send is quiet while queued');
    for (let i = 0; i < 50 && (await played()).length < 2; i++) await sp.waitForTimeout(500); // the server wakes snoozes every 15 s
    assert.deepEqual(await played(), ['/assets/sounds/chime.wav', '/assets/sounds/chime.wav'], 'a scheduled send rings when it arrives');

    await pick('pop');
    assert.deepEqual(await played(), ['/assets/sounds/pop.wav'], 'choosing a sound previews it');
    await sp.getByRole('button', { name: 'Play', exact: true }).click();
    assert.equal((await played()).length, 2, 'Play previews again');
    await inbox();
    await arrive();
    assert.deepEqual(await played(), ['/assets/sounds/pop.wav'], 'the chosen sound plays');

    // Repeat (#260): the card says how often it alerted and when next, each
    // re-alert rings, and Ack stops it (the server checks every 15 s, so up to 75 s).
    await api(phone, '/api/v1/notifications', { topic: 'sound', title: 'Pager', repeat: '1m', repeat_times: 2 });
    const pager = sp.locator('article.card', { hasText: 'Pager' });
    await pager.locator('[data-repeat]', { hasText: 'Alerted 1 time, next at' }).waitFor();
    const rang = (await played()).length;
    await pager.locator('[data-repeat]', { hasText: 'Alerted 2 times, next at' }).waitFor({ timeout: 90000 });
    assert.equal((await played()).length, rang + 1, 'a re-alert rings');
    await pager.getByRole('button', { name: 'Ack', exact: true }).click();
    await pager.locator('.badge[data-state="acknowledged"]').waitFor(); // the card stays, so the line below is the repeat stopping
    await pager.locator('[data-repeat]').waitFor({ state: 'detached' });
    console.log('ok: a repeating alert rings again and shows its count until acked');

    // The choice lives on the server, per device (#297): saved there, set by an
    // agent while Settings is open, and a browser-only choice moves there.
    const mine = async () => (await (await ctx.request.get(base + '/api/v1/settings')).json()).sound;
    const local = () => sp.evaluate(() => localStorage.getItem('clrd.sound'));
    const settle = async (want, msg) => { for (let i = 0; i < 20 && (await mine()) !== want; i++) await sp.waitForTimeout(250); assert.equal(await mine(), want, msg); };
    await settle('pop', 'the choice is saved on the server');
    assert.equal(await local(), null, 'and not kept in the browser');
    await sp.goto(base + '/app/settings'); await sp.locator('.is-live').waitFor({ timeout: 5000 });
    const me = (await (await ctx.request.get(base + '/api/v1/devices?all=1')).json()).devices.find(d => d.this);
    const put = await fetch(`${base}/api/v1/devices/${me.id}`, { method: 'PUT', headers: { Authorization: `Bearer ${phone}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ sound: 'bell' }) });
    assert.ok(put.ok, `agent sets the sound: ${put.status}`);
    await sp.waitForFunction(() => document.querySelector('[data-sound-setting]').value === 'bell');
    await sp.evaluate(() => localStorage.setItem('clrd.sound', 'soft'));
    await inbox();
    await settle('soft', 'a browser-only choice moves to the server');
    assert.equal(await local(), null, 'and leaves the browser');

    await pick('none');
    await inbox();
    await arrive();
    assert.deepEqual(await played(), [], 'None plays nothing');

    await sp.evaluate(() => localStorage.setItem('clrd.sound', 'klaxon'));
    await inbox();
    await arrive();
    assert.deepEqual(await played(), ['/assets/sounds/chime.wav'], 'an unknown name plays the default');

    await sp.evaluate(() => { window.blockSound = true; });
    await arrive();
    await sp.getByText('Sound is blocked until you click or press a key on this page.').waitFor();
    await pick('ding');
    await sp.evaluate(() => { window.blockSound = true; });
    await sp.getByRole('button', { name: 'Play', exact: true }).click();
    await sp.locator('[data-sound-hint]', { hasText: 'Sound is blocked' }).waitFor();
    await ctx.close();
    console.log('ok: sound for new notifications, picker with preview, blocked hint');
  }

  // Silences in Settings (#257): start one, it mutes the next publish on that
  // topic in the inbox and shows in the list; End now ends it.
  {
    const ctx = await browser.newContext();
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'silences' } })).ok(), 'silences sign-in');
    await ctx.addInitScript(() => { window.played = []; HTMLMediaElement.prototype.play = function () { window.played.push(this.src); return Promise.resolve(); }; });
    const sp = await ctx.newPage();
    await sp.goto(base + '/app/settings');
    const form = sp.locator('[data-silence-form]');
    await form.getByRole('button', { name: 'Start silence' }).click();
    await sp.locator('[data-silence-status]', { hasText: 'Give a topic, a tag, or both.' }).waitFor();
    await form.getByLabel('Topic').fill('quiet');
    await form.getByLabel('For').selectOption('8h');
    await form.getByLabel('Comment').fill('deploy window');
    await Promise.all([sp.waitForEvent('load'), form.getByRole('button', { name: 'Start silence' }).click()]);
    const row = sp.locator('tr[data-silence]', { hasText: 'quiet' });
    await row.getByText('deploy window').waitFor();
    assert.match(await row.textContent(), /7h 59m left|8h 0m left/, 'time left shows');
    await sp.goto(base + '/app'); await sp.locator('.is-live').waitFor({ timeout: 5000 });
    assert.equal((await api(phone, '/api/v1/notifications', { topic: 'quiet', title: 'Muted one' })).silenced, true, 'the next publish is silenced');
    await sp.locator('article.card', { hasText: 'Muted one' }).waitFor();
    await sp.waitForTimeout(300);
    assert.deepEqual(await sp.evaluate(() => window.played), [], 'and makes no sound in the inbox');
    await sp.goto(base + '/app/settings');
    await Promise.all([sp.waitForEvent('load'), sp.locator('tr[data-silence]', { hasText: 'quiet' }).getByRole('button', { name: 'End now' }).click()]);
    assert.equal(await sp.locator('tr[data-silence]', { hasText: 'quiet' }).count(), 0, 'End now removes it from the list');
    assert.equal((await api(phone, '/api/v1/notifications', { topic: 'quiet', title: 'Loud one' })).silenced, false, 'and publishes alert again');
    await ctx.close();
    console.log('ok: start and end a silence in Settings, the inbox stays quiet');
  }

  // A topic's own keep for cleared messages (#323): the preview counts that
  // topic only, a save that deletes asks first, No leaves it as it was, and
  // Default drops the topic's own value.
  {
    await api(phone, '/api/v1/notifications', { topic: 'keepy', title: 'Keep me' });
    const ctx = await browser.newContext();
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'keep' } })).ok(), 'keep sign-in');
    const kp = await ctx.newPage();
    const asked = [];
    await kp.route('**/api/v1/counts?*', async r => { asked.push(new URL(r.request().url()).search); await r.fulfill({ json: { would_remove: 3 } }); });
    const topicKeep = async () => (await (await fetch(base + '/api/v1/settings', { headers: { Authorization: `Bearer ${phone}` } })).json()).topic_keep_cleared_days || {};
    await kp.goto(base + '/app/settings');
    const sel = kp.locator('#tk-keepy');
    kp.once('dialog', d => d.dismiss());
    await sel.selectOption('1');
    await kp.waitForFunction(() => !document.querySelector('[data-topic-keep] select').disabled);
    assert.equal(await sel.inputValue(), '', 'No puts the select back');
    assert.deepEqual(await topicKeep(), {}, 'and saves nothing');
    let msg = '';
    kp.once('dialog', d => { msg = d.message(); d.accept(); });
    await Promise.all([kp.waitForEvent('load'), sel.selectOption('7')]);
    assert.match(msg, /deletes 3 cleared messages in keepy/, 'asks with the count');
    assert.deepEqual(asked, ['?keep_cleared_days=1&topic=keepy', '?keep_cleared_days=7&topic=keepy'], 'the preview is for that topic only');
    assert.deepEqual(await topicKeep(), { keepy: 7 }, 'Yes saves it');
    assert.equal(await sel.inputValue(), '7', 'and the page shows it');
    kp.once('dialog', d => d.accept());
    await Promise.all([kp.waitForEvent('load'), sel.selectOption('')]);
    assert.deepEqual(await topicKeep(), {}, 'Default drops it');
    assert.equal(asked.at(-1), '?keep_cleared_days=default&topic=keepy', 'Default previews the exact global keep, not whole days (#336)');
    await ctx.close();
    console.log('ok: a topic keeps cleared messages for its own period, set in Settings');
  }

  // Silences without JavaScript (#326): Start and End now are plain form posts.
  // A read-only device sees the list but not the controls it cannot use.
  {
    const ctx = await browser.newContext({ javaScriptEnabled: false });
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'silences-nojs' } })).ok(), 'no-js sign-in');
    const sp = await ctx.newPage();
    await sp.goto(base + '/app/settings');
    const form = sp.locator('[data-silence-form]');
    assert.equal(await form.locator('option[value="until"]').evaluate(o => getComputedStyle(o).display), 'none', 'script-only end time shown without script');
    await form.getByLabel('Topic').fill('nojs');
    await form.getByLabel('For').selectOption('8h');
    await form.getByRole('button', { name: 'Start silence' }).click();
    await sp.locator('tr[data-silence]', { hasText: 'nojs' }).waitFor();
    assert.equal(new URL(sp.url()).pathname, '/app/settings', 'back on Settings');
    assert.equal((await api(phone, '/api/v1/notifications', { topic: 'nojs', title: 'Muted' })).silenced, true, 'the no-script silence mutes');

    const reader = execFileSync(bin, ['token', 'create', '--name', 'reader', '--scope', 'read'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const rctx = await browser.newContext();
    assert.ok((await rctx.request.post(base + '/api/v1/session', { data: { token: reader, name: 'reader' } })).ok(), 'reader sign-in');
    const rp = await rctx.newPage();
    await rp.goto(base + '/app/settings');
    const rrow = rp.locator('tr[data-silence]', { hasText: 'nojs' });
    await rrow.waitFor();
    assert.equal(await rrow.getByRole('button', { name: 'End now' }).count(), 0, 'End now offered to a read-only token');
    assert.equal(await rp.locator('[data-silence-form]').count(), 0, 'Start silence offered to a read-only token');
    await rp.getByText('This device can see silences but not start or end them').waitFor();
    await rctx.close();

    await sp.locator('tr[data-silence]', { hasText: 'nojs' }).getByRole('button', { name: 'End now' }).click();
    await sp.locator('h2', { hasText: 'Silences' }).waitFor();
    assert.equal(await sp.locator('tr[data-silence]', { hasText: 'nojs' }).count(), 0, 'End now without script ends it');
    assert.equal((await api(phone, '/api/v1/notifications', { topic: 'nojs', title: 'Loud' })).silenced, false, 'and publishes alert again');
    await ctx.close();
    console.log('ok: silences work without JavaScript; a read-only device sees no controls');
  }

  // Quick silence on a card (#326): one tap in the snooze menu mutes the
  // topic for an hour and comes back to the same inbox page. A device that
  // cannot silence the topic is not offered it.
  {
    await api(phone, '/api/v1/notifications', { topic: 'qsil', title: 'Noisy one' });
    const ctx = await browser.newContext();
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'qsil' } })).ok(), 'quick silence sign-in');
    const p = await ctx.newPage();
    await p.goto(base + '/app?topic=qsil');
    const card = p.locator('article', { hasText: 'Noisy one' });
    await card.locator('summary[aria-label="More snooze times"]').click();
    await Promise.all([p.waitForEvent('load'), card.getByRole('button', { name: 'Silence qsil for 1 hour' }).click()]);
    assert.equal(new URL(p.url()).pathname + new URL(p.url()).search, '/app?topic=qsil', 'back on the same inbox page');
    const sils = (await (await fetch(base + '/api/v1/silences', { headers: { Authorization: `Bearer ${laptop}` } })).json()).silences.filter(s => s.topic === 'qsil');
    assert.equal(sils.length, 1, 'one silence on the topic');
    const left = new Date(sils[0].until) - Date.now();
    assert.ok(left > 55 * 60e3 && left <= 60 * 60e3, `the silence runs ${left} ms, not an hour`);
    assert.ok(!sils[0].tag, 'the quick silence is for the whole topic');
    assert.equal((await api(phone, '/api/v1/notifications', { topic: 'qsil', title: 'Muted one' })).silenced, true, 'the next publish is silenced');

    const reader = execFileSync(bin, ['token', 'create', '--name', 'qsil-reader', '--scope', 'read'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const rctx = await browser.newContext();
    assert.ok((await rctx.request.post(base + '/api/v1/session', { data: { token: reader, name: 'qsil-reader' } })).ok(), 'reader sign-in');
    const rp = await rctx.newPage();
    await rp.goto(base + '/app?topic=qsil');
    await rp.locator('article', { hasText: 'Noisy one' }).waitFor();
    assert.equal(await rp.locator('[data-silence-quick]').count(), 0, 'quick silence offered to a read-only token');
    await rctx.close();
    await ctx.close();
    console.log('ok: a card silences its topic for an hour in one tap; a read-only device is not offered it');
  }

  // Webhooks in Settings (#238), without script so the plain form posts are tested.
  // The secret never reaches the page, a blank secret on Edit keeps it, and a
  // device that is not admin sees no hooks and cannot save one.
  {
    const ctx = await browser.newContext({ javaScriptEnabled: false });
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'hooks' } })).ok(), 'hooks sign-in');
    const sp = await ctx.newPage();
    await sp.goto(base + '/app/settings');
    await sp.getByText('No webhooks yet.').waitFor();
    const add = sp.locator('[data-hook-add]');
    await add.getByLabel('Topic').fill('hooked');
    await add.getByLabel('Name').fill('ops');
    await add.getByLabel('URL').fill('https://ops.example.invalid/in');
    await add.getByLabel('Snoozed').uncheck();
    await add.getByLabel('Secret').fill('topsecret-238');
    await add.getByRole('button', { name: 'Add webhook' }).click();
    const card = sp.locator('[data-hook="hooked/ops"]');
    await card.waitFor();
    assert.match(await card.innerText(), /Sends Dismissed, Acknowledged, Resolved, Action tapped\. Signed with a secret\./, 'saved events and secret');
    assert.ok(!(await sp.content()).includes('topsecret-238'), 'the secret is in the Settings page');

    // A dismiss on the topic shows up in the delivery log (.invalid never resolves, so it fails).
    const n = await api(phone, '/api/v1/notifications', { topic: 'hooked', title: 'Hook me' });
    await api(phone, `/api/v1/notifications/${n.id}/dismiss`);
    // The log sits in a closed <details>, so read textContent, not innerText.
    let row = '';
    for (let i = 0; ; i++) {
      await sp.reload();
      row = (await card.locator('[data-hook-log] tbody tr', { hasText: 'dismissed' }).allTextContents())[0] || '';
      if (/Retrying|Failed/.test(row)) break;
      assert.ok(i < 20, `no failed delivery in the log: "${row}"`);
      await new Promise(r => setTimeout(r, 500));
    }
    assert.match(row, /Retrying, try 1/, 'a failed delivery says which try');

    // Edit with the secret left empty keeps it.
    await card.locator('summary', { hasText: 'Edit' }).click();
    const edit = card.locator('form', { has: sp.getByRole('button', { name: 'Save webhook' }) });
    await edit.getByLabel('URL').fill('https://ops2.example.invalid/in');
    await edit.getByRole('button', { name: 'Save webhook' }).click();
    await card.getByText('ops2.example.invalid').waitFor();
    assert.match(await card.innerText(), /Signed with a secret\./, 'a blank secret on Edit removed it');

    // Not admin: no hooks on the page, and the form post is refused.
    const pub = execFileSync(bin, ['token', 'create', '--name', 'hooks-pub', '--scope', 'read,publish'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const rctx = await browser.newContext();
    assert.ok((await rctx.request.post(base + '/api/v1/session', { data: { token: pub, name: 'hooks-pub' } })).ok(), 'publish sign-in');
    const rp = await rctx.newPage();
    await rp.goto(base + '/app/settings');
    await rp.getByText('Webhooks need an admin sign-in.').waitFor();
    assert.ok(!(await rp.content()).includes('ops2.example.invalid'), 'a hook URL shown to a non-admin');
    const refused = await rctx.request.post(base + '/app/hooks', { form: { topic: 'hooked', name: 'evil', url: 'https://x.example.invalid/' }, headers: { Origin: base }, maxRedirects: 0 });
    assert.equal(refused.status(), 400, `non-admin hook save: ${refused.status()}`);
    await rctx.close();
    const listed = await (await fetch(base + '/api/v1/topics/hooked/hooks', { headers: { Authorization: `Bearer ${laptop}` } })).json();
    assert.deepEqual(listed.hooks.map(h => h.name), ['ops'], 'a non-admin saved a hook');

    // Delete removes it.
    await card.getByRole('button', { name: 'Delete' }).click();
    await sp.getByText('No webhooks yet.').waitFor();
    await ctx.close();
    console.log('ok: webhooks are added, logged, edited and deleted in Settings; a non-admin sees none');
  }

  // Who may approve (#329). An Ask browser's tap goes to an allowed device to
  // confirm; the Ask browser gets no menu; the allowed browser sets others in
  // Settings and can lower, never raise, itself.
  {
    const asker = execFileSync(bin, ['token', 'create', '--name', 'asker', '--scope', 'admin', '--approve', 'ask'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const actx = await browser.newContext();
    assert.ok((await actx.request.post(base + '/api/v1/session', { data: { token: asker, name: 'asker-web' } })).ok(), 'asker sign-in');
    const ap = await actx.newPage();
    await ap.goto(base + '/app/settings');
    await ap.getByText('May approve: Ask').first().waitFor();
    assert.equal(await ap.locator('[data-approve]').count(), 0, 'an Ask device gets a menu to change settings');

    const q = await api(phone, '/api/v1/notifications', { topic: 'agents', title: 'Ship 329?', message: 'all green', actions: [{ label: 'Ship', reply: 'ship' }] });
    await ap.goto(base + '/app');
    const acard = ap.locator('article.card', { hasText: 'Ship 329?' });
    await acard.getByRole('button', { name: 'Ship' }).click();
    await ap.locator('[data-toast]').filter({ hasText: 'Sent for confirmation. An allowed device has to confirm it.' }).waitFor();
    assert.ok(!(await get(laptop, q.id)).reply, 'an Ask tap answered without a confirm');

    const bctx = await browser.newContext();
    assert.ok((await bctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'boss-web' } })).ok(), 'boss sign-in');
    const bp = await bctx.newPage();
    await bp.goto(base + '/app');
    await bp.locator('article.card', { hasText: "Confirm asker-web's answer: Ship" }).getByRole('button', { name: 'Confirm' }).click();
    for (let i = 0; (await get(laptop, q.id)).reply !== 'ship'; i++) { assert.ok(i < 20, 'confirm did not answer the question'); await new Promise(r => setTimeout(r, 250)); }

    // The allowed browser lowers the asker token to Denied from Settings.
    await bp.goto(base + '/app/settings');
    const sel = bp.locator('select[data-name="asker"]');
    assert.deepEqual(await sel.locator('option').allTextContents(), ['Denied', 'Ask', 'Allowed'], 'menu for another tree');
    await sel.selectOption('denied');
    await bp.locator('[data-toast]').filter({ hasText: 'asker may approve: Denied.' }).waitFor({ state: 'attached' });
    const list = await (await fetch(base + '/api/v1/devices?all=1', { headers: { Authorization: `Bearer ${laptop}` } })).json();
    assert.equal(list.devices.find(d => d.name === 'asker').approve, 'denied', 'the menu did not save');

    // Its own row lowers after a confirm, and then the menus are gone: nothing raises it back here.
    const own = bp.locator('select[data-name="boss-web"]');
    assert.ok(await own.evaluate(s => s.hasAttribute('data-own')), 'own row not marked');
    bp.once('dialog', d => { assert.match(d.message(), /Lower boss-web to Ask\?/); d.accept(); });
    await own.selectOption('ask');
    await bp.locator('[data-toast]').filter({ hasText: 'boss-web may approve: Ask.' }).waitFor({ state: 'attached' });
    await bp.reload();
    assert.equal(await bp.locator('[data-approve]').count(), 0, 'a device that lowered itself to Ask can still change settings');
    const up = await bctx.request.put(base + '/api/v1/devices/' + list.devices.find(d => d.name === 'boss-web').id + '/approve', { data: { approve: 'allowed' }, headers: { Origin: base } });
    assert.equal(up.status(), 403, `an Ask browser raised itself: ${up.status()}`);
    await actx.close();
    await bctx.close();
    console.log('ok: an Ask tap waits for a confirm; Settings lowers devices and never raises its own');
  }

  // Webhook templates in Settings (#129): a saved template turns a real post into
  // a notification, a blank secret on Edit keeps the secret, Preview runs the
  // unsaved fields and publishes nothing, and a read-only device gets no forms.
  {
    const post = (body, secret) => fetch(`${base}/tpl?template=my-app`, { method: 'POST', headers: { Authorization: `Bearer ${phone}`, 'Content-Type': 'application/json', ...(secret && { 'X-Clrd-Secret': secret }) }, body: JSON.stringify(body) });
    const titles = async () => (await (await fetch(`${base}/api/v1/notifications?topic=tpl`, { headers: { Authorization: `Bearer ${phone}` } })).json()).notifications.map(n => n.title);
    const ctx = await browser.newContext();
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'templates' } })).ok(), 'templates sign-in');
    const sp = await ctx.newPage();
    await sp.goto(base + '/app/settings');
    await sp.getByText('No saved templates yet.').waitFor();
    const add = sp.locator('[data-template-add]');
    await add.getByLabel('Topic').fill('tpl');
    await add.getByLabel('Name').fill('my-app');
    await add.getByLabel('Title').fill('{{get . "name"}}');
    await add.getByLabel('ID', { exact: true }).fill('{{get . "id"}}');
    await add.getByLabel('Secret').fill('s3cret-129');
    await add.getByRole('button', { name: 'Add template' }).click();
    const card = sp.locator('[data-template="tpl/my-app"]');
    await card.waitFor();
    assert.match(await card.innerText(), /Checked with a secret\./, 'saved secret');
    assert.ok(!(await sp.content()).includes('s3cret-129'), 'the secret is in the Settings page');
    assert.equal((await post({ name: 'Disk full', id: 'd1' }, 's3cret-129')).status, 200, 'post with the secret');
    assert.ok((await titles()).includes('Disk full'), 'the saved template made no notification');

    // Edit with the secret left empty keeps it.
    await card.locator('summary', { hasText: 'Edit' }).click();
    const edit = card.locator('form[data-template-form]');
    assert.equal(await edit.getByLabel('Title').inputValue(), '{{get . "name"}}', 'Edit shows the saved title');
    await edit.getByLabel('Title').fill('Edited: {{get . "name"}}');
    await edit.getByRole('button', { name: 'Save template' }).click();
    await card.waitFor();
    assert.match(await card.innerText(), /Checked with a secret\./, 'a blank secret on Edit removed it');
    assert.equal((await post({ name: 'No secret', id: 'n1' })).status, 401, 'a post without the secret');
    assert.equal((await post({ name: 'Kept', id: 'k1' }, 's3cret-129')).status, 200, 'post after Edit');
    assert.ok((await titles()).includes('Edited: Kept'), 'the edit did not take');

    // Preview runs the unsaved fields over the sample and publishes nothing.
    const before = (await titles()).length;
    await card.locator('summary', { hasText: 'Edit' }).click();
    await edit.getByLabel('Title').fill('Try {{get . "name"}}');
    await edit.locator('summary', { hasText: 'Try it on a sample' }).click();
    await edit.getByLabel('Sample JSON').fill('{"name": "Preview only", "id": 9007199254740993}');
    await edit.getByRole('button', { name: 'Preview' }).click();
    await edit.locator('[data-template-out]').getByText('Try Preview only').waitFor();
    assert.match(await edit.locator('[data-template-status]').innerText(), /makes 1 notification\./, 'preview count');
    assert.match(await edit.locator('[data-template-out]').innerText(), /ID 9007199254740993\b/, 'preview shows the id, a big number unrounded');
    await edit.getByLabel('Sample JSON').fill('{oops');
    await edit.getByRole('button', { name: 'Preview' }).click();
    await edit.getByText('The sample is not valid JSON.').waitFor();
    assert.equal((await titles()).length, before, 'Preview published a notification');

    // Read only: no forms, and the form post saves nothing.
    const ro = execFileSync(bin, ['token', 'create', '--name', 'tpl-ro', '--scope', 'read'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const rctx = await browser.newContext();
    assert.ok((await rctx.request.post(base + '/api/v1/session', { data: { token: ro, name: 'tpl-ro' } })).ok(), 'read-only sign-in');
    const rp = await rctx.newPage();
    await rp.goto(base + '/app/settings');
    await rp.getByText('This device cannot save templates: its token cannot publish.').waitFor();
    assert.equal(await rp.locator('[data-template-form], [data-template]').count(), 0, 'template forms shown to a read-only device');
    await rctx.request.post(base + '/app/templates', { form: { topic: 'tpl', name: 'evil', title: 'x' }, headers: { Origin: base }, maxRedirects: 0 });
    await rctx.close();
    const listed = await (await fetch(base + '/api/v1/topics/tpl/templates', { headers: { Authorization: `Bearer ${laptop}` } })).json();
    assert.deepEqual(listed.webhooks.map(t => t.name), ['my-app'], 'a read-only device saved a template');

    // A field saved with a newline through the API keeps it through Edit (#376).
    const api = (method, body) => fetch(base + '/api/v1/topics/tpl/templates/multi', { method, headers: { Authorization: `Bearer ${laptop}`, 'Content-Type': 'application/json' }, ...(body && { body: JSON.stringify(body) }) });
    assert.ok((await api('PUT', { title: '\nBuild\n{{get . "name"}}', tags: 'a,\nb' })).ok, 'save a two-line template');
    await sp.reload();
    const multi = sp.locator('[data-template="tpl/multi"]');
    await multi.locator('summary', { hasText: 'Edit' }).click();
    await multi.getByRole('button', { name: 'Save template' }).click();
    await multi.waitFor();
    const kept = (await (await fetch(base + '/api/v1/topics/tpl/templates', { headers: { Authorization: `Bearer ${laptop}` } })).json()).webhooks.find(t => t.name === 'multi');
    assert.deepEqual([kept.title.replace(/\r/g, ''), kept.tags.replace(/\r/g, '')], ['\nBuild\n{{get . "name"}}', 'a,\nb'], 'Edit dropped a newline');
    assert.ok((await api('DELETE')).ok, 'delete the two-line template');
    await sp.reload();

    // Delete removes it, after a confirm.
    sp.once('dialog', d => d.accept());
    await card.getByRole('button', { name: 'Delete' }).click();
    await sp.getByText('No saved templates yet.').waitFor();
    await ctx.close();
    console.log('ok: webhook templates are added, edited, previewed and deleted in Settings; a read-only device gets no forms');
  }

  // Format check in Settings (#320): one group per sender, dismiss for one
  // sender, leave the topic out and check it again, undo. A read-only device
  // sees the findings but no buttons.
  {
    const bot = execFileSync(bin, ['token', 'create', '--name', 'fmt-bot', '--scope', 'publish', '--topics', 'fmtchk'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    await api(laptop, '/api/v1/notifications', { topic: 'fmtchk', message: 'untitled from laptop' });
    await api(bot, '/api/v1/notifications', { topic: 'fmtchk', message: 'untitled from the bot' });
    const ctx = await browser.newContext();
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'fmt' } })).ok(), 'format sign-in');
    const sp = await ctx.newPage();
    await sp.goto(base + '/app/settings');
    const botG = sp.locator('[data-format-topic="fmtchk/fmt-bot"]'), lapG = sp.locator('[data-format-topic="fmtchk/laptop"]');
    await botG.getByText('fmtchk from fmt-bot').waitFor();
    await lapG.waitFor();
    const click = b => Promise.all([sp.waitForEvent('load'), b.click()]);
    await click(botG.locator('li', { hasText: 'No title' }).getByRole('button', { name: 'Dismiss for this sender' }));
    assert.equal(await botG.count(), 0, 'dismissed for the bot, its group still shows');
    assert.equal(await lapG.count(), 1, 'dismissing for the bot hid the laptop too');
    const dis = sp.locator('[data-format-dismissed-item="fmtchk/no_title/fmt-bot"]');
    await dis.getByText('fmtchk: No title, from fmt-bot').waitFor();
    await click(lapG.getByRole('button', { name: 'Stop checking fmtchk' }));
    assert.equal(await lapG.count(), 0, 'a topic left out still shows');
    const off = (await (await fetch(base + '/api/v1/format-check', { headers: { Authorization: `Bearer ${laptop}` } })).json()).off;
    assert.deepEqual(off, ['fmtchk'], 'Stop checking did not reach the server');
    await click(sp.locator('[data-format-off-item="fmtchk"]').getByRole('button', { name: 'Check again' }));
    await lapG.waitFor();
    await click(dis.getByRole('button', { name: 'Undo' }));
    await botG.waitFor();
    assert.equal(await sp.locator('[data-format-dismissed-item^="fmtchk/"]').count(), 0, 'Undo left the dismissal listed');
    // Dismissed again so the read-only page below also lists a dismissal by the bot.
    await click(botG.locator('li', { hasText: 'No title' }).getByRole('button', { name: 'Dismiss for this sender' }));
    await dis.waitFor();

    const ro = execFileSync(bin, ['token', 'create', '--name', 'fmt-ro', '--scope', 'read', '--topics', 'fmtchk'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const rctx = await browser.newContext();
    assert.ok((await rctx.request.post(base + '/api/v1/session', { data: { token: ro, name: 'fmt-ro' } })).ok(), 'read-only sign-in');
    const rp = await rctx.newPage();
    await rp.goto(base + '/app/settings');
    // Not an admin, so other people's devices are not named (#245).
    await rp.locator('[data-format-topic="fmtchk/"]', { hasText: 'fmtchk from another device' }).first().waitFor();
    await rp.locator('[data-format-dismissed-item="fmtchk/no_title/"]', { hasText: 'from another device' }).waitFor();
    assert.ok(!(await rp.locator('[data-format-check]').innerHTML()).includes('fmt-bot'), 'a read-only device sees the bot\'s name, in text or markup');
    assert.equal(await rp.locator('[data-format-dismiss], [data-format-off]').count(), 0, 'a read-only device got dismiss buttons');
    await rctx.close();
    await ctx.close();
    console.log('ok: the format check groups by sender, dismisses, leaves a topic out and undoes in Settings');
  }
} finally {
  await browser?.close();
  server.kill();
}
