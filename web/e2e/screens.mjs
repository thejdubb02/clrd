// Screenshots of every page, light and dark, phone and desktop, against the
// real binary with fixed data. Fails on a console or CSP error, on an axe
// (WCAG 2 A and AA) violation, and on a page wider than a 360 px phone.
//
//   CLRD_BIN=/path/to/clrd OUT=/tmp/shots node web/e2e/screens.mjs
//
// .github/workflows/web-e2e.yml runs it in the official Playwright image and
// uploads OUT as an artifact.
import { spawn, execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { chromium } from 'playwright';
import { AxeBuilder } from '@axe-core/playwright';

const bin = process.env.CLRD_BIN || 'clrd';
const out = process.env.OUT || 'shots';
mkdirSync(out, { recursive: true });

const freePort = () => new Promise(ok => { const s = createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => ok(p)); }); });

// One clrd with its own database. Resolves once /healthz answers; log holds stderr.
async function start() {
  const port = await freePort();
  const env = { ...process.env, CLRD_DB: join(mkdtempSync(join(tmpdir(), 'clrd-shots-')), 'clrd.db'), CLRD_LISTEN: `127.0.0.1:${port}`, CLRD_BASE_URL: `http://127.0.0.1:${port}` };
  const s = { base: `http://127.0.0.1:${port}`, env, log: '' };
  s.proc = spawn(bin, [], { env, stdio: ['ignore', 'pipe', 'pipe'] });
  const keep = d => { s.log += d; };
  s.proc.stdout.on('data', keep);
  s.proc.stderr.on('data', keep);
  for (let i = 0; ; i++) {
    try { if ((await fetch(s.base + '/healthz')).ok) break; } catch {}
    if (i > 50) throw new Error('server did not start:\n' + s.log);
    await new Promise(r => setTimeout(r, 100));
  }
  return s;
}

