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
// until polls fn in the page until it returns true. Never page.waitForFunction:
// Playwright runs its predicate through eval on every recheck after the first,
// and the page CSP refuses eval, so it fails only when the first check is false.
const until = async (p, fn, arg) => {
  for (let i = 0; i < 100; i++) { if (await p.evaluate(fn, arg)) return; await p.waitForTimeout(100); }
  throw new Error(`timed out waiting for ${fn}`);
};

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
  await until(page, async id => (await (await fetch(`/api/v1/notifications/${id}`)).json()).state === 'active', a.id);
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
  const box = await card('Renew domain').locator('details[open] [data-popover]').boundingBox();
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
  // The new copy can show its cards before its script runs: a press that starts
  // then is no long press at all (#714). Live means the script is up.
  await sp.locator('#sync.is-live').waitFor();
  await sc('Prune images').getByText('Snooze', { exact: true }).click({ delay: 700 });
  assert.equal(await sc('Prune images').locator('details[open] [data-popover]').isVisible(), true, 'long press did not open the snooze times');
  assert.deepEqual(await sc('Prune images').locator('[data-popover] [data-for]').allTextContents(), ['15 min', '2 hours', '2 days']);
  await sc('Prune images').locator('details[open]').evaluate(d => { d.open = false; });
  // A busy page handles the release before its 500 ms timer: still a long press,
  // never a tap that snoozes (#476). The page is held busy from 300 to 900 ms,
  // in its own browser: this one's clock is faked above, so a busy loop never ends.
  const bctx = await browser.newContext();
  assert.ok((await bctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'busy-web' } })).ok());
  const bz = await bctx.newPage();
  await bz.goto(`${base}/app`);
  const pb = await bz.locator('article.card', { hasText: 'Prune images' }).getByText('Snooze', { exact: true }).boundingBox();
  await bz.mouse.move(pb.x + pb.width / 2, pb.y + pb.height / 2);
  await bz.evaluate(() => document.addEventListener('pointerdown', () => setTimeout(() => { const t = performance.now(); while (performance.now() - t < 600); }, 300), { once: true, capture: true }));
  await bz.mouse.down(); await bz.waitForTimeout(700); await bz.mouse.up();
  assert.equal(await bz.locator('article.card', { hasText: 'Prune images' }).locator('details[open] [data-popover]').isVisible(), true, 'a long press on a busy page did not open the snooze times');
  assert.equal((await get(laptop, hold.id)).state, 'active', 'a long press on a busy page snoozed');
  await bctx.close();
  await sc('Prune images').getByText('Snooze', { exact: true }).click({ button: 'right' });
  assert.equal(await sc('Prune images').locator('details[open] [data-popover]').isVisible(), true, 'right click did not open the snooze times');
  await sp.waitForTimeout(300);
  assert.equal((await get(laptop, hold.id)).state, 'active', 'long press or right click snoozed');
  await sc('Prune images').locator('details[open]').evaluate(d => { d.open = false; });
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
  // Appearance (#583): a saved choice beats the device, at once and on every page, before
  // first paint; System follows the device again.
  const dark = () => sp.evaluate(() => document.documentElement.classList.contains('dark'));
  await sp.emulateMedia({ colorScheme: 'light' });
  await sp.getByLabel('Light or dark').selectOption('dark');
  assert.ok(await dark(), 'Dark did not apply at once');
  assert.equal(await sp.evaluate(() => getComputedStyle(document.body).backgroundColor), 'rgb(15, 17, 20)', 'Dark set the class but not the colors');
  await sp.addInitScript(() => new MutationObserver((_, o) => {
    if (document.body) { window.darkAtBody = document.documentElement.classList.contains('dark'); o.disconnect(); }
  }).observe(document, { childList: true, subtree: true }));
  for (const path of ['/', '/docs/spec', '/app/settings']) {
    await sp.goto(base + path);
    assert.equal(await sp.evaluate(() => window.darkAtBody), true, `${path} painted light before going dark`);
  }
  assert.equal(await sp.getByLabel('Light or dark').inputValue(), 'dark', 'the saved choice did not show');
  await sp.emulateMedia({ colorScheme: 'dark' });
  await sp.getByLabel('Light or dark').selectOption('light');
  assert.equal(await dark(), false, 'Light lost to a dark device');
  await sp.getByLabel('Light or dark').selectOption('');
  assert.ok(await dark(), 'System did not follow a dark device');
  await sp.emulateMedia({ colorScheme: 'light' });
  // The device change arrives as an event, so poll for it (#714).
  await until(sp, () => !document.documentElement.classList.contains('dark'))
    .catch(() => assert.fail('System did not follow the device changing'));
  // Color themes (#584): preview locally, reject faint text, then follow the
  // saved palette live in an independent browser and before its next paint.
  const theme = sp.getByLabel('Color theme', { exact: true });
  const saveTheme = sp.getByRole('button', { name: 'Save theme' });
  const paletteStatus = sp.locator('[data-palette-status]');
  const peerContext = await browser.newContext({ colorScheme: 'dark' });
  const themePeer = await peerContext.newPage();
  await themePeer.addInitScript(() => new MutationObserver((_, o) => {
    if (document.body) {
      window.paletteAtBody = document.documentElement.style.getPropertyValue('--background');
      o.disconnect();
    }
  }).observe(document, { childList: true, subtree: true }));
  await themePeer.goto(`${base}/app?auth=${link}`);
  const bg = p => p.evaluate(() => getComputedStyle(document.body).backgroundColor);
  assert.equal(await bg(sp), 'rgb(245, 243, 238)', 'Default changed the light appearance');
  assert.equal(await bg(themePeer), 'rgb(15, 17, 20)', 'Default changed the dark appearance');
  await theme.selectOption('midnight');
  assert.equal(await bg(sp), 'rgb(237, 241, 248)', 'Midnight did not preview locally');
  assert.equal(await bg(themePeer), 'rgb(15, 17, 20)', 'unsaved preview leaked to another browser');
  await saveTheme.click();
  await until(themePeer, () => getComputedStyle(document.body).backgroundColor === 'rgb(0, 0, 0)');
  await themePeer.reload();
  assert.equal(await themePeer.evaluate(() => window.paletteAtBody), '#000000', 'Midnight applied after the body appeared');
  await theme.selectOption('custom');
  await sp.getByLabel('Text', { exact: true }).fill('#ffffff');
  assert.match(await paletteStatus.textContent(), /Text on background.*4\.5:1/);
  assert.equal(await saveTheme.isDisabled(), true, 'faint text could be saved');
  await sp.getByLabel('Text', { exact: true }).fill('#101010');
  await sp.getByLabel('Background', { exact: true }).fill('#f1e4d2');
  assert.equal(await bg(sp), 'rgb(241, 228, 210)', 'custom background did not preview');
  assert.equal(await bg(themePeer), 'rgb(0, 0, 0)', 'unsaved custom colors reached another browser');
  // A successful save in another Settings section reloads the page.
  await sp.getByLabel('Times').fill('1h, 3h');
  await sp.locator('[data-snooze-settings]').getByRole('button', { name: 'Save' }).click();
  await sp.locator('[data-snooze="1h,1h,3h"]').waitFor({ state: 'attached' });
  await saveTheme.waitFor();
  assert.equal(await sp.getByLabel('Background', { exact: true }).inputValue(), '#f1e4d2', 'snooze save dropped the palette draft');
  assert.equal(await bg(sp), 'rgb(241, 228, 210)', 'snooze save dropped the palette preview');
  await themePeer.locator('[data-snooze="1h,1h,3h"]').waitFor({ state: 'attached' });
  assert.equal(await bg(themePeer), 'rgb(0, 0, 0)', 'saving snooze times published the palette draft');
  await saveTheme.click();
  await until(themePeer, () => getComputedStyle(document.body).backgroundColor === 'rgb(241, 228, 210)');
  assert.equal(await themePeer.evaluate(() => document.documentElement.classList.contains('dark')), false, 'custom light background should use light controls');
  const override = sp.getByLabel('Theme on this browser');
  await override.selectOption('high-contrast');
  await until(sp, () => getComputedStyle(document.body).backgroundColor === 'rgb(255, 255, 255)');
  assert.equal(await bg(themePeer), 'rgb(241, 228, 210)', 'device override changed the saved theme');
  await override.selectOption('');
  await until(sp, () => getComputedStyle(document.body).backgroundColor === 'rgb(241, 228, 210)');
  await theme.selectOption('default');
  await saveTheme.click();
  await until(themePeer, () => getComputedStyle(document.body).backgroundColor === 'rgb(15, 17, 20)');
  assert.equal((await themePeer.request.delete(base + '/api/v1/devices/this')).status(), 204);
  await peerContext.close();
  await putSettings({ snooze_intervals: ['1h', '4h'] });
  await sp.close();
  console.log('ok: snooze, appearance, and synced color themes');

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
  assert.deepEqual(sent[0].devices, [], 'a send with nothing ticked was aimed at chosen devices');
  console.log('ok: send a notification from the inbox');

  // Alert only these devices (#82): off by default, sends the ticked ids, clears
  // after a send, and a device the server refuses is named, not shown as an id.
  {
    const pumps = execFileSync(bin, ['token', 'create', '--name', 'pumps-only', '--scope', 'read', '--topics', 'pumps'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const all = (await (await fetch(`${base}/api/v1/devices?all=1`, { headers: { Authorization: `Bearer ${laptop}` } })).json()).devices;
    const idOf = n => all.find(d => d.name === n).id;
    const dp = await page.context().newPage();
    await dp.goto(`${base}/app`);
    const box = dp.getByRole('dialog', { name: 'Send a notification' });
    await dp.getByRole('button', { name: 'Send', exact: true }).click();
    assert.ok(!(await box.getByLabel('pumps-only').isVisible()), 'the device picker starts open');
    await box.locator('summary', { hasText: 'Alert only these devices' }).click();
    assert.equal(await box.locator('[name=devices]:checked').count(), 0, 'a device is ticked by default');
    await box.getByLabel('Topic').fill('notes');
    await box.getByLabel('Message').fill('only the phone');
    await box.getByLabel('pixel-9').check();
    await box.getByLabel('pumps-only').check();
    await box.getByRole('button', { name: 'Send' }).click();
    const refused = box.locator('[data-send-status]').filter({ hasText: '"pumps-only" is not a device that may read' });
    await refused.waitFor();
    assert.ok(!(await refused.textContent()).includes(idOf('pumps-only')), 'the refusal shows an id, not the name');
    await box.getByLabel('pumps-only').uncheck();
    await box.getByRole('button', { name: 'Send' }).click();
    await box.getByLabel('Message').waitFor({ state: 'hidden' });
    const got = (await (await fetch(`${base}/api/v1/notifications?topic=notes`, { headers: { Authorization: `Bearer ${phone}` } })).json()).notifications.find(n => n.message === 'only the phone');
    assert.deepEqual(got.devices, [idOf('pixel-9')], 'the ticked device was not the target');
    await dp.getByRole('button', { name: 'Send', exact: true }).click();
    assert.equal(await box.locator('[name=devices]:checked').count(), 0, 'a ticked device carried over to the next send');
    await dp.close();
    assert.ok(pumps, 'pumps-only token');
    console.log('ok: a send from the inbox can alert only chosen devices');
  }

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

  // A typed answer (#153): the text action gets a box, the others stay buttons;
  // a refusal keeps what was typed; the answer shows as plain text.
  {
    const tq = await api(phone, '/api/v1/notifications', { topic: 'agents', title: 'Name the release?', actions: [{ label: 'Keep', reply: 'keep' }, { label: 'Other', reply: 'other', text: true }] });
    await page.reload();
    const tc = card('Name the release?');
    const box = tc.getByLabel('Other: type an answer');
    await tc.getByRole('button', { name: 'Keep' }).waitFor();
    await tc.getByRole('button', { name: 'Other' }).click();
    await page.locator('[data-toast]').filter({ hasText: 'Type an answer first.' }).waitFor({ state: 'attached' });
    await box.fill('x'.repeat(1001));
    await tc.getByRole('button', { name: 'Other' }).click();
    await tc.locator('[data-note]').filter({ hasText: 'Not answered:' }).waitFor();
    assert.equal((await box.inputValue()).length, 1001, 'a refused answer lost what was typed');
    assert.ok(!(await get(laptop, tq.id)).reply, 'an over-long answer was stored');
    // A live change (a mark from another device) redraws the card: the half-typed answer stays.
    {
      const lp = await page.context().newPage(); // page no longer hears live events (above)
      await lp.goto(page.url());
      await lp.locator('.is-live').waitFor();
      const lc = lp.locator('article.card').filter({ hasText: 'Name the release?' }), lbox = lc.getByLabel('Other: type an answer');
      await lbox.fill('half typed');
      const seq0 = await lc.getAttribute('data-seq');
      await api(phone, `/api/v1/notifications/${tq.id}/mark`, { mark: 'seen' });
      await lp.locator(`article.card[data-id="${tq.id}"]:not([data-seq="${seq0}"])`).waitFor({ state: 'attached' });
      assert.equal(await lbox.inputValue(), 'half typed', 'a live update wiped a half-typed answer');
      assert.ok(await lbox.evaluate(e => e === document.activeElement), 'a live update took focus off the text box');
      // A new card arriving moves this unchanged card: typing goes on in the box (#517).
      await api(phone, '/api/v1/notifications', { topic: 'agents', title: 'Arrived 517' });
      await lp.locator('article.card', { hasText: 'Arrived 517' }).waitFor();
      assert.ok(await lbox.evaluate(e => e === document.activeElement), 'a new card took focus off the text box');
      await lp.close();
    }
    // Shift+Enter makes a new line (#517); Enter sends.
    await box.fill('  <b>calm</b>');
    await box.press('Shift+Enter');
    await box.pressSequentially('fox  ');
    assert.ok(!(await get(laptop, tq.id)).reply, 'Shift+Enter sent the answer');
    await box.press('Enter');
    await announce.filter({ hasText: 'Answered Other' }).waitFor({ state: 'attached' });
    const ta = await get(laptop, tq.id);
    assert.equal(ta.reply, 'other');
    assert.equal(ta.reply_text, '<b>calm</b>\nfox', 'the inner line break was lost');
    await tc.locator('[data-reply-text]').filter({ hasText: 'Answer: <b>calm</b> fox' }).waitFor();
    assert.match(await tc.locator('[data-reply-text]').innerText(), /calm<\/b>\nfox/, 'the shown answer lost its line break');
    assert.equal(await tc.getByLabel('Other: type an answer').count(), 0, 'answered card still offers the text box');
    console.log("ok: a typed answer is sent, refused text stays, Shift+Enter keeps a line break, the answer shows as text");
  }

  // Marks (#451): Enter in the note marks it actioned, never acks or answers
  // the card; Remove takes only this reader's mark off.
  const mq = await api(phone, '/api/v1/notifications', { topic: 'agents', title: 'Rollback 214?', actions: [{ label: 'Go', reply: 'go' }] });
  await api(phone, `/api/v1/notifications/${mq.id}/mark`, { mark: 'seen' });
  await page.reload();
  await card('Rollback 214?').locator('summary[aria-label="More snooze times"]').click();
  await card('Rollback 214?').locator('[data-mark-menu] summary').click();
  await until(page, () => [...document.querySelectorAll('article')].find(a => a.textContent.includes('Rollback 214?')).querySelectorAll('details[open]').length === 1);
  await card('Rollback 214?').getByLabel('Note').fill('on it');
  await card('Rollback 214?').getByLabel('Note').press('Enter');
  await announce.filter({ hasText: 'Marked actioned.' }).waitFor({ state: 'attached' });
  await card('Rollback 214?').locator('[data-mark="actioned"]', { hasText: 'laptop actioned: on it' }).waitFor();
  let mn = await get(laptop, mq.id);
  assert.equal(mn.state, 'active', 'Enter in the note changed the card state');
  assert.ok(!mn.reply, 'Enter in the note answered the card');
  assert.equal(mn.marks.length, 2, 'own mark not added next to the phone one');
  await card('Rollback 214?').getByText('Marked actioned').click();
  await card('Rollback 214?').getByRole('button', { name: 'Remove my mark' }).click();
  await announce.filter({ hasText: 'Your mark is removed.' }).waitFor({ state: 'attached' });
  await until(page, () => !document.querySelector('[data-mark="actioned"]'));
  mn = await get(laptop, mq.id);
  assert.deepEqual(mn.marks.map(m => m.by), ['pixel-9'], 'Remove took the wrong mark');
  console.log('ok: mark a card seen or actioned and remove your own mark');

  // A color rule change recolors a card in place: its open mark menu and draft note stay (#696).
  {
    const cp = await page.context().newPage(); // page no longer hears live events (above)
    await cp.goto(page.url());
    await cp.locator('.is-live').waitFor();
    const rc = cp.locator('article.card', { hasText: 'Rollback 214?' });
    await rc.locator('[data-mark-menu] summary').click();
    await rc.getByLabel('Note').fill('half typed');
    await putSettings({ color_rules: [{ title: 'rollback', color: '#DC2626', style: 'solid' }] });
    await until(cp, () => document.querySelector('article.cc-DC2626.cs-solid [data-mark-menu][open]'));
    assert.equal(await rc.getByLabel('Note').inputValue(), 'half typed', 'recolor lost the draft note');
    await putSettings({ color_rules: [] });
    await until(cp, () => !document.querySelector('article.cs-solid'));
    await cp.close();
  }
  console.log('ok: a color rule change keeps an open menu and its draft');

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
  const menu = await wide.locator('details[open] [data-popover]').boundingBox();
  assert.ok(menu.x >= 0 && menu.x + menu.width <= 360, `snooze menu spans ${menu.x} to ${menu.x + menu.width} on a 360 px screen`);
  assert.equal(await narrow.evaluate(() => document.documentElement.scrollWidth), 360, 'page scrolls sideways with the menu open');
  // A very small phone too.
  await narrow.setViewportSize({ width: 300, height: 740 });
  const small = await wide.locator('details[open] [data-popover]').boundingBox();
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
  const tr = await api(phone, '/api/v1/notifications', { topic: 'home', title: 'Which door?', actions: [{ label: 'Say', reply: 'say', text: true }] });
  await nojs.goto(`${base}/app?topic=home`);
  await plain('Which door?').getByLabel('Say: type an answer').fill('back\ndoor');
  await plain('Which door?').getByRole('button', { name: 'Say' }).click();
  await plain('Which door?').getByText('Answer: back door').waitFor();
  assert.equal((await get(laptop, tr.id)).reply_text, 'back\ndoor', 'a form post kept CRLF line breaks (#517)');
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
    // Each refresh rebuilds the stack node, so a locator's evaluate can land on
    // the old, detached copy: look it up and check it in one step (#675).
    const onStack = (sel, cls) => sp.evaluate(([sel, cls]) => { const s = document.querySelector('[data-stack="ci-stack"]'), e = s && (sel ? s.querySelector(sel) : s); return !!e && e === document.activeElement && (!cls || s.classList.contains(cls)); }, [sel, cls]);
    assert.equal(await onStack('[data-stack-toggle][aria-expanded="true"]'), true, 'focus lost from the stack on refresh'); // still open: Hide, not Show all
    // Clear all on the stack clears all four on the server, and Undo reopens them.
    await stack.getByRole('button', { name: 'Clear all' }).click();
    await sp.locator('[data-toast]').filter({ hasText: 'Cleared 4 notifications.' }).waitFor();
    for (const id of ids) assert.equal((await get(laptop, id)).state, 'dismissed');
    await sp.locator('[data-toast]').getByRole('button', { name: 'Undo' }).click();
    for (const id of ids) await until(sp, async i => (await (await fetch(`/api/v1/notifications/${i}`)).json()).state === 'active', id);
    await stack.locator('[data-stack-count]', { hasText: '4 notifications' }).waitFor();
    // J lands on the closed stack and Enter opens it (#227).
    await sp.reload(); // nothing selected, so J starts from the top
    await stack.waitFor();
    for (let i = 0; i < 200 && !(await sp.evaluate(() => document.querySelector('[data-stack="ci-stack"]')?.classList.contains('is-selected'))); i++) await sp.keyboard.press('j');
    assert.equal(await onStack(null, 'is-selected'), true, 'J skipped the closed stack');
    await sp.keyboard.press('c');
    await until(sp, () => document.querySelector('[data-announce]').textContent === 'Open the stack to act on one notification.');
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
    // The exit path marks the card leaving first: watch for that, not the clock (#714).
    await sp.evaluate(id => { const c = document.querySelector(`.card[data-id="${id}"][hidden]`); window.hiddenLeft = !c;
      if (c) new MutationObserver(() => { if (c.classList.contains('is-leaving')) window.hiddenLeft = true; }).observe(c, { attributes: true }); }, ids[3]);
    await api(phone, `/api/v1/notifications/${ids[3]}/dismiss`);
    await stack.locator('[data-stack-count]', { hasText: '3 notifications' }).waitFor();
    assert.equal(await sp.evaluate(() => window.hiddenLeft), false, 'the hidden card was missing or took the exit timer');
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
    // While one stack's Clear all waits for its reply, another device clears a
    // card inside a different closed stack: that stack's count changes at
    // once, and the waiting stack stays as it is (#397).
    await sp.unroute('**/api/v1/notifications/clear');
    const holdClear = () => {
      let go; const held = new Promise(r => { go = r; });
      return sp.route('**/api/v1/notifications/clear', async route => { const res = await route.fetch(); await held; await route.fulfill({ response: res }); }).then(() => go);
    };
    const late3 = await send('late', 'Late 3');
    for (const t of ['Pump 5 down', 'Pump 6 down']) await send('pumps', t);
    await stackOf('late').locator('[data-stack-count]', { hasText: '3 notifications' }).waitFor();
    await stackOf('pumps').waitFor();
    let go = await holdClear();
    await stackOf('pumps').getByRole('button', { name: 'Clear all' }).click();
    await api(phone, `/api/v1/notifications/${late3}/dismiss`);
    await stackOf('late').locator('[data-stack-count]', { hasText: '2 notifications' }).waitFor({ timeout: 1000 });
    assert.equal(await stackOf('pumps').locator('[data-stack-count]').textContent(), '2 notifications', 'the waiting stack changed before its reply');
    go();
    await stackOf('pumps').waitFor({ state: 'detached' });
    // A page load already on its way when Clear all starts: its answer, from
    // before the clear, changes nothing until the clear's reply (#397).
    await sp.unroute('**/api/v1/notifications/clear');
    let loadGo, loaded; const loadHeld = new Promise(r => { loadGo = r; }), asked = new Promise(r => { loaded = r; });
    const isLoad = u => new URL(u).pathname === '/app';
    await sp.route(isLoad, async route => {
      if (route.request().resourceType() !== 'fetch') return route.continue();
      // Hold only the load that has Late 4, so an older refresh cannot stand in for it.
      const res = await route.fetch();
      if ((await res.text()).includes('Late 4')) { loaded(); await loadHeld; }
      await route.fulfill({ response: res });
    });
    go = await holdClear();
    await send('late', 'Late 4');
    await asked;
    await stackOf('late').getByRole('button', { name: 'Clear all' }).click();
    loadGo();
    await sp.waitForTimeout(1000); // nothing to wait on: the late answer must change nothing
    assert.equal(await stackOf('late').locator('[data-stack-count]').textContent(), '2 notifications', 'a page load from before the clear changed the stack');
    go();
    await sp.locator('[data-stack="late"].is-leaving').waitFor();
    await sp.locator('.card-title', { hasText: 'Late 4' }).waitFor();
    await sp.unroute(isLoad);
    await sp.unroute('**/api/v1/notifications/clear');
    await sp.context().close();
    console.log('ok: stacks stay right while Clear all waits, while one fades, and with a load in flight');
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

  // Add an agent (#456): Settings makes a one-use invite, the agent claims it,
  // shows in Linked devices as an agent and can be unlinked there.
  {
    const ag = await page.context().newPage(); // page no longer hears live events (above)
    await ag.goto(base + '/app/settings');
    const at = ag.url();
    await ag.locator('.is-live').waitFor({ timeout: 5000 }); // the stream is up, so the new agent shows without a reload
    await ag.getByRole('button', { name: 'Add agent' }).click();
    await ag.getByLabel('Agent name').fill('build-bot');
    await ag.locator('[data-agent-form] [name=more]').fill('builds, deploys');
    assert.equal(await ag.getByLabel('Invite works for').inputValue(), '15m', 'default 15 minutes');
    const [made] = await Promise.all([
      ag.waitForResponse(r => r.url().endsWith('/api/v1/agents/invite')),
      ag.getByRole('button', { name: 'Make invite' }).click(),
    ]);
    assert.equal(made.status(), 201, 'invite made');
    const inv = await made.json();
    assert.deepEqual(JSON.parse(made.request().postData()).topics, ['builds', 'deploys'], 'topics from the box');
    await ag.getByText('It works once, until').waitFor();
    assert.equal(await ag.locator('[data-agent-invite]').textContent(), inv.url, 'shows the invite');
    assert.equal(ag.url(), at, 'the code is never in the address');
    assert.ok(await ag.evaluate(() => document.activeElement === document.querySelector('[data-agent-invite]').parentElement), 'focus moves to the invite');
    assert.match(await ag.locator('[data-agent-done]').textContent(), /May approve starts at Denied/, 'says approvals start denied');
    const claim = await fetch(base + '/api/v1/agents/claim', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code: inv.code }) });
    assert.equal(claim.status, 200, 'agent claims');
    const { token: bot } = await claim.json();
    const row = ag.locator('tr', { hasText: 'build-bot' });
    await row.waitFor({ timeout: 5000 });
    assert.match(await row.textContent(), /Agent/, 'row says agent');
    // A second agent of the same name: the server's reason shows in the form.
    await ag.getByRole('button', { name: 'Add another agent' }).click();
    await ag.getByLabel('Agent name').fill('build-bot');
    await ag.locator('[data-agent-form] [name=more]').fill('builds');
    await ag.getByRole('button', { name: 'Make invite' }).click();
    await ag.locator('[data-agent-status]', { hasText: 'exists' }).waitFor();
    await row.getByRole('button', { name: 'Unlink' }).click();
    await ag.getByRole('dialog').getByRole('button', { name: 'Unlink' }).click();
    await row.waitFor({ state: 'detached' });
    const after = await fetch(base + '/api/v1/notifications', { headers: { Authorization: `Bearer ${bot}` } });
    assert.equal(after.status, 401, 'unlinked agent is signed out');
    await ag.close();
    console.log('ok: add an agent from Settings, see it as an agent, unlink it');
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
    await until(sp, () => document.querySelector('[data-sound-setting]').value === 'bell');
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
    // The notice hides after 6s, which a throttled page can pass before the card shows (#714).
    await sp.getByText('Sound is blocked until you click or press a key on this page.').waitFor({ state: 'attached' });
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
    await until(kp, () => !document.querySelector('[data-topic-keep] select').disabled);
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
    await sp.locator('h3', { hasText: 'Silences' }).waitFor();
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

    // A body template saves from Edit, comes back in the form, and clears.
    const tmpl = '{"text": {{json .notification.title}}}';
    const hookBody = async () => (await (await fetch(base + '/api/v1/topics/hooked/hooks', { headers: { Authorization: `Bearer ${laptop}` } })).json()).hooks[0].body;
    await card.locator('summary', { hasText: 'Edit' }).click();
    assert.ok(!(await edit.getByRole('button', { name: 'Preview' }).isVisible()), 'Preview shown without script, where it cannot work');
    await edit.getByLabel('Body template').fill(tmpl);
    await edit.getByRole('button', { name: 'Save webhook' }).click();
    await card.getByText('Custom body.').waitFor();
    assert.equal(await hookBody(), tmpl, 'the body template was not saved');
    // A form from before the field (no body sent) keeps the saved template.
    const old = await ctx.request.post(base + '/app/hooks', { form: { topic: 'hooked', name: 'ops', url: 'https://ops2.example.invalid/in' }, headers: { Origin: base }, maxRedirects: 0 });
    assert.equal(old.status(), 303, `old-form hook save: ${old.status()}`);
    assert.equal(await hookBody(), tmpl, 'a save without the body field wiped the template');
    // With script, Preview renders the field against a sample event (#495),
    // says why a broken one fails, and saves nothing.
    {
      const jctx = await browser.newContext();
      assert.ok((await jctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'hooks-js' } })).ok(), 'hooks-js sign-in');
      const jp = await jctx.newPage();
      await jp.goto(base + '/app/settings');
      const jcard = jp.locator('[data-hook="hooked/ops"]');
      await jcard.locator('summary', { hasText: 'Edit' }).click();
      const jedit = jcard.locator('form', { has: jp.getByRole('button', { name: 'Save webhook' }) });
      const out = jedit.locator('[data-hook-preview-out]');
      await jedit.getByLabel('Body template').fill(tmpl.slice(0, -1));
      await jedit.getByRole('button', { name: 'Preview' }).click();
      await out.getByText('not valid JSON').waitFor();
      assert.ok((await out.getAttribute('class')).includes('text-destructive'), 'a broken preview is not shown as an error');
      await jedit.getByLabel('Body template').fill('{"t": {{json .notification.title}}}');
      await jedit.getByRole('button', { name: 'Preview' }).click();
      await out.getByText('{"t": "Disk full"}', { exact: true }).waitFor();
      assert.ok(!(await out.getAttribute('class')).includes('text-destructive'), 'a good preview is shown as an error');
      assert.equal(await hookBody(), tmpl, 'Preview saved the template');
      // A slow earlier answer never replaces the latest one.
      let slow = true;
      await jp.route('**/hooks/preview', async r => { if (slow) { slow = false; await new Promise(ok => setTimeout(ok, 800)); } await r.continue(); });
      const late = jp.waitForResponse(r => r.url().endsWith('/hooks/preview') && r.status() === 400);
      await jedit.getByLabel('Body template').fill(tmpl.slice(0, -1));
      await jedit.getByRole('button', { name: 'Preview' }).click();
      await jedit.getByLabel('Body template').fill('{"u": {{json .notification.title}}}');
      await jedit.getByRole('button', { name: 'Preview' }).click();
      await out.getByText('{"u": "Disk full"}', { exact: true }).waitFor();
      await late;
      await jp.evaluate(() => new Promise(ok => setTimeout(() => requestAnimationFrame(() => requestAnimationFrame(ok)), 50)));
      assert.equal(await out.textContent(), '{"u": "Disk full"}', 'a stale preview replaced the latest one');
      await jctx.close();
    }
    await sp.reload();
    await card.locator('summary', { hasText: 'Edit' }).click();
    assert.equal((await edit.getByLabel('Body template').inputValue()).trim(), tmpl, 'Edit does not show the saved body');
    await edit.getByLabel('Body template').fill('');
    await edit.getByRole('button', { name: 'Save webhook' }).click();
    await card.getByText('Custom body.').waitFor({ state: 'detached' });
    assert.equal(await hookBody(), '', 'clearing the body template kept it');

    // Not admin: no hooks on the page, and the form post is refused.
    const pub = execFileSync(bin, ['token', 'create', '--name', 'hooks-pub', '--scope', 'read,publish'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const rctx = await browser.newContext();
    assert.ok((await rctx.request.post(base + '/api/v1/session', { data: { token: pub, name: 'hooks-pub' } })).ok(), 'publish sign-in');
    const rp = await rctx.newPage();
    await rp.goto(base + '/app/settings');
    await rp.locator('#templates').waitFor();
    assert.equal(await rp.locator('#webhooks').count(), 0, 'Webhooks shown to a device that cannot open them');
    assert.equal(await rp.locator('#keep-cleared').count(), 0, 'Cleared messages shown to a device that cannot change it');
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
    console.log('ok: webhooks are added, logged, edited (body template and preview too) and deleted in Settings; a non-admin sees none');
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

    // A typed answer from the Ask browser: the confirm prompt shows the text (#153).
    const tq = await api(phone, '/api/v1/notifications', { topic: 'agents', title: 'Why 153?', actions: [{ label: 'Because', reply: 'because', text: true }] });
    await ap.goto(base + '/app');
    const tcard = ap.locator('article.card', { hasText: 'Why 153?' });
    await tcard.getByLabel('Because: type an answer').fill('the tests pass');
    await tcard.getByRole('button', { name: 'Because' }).click();
    await ap.locator('[data-toast]').filter({ hasText: 'Sent for confirmation.' }).waitFor();
    await bp.goto(base + '/app');
    const prompt = bp.locator('article.card', { hasText: "Confirm asker-web's answer: Because" });
    await prompt.getByText('Answer: the tests pass').waitFor();
    await prompt.getByRole('button', { name: 'Confirm' }).click();
    for (let i = 0; (await get(laptop, tq.id)).reply_text !== 'the tests pass'; i++) { assert.ok(i < 20, 'confirm did not store the typed answer'); await new Promise(r => setTimeout(r, 250)); }

    // The allowed browser lowers the asker token to Denied from Settings.
    // Pick after the redraw that every page load does once it is live, so the
    // pick is not racing it (#476). The menu's handler is on the document and
    // survives a redraw; this keeps the step about the menu, not the timing.
    const settled = bp.waitForResponse(r => r.url() === base + '/app/settings' && r.request().resourceType() === 'fetch');
    await bp.goto(base + '/app/settings');
    await (await settled).finished(); // the whole body, which the redraw reads before it swaps
    const sel = bp.locator('select[data-name="asker"]');
    assert.deepEqual(await sel.locator('option').allTextContents(), ['Denied', 'Ask', 'Allowed'], 'menu for another tree');
    // Wait on the saved setting, not the toast: a toast can come and go
    // before a busy runner looks for it (#599).
    const devices = async () => (await (await fetch(base + '/api/v1/devices?all=1', { headers: { Authorization: `Bearer ${laptop}` } })).json()).devices;
    const saved = async (name, want, msg) => {
      for (let i = 0; (await devices()).find(d => d.name === name).approve !== want; i++) { assert.ok(i < 40, msg); await new Promise(r => setTimeout(r, 250)); }
    };
    await sel.selectOption('denied');
    await saved('asker', 'denied', 'the menu did not save');
    // The save redraws the table: a pick on the old menu as it is swapped out
    // reaches no handler (#714). The redrawn menu marks Denied as saved.
    await sel.locator('option[value="denied"][selected]').waitFor({ state: 'attached' });

    // Its own row lowers after a confirm, and then the menus are gone: nothing raises it back here.
    const own = bp.locator('select[data-name="boss-web"]');
    assert.ok(await own.evaluate(s => s.hasAttribute('data-own')), 'own row not marked');
    bp.once('dialog', d => { assert.match(d.message(), /Lower boss-web to Ask\?/); d.accept(); });
    await own.selectOption('ask');
    await saved('boss-web', 'ask', 'boss-web did not lower itself to Ask');
    await bp.reload();
    assert.equal(await bp.locator('[data-approve]').count(), 0, 'a device that lowered itself to Ask can still change settings');
    const up = await bctx.request.put(base + '/api/v1/devices/' + (await devices()).find(d => d.name === 'boss-web').id + '/approve', { data: { approve: 'allowed' }, headers: { Origin: base } });
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

  // A topic's first question flags it for approvals (#623): an older card in
  // that topic, kept as it was by the live update, still warns on Unsubscribe.
  {
    await api(phone, '/api/v1/notifications', { topic: 'qflag', title: 'Plain one' });
    const ctx = await browser.newContext();
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'qflag' } })).ok(), 'qflag sign-in');
    const qp = await ctx.newPage();
    await qp.goto(base + '/app');
    const old = qp.locator('article.card', { hasText: 'Plain one' }).locator('[data-unsub]');
    await old.waitFor({ state: 'attached' });
    await old.evaluate(b => { b.closest('article').kept = true; });
    const sq = await api(phone, '/api/v1/notifications', { topic: 'qflag', title: 'Ship it?', actions: [{ label: 'Yes', reply: 'yes' }] });
    await qp.locator('article.card', { hasText: 'Ship it?' }).waitFor();
    // The question leaves the inbox: the older card still warns on its own (#700).
    await api(phone, `/api/v1/notifications/${sq.id}/dismiss`);
    await qp.locator('article.card', { hasText: 'Ship it?' }).waitFor({ state: 'detached' });
    assert.ok(await old.evaluate(b => b.closest('article').kept), 'the older card was redrawn, so this does not test the live case');
    let msg = '';
    qp.once('dialog', d => { msg = d.message(); d.dismiss(); });
    await old.dispatchEvent('click');
    await qp.waitForTimeout(300);
    assert.match(msg, /Unsubscribe anyway/, 'no warning from the older card');
    await ctx.close();
    console.log('ok: a topic that got a question warns on Unsubscribe from any of its cards');
  }

  // Subscriptions follow-ups (#649): a change from another device reloads the
  // page and keeps a half-typed answer; Undo shows only for an Unsubscribe
  // that landed.
  {
    const q = await api(phone, '/api/v1/notifications', { topic: 'drafts', title: 'Name it?', actions: [{ label: 'Say', reply: 'say', text: true }] });
    await api(phone, '/api/v1/notifications', { topic: 'drafts2', title: 'Leave me' });
    const sign = async name => { const c = await browser.newContext(); assert.ok((await c.request.post(base + '/api/v1/session', { data: { token: laptop, name } })).ok(), name + ' sign-in'); return c; };
    const ctx = await sign('drafts'), other = await sign('drafts-other');
    const dp = await ctx.newPage();
    await dp.goto(base + '/app');
    await dp.locator(`[data-id="${q.id}"] [data-text-reply] [name=text]`).fill('half typed');
    const put = (c, topics, request_id) => c.request.put(base + '/api/v1/subscriptions', { data: { topics, request_id }, headers: { Origin: base } });
    const [, r1] = await Promise.all([dp.waitForEvent('load'), put(other, { drafts2: false })]);
    assert.ok(r1.ok(), 'unsubscribe from another device');
    assert.equal(await dp.locator(`[data-id="${q.id}"] [data-text-reply] [name=text]`).inputValue(), 'half typed', 'the reload dropped the half-typed answer');
    const [, r2] = await Promise.all([dp.waitForEvent('load'), put(other, { drafts2: true })]);
    assert.ok(r2.ok(), 'subscribe again');
    // Its own topic unsubscribed elsewhere: the card leaves, and resubscribing brings the answer back (#687).
    await Promise.all([dp.waitForEvent('load'), put(other, { drafts: false })]);
    assert.equal(await dp.locator(`[data-id="${q.id}"]`).count(), 0, 'card still shown after its topic was unsubscribed');
    await Promise.all([dp.waitForEvent('load'), put(other, { drafts: true })]);
    assert.equal(await dp.locator(`[data-id="${q.id}"] [data-text-reply] [name=text]`).inputValue(), 'half typed', 'resubscribing lost the half-typed answer');
    // This tab's Unsubscribe marker, but the topic was turned off by another
    // device's change: no Undo, it would reverse their choice (#702).
    await Promise.all([dp.waitForEvent('load'), put(other, { drafts2: false }, 'other-device')]);
    await dp.evaluate(() => sessionStorage.setItem('clrd-unsub', JSON.stringify(['drafts2', 'this-tab'])));
    await Promise.all([dp.waitForResponse(r => r.url().endsWith('/api/v1/subscriptions') && r.request().method() === 'GET'), dp.reload()]);
    await dp.waitForTimeout(300); // the check has answered; give a wrong toast time to show
    assert.equal(await dp.locator('[data-toast]', { hasText: 'Unsubscribed from' }).count(), 0, "Undo offered for another device's Unsubscribe");
    await Promise.all([dp.waitForEvent('load'), put(other, { drafts2: true })]);
    // One that did land still offers it. Hold off the live reload on the new
    // page so Undo can race another device turning the topic on and off (#716).
    await dp.routeWebSocket(/\/api\/v1\/ws/, () => {});
    await dp.locator('article.card', { hasText: 'Leave me' }).locator('[data-unsub]').dispatchEvent('click');
    await dp.locator('[data-toast]', { hasText: 'Unsubscribed from drafts2' }).waitFor();
    assert.ok((await put(other, { drafts2: true }, 'other-on')).ok(), 'other device resubscribes');
    assert.ok((await put(other, { drafts2: false }, 'other-off')).ok(), 'other device unsubscribes');
    await dp.locator('[data-toast]').getByRole('button', { name: 'Undo' }).click();
    await dp.locator('[data-toast]', { hasText: 'Changed on another device' }).waitFor();
    const after = await (await other.request.get(base + '/api/v1/subscriptions')).json();
    assert.equal(after.request_id, 'other-off', 'Undo overwrote another device');
    assert.equal(after.topics.find(x => x.topic === 'drafts2').subscribed, false, 'Undo turned drafts2 back on');
    // When nothing changed in between, Undo still brings the topic back.
    assert.ok((await put(other, { drafts2: true })).ok(), 'subscribe again');
    await dp.reload();
    await dp.locator('article.card', { hasText: 'Leave me' }).locator('[data-unsub]').dispatchEvent('click');
    await dp.locator('[data-toast]', { hasText: 'Unsubscribed from drafts2' }).waitFor();
    await Promise.all([dp.waitForEvent('load'), dp.locator('[data-toast]').getByRole('button', { name: 'Undo' }).click()]);
    await dp.locator('article.card', { hasText: 'Leave me' }).waitFor();
    await ctx.close(); await other.close();
    console.log('ok: a subscriptions change keeps a half-typed answer, and Undo shows only for an Unsubscribe that landed and refuses a stale one');
  }

  // Format check in Settings (#320, #618): one card per topic with a group per
  // sender, dismiss for one sender from a row's menu, leave the topic out and
  // check it again, undo. A read-only device sees the findings but no buttons.
  {
    const bot = execFileSync(bin, ['token', 'create', '--name', 'fmt-bot', '--scope', 'publish', '--topics', 'fmtchk'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    await api(laptop, '/api/v1/notifications', { topic: 'fmtchk', message: 'untitled from laptop' });
    await api(bot, '/api/v1/notifications', { topic: 'fmtchk', message: 'untitled from the bot' });
    const ctx = await browser.newContext();
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'fmt' } })).ok(), 'format sign-in');
    const sp = await ctx.newPage();
    await sp.goto(base + '/app/settings');
    const botG = sp.locator('[data-format-topic="fmtchk/fmt-bot"]'), lapG = sp.locator('[data-format-topic="fmtchk/laptop"]');
    // Other tests leave many topics with findings, so cards start collapsed.
    const card = sp.locator('[data-format-card="fmtchk"]'), open = () => card.evaluate(d => { d.open = true; });
    await open();
    await botG.getByRole('heading', { name: /^fmt-bot, \d+ of the last \d+ messages$/ }).waitFor();
    assert.equal(await card.locator('[data-format-topic]').count(), 2, 'both senders in one fmtchk card');
    await card.locator('summary', { hasText: '2 findings' }).first().waitFor();
    await lapG.waitFor({ state: 'attached' });
    const click = b => Promise.all([sp.waitForEvent('load'), b.click()]);
    const dismiss = async g => { await open(); const li = g.locator('li', { hasText: 'No title' }); await li.locator('summary').click(); await click(li.getByRole('button', { name: 'Dismiss for this sender' })); };
    await dismiss(botG);
    assert.equal(await botG.count(), 0, 'dismissed for the bot, its group still shows');
    assert.equal(await lapG.count(), 1, 'dismissing for the bot hid the laptop too');
    const dis = sp.locator('[data-format-dismissed-item="fmtchk/no_title/fmt-bot"]');
    await dis.getByText('fmtchk: No title, from fmt-bot').waitFor();
    await open();
    await click(card.getByRole('button', { name: 'Stop checking fmtchk' }));
    assert.equal(await lapG.count(), 0, 'a topic left out still shows');
    const off = (await (await fetch(base + '/api/v1/format-check', { headers: { Authorization: `Bearer ${laptop}` } })).json()).off;
    assert.deepEqual(off, ['fmtchk'], 'Stop checking did not reach the server');
    await click(sp.locator('[data-format-off-item="fmtchk"]').getByRole('button', { name: 'Check again' }));
    await lapG.waitFor({ state: 'attached' });
    await click(dis.getByRole('button', { name: 'Undo' }));
    await botG.waitFor({ state: 'attached' });
    assert.equal(await sp.locator('[data-format-dismissed-item^="fmtchk/"]').count(), 0, 'Undo left the dismissal listed');
    // Dismissed again so the read-only page below also lists a dismissal by the bot.
    await dismiss(botG);
    await dis.waitFor();

    const ro = execFileSync(bin, ['token', 'create', '--name', 'fmt-ro', '--scope', 'read', '--topics', 'fmtchk'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const rctx = await browser.newContext();
    assert.ok((await rctx.request.post(base + '/api/v1/session', { data: { token: ro, name: 'fmt-ro' } })).ok(), 'read-only sign-in');
    const rp = await rctx.newPage();
    await rp.goto(base + '/app/settings');
    // Not an admin, so other people's devices are not named (#245).
    await rp.locator('[data-format-topic="fmtchk/"]', { hasText: 'Another device, ' }).first().waitFor({ state: 'attached' });
    await rp.locator('[data-format-dismissed-item="fmtchk/no_title/"]', { hasText: 'from another device' }).waitFor();
    assert.ok(!(await rp.locator('[data-format-check]').innerHTML()).includes('fmt-bot'), 'a read-only device sees the bot\'s name, in text or markup');
    assert.equal(await rp.locator('[data-format-dismiss], [data-format-off]').count(), 0, 'a read-only device got dismiss buttons');
    await rctx.close();
    await ctx.close();
    console.log('ok: the format check groups by sender, dismisses, leaves a topic out and undoes in Settings');
  }

  // Senders (#119): an admin sees who sent what, on a page that works without
  // JavaScript, and opens one sender; anyone else gets 403 and no nav link.
  {
    const bot = execFileSync(bin, ['token', 'create', '--name', 'sens-bot', '--scope', 'publish'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    await api(bot, '/api/v1/notifications', { topic: 'sens-a', title: 'Pump 3 stopped', message: 'x' });
    await api(bot, '/api/v1/notifications', { topic: 'sens-b', title: 'Tank low', message: 'y' });
    const ctx = await browser.newContext({ javaScriptEnabled: false });
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'sens-web' } })).ok(), 'senders sign-in');
    const sp = await ctx.newPage();
    await sp.goto(base + '/app');
    await sp.getByRole('link', { name: 'Senders', exact: true }).click();
    const row = sp.locator('tr[data-sender]', { hasText: 'sens-bot' });
    await row.waitFor();
    assert.match(await row.innerText(), /sens-a \(1\)\s+sens-b \(1\)\s+2/, 'the row lists both topics and the count');
    await row.getByRole('link', { name: 'sens-bot' }).click();
    await sp.getByRole('heading', { name: /sens-bot/ }).waitFor();
    assert.equal(await sp.locator('tr[data-recent]').count(), 2, 'the detail lists both notifications');
    await sp.locator('tr[data-recent]', { hasText: 'Pump 3 stopped' }).waitFor();
    assert.equal((await sp.goto(base + '/app/senders/nope')).status(), 404, 'an unknown sender');
    const rctx = await browser.newContext();
    const reader = execFileSync(bin, ['token', 'create', '--name', 'sens-ro', '--scope', 'read'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    assert.ok((await rctx.request.post(base + '/api/v1/session', { data: { token: reader, name: 'sens-ro' } })).ok(), 'reader sign-in');
    const rp = await rctx.newPage();
    assert.equal((await rp.goto(base + '/app/senders')).status(), 403, 'a non-admin on Senders');
    assert.ok(!(await rp.content()).includes('sens-bot'), 'a non-admin sees a sender');
    await rp.goto(base + '/app');
    assert.equal(await rp.getByRole('link', { name: 'Senders', exact: true }).count(), 0, 'a non-admin got the Senders link');
    await rctx.close();
    await ctx.close();
    console.log('ok: Senders lists who sent what without JavaScript, opens one sender, 404s an unknown one and 403s a non-admin');
  }

  // Add a service (#119): the page that shows the token waits for its first
  // message and flips to connected live, with no reload.
  {
    const ctx = await browser.newContext();
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'add-web' } })).ok(), 'add sign-in');
    const p = await ctx.newPage();
    await p.goto(base + '/app/senders');
    await p.getByRole('link', { name: 'Add a service' }).click();
    await p.locator('#as-service').selectOption('curl');
    await p.locator('#as-topic').fill('add-a');
    await p.getByRole('button', { name: 'Add', exact: true }).click();
    const secret = await p.locator('[data-secret]').innerText();
    await p.locator('[data-live=added]').getByText('Waiting for the first message').waitFor();
    assert.match(new URL(p.url()).search, /^\?id=/, 'the address bar left the form post');
    await api(secret, '/api/v1/notifications', { topic: 'add-a', message: 'first' });
    await p.locator('[data-live=added]').getByText('Connected: 1 sent').waitFor();
    assert.equal(await p.locator('[data-secret]').innerText(), secret, 'the secret stays on screen after the flip');
    await ctx.close();
    console.log('ok: Add a service mints a one-topic token and flips to connected live on its first message');
  }

  // Sender rules (#119 part 2): off until set; Pause drops what it sends,
  // the change log shows it, Undo puts it back, routes and priorities round
  // trip, bad routes and a non-admin get 400. No JavaScript.
  {
    const bot = execFileSync(bin, ['token', 'create', '--name', 'rule-bot', '--scope', 'publish'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    await api(bot, '/api/v1/notifications', { topic: 'rule-a', title: 'Before rules', message: 'x' });
    const ctx = await browser.newContext({ javaScriptEnabled: false });
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'rule-web' } })).ok(), 'rules sign-in');
    const sp = await ctx.newPage();
    await sp.goto(base + '/app/senders');
    await sp.locator('tr[data-sender]', { hasText: 'rule-bot' }).getByRole('link', { name: 'rule-bot' }).click();
    const rules = sp.locator('[data-rules]');
    await rules.getByText('No changes yet.').waitFor();
    assert.equal(await sp.locator('[name=paused]').isChecked(), false, 'paused before any rule');
    const path = new URL(sp.url()).pathname;

    await sp.locator('[name=paused]').check();
    await sp.getByRole('button', { name: 'Save rules' }).click();
    const change = rules.locator('tr[data-change]');
    await change.first().waitFor();
    assert.match(await change.first().innerText(), /Paused[\s\S]*was: No rules/, 'the change row');
    assert.equal(await sp.locator('[name=paused]').isChecked(), true, 'paused after save');
    await api(bot, '/api/v1/notifications', { topic: 'rule-a', title: 'Dropped one', message: 'x' });
    await sp.reload();
    assert.equal(await sp.locator('tr[data-recent]', { hasText: 'Dropped one' }).count(), 0, 'a paused sender stored a notification');

    await change.first().getByRole('button', { name: 'Undo' }).click();
    await rules.locator('tr[data-change]:nth-child(2)').waitFor();
    assert.match(await change.first().innerText(), /^No rules[\s\S]*was: Paused/, 'the undo row');
    assert.equal(await sp.locator('[name=paused]').isChecked(), false, 'paused after undo');
    await api(bot, '/api/v1/notifications', { topic: 'rule-a', title: 'Kept one', message: 'x' });
    await sp.reload();
    await sp.locator('tr[data-recent]', { hasText: 'Kept one' }).waitFor();

    await sp.locator('#r-pmin').selectOption('2');
    await sp.locator('#r-routes').fill('rule-a: rule-b, rule-c\n*: rule-b');
    await sp.getByRole('button', { name: 'Save rules' }).click();
    await rules.locator('tr[data-change]:nth-child(3)').waitFor();
    assert.equal(await sp.locator('#r-pmin').inputValue(), '2', 'priority min after save');
    assert.equal((await sp.locator('#r-routes').inputValue()).trim(), '*: rule-b\nrule-a: rule-b, rule-c', 'routes after save');

    // Each refused post leaves the rules alone (checked by the count below).
    const full = { priority_min: '0', priority_max: '0', base_seq: '0' };
    for (const [form, why] of [[{ ...full, routes: 'no colon here' }, 'bad routes'], [{ ...full, routes: 'a: b\na: ' }, 'a topic routed twice'], [{}, 'an empty post'], [{ ...full, priority_min: 'x' }, 'a bad priority']]) {
      const bad = await ctx.request.post(base + path, { form, headers: { Origin: base }, maxRedirects: 0 });
      assert.equal(bad.status(), 400, `${why}: ${bad.status()}`);
      assert.match(await bad.text(), /Not saved/, `${why} message`);
    }

    const rctx = await browser.newContext();
    const reader = execFileSync(bin, ['token', 'create', '--name', 'rule-ro', '--scope', 'read'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    assert.ok((await rctx.request.post(base + '/api/v1/session', { data: { token: reader, name: 'rule-ro' } })).ok(), 'reader sign-in');
    const refused = await rctx.request.post(base + path, { form: { ...full, paused: 'on', base_seq: await sp.locator('[name=base_seq]').inputValue() }, headers: { Origin: base }, maxRedirects: 0 });
    assert.equal(refused.status(), 400, `non-admin rules save: ${refused.status()}`);
    await rctx.close();
    await sp.reload();
    assert.equal(await sp.locator('[name=paused]').isChecked(), false, 'a non-admin paused a sender');
    assert.equal(await change.count(), 3, 'a refused save logged a change');

    // A stale page (#543): a second tab mutes the sender; saving the first
    // tab's older form is refused and the mute stays.
    const sp2 = await ctx.newPage();
    await sp2.goto(base + path);
    await sp2.locator('[name=muted]').check();
    await sp2.getByRole('button', { name: 'Save rules' }).click();
    await sp2.locator('[data-rules] tr[data-change]:nth-child(4)').waitFor();
    await sp2.close();
    await sp.locator('[name=paused]').check();
    await sp.getByRole('button', { name: 'Save rules' }).click();
    await sp.getByText(/Not saved: the rules changed since you loaded them/).waitFor();
    await sp.goto(base + path);
    assert.equal(await sp.locator('[name=muted]').isChecked(), true, 'a stale form undid a newer mute');
    assert.equal(await sp.locator('[name=paused]').isChecked(), false, 'a stale form saved');
    assert.equal(await change.count(), 4, 'a stale save logged a change');
    await sp.locator('[name=paused]').check();
    await sp.getByRole('button', { name: 'Save rules' }).click();
    await rules.locator('tr[data-change]:nth-child(5)').waitFor();
    assert.equal(await sp.locator('[name=muted]').isChecked(), true, 'muted after a fresh save');

    // The quiet alarm (#119 part 3, #560): a bad duration comes back as the
    // page with the server's message by the field and the typed value kept.
    await sp.locator('#r-expect').fill('5s');
    await sp.getByRole('button', { name: 'Save rules' }).click();
    await sp.locator('#r-expect-err', { hasText: /Not saved: expect must be a duration from 1m to 8784h/ }).waitFor();
    assert.equal(await sp.locator('#r-expect').inputValue(), '5s', 'typed duration kept');
    assert.equal(await sp.locator('#r-expect').getAttribute('aria-invalid'), 'true', 'expect marked invalid');
    assert.equal(await change.count(), 5, 'a bad duration logged a change');
    const badExpect = await ctx.request.post(base + path, { form: { ...full, expect: '5s', base_seq: await sp.locator('[name=base_seq]').inputValue() }, headers: { Origin: base }, maxRedirects: 0 });
    assert.equal(badExpect.status(), 400, `bad duration: ${badExpect.status()}`);
    await sp.locator('#r-expect').fill('25h');
    await sp.locator('#r-alert').fill('rule-alarm');
    await sp.getByRole('button', { name: 'Save rules' }).click();
    await rules.locator('tr[data-change]:nth-child(6)').waitFor();
    await sp.reload();
    assert.equal(await sp.locator('#r-expect').inputValue(), '25h', 'expect after reload');
    assert.equal(await sp.locator('#r-alert').inputValue(), 'rule-alarm', 'alert topic after reload');
    await rules.locator('tr[data-change]', { hasText: 'alarm if quiet 25h in rule-alarm' }).first().waitFor();
    // Emptied fields turn it off, not keep the old value.
    await sp.locator('#r-expect').fill('');
    await sp.locator('#r-alert').fill('');
    await sp.getByRole('button', { name: 'Save rules' }).click();
    await rules.locator('tr[data-change]:nth-child(7)').waitFor();
    assert.equal(await sp.locator('#r-expect').inputValue(), '', 'expect after clearing');
    assert.equal(await sp.locator('#r-alert').inputValue(), '', 'alert topic after clearing');
    await ctx.close();
    console.log('ok: sender rules pause, log, undo, quiet alarm and round trip without JavaScript; bad routes, a bad duration, a stale page and a non-admin get 400');
  }

  // Swipe on a touch screen (#161): a part swipe shows the buttons Settings
  // picked for that side and changes nothing; swiping back, a vertical drag
  // and a full swipe change nothing either; a tapped button acts; a mouse drag
  // shows nothing; a sign-in that may not change settings sees them read-only.
  {
    const ctx = await browser.newContext({ hasTouch: true, isMobile: true, viewport: { width: 390, height: 800 } });
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'swiper' } })).ok(), 'swipe sign-in');
    const sp = await ctx.newPage();
    const cdp = await ctx.newCDPSession(sp);
    const touch = (type, x, y) => cdp.send('Input.dispatchTouchEvent', { type, touchPoints: type === 'touchEnd' ? [] : [{ x, y }] });
    const swipe = async (loc, dx, dy = 0, held) => {
      const b = await loc.boundingBox(), x = b.x + b.width / 2, y = b.y + 20; // on the title, not a button
      await touch('touchStart', x, y);
      for (let i = 1; i <= 8; i++) { await touch('touchMove', x + dx * i / 8, y + dy * i / 8); await sp.waitForTimeout(20); }
      await sp.waitForTimeout(150); // held still, so no fling follows to eat the next tap
      if (held) await held(); // checked while the finger is still down
      await touch('touchEnd');
    };
    const a = await api(laptop, '/api/v1/notifications', { topic: 'swp', title: 'swipe-a', message: 'one' });
    const b = await api(laptop, '/api/v1/notifications', { topic: 'swp', title: 'swipe-b', message: 'two' });
    await sp.goto(base + '/app');
    const ca = sp.locator('article.card', { hasText: 'swipe-a' }), cb = sp.locator('article.card', { hasText: 'swipe-b' });
    const tray = c => c.locator('[data-swipe-tray]');
    await swipe(ca, -120);
    await tray(ca).getByRole('button', { name: 'Clear' }).waitFor();
    assert.deepEqual(await tray(ca).getByRole('button').allTextContents(), ['Snooze', 'Clear'], 'swipe left shows Snooze, then Clear at the edge');
    assert.equal((await get(laptop, a.id)).state, 'active', 'a part swipe changes nothing');
    await swipe(ca, 120);
    await tray(ca).waitFor({ state: 'detached' });
    await swipe(ca, -150, 200); // far enough to open, were it sideways
    assert.equal(await tray(ca).count(), 0, 'a mostly vertical drag shows nothing');
    let mid; await swipe(ca, -380, 0, async () => { mid = await ca.evaluate(c => c.x <= c.trayW); });
    assert.ok(mid, 'the slide stops at the buttons while the finger is still moving');
    await tray(ca).waitFor();
    assert.ok(await ca.evaluate(c => c.x === c.trayW), 'a full swipe stops at the buttons');
    await sp.waitForTimeout(300);
    assert.equal((await get(laptop, a.id)).state, 'active', 'a full swipe does not clear');
    await tray(ca).getByRole('button', { name: 'Clear' }).tap();
    await sp.locator('[data-toast]').getByRole('button', { name: 'Undo' }).waitFor();
    assert.equal((await get(laptop, a.id)).state, 'dismissed', 'the tapped Clear clears');
    await swipe(cb, 120);
    assert.deepEqual(await tray(cb).getByRole('button').allTextContents(), ['Ack'], 'swipe right shows Ack');
    await tray(cb).getByRole('button', { name: 'Ack' }).tap();
    await cb.locator('.badge[data-state=acknowledged]').waitFor();
    assert.equal((await get(laptop, b.id)).state, 'acknowledged', 'the tapped Ack acknowledges');

    // Settings move Ack to the left and leave the right empty.
    await sp.goto(base + '/app/settings');
    await sp.getByLabel('Swipe left, button 1').selectOption('ack');
    await sp.getByLabel('Swipe left, button 2').selectOption('');
    await sp.getByLabel('Swipe right, button 1').selectOption('');
    await Promise.all([sp.waitForEvent('load'), sp.locator('[data-swipe-settings]').getByRole('button', { name: 'Save' }).click()]);
    assert.equal(await sp.getByLabel('Swipe left, button 1').inputValue(), 'ack', 'swipe setting saved');
    const c = await api(laptop, '/api/v1/notifications', { topic: 'swp', title: 'swipe-c', message: 'three' });
    await sp.goto(base + '/app');
    const cc = sp.locator('article.card', { hasText: 'swipe-c' });
    await swipe(cc, 120);
    assert.equal(await tray(cc).count(), 0, 'an empty side shows nothing');
    await swipe(cc, -120);
    assert.deepEqual(await tray(cc).getByRole('button').allTextContents(), ['Ack'], 'the new left side');
    await ctx.close();

    // A mouse drag shows nothing.
    const mctx = await browser.newContext();
    assert.ok((await mctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'mouse' } })).ok(), 'mouse sign-in');
    const mp = await mctx.newPage();
    await mp.goto(base + '/app');
    const pm = mp.locator('article.card', { hasText: 'swipe-c' }), box = await pm.boundingBox();
    await mp.mouse.move(box.x + box.width / 2, box.y + 20);
    await mp.mouse.down();
    await mp.mouse.move(box.x + 20, box.y + 20, { steps: 8 });
    await mp.mouse.up();
    assert.equal(await tray(pm).count(), 0, 'a mouse drag shows nothing');
    assert.equal((await get(laptop, c.id)).state, 'active', 'and changes nothing');
    // Swipe settings changed elsewhere reload an open page, so its buttons and form are current.
    await mp.locator('#sync.is-live').waitFor();
    const [, put] = await Promise.all([mp.waitForEvent('load'), fetch(base + '/api/v1/settings', { method: 'PUT', headers: { Authorization: `Bearer ${laptop}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ swipe_start: ['clear', 'snooze'], swipe_end: ['ack'] }) })]);
    assert.equal(put.status, 200, 'swipe settings put back');
    assert.equal(await mp.locator('[data-inbox]').getAttribute('data-swipe-end'), 'ack ', 'the reloaded page has the new buttons');
    await mctx.close();

    // A topic-limited sign-in cannot change settings: 403, then read-only.
    const lim = execFileSync(bin, ['token', 'create', '--name', 'swp-only', '--scope', 'read,publish', '--topics', 'swp'], { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const lctx = await browser.newContext();
    assert.ok((await lctx.request.post(base + '/api/v1/session', { data: { token: lim, name: 'swp-only' } })).ok(), 'limited sign-in');
    const lp = await lctx.newPage();
    await lp.goto(base + '/app/settings');
    await lp.locator('[data-swipe-settings]').getByRole('button', { name: 'Save' }).click();
    await lp.locator('[data-swipe-status]:not(:empty)').waitFor();
    assert.ok(await lp.getByLabel('Swipe left, button 1').isDisabled(), 'read-only after a refused save');
    await lctx.close();
    console.log('ok: on a touch screen a part swipe shows the chosen buttons and acts only on a tap; swipe back, vertical, full and mouse drags change nothing; settings move them; a refused save is read-only');
  }

  // Web Push (#164): nothing asks for permission on load; the switch subscribes
  // and unsubscribes against the server; a lost subscription and a blocked
  // site tidy both sides to off; a push shows a notification, fetches the rest
  // when only the id came, and clears nothing. Headless Chromium denies
  // notifications, so permission and the push service are stubbed: no real
  // push service is reached and a notification tap cannot be dispatched.
  {
    const { createECDH, randomBytes } = await import('node:crypto');
    const ec = createECDH('prime256v1'); ec.generateKeys();
    const fake = { endpoint: 'https://push.example.invalid/sub/1', keys: { p256dh: ec.getPublicKey().toString('base64url'), auth: randomBytes(16).toString('base64url') } };
    const ctx = await browser.newContext();
    await ctx.addInitScript(f => {
      const ss = (k, v) => { try { return v === undefined ? sessionStorage.getItem(k) : sessionStorage.setItem(k, v); } catch { return null; } };
      window.asked = 0;
      let perm = ss('perm') || 'default';
      Object.defineProperty(Notification, 'permission', { get: () => perm, configurable: true });
      Notification.requestPermission = async () => { window.asked++; perm = ss('answer') || 'granted'; ss('perm', perm); return perm; };
      // Gone after a reload (the browser lost it) unless keep is set.
      const mk = () => ({ ...f, toJSON: () => f, unsubscribe: async () => { sub = null; ss('keep', ''); return true; } });
      let sub = ss('keep') ? mk() : null;
      PushManager.prototype.subscribe = async () => (sub = mk());
      PushManager.prototype.getSubscription = async () => sub;
    }, fake);
    const pp = await ctx.newPage();
    await pp.goto(`${base}/app?auth=${Buffer.from(`Bearer ${laptop}`).toString('base64url')}`);
    await pp.goto(base + '/app/settings');
    const btn = pp.locator('[data-push]');
    const subscribed = async () => (await (await ctx.request.get(base + '/api/v1/push')).json()).subscribed;
    const ready = async name => { await pp.getByRole('button', { name }).waitFor(); await until(pp, () => !document.querySelector('[data-push]').disabled); };
    await ready('Turn on notifications');
    assert.equal(await pp.evaluate(() => window.asked), 0, 'asked for permission on load');
    assert.equal(await pp.evaluate(async () => (await navigator.serviceWorker.getRegistrations()).length), 0, 'a worker registered before opting in');
    assert.equal(await subscribed(), false);

    const swe = ctx.waitForEvent('serviceworker');
    await btn.click();
    await ready('Turn off notifications');
    assert.equal(await pp.evaluate(() => window.asked), 1);
    assert.equal(await subscribed(), true, 'turning on reached the server');
    // A push: record what the worker shows instead of the browser's own call.
    const sw = await swe;
    await sw.evaluate(() => { self.seen = []; self.registration.showNotification = async (t, o) => { self.seen.push({ t, ...o }); }; });
    const cdp = await ctx.newCDPSession(pp);
    const regs = new Promise(ok => cdp.on('ServiceWorker.workerRegistrationUpdated', e => e.registrations[0] && ok(e.registrations[0].registrationId)));
    await cdp.send('ServiceWorker.enable');
    const regId = await regs;
    const n = await api(phone, '/api/v1/notifications', { topic: 'pushy', title: 'Disk full', message: 'Free 2 GB' });
    const push = async d => { await cdp.send('ServiceWorker.deliverPushMessage', { origin: base, registrationId: regId, data: JSON.stringify(d) }); };
    await push({ type: 'notification', id: 'abc', topic: 'pushy', title: 'Short one', message: 'body' });
    await push({ type: 'notification', via: 'new', seq: 1, id: n.id, topic: 'pushy' }); // too big: only the id came
    for (let i = 0; i < 50 && (await sw.evaluate(() => self.seen.length)) < 2; i++) await pp.waitForTimeout(100);
    const seen = await sw.evaluate(() => self.seen);
    assert.deepEqual(seen.map(s => [s.t, s.body, s.tag, s.renotify]), [['Short one', 'body', 'abc', true], ['Disk full', 'Free 2 GB', n.id, true]], 'what the worker showed');
    assert.equal((await get(laptop, n.id)).state, 'active', 'a push cleared the notification');

    // The browser lost its subscription (stub gone after reload): tidy to off.
    await pp.reload();
    await ready('Turn on notifications');
    assert.equal(await subscribed(), false, 'a lost browser subscription left the server row');
    // Both sides keep it: still on after a reload.
    await btn.click();
    await ready('Turn off notifications');
    await pp.evaluate(() => sessionStorage.setItem('keep', '1'));
    await pp.reload();
    await ready('Turn off notifications');
    assert.equal(await subscribed(), true);
    // The server dropped it (the push service said gone): the browser drops it too, never re-sends it.
    await ctx.request.delete(base + '/api/v1/push');
    await pp.reload();
    await ready('Turn on notifications');
    assert.equal(await subscribed(), false, 'a subscription the server dropped was sent again');
    assert.equal(await pp.evaluate(() => sessionStorage.getItem('keep')), '', 'the browser kept a subscription the server dropped');
    // On, then off.
    await btn.click();
    await ready('Turn off notifications');
    await btn.click();
    await ready('Turn on notifications');
    assert.equal(await subscribed(), false, 'turning off reached the server');
    // Blocked after opting in: the next load tidies the server and says why.
    await btn.click();
    await ready('Turn off notifications');
    await pp.evaluate(() => sessionStorage.setItem('perm', 'denied'));
    await pp.reload();
    await pp.locator('[data-push-hint]', { hasText: 'blocked' }).waitFor();
    assert.equal(await btn.isDisabled(), true, 'the switch works while the site is blocked');
    assert.equal(await subscribed(), false, 'a blocked site kept the server row');
    // Refused when asked: nothing subscribed.
    await pp.evaluate(() => { sessionStorage.setItem('perm', 'default'); sessionStorage.setItem('answer', 'denied'); });
    await pp.reload();
    await ready('Turn on notifications');
    await btn.click();
    await pp.locator('[data-push-hint]', { hasText: 'blocked' }).waitFor();
    assert.equal(await subscribed(), false, 'a refused permission subscribed');
    await ctx.close();
    console.log('ok: web push asks only on tap, subscribes and unsubscribes, tidies a lost or blocked subscription, and a push shows without clearing');
  }

  // Settings search (#575): five sections in the order SPEC 9.1 gives; a
  // keyword that is in no label finds its setting and hides the other
  // sections; Enter goes to it; no match shows the empty state and Clear
  // brings every setting back. Without script every section shows and there
  // is no search box.
  {
    const order = ['Notifications', 'Inbox', 'Display', 'Devices', 'Integrations', 'About'];
    const ctx = await browser.newContext();
    assert.ok((await ctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'search' } })).ok(), 'search sign-in');
    const sp = await ctx.newPage();
    await sp.goto(base + '/app/settings');
    const groups = () => sp.locator('[data-settings-group] > h2:visible').allTextContents();
    const sets = () => sp.locator('[data-setting] > h3:visible').allTextContents();
    assert.deepEqual(await groups(), order, 'settings sections or their order');
    const q = sp.getByLabel('Search settings');
    await q.fill('do not disturb');
    await sp.locator('#sound').waitFor({ state: 'hidden' });
    assert.deepEqual(await sets(), ['Silences'], 'a keyword found the wrong settings');
    assert.deepEqual(await groups(), ['Notifications'], 'a section with no match still shows');
    await q.press('Enter');
    await until(sp, () => location.hash === '#silences' && document.activeElement?.id === 'silences');
    await q.fill("what's new"); // About (#580)
    await sp.locator('#silences').waitFor({ state: 'hidden' });
    assert.deepEqual(await sets(), ['Version'], 'search missed the About section');
    await q.fill('Dark Mode ');
    await sp.locator('#about').waitFor({ state: 'hidden' });
    assert.deepEqual(await sets(), ['Appearance'], 'a keyword in Display found the wrong settings');
    assert.deepEqual(await groups(), ['Display'], 'only the matching section shows');
    await q.fill('confirm first'); // in Linked devices' second paragraph
    await sp.locator('#appearance').waitFor({ state: 'hidden' });
    assert.ok((await sets()).includes('Linked devices'), 'search missed a later paragraph');
    await sp.evaluate(() => { location.hash = '#dev-h'; }); // a link to a setting, while searching
    await sp.locator('#sound').waitFor();
    assert.equal(await q.inputValue(), '', 'a link to a setting left the search on');
    await q.fill('nothing like this');
    await sp.locator('[data-settings-none]').waitFor();
    assert.equal(await sp.locator('[data-settings-term]').textContent(), 'nothing like this', 'empty state names the search');
    assert.deepEqual(await sets(), [], 'a setting shown with no match');
    await sp.getByRole('button', { name: 'Clear search' }).click();
    await sp.locator('#sound').waitFor();
    assert.equal(await q.inputValue(), '', 'Clear left the search text');
    assert.ok(await q.evaluate(e => e === document.activeElement), 'Clear did not return to the search box');
    assert.deepEqual(await groups(), order, 'Clear did not bring every section back');
    await ctx.close();

    // After-update What's new note (#580): quiet on first open ever, shows when
    // the server version moves forward, dismissible, opens What's new in Settings,
    // and can be turned off per browser in Settings.
    const uctx = await browser.newContext();
    assert.ok((await uctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'whatsnew' } })).ok(), 'whatsnew sign-in');
    const up = await uctx.newPage();
    await up.goto(base + '/app');
    const note = up.locator('[data-update-note]');
    assert.equal(await note.isVisible(), false, 'update note showed on first visit ever');
    const curVer = await up.locator('[data-inbox]').getAttribute('data-ver');
    assert.equal(await up.evaluate(() => localStorage.getItem('clrd.seen_version')), curVer, 'first visit did not record seen version');
    await up.evaluate(() => localStorage.setItem('clrd.seen_version', '0.0.1'));
    await up.reload();
    await note.waitFor({ state: 'visible' });
    assert.match(await note.textContent(), new RegExp(`Updated to clrd ${curVer.replaceAll('.', '\\.')}`), 'update note missed running version');
    await note.getByRole('button', { name: 'Dismiss' }).click();
    await note.waitFor({ state: 'hidden' });
    await up.reload();
    assert.equal(await note.isVisible(), false, 'update note came back after Dismiss');
    await up.evaluate(() => localStorage.setItem('clrd.seen_version', '0.0.1'));
    await up.reload();
    await note.waitFor({ state: 'visible' });
    await note.getByRole('link', { name: "What's new" }).click();
    await up.locator('[data-whats-new][open]').waitFor();
    await up.goto(base + '/app');
    assert.equal(await note.isVisible(), false, 'update note stayed after opening What\'s new');
    await up.goto(base + '/app/settings');
    await up.locator('[data-whats-new-setting]').uncheck();
    await up.evaluate(() => localStorage.setItem('clrd.seen_version', '0.0.1'));
    await up.goto(base + '/app');
    assert.equal(await note.isVisible(), false, 'update note showed while turned off in Settings');
    await uctx.close();
    console.log("ok: after-update What's new note shows on a new version, dismisses, opens Settings About and obeys its Settings switch");

    // #591: a search typed while pair.js still loads finds Link a phone once pair.js shows it.
    const lctx = await browser.newContext();
    assert.ok((await lctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'search-late' } })).ok(), 'late sign-in');
    const lp = await lctx.newPage();
    let letPair;
    const pairHeld = new Promise(r => { letPair = r; });
    await lp.route('**/pair.js*', async r => { await pairHeld; await r.continue(); });
    await lp.goto(base + '/app/settings', { waitUntil: 'commit' });
    await lp.getByLabel('Search settings').fill('qr');
    await lp.locator('[data-settings-none]').waitFor(); // inbox.js ran; Link a phone is not shown yet
    letPair();
    await lp.locator('#link-phone').waitFor();
    assert.equal(await lp.locator('[data-settings-none]').isVisible(), false, 'empty state stayed after Link a phone appeared');
    assert.equal(await lp.locator('[data-settings-n]').textContent(), '1 setting matches. Press Enter to go to the first.', 'match count missed Link a phone');
    await lctx.close();

    const nctx = await browser.newContext({ javaScriptEnabled: false });
    assert.ok((await nctx.request.post(base + '/api/v1/session', { data: { token: laptop, name: 'search-nojs' } })).ok(), 'no-js sign-in');
    const np = await nctx.newPage();
    await np.goto(base + '/app/settings');
    assert.deepEqual(await np.locator('[data-settings-group] > h2:visible').allTextContents(), order, 'a section missing without script');
    assert.equal(await np.getByLabel('Search settings').isVisible(), false, 'search box shown without script');
    assert.ok(await np.locator('#silences').isVisible(), 'Silences hidden without script');
    await nctx.close();
    console.log('ok: settings sit in six sections, search finds by keyword, goes to the match, shows an empty state and clears; without script all sections show');
  }

  // Motion (#598, SPEC 10.1), on its own server so no other card is in the way.
  // A recorder in the page logs each card animation and slide as it starts,
  // so a 200ms step is never missed on a busy machine.
  {
    const p3 = await new Promise(ok => { const s = createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => ok(p)); }); });
    const b3 = `http://127.0.0.1:${p3}`;
    const env3 = { ...process.env, CLRD_DB: join(mkdtempSync(join(tmpdir(), 'clrd-motion-')), 'clrd.db'), CLRD_LISTEN: `127.0.0.1:${p3}`, CLRD_BASE_URL: b3 + '/' };
    const mk = name => execFileSync(bin, ['token', 'create', '--name', name, '--scope', 'admin', '--approve', 'allowed'], { env: env3, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const tok = mk('motion'), other = mk('other-phone');
    const srv3 = spawn(bin, [], { env: env3, stdio: 'inherit' });
    const post3 = async (path, body) => {
      const r = await fetch(b3 + path, { method: 'POST', headers: { Authorization: `Bearer ${other}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });
      assert.equal(r.status, 200, `${path}: ${r.status}`);
      return r.json();
    };
    const send = title => post3('/api/v1/notifications', { topic: 'motion', title });
    try {
      for (let i = 0; ; i++) {
        try { if ((await fetch(b3 + '/healthz')).ok) break; } catch {}
        if (i > 50) throw new Error('motion server did not start');
        await new Promise(r => setTimeout(r, 100));
      }
      const old = await send('Was here first');
      const ctx = await browser.newContext();
      await ctx.addInitScript(() => {
        window.moves = [];
        const rec = e => {
          const c = e.target;
          if (e.pseudoElement || !c.matches || !c.matches('article.card')) return;
          const name = e.type === 'animationstart' ? e.animationName : e.propertyName;
          if (name === 'opacity') return;
          const entry = { name, title: c.querySelector('.card-title')?.textContent.trim(), at: e.timeStamp };
          window.moves.push(entry);
          // A card sliding out past the list's edge must not widen the page.
          if (name === 'transform') setTimeout(() => { entry.wide = document.documentElement.scrollWidth > document.documentElement.clientWidth; }, 150); // mid-slide, past the edge
        };
        addEventListener('animationstart', rec, true);
        addEventListener('transitionrun', rec, true);
      });
      const mp = await ctx.newPage();
      await mp.setViewportSize({ width: 390, height: 800 });
      // Live sync runs through here, so a step can drop it (down) or hold what
      // the server sends (held) and then let it all through at once.
      let sock, down = false, held = null;
      await mp.routeWebSocket(/\/api\/v1\/ws/, ws => {
        if (down) return ws.close();
        const srv = ws.connectToServer();
        srv.onMessage(m => held ? held.push(m) : ws.send(m));
        sock = { ws, srv };
      });
      const heldNotes = () => (held || []).map(m => JSON.parse(m).notification).filter(Boolean);
      const hold = async (ok, what) => {
        for (let i = 0; i < 200 && !ok(heldNotes()); i++) await mp.waitForTimeout(100);
        assert.ok(ok(heldNotes()), `live sync never sent ${what}`);
        const all = held; held = null;
        all.forEach(m => sock.ws.send(m));
      };
      await mp.goto(`${b3}/app?auth=${Buffer.from(`Bearer ${tok}`).toString('base64url')}`);
      await mp.locator('.is-live').waitFor({ timeout: 5000 });
      const mc = t => mp.locator('article.card', { hasText: t });
      const moved = (name, title) => mp.evaluate(([n, t]) => window.moves.some(m => m.name === n && m.title === t), [name, title]);
      await mc('Was here first').waitFor();
      await mp.waitForTimeout(400);
      assert.equal(await moved('clrd-in', 'Was here first'), false, 'a card already there on first load slid in');

      // Live, tab in front: it slides in and wiggles once.
      await send('Just arrived');
      await until(mp, () => window.moves.some(m => m.name === 'clrd-wiggle' && m.title === 'Just arrived'));
      assert.ok(await moved('clrd-in', 'Just arrived'), 'a live card did not slide in');

      // A burst: all slide in, only the newest wiggles. Held and let through
      // together, so the burst lands in one refresh even on a slow machine (#612).
      held = [];
      for (const t of ['Burst one', 'Burst two', 'Burst three']) await send(t);
      await hold(ns => ns.length >= 3, 'the burst');
      for (const t of ['Burst one', 'Burst two', 'Burst three']) await mc(t).waitFor();
      await until(mp, () => window.moves.some(m => m.name === 'clrd-wiggle' && m.title === 'Burst three'));
      const burst = await mp.evaluate(() => window.moves.filter(m => m.title.startsWith('Burst')).map(m => `${m.name}:${m.title}`));
      assert.ok(['one', 'two', 'three'].every(n => burst.includes(`clrd-in:Burst ${n}`)),`a burst card did not slide in: ${burst}`);
      assert.ok(!burst.includes('clrd-wiggle:Burst one') && !burst.includes('clrd-wiggle:Burst two'), `an older card in a burst wiggled: ${burst}`);

      // Tab in the background: the card is there when the person comes back, with no motion.
      await mp.evaluate(() => Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' }));
      await send('Came while away');
      await mc('Came while away').waitFor();
      await mp.waitForTimeout(400);
      assert.equal(await moved('clrd-in', 'Came while away'), false, 'a card that came while the tab was hidden slid in');
      await mp.evaluate(() => delete document.visibilityState);

      // Live sync drops and a card is sent meanwhile: it comes in with the
      // catch-up on reconnect, so it just appears (#614).
      down = true;
      sock.ws.close(); sock.srv.close();
      await mp.locator('.is-live').waitFor({ state: 'detached' });
      await send('Sent while offline');
      down = false;
      await mc('Sent while offline').waitFor({ timeout: 20000 });
      await mp.locator('.is-live').waitFor();
      await mp.waitForTimeout(400);
      assert.equal(await moved('clrd-in', 'Sent while offline'), false, 'a card from the catch-up after a reconnect slid in');

      // Cleared on another device: the reason shows, then it slides away, without widening the page.
      await post3(`/api/v1/notifications/${old.id}/dismiss`);
      await until(mp, () => window.moves.some(m => m.name === 'transform' && m.title === 'Was here first'));
      await mc('Was here first').waitFor({ state: 'detached' });
      assert.equal(await mp.evaluate(() => window.moves.find(m => m.name === 'transform' && m.title === 'Was here first').wide), false, 'a sliding card widened the page');

      // Clear all: each card starts its slide a little after the one above it.
      const top = (await mp.locator('article.card .card-title').allTextContents()).map(t => t.trim());
      assert.equal(top.length, 6, `cards before Clear all: ${top}`);
      await mp.getByRole('button', { name: 'Clear all' }).first().click();
      await until(mp, () => window.moves.filter(m => m.name === 'transform').length >= 7);
      const at = Object.fromEntries(await mp.evaluate(() => window.moves.filter(m => m.name === 'transform' && m.title !== 'Was here first').map(m => [m.title, m.at])));
      // Start times in on-screen order: two whose delays both passed before a
      // frame start in the same frame, so only a later card starting earlier is wrong (#632).
      const order = top.map(t => [t, at[t]]);
      // Events of one frame carry stamps up to a fraction of a ms apart in either order (#714).
      assert.ok(order.every(([, t], i) => i === 0 || t >= order[i - 1][1] - 1), `Clear all slid out of order: ${order}`);
      // 40ms a card, so 200ms from first to last; a busy machine can only fire the first one late.
      assert.ok(order.at(-1)[1] - order[0][1] >= 80, `Clear all did not stagger: ${order}`);
      await mp.locator('article.card').first().waitFor({ state: 'detached' }); // Clear all done folding

      // A snooze that ends while the card is still leaving: it comes back with
      // its slide in and wiggle (#611). Live sync holds the snooze until the
      // server wakes it, so both changes reach the page together.
      const back = await send('Back soon');
      await until(mp, () => window.moves.some(m => m.name === 'clrd-wiggle' && m.title === 'Back soon'));
      held = [];
      await post3(`/api/v1/notifications/${back.id}/snooze`, { until: new Date(Date.now() + 3000).toISOString() }); // slack for a busy runner; the wake tick is 15s
      await hold(ns => ns.some(n => n.id === back.id && n.state === 'active'), 'the snooze ending');
      await mc('Back soon').and(mp.locator('.is-leaving')).waitFor();
      await until(mp, () => window.moves.filter(m => m.name === 'clrd-wiggle' && m.title === 'Back soon').length === 2);
      assert.equal(await mp.evaluate(() => window.moves.filter(m => m.name === 'clrd-in' && m.title === 'Back soon').length), 2, 'a card back from a snooze did not slide in');
      await post3(`/api/v1/notifications/${back.id}/dismiss`);
      await mc('Back soon').waitFor({ state: 'detached' });

      // The same while a Clear all waits for its reply (#656): the server has
      // cleared the card, another device reopens it for later and it wakes, all before
      // the reply. The card is not leaving yet, but the reply takes it, so its
      // return still slides in.
      const mid = await send('Mid clear');
      await until(mp, () => window.moves.some(m => m.name === 'clrd-wiggle' && m.title === 'Mid clear'));
      let go; const gate = new Promise(r => { go = r; });
      await mp.route('**/api/v1/notifications/clear', async route => { const res = await route.fetch(); await gate; await route.fulfill({ response: res }); });
      held = [];
      await mp.getByRole('button', { name: 'Clear all' }).first().click();
      await hold(ns => ns.some(n => n.id === mid.id && n.state === 'dismissed'), 'the clear');
      held = [];
      await post3(`/api/v1/notifications/${mid.id}/reopen`, { until: new Date(Date.now() + 3000).toISOString() });
      await hold(ns => ns.some(n => n.id === mid.id && n.state === 'active'), 'the snooze ending');
      await mp.waitForTimeout(300); // the wake reaches the page before the reply
      go();
      await until(mp, () => window.moves.filter(m => m.name === 'clrd-in' && m.title === 'Mid clear').length === 2);
      await mp.unroute('**/api/v1/notifications/clear');
      await post3(`/api/v1/notifications/${mid.id}/dismiss`);
      await mc('Mid clear').waitFor({ state: 'detached' });

      // Reduce motion: a live card just appears and a cleared one just goes.
      await mp.emulateMedia({ reducedMotion: 'reduce' });
      // Every frame looks at document.getAnimations(), so Element.animate and
      // the check mark's path are seen too, not only the card's own CSS (#613).
      await mp.evaluate(() => {
        window.moves = []; window.anims = [];
        const look = () => {
          for (const a of document.getAnimations()) {
            const t = a.effect?.target;
            // A button fading as it is disabled is not motion: opacity alone is left out, as above.
            if (t?.closest?.('[data-list]') && a.transitionProperty !== 'opacity') window.anims.push(`${a.animationName || a.transitionProperty || 'animate'} on ${t.tagName}`);
          }
          window.looking = requestAnimationFrame(look);
        };
        look();
      });
      const still = await send('Quiet arrival');
      await mc('Quiet arrival').waitFor();
      await post3(`/api/v1/notifications/${still.id}/dismiss`);
      await mc('Quiet arrival').waitFor({ state: 'detached' });
      assert.deepEqual(await mp.evaluate(() => window.moves), [], 'something moved with reduce motion on');
      assert.deepEqual(await mp.evaluate(() => { cancelAnimationFrame(window.looking); return [...new Set(window.anims)]; }), [], 'something animated with reduce motion on');
      await ctx.close();
      console.log('ok: a live card slides in and wiggles, only the newest in a burst, none on load or while hidden; clears slide away and Clear all staggers; reduce motion plays nothing');
    } finally { srv3.kill(); }
  }

  // Under a sub-path (#528): a second server at /clrd/. The inbox and pair
  // scripts send every request inside it, and a setting saved from the page
  // reaches the server.
  {
    const p2 = await new Promise(ok => { const s = createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => ok(p)); }); });
    const root = `http://127.0.0.1:${p2}`, sub = root + '/clrd';
    const env2 = { ...process.env, CLRD_DB: join(mkdtempSync(join(tmpdir(), 'clrd-sub-')), 'clrd.db'), CLRD_LISTEN: `127.0.0.1:${p2}`, CLRD_BASE_URL: sub + '/' };
    const tok = execFileSync(bin, ['token', 'create', '--name', 'sub', '--scope', 'admin', '--approve', 'allowed'], { env: env2, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
    const srv2 = spawn(bin, [], { env: env2, stdio: 'inherit' });
    try {
      for (let i = 0; ; i++) {
        try { if ((await fetch(sub + '/healthz')).ok) break; } catch {}
        if (i > 50) throw new Error('sub-path server did not start');
        await new Promise(r => setTimeout(r, 100));
      }
      const ctx = await browser.newContext();
      const outside = [];
      ctx.on('request', r => { const u = new URL(r.url()); if (u.origin === root && !u.pathname.startsWith('/clrd/')) outside.push(u.pathname); });
      const sp = await ctx.newPage();
      await sp.goto(`${sub}/app?auth=${Buffer.from(`Bearer ${tok}`).toString('base64url')}`);
      await sp.goto(sub + '/app/settings');
      await sp.locator('.is-live').waitFor({ timeout: 5000 });
      await sp.getByRole('combobox', { name: 'Sound', exact: true }).selectOption('pop');
      const mine = async () => (await (await ctx.request.get(sub + '/api/v1/settings')).json()).sound;
      for (let i = 0; i < 20 && (await mine()) !== 'pop'; i++) await sp.waitForTimeout(250);
      assert.equal(await mine(), 'pop', 'a setting saved under a sub-path reached the server');
      // Pair page: the sign-in code request also stays inside the sub-path.
      const pc = await (await browser.newContext({ viewport: { width: 390, height: 844 } })).newPage();
      pc.on('request', r => { const u = new URL(r.url()); if (u.origin === root && !u.pathname.startsWith('/clrd/')) outside.push(u.pathname); });
      await pc.goto(sub + '/app');
      await pc.locator('footer a[href="https://github.com/thejdubb02/clrd"]').waitFor({ state: 'visible' }); // visible on phone width (#586)
      await pc.getByRole('button', { name: 'Show sign-in code' }).click();
      await pc.locator('[data-pair-qr]').waitFor({ state: 'visible' });
      assert.deepEqual(outside, [], 'requests left the sub-path');
      await pc.context().close();
      await ctx.close();
      console.log('ok: under a sub-path the inbox and sign-in code requests stay inside it and a setting saves');
    } finally { srv2.kill(); }
  }
} finally {
  await browser?.close();
  server.kill();
}
