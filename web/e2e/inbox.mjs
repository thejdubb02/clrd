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

  // Empty first run: one tip card, send-a-test first; Next moves on, and Copy
  // holds this instance's own address.
  const tip = page.locator('[data-tip]:visible');
  assert.equal(await tip.count(), 1, 'one tip shows');
  assert.equal(await tip.locator('h4').textContent(), 'Send your first notification');
  await page.evaluate(() => { window.copied = []; navigator.clipboard.writeText = async t => { window.copied.push(t); }; });
  await tip.getByRole('button', { name: 'Copy' }).click();
  assert.ok((await page.evaluate(() => window.copied[0])).includes(`${base}/api/v1/notifications`), 'copy holds the base URL');
  await page.getByRole('button', { name: 'Next tip' }).click();
  assert.notEqual(await tip.locator('h4').textContent(), 'Send your first notification', 'Next changes the tip');
  assert.equal(await page.locator('[data-tip-count]').textContent(), '2 of 9');
  assert.equal(await announce.textContent(), await tip.locator('h4').textContent(), 'Next announces the new tip');
  console.log('ok: tip card shows, Next changes it, Copy has the base URL');

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
  await sp.getByLabel('Default').selectOption('24h'); // a time just typed can be the default before saving
  // While the save is on its way, the forms are locked: the reload after it would drop an edit (#214).
  let release;
  const held = new Promise(ok => { release = ok; });
  await sp.route('**/api/v1/settings', async r => { if (r.request().method() === 'PUT') await held; await r.continue(); });
  await sp.getByRole('button', { name: 'Save' }).click();
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
  await sp.getByRole('button', { name: 'Save' }).click();
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
    await api(phone, '/api/v1/notifications', { topic: 'constructor', title: 'Odd topic name', message: 'x' });
    await item('Odd topic name').waitFor();
    assert.equal(await stack.getByRole('button', { name: 'Hide' }).evaluate(b => b === document.activeElement), true, 'focus lost from the stack on refresh');
    // Clear all on the stack clears all four on the server, and Undo reopens them.
    await stack.getByRole('button', { name: 'Clear all' }).click();
    await sp.locator('[data-toast]').filter({ hasText: 'Cleared 4 notifications.' }).waitFor();
    for (const id of ids) assert.equal((await get(laptop, id)).state, 'dismissed');
    await sp.locator('[data-toast]').getByRole('button', { name: 'Undo' }).click();
    for (const id of ids) await sp.waitForFunction(async i => (await (await fetch(`/api/v1/notifications/${i}`)).json()).state === 'active', id);
    await stack.locator('[data-stack-count]', { hasText: '4 notifications' }).waitFor();
    // A single topic's page never stacks.
    await sp.goto(`${base}/app?topic=ci-stack`);
    assert.equal(await stack.count(), 0, 'stacked on the topic page');
    await sp.context().close();
    console.log('ok: Stack by topic folds a topic into one card that opens and clears');
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
} finally {
  await browser?.close();
  server.kill();
}