const failures = [];
let browser;
const servers = [];
try {
  // Setup only exists while there is no admin token, so it gets its own server.
  const fresh = await start();
  servers.push(fresh);
  const setupURL = (fresh.log.match(/url="?([^"\s]+\/setup\?code=[^"\s]+)/) || [])[1];
  if (!setupURL) throw new Error('no setup link in the log:\n' + fresh.log);

  const main = await start();
  servers.push(main);
  // Allowed to approve, so Settings shows the May approve menus at phone width (#329).
  const token = name => execFileSync(bin, ['token', 'create', '--name', name, '--scope', 'admin', '--approve', 'allowed'], { env: main.env, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
  const laptop = token('laptop');
  const phone = token('pixel-9');
  const api = async (path, body) => {
    const r = await fetch(main.base + path, { method: 'POST', headers: { Authorization: `Bearer ${phone}`, 'Content-Type': 'application/json' }, body: body && JSON.stringify(body) });
    if (!r.ok) throw new Error(`${path}: ${r.status}`);
    return r.json();
  };
  const send = n => api('/api/v1/notifications', n);

  // Every state, every priority, a reply question, links, and text that does not wrap on its own.
  await send({ topic: 'home', title: 'Washer done', message: 'Cycle finished at 40 degrees.', priority: 2 });
  await send({ topic: 'agents', title: 'Merge pull request 212?', message: 'All 48 checks pass. The agent is waiting for your answer.', actions: [{ label: 'Approve', reply: 'yes' }, { label: 'Reject', reply: 'no' }] });
  await send({ topic: 'backups', title: 'Nightly backup failed', message: 'restic exited 1 after 4m 12s: repository is locked by another process.', priority: 5, tags: ['restic', 'nas'], actions: [{ label: 'Open logs', url: 'https://example.com/logs' }] });
  await send({ topic: 'infra', title: 'Disk 91% on web-1', message: '/var is filling at 2 GB an hour.', priority: 4, tags: ['disk'] });
  await send({ topic: 'infra', title: 'Averyveryverylongunbrokentitlethatcomesfromamonitorwithnospacesatall_' + 'x'.repeat(60), message: 'https://example.com/' + 'a'.repeat(120) });
  const acked = await send({ topic: 'infra', title: 'Load high on db-1', message: 'load average 14 for 10 minutes' });
  await api(`/api/v1/notifications/${acked.id}/ack`);
  const snz = await send({ topic: 'certs', title: 'Certificate renews in 7 days', message: 'clrd.example.com' });
  await api(`/api/v1/notifications/${snz.id}/snooze`, { until: '2031-06-01T14:00:00Z' });
  const dis = await send({ topic: 'home', title: 'Front door open', message: 'Open for 5 minutes.' });
  await api(`/api/v1/notifications/${dis.id}/dismiss`);
  const res = await send({ topic: 'infra', title: 'API 5xx rate above 2%', message: 'Recovered after deploy.', external_id: 'api-5xx' });
  await api(`/api/v1/notifications/${res.id}/resolve`);
  // A sender with format problems, for the format check on Settings (#308).
  const ci = await send({ topic: 'ci', format: 'text', message: '**Build** failed on main\nSee [the log](https://ci.example.com/42)' });
  await api(`/api/v1/notifications/${ci.id}/dismiss`);
  // A webhook with a delivery in its log, for Settings (#238). .invalid never resolves, so nothing leaves the box.
  const hook = await fetch(main.base + '/api/v1/topics/infra/hooks/ops-bot', { method: 'PUT', headers: { Authorization: `Bearer ${phone}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ url: 'https://ops.example.invalid/clrd', events: ['dismissed', 'resolved'], secret: 's3' }) });
  if (!hook.ok) throw new Error(`hook: ${hook.status}`);
  // A saved template with long fields, for Settings at phone width (#129).
  const tpl = await fetch(main.base + '/api/v1/topics/infra/templates/uptime-robot-monitor', { method: 'PUT', headers: { Authorization: `Bearer ${phone}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ each: 'monitors', title: '{{get . "monitor.friendly_name" "monitor.url"}} is {{get . "alert.type_name"}}', message: '{{get . "alert.details"}} for {{get . "alert.duration"}} seconds', id: '{{get . "monitor.id"}}', resolve: '{{eq (get . "alert.type") "2"}}', secret: 's3' }) });
  if (!tpl.ok) throw new Error(`template: ${tpl.status}`);
  const hooked = await send({ topic: 'infra', title: 'Swap in use on web-2', message: 'Back under 10%.' });
  await api(`/api/v1/notifications/${hooked.id}/dismiss`);
  await new Promise(r => setTimeout(r, 3000)); // the delivery job runs every 2 seconds

  browser = await chromium.launch();
  const link = Buffer.from(`Bearer ${laptop}`).toString('base64url');
  const sizes = { phone: { width: 360, height: 780 }, desktop: { width: 1280, height: 860 } };

  const pages = [
    ['landing', '/'],
    ['docs', '/docs/'],
    ['docs-spec', '/docs/spec'],
    ['docs-missing', '/docs/nope'],
    ['ui', '/app/ui', { optional: true }],
    ['inbox', '/app'],
    ['inbox-topic', '/app?topic=infra'],
    ['inbox-empty-search', '/app?q=nothing-matches-this'],
    ['inbox-empty', '/app?topic=quiet'],
    ['snoozed', '/app/snoozed'],
    ['cleared', '/app/cleared'],
    ['settings', '/app/settings'],
    ['signin', '/app', { signedOut: true }],
    ['signin-error', '/app?error=token&name=work+laptop', { signedOut: true }],
  ];

  for (const scheme of ['light', 'dark']) {
    for (const [size, viewport] of Object.entries(sizes)) {
      const ctx = await browser.newContext({ viewport, colorScheme: scheme, reducedMotion: 'reduce', timezoneId: 'America/Chicago', locale: 'en-US' });
      const out_ = await browser.newContext({ viewport, colorScheme: scheme, reducedMotion: 'reduce', timezoneId: 'America/Chicago', locale: 'en-US' });
      const signIn = await ctx.newPage();
      await signIn.goto(`${main.base}/app?auth=${link}`);
      await signIn.close();

      const shoot = async (c, name, url, opts = {}) => {
        const page = await c.newPage();
        const errs = [];
        page.on('console', m => { if (m.type() === 'error') errs.push(m.text()); });
        page.on('pageerror', e => errs.push(String(e)));
        const r = await page.goto(url, { waitUntil: 'networkidle' });
        if (opts.optional && r.status() === 404) { await page.close(); return; }
        await page.evaluate(() => document.fonts.ready);
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        if (size === 'phone' && wide > 0) failures.push(`${name} ${scheme} ${size}: ${wide}px wider than the screen`);
        // 401 and 404 pages log a failed resource load on purpose; anything else is a fault.
        for (const e of errs) if (!/status of (401|404)/.test(e)) failures.push(`${name} ${scheme} ${size}: ${e}`);
        const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze();
        for (const v of axe.violations) failures.push(`${name} ${scheme} ${size}: axe ${v.id}: ${v.nodes.map(n => n.target.join(' ')).slice(0, 3).join(', ')}`);
        await page.screenshot({ path: join(out, `${name}-${scheme}-${size}.png`), fullPage: true });
        await page.close();
      };

      for (const [name, path, opts = {}] of pages) await shoot(opts.signedOut ? out_ : ctx, name, main.base + path, opts);
      if (scheme === 'light' && size === 'phone') {
        // The setup form, once; posting it uses the code up.
        await shoot(out_, 'setup', setupURL);
      }
      await ctx.close();
      await out_.close();
    }
  }
  console.log(`ok: screenshots in ${out}`);
} finally {
  await browser?.close();
  for (const s of servers) s.proc.kill();
}
if (failures.length) {
  console.error(failures.join('\n'));
  process.exit(1);
}
