// clrd inbox: live updates on top of the server-rendered page. No framework.
// The page works without this file; it adds the buttons, the WebSocket, relative
// times, the command palette (Ctrl or Cmd+K) with its shortcuts, and Undo.
(() => {
  // A server under a sub-path (CLRD_BASE_URL) puts it in data-base; every
  // site path below goes through fetch or B.
  const B = document.documentElement.dataset.base || '';
  const fetch = (u, o) => window.fetch(u.startsWith('/') ? B + u : u, o);
  document.documentElement.classList.remove('no-js');
  const shell = document.querySelector('[data-inbox]');
  if (!shell) return;

  const show = { inbox: ['active', 'acknowledged'], snoozed: ['snoozed'], cleared: ['dismissed', 'resolved'] }[shell.dataset.view] || [];
  const $ = s => document.querySelector(s);
  const list = $('[data-list]'), empty = $('main [data-empty]'), clearAll = $('[data-act=clear-all]');
  const toast = $('[data-toast]'), announce = $('[data-announce]'), conn = $('[data-conn]'), sync = $('#sync');
  let seq = Number(shell.dataset.seq) || 0;
  let epoch = shell.dataset.epoch || ''; // the database seq belongs to

  // Times are rendered in UTC on the server. Recent ones read as relative
  // ("5 min. ago"), the rest as a date in the reader's zone; the title keeps
  // the full time.
  const fmt = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  const fmtY = new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  const full = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto', style: 'short' });
  const ago = d => {
    const s = (d - Date.now()) / 1000, a = Math.abs(s);
    // The future (snooze until) and anything a day old or more: a date and time.
    if (s > 30 || a >= 86400) return (d.getFullYear() === new Date().getFullYear() ? fmt : fmtY).format(d);
    if (a < 45) return rtf.format(0, 'second');
    if (a < 3600) return rtf.format(Math.round(s / 60), 'minute');
    return rtf.format(Math.round(s / 3600), 'hour');
  };
  const localize = el => el.querySelectorAll('time[datetime]').forEach(t => {
    const d = new Date(t.dateTime);
    if (isNaN(d)) return;
    t.textContent = ago(d);
    t.title = full.format(d);
  });
  localize(document);

  // Link previews (docs/formatting.md): a card for the first web link, fetched
  // by the server. Built with textContent only; the image is a data: URL.
  const previews = el => el.querySelectorAll('[data-preview]').forEach(async b => {
    const a = [...b.querySelectorAll('a[href]')].find(x => /^https?:\/\//i.test(x.getAttribute('href')));
    if (!a || b.dataset.previewDone) return;
    b.dataset.previewDone = 1;
    const r = await fetch(`/api/v1/preview?${new URLSearchParams({ url: a.href, topic: b.dataset.preview })}`, { credentials: 'same-origin' }).catch(() => null);
    const p = r && r.ok && await r.json().catch(() => null);
    if (!p || !p.title) return;
    const card = document.createElement('a');
    card.className = 'preview';
    card.href = a.href;
    card.rel = 'noopener noreferrer';
    if (/^data:image\//.test(p.image || '')) {
      const img = document.createElement('img');
      img.src = p.image;
      img.alt = '';
      card.append(img);
    }
    const text = document.createElement('span');
    for (const [cls, v] of [['site', p.site_name], ['title', p.title], ['desc', p.description]]) {
      if (!v) continue;
      const s = document.createElement('span');
      s.className = cls;
      s.textContent = v;
      text.append(s);
    }
    card.append(text);
    b.after(card);
  });
  previews(document);
  setInterval(() => localize(document), 30000);

  const card = id => list && [...list.children].find(c => c.dataset.id === id);
  const cards = () => list ? [...list.querySelectorAll('.card[data-id]:not(.is-leaving)')] : [];
  const shown = () => cards().filter(c => !c.hidden); // not folded into a stack

  // The card the keyboard works on (J and K). Kept by id: a refresh swaps the nodes.
  let selId = null;
  const selected = () => { const c = selId && card(selId); return c && !c.hidden ? c : null; };
  function select(c, focus = true) {
    list && list.querySelectorAll('.is-selected').forEach(x => x.classList.remove('is-selected'));
    selId = c ? c.dataset.id : null;
    if (!c) return;
    c.classList.add('is-selected');
    if (focus) { c.focus({ preventScroll: true }); c.scrollIntoView({ block: 'nearest' }); }
  }
  const move = by => {
    const all = shown(), i = all.indexOf(selected());
    const next = all[i < 0 ? (by > 0 ? 0 : all.length - 1) : Math.max(0, Math.min(all.length - 1, i + by))];
    if (next) select(next);
  };
  const tidy = () => {
    if (!list) return;
    const none = cards().length === 0 && !list.querySelector('.is-leaving');
    if (empty) empty.hidden = !none;
    if (clearAll) clearAll.hidden = cards().length === 0;
  };

  // Stack by topic (#108), a setting kept in this browser: two or more
  // notifications from one topic fold into one card. Rebuilt from scratch after
  // every refresh, so a new one only bumps the count. Not on a single topic's page.
  const stackTpl = $('[data-stack-tpl]'), unfolded = new Set();
  let stacking = false;
  try { stacking = localStorage.getItem('clrd.stack') === '1'; } catch {}
  const stackOn = () => stacking && !new URLSearchParams(location.search).has('topic');
  const byTopic = cs => { const m = new Map(); for (const c of cs) m.set(c.dataset.topic, [...(m.get(c.dataset.topic) || []), c]); return m; };
  // Where the cards go with stacking on: each topic's cards together, at its newest one's place.
  const stackOrder = cs => stackOn() ? [...byTopic(cs).values()].flat() : cs;
  // Focus inside a stack, as the topic and which button, so a rebuild can put it back.
  const stackFocus = () => {
    const el = document.activeElement, s = el && el.closest && el.closest('[data-stack]:not(.is-leaving)');
    return s && { t: s.dataset.stack, sel: el.matches('[data-act]') ? '[data-act]' : '[data-stack-toggle]' };
  };
  function restack(focus = stackFocus()) {
    if (!list || !stackTpl) return;
    list.querySelectorAll('[data-stack]:not(.is-leaving)').forEach(s => s.remove());
    const groups = byTopic(cards());
    cards().forEach(c => { c.hidden = false; });
    if (!stackOn()) return;
    for (const [t, g] of groups) {
      if (g.length < 2) continue;
      const s = stackTpl.content.firstElementChild.cloneNode(true), open = unfolded.has(t);
      s.dataset.stack = t;
      s.querySelector('[data-stack-topic]').textContent = t;
      s.querySelector('[data-stack-count]').textContent = `${g.length} notifications`;
      s.querySelector('[data-stack-latest]').textContent = g[0].querySelector('.card-title').textContent.trim();
      const top = Math.max(...g.map(c => Number(c.dataset.priority)));
      s.querySelector('[data-stack-dot]').classList.replace('bg-border', top >= 5 ? 'bg-destructive' : top === 4 ? 'bg-foreground' : 'bg-border');
      const side = [...document.querySelectorAll('[data-topic-icon]')].find(a => a.dataset.name === t);
      const icon = (side && side.dataset.topicIcon) || g[0].querySelector('img[data-icon]')?.getAttribute('src');
      if (icon) Object.assign(s.querySelector('img'), { src: icon, hidden: false });
      const tg = s.querySelector('[data-stack-toggle]');
      tg.setAttribute('aria-expanded', open);
      tg.textContent = open ? 'Hide' : 'Show all';
      g[0].before(s);
      // The topic's cards sit under its stack. Move only what is out of place:
      // moving a node drops the focus inside it.
      g.reduce((prev, c) => { if (prev.nextElementSibling !== c) prev.after(c); return c; }, s);
      g.forEach(c => { c.hidden = !open; });
      if (focus && focus.t === t) s.querySelector(focus.sel).focus();
    }
  }
  restack();
  // Tap a stack to open or close it in place.
  list && list.addEventListener('click', e => {
    const s = e.target.closest('[data-stack]');
    if (!s || e.target.closest('[data-act]') || s.classList.contains('is-leaving')) return;
    const t = s.dataset.stack;
    if (!unfolded.delete(t)) unfolded.add(t);
    restack({ t, sel: '[data-stack-toggle]' });
  });
  // The switch in Settings, and the same switch flipped in another tab.
  const stackSw = $('[data-stack-setting]');
  if (stackSw) {
    stackSw.checked = stacking;
    stackSw.addEventListener('change', () => { try { localStorage.setItem('clrd.stack', stackSw.checked ? '1' : '0'); } catch {} });
  }
  addEventListener('storage', e => { if (e.key === 'clrd.stack') { stacking = e.newValue === '1'; restack(); tidy(); } });

  // Sound for a new notification while the page is open (#282, SPEC 2.2).
  // The server keeps each device's choice, so an agent can set it too (#297).
  // A choice the server refused (a token limited to some topics, no
  // connection) stays in this browser and wins; one from before #297 moves to
  // the server on the next load. Unset, default or a name this page does not
  // know plays chime: a browser has no sound of its own.
  const sounds = ['chime', 'ding', 'pop', 'bell', 'alert', 'soft'];
  let serverSound = shell.dataset.sound || 'default';
  const localSound = () => { try { return localStorage.getItem('clrd.sound'); } catch { return null; } };
  const keepLocal = s => { try { s ? localStorage.setItem('clrd.sound', s) : localStorage.removeItem('clrd.sound'); } catch {} };
  const soundPick = () => localSound() || serverSound;
  // Saves s for this device; returns '' or why it is kept in this browser only.
  // Kept in the browser first, so leaving the page mid-save loses nothing, and
  // saves go one at a time, so an older one never lands after a newer one.
  let saving = Promise.resolve();
  const saveSound = s => {
    keepLocal(s);
    return saving = saving.then(async () => {
      const r = await fetch('/api/v1/settings', { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ sound: s }) }).catch(() => null);
      if (r && r.ok) { serverSound = s; if (localSound() === s) keepLocal(null); return ''; }
      return 'Saved in this browser only: ' + (r ? ((await r.json().catch(() => ({}))).error || `the server said ${r.status}.`) : 'no connection.');
    });
  };
  if (localSound()) saveSound(localSound());
  const soundFile = s => s === 'none' ? '' : sounds.includes(s) ? s : 'chime';
  // A browser blocks sound until the page has had a click or key press: say so, never an error.
  const blocked = 'Sound is blocked until you click or press a key on this page.';
  let hinted = false;
  const playSound = (s, onBlocked) => {
    const f = soundFile(s);
    if (f) new Audio(`${B}/assets/sounds/${f}.wav`).play().catch(e => { if (e.name === 'NotAllowedError') onBlocked(); });
  };
  const alertSound = n => {
    if (n.silenced) return; // a silence wins over the choice
    playSound(soundPick(), () => { if (!hinted) { hinted = true; say(blocked); } });
  };
  const soundSel = $('[data-sound-setting]');
  if (soundSel) {
    soundSel.value = soundPick();
    if (!soundSel.value) soundSel.value = 'default';
    const hint = $('[data-sound-hint]');
    const preview = () => { hint.textContent = ''; playSound(soundSel.value, () => { hint.textContent = blocked; }); };
    soundSel.addEventListener('change', async () => { preview(); const why = await saveSound(soundSel.value); if (why) hint.textContent = why; });
    $('[data-sound-play]').addEventListener('click', preview);
  }

  // A card that no longer belongs in this view: the cleared moment (clrd.css).
  // Green washes across it with a check and the reason, then it folds away.
  // Timers, not animation events: with reduced motion there are none.
  const still = matchMedia('(prefers-reduced-motion: reduce)');
  const check = '<svg class="size-4 shrink-0" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>';
  function leave(c, why) {
    if (!c || c.classList.contains('is-leaving')) return;
    if (c.dataset.id === selId) { const all = shown(), i = all.indexOf(c); select(all[i + 1] || all[i - 1] || null, c.contains(document.activeElement)); }
    const p = document.createElement('p');
    p.className = 'gone';
    p.innerHTML = check;
    p.append(why);
    c.append(p);
    c.classList.add('is-leaving');
    c.querySelectorAll('button').forEach(b => { b.disabled = true; });
    announce.textContent = `${c.querySelector('.card-title').textContent.trim()}: ${why}`;
    setTimeout(() => {
      c.classList.add('is-folding');
      if (!still.matches) {
        c.animate([{ height: `${c.offsetHeight}px` }, { height: '0px', opacity: 0, marginBottom: '-0.5rem', borderWidth: 0 }],
          { duration: 260, easing: 'cubic-bezier(.4, 0, .2, 1)', fill: 'forwards' });
      }
      setTimeout(() => { c.remove(); tidy(); }, still.matches ? 0 : 260);
    }, 1400);
  }

  const reason = n => {
    const by = n.state_by ? (n.state === 'resolved' ? ' by ' : ' on ') + n.state_by : '';
    return ({ dismissed: 'Cleared', resolved: 'Resolved', snoozed: 'Snoozed', acknowledged: 'Acknowledged', active: 'Back in the inbox' }[n.state] || 'Moved') + by;
  };

  // Re-fetch this page and patch it in: cards keyed by id and seq, plus the sidebar.
  let pending, gen = 0;
  const refresh = () => { clearTimeout(pending); pending = setTimeout(patch, 250); };
  async function patch() {
    if (list && list.querySelector('.is-leaving')) return refresh();
    let doc;
    const mine = ++gen; // a slower, older response must not overwrite a newer one
    try {
      const r = await fetch(location.href, { credentials: 'same-origin', headers: { Accept: 'text/html' } });
      if (mine !== gen) return;
      if (r.status === 401) return location.reload(); // signed out: show the sign-in page
      if (!r.ok) throw r.status;
      const html = await r.text();
      if (mine !== gen) return;
      doc = new DOMParser().parseFromString(html, 'text/html');
    } catch {
      // seq has already moved past this change, so the WebSocket will not
      // replay it: keep trying until the page loads.
      clearTimeout(pending);
      pending = setTimeout(patch, 5000);
      return;
    }
    document.title = doc.title; // the open count in the tab
    document.querySelectorAll('[data-live]').forEach(cur => {
      const next = doc.querySelector(`[data-live="${cur.dataset.live}"]`);
      if (next && !cur.contains(document.activeElement)) cur.replaceWith(document.adoptNode(next));
    });
    const nextList = doc.querySelector('[data-list]');
    if (!list || !nextList) return tidy();
    const sf = stackFocus();
    const keep = stackOrder([...nextList.children].map(n => {
      const old = card(n.dataset.id);
      if (old && old.dataset.seq === n.dataset.seq) return old;
      const fresh = document.adoptNode(n);
      localize(fresh);
      previews(fresh);
      return fresh;
    }));
    const now = [...list.children].filter(c => !c.matches('[data-stack]'));
    if (keep.some(c => c.contains(document.activeElement)) || keep.length !== now.length || keep.some((c, i) => c !== now[i])) {
      const had = list.contains(document.activeElement) && !document.activeElement.closest('[data-stack]') && document.activeElement;
      list.replaceChildren(...keep);
      if (had && !had.isConnected) select(selected()); // focus was on a node the refresh replaced
    }
    restack(sf);
    const s = selected();
    if (s) select(s, false); else selId = null;
    tidy();
  }

  function onEvent(m) {
    const n = m.notification;
    if (m.seq > seq) seq = m.seq;
    const c = card(n.id);
    if (c && m.seq <= Number(c.dataset.seq)) return;
    if (c && !show.includes(n.state)) leave(c, reason(n));
    refresh();
  }

  // A notice only for Undo and for errors; everything else shows on the card
  // and is read out by the live region.
  let toastTimer;
  function say(text, undoIds) {
    const span = document.createElement('span');
    span.textContent = text;
    toast.replaceChildren(span);
    if (undoIds) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'h-7 rounded-md px-2.5 font-semibold underline underline-offset-3 hover:bg-background/10';
      b.textContent = 'Undo';
      b.addEventListener('click', async () => {
        toast.hidden = true;
        for (const id of undoIds) await post(`/api/v1/notifications/${encodeURIComponent(id)}/reopen`);
        refresh();
      });
      toast.append(b);
    }
    toast.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { toast.hidden = true; }, undoIds ? 30000 : 6000);
  }

  async function post(url, body, refusals) {
    try {
      const r = await fetch(url, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ via: 'web', ...body }), // via: so the server log says the web inbox did it (#267)
      });
      if (r.status === 401) { location.reload(); return null; } // signed out: show the sign-in page
      if (!r.ok && refusals) return r; // the caller says why
      if (!r.ok) { say(`The server refused that (${r.status}). Nothing changed.`); return null; }
      return r;
    } catch {
      say('Could not reach the server. Nothing changed.');
      return null;
    }
  }

  // Snooze times the server cannot work out: it does not know the reader's time zone.
  const snoozeAt = (at, c) => {
    const d = new Date();
    if (at === 'tonight') d.setHours(20, 0, 0, 0);
    else if (at === 'tomorrow') { d.setDate(d.getDate() + 1); d.setHours(9, 0, 0, 0); }
    else return c.querySelector('[data-until]').value ? new Date(c.querySelector('[data-until]').value) : null;
    return d;
  };
  // Tonight is offered only while it is still ahead.
  document.addEventListener('toggle', e => {
    const t = e.target.querySelector && e.target.querySelector('[data-at=tonight]');
    if (t) t.hidden = new Date().getHours() >= 20;
  }, true);

  // Enter in the date box would submit the card form with its first button (Ack).
  document.addEventListener('keydown', e => {
    if (e.key === 'Enter' && e.target.matches && e.target.matches('[data-until]')) {
      e.preventDefault();
      e.target.closest('.snooze-custom').querySelector('[data-act]').click();
    }
  });

  // Long press (or right click) on Snooze opens the other times instead of
  // snoozing for the default; the click that ends a long press is dropped.
  let hold = 0, held = null;
  const more = b => { held = b; b.nextElementSibling.open = true; };
  document.addEventListener('pointerdown', e => {
    const b = e.button === 0 && e.target.closest('[data-snooze-main]');
    held = null;
    if (b) hold = setTimeout(() => more(b), 500);
  });
  ['pointerup', 'pointercancel', 'pointerleave'].forEach(t => document.addEventListener(t, () => clearTimeout(hold), true));
  document.addEventListener('contextmenu', e => {
    const b = e.target.closest('[data-snooze-main]');
    if (b) { e.preventDefault(); clearTimeout(hold); more(b); }
  });
  document.addEventListener('click', e => {
    if (held && e.target.closest('[data-snooze-main]') === held) { e.preventDefault(); e.stopImmediatePropagation(); }
    held = null;
  }, true);
  document.addEventListener('keydown', () => { held = null; }, true); // a touch long press may end with no click

  // Settings: snooze times. "2d" is sent as "48h", the form the server reads.
  const snz = $('[data-snooze-settings]');
  const snzTyped = () => snz.times.value.split(',').map(v => v.trim()).filter(Boolean);
  const snzValue = v => v.replace(/^(\d+)d$/, (_, n) => `${n * 24}h`);
  const snzSaved = snz && snz.default.value;
  const snzDirty = () => snz && (snz.times.value !== snz.times.defaultValue || snz.default.value !== snzSaved);
  if (snz) {
    // The Default list follows the Times box as it is typed, so a time just added can be the default (#182).
    const labels = Object.fromEntries([...snz.default.options].map(o => [o.value, o.text]));
    let want = snzSaved; // the default last picked, kept while its time is retyped
    snz.default.addEventListener('change', () => { want = snz.default.value; });
    snz.times.addEventListener('input', () => {
      snz.default.replaceChildren(...snzTyped().map(v => new Option(labels[snzValue(v)] || v, snzValue(v), false, snzValue(v) === want)));
    });
  }
  // A saved setting reloads the page, so while one save runs the snooze and
  // display forms are locked: an edit made then would be lost (#214), and an
  // older save cannot land after a newer one.
  const disp = $('[data-display-settings]');
  const keep = $('[data-keep-settings]');
  const sil = $('[data-silence-form]');
  const tkeep = $('[data-topic-keep]');
  const lock = on => [snz, disp, keep, sil, tkeep].forEach(f => f && [...f.elements].forEach(c => { c.disabled = on; }));
  snz && snz.addEventListener('submit', async e => {
    e.preventDefault();
    const times = snzTyped().map(snzValue);
    const body = { snooze_intervals: times };
    if (times.includes(snz.default.value)) body.snooze_default = snz.default.value; // otherwise the server keeps or picks the first
    const status = $('[data-snooze-status]');
    lock(true);
    const r = await fetch('/api/v1/settings', { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null);
    if (r && r.ok) return location.reload();
    lock(false);
    status.textContent = r ? ((await r.json().catch(() => ({}))).error || `Not saved (${r.status}).`) : 'Not saved: no connection.';
  });

  // Settings: how long cleared messages are kept (#250). Before a save that
  // deletes anything, say how many go and ask.
  keep && keep.days.addEventListener('change', () => { $('[data-keep-custom]').hidden = keep.days.value !== 'custom'; });
  keep && keep.addEventListener('submit', async e => {
    e.preventDefault();
    const custom = keep.days.value === 'custom', days = Number(custom ? keep.custom.value : keep.days.value);
    const status = $('[data-keep-status]');
    if (!Number.isInteger(days) || days < (custom ? 1 : 0) || days > 36500) { status.textContent = 'Enter a whole number of days, 1 to 36500.'; return; }
    lock(true);
    const c = await fetch(`/api/v1/counts?keep_cleared_days=${days}`, { credentials: 'same-origin' }).then(r => r.ok ? r.json() : null).catch(() => null);
    if (!c) { lock(false); status.textContent = 'Not saved: could not check how many messages this deletes.'; return; } // never delete without the count
    const n = c.would_remove || 0;
    if (n && !confirm(`This deletes ${n} cleared message${n === 1 ? '' : 's'} within the hour. Keep cleared messages for ${days} day${days === 1 ? '' : 's'}?`)) { lock(false); return; }
    const r = await fetch('/api/v1/settings', { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ keep_cleared_days: days }) }).catch(() => null);
    if (r && r.ok) return location.reload();
    lock(false);
    status.textContent = r ? ((await r.json().catch(() => ({}))).error || `Not saved (${r.status}).`) : 'Not saved: no connection.';
  });

  // Settings: a topic's own keep (#323), saved on change. Default drops it, so
  // the topic follows the period above; that can delete too, so it is checked the same way.
  tkeep && tkeep.addEventListener('change', async e => {
    const sel = e.target, t = sel.dataset.topic, status = $('[data-topic-keep-status]');
    const days = sel.value === '' ? null : Number(sel.value);
    const undo = msg => { sel.value = [...sel.options].find(o => o.defaultSelected).value; lock(false); status.textContent = msg; };
    lock(true);
    const c = await fetch(`/api/v1/counts?keep_cleared_days=${days ?? shell.dataset.keep}&topic=${encodeURIComponent(t)}`, { credentials: 'same-origin' }).then(r => r.ok ? r.json() : null).catch(() => null);
    if (!c) return undo('Not saved: could not check how many messages this deletes.'); // never delete without the count
    const n = c.would_remove || 0;
    if (n && !confirm(`This deletes ${n} cleared message${n === 1 ? '' : 's'} in ${t} within the hour. Save?`)) return undo('');
    const r = await fetch('/api/v1/settings', { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ topic_keep_cleared_days: { [t]: days } }) }).catch(() => null);
    if (r && r.ok) return location.reload();
    undo(r ? ((await r.json().catch(() => ({}))).error || `Not saved (${r.status}).`) : 'Not saved: no connection.');
  });

  // Settings: silences (docs/silences.md). Start one, or end one early.
  const silSave = async (req, status) => {
    lock(true);
    const r = await fetch(...req).catch(() => null);
    // A 404 on End now means the silence already ran out: the list is just stale.
    if (r && (r.ok || (r.status === 404 && req[1].method === 'DELETE'))) return location.reload();
    lock(false);
    status.textContent = r ? ((await r.json().catch(() => ({}))).error || `Not saved (${r.status}).`) : 'Not saved: no connection.';
  };
  sil && sil.for.addEventListener('change', () => { $('[data-silence-until]').hidden = sil.for.value !== 'until'; });
  sil && sil.addEventListener('submit', e => {
    e.preventDefault();
    const status = $('[data-silence-status]'), body = { topic: sil.topic.value.trim(), tag: sil.tag.value.trim(), comment: sil.comment.value.trim() };
    if (!body.topic && !body.tag) { status.textContent = 'Give a topic, a tag, or both.'; return; }
    if (sil.for.value !== 'until') body.for = sil.for.value;
    else if (isNaN(new Date(sil.until.value))) { status.textContent = 'Pick the time it ends.'; return; }
    else body.until = new Date(sil.until.value).toISOString(); // datetime-local is this browser's time
    silSave(['/api/v1/silences', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }], status);
  });
  document.addEventListener('click', e => {
    const b = e.target.closest('[data-silence-end]');
    if (b) e.preventDefault(), silSave([`/api/v1/silences/${encodeURIComponent(b.dataset.silenceEnd)}`, { method: 'DELETE', credentials: 'same-origin' }], $('[data-silence-status]'));
  });
  // Settings: deleting a webhook also drops its delivery log, so ask first.
  document.addEventListener('submit', e => {
    const f = e.target.closest('[data-hook-delete]');
    if (f && !confirm(`Delete the webhook ${f.dataset.hookDelete} and its delivery log?`)) e.preventDefault();
    const t = e.target.closest('[data-template-delete]');
    if (t && !confirm(`Delete the template ${t.dataset.templateDelete}?`)) e.preventDefault();
  });

  // Settings: run a template's unsaved fields over a sample post (#129).
  // Nothing is published or stored; the server shows what a real post would make.
  document.addEventListener('click', async e => {
    const b = e.target.closest('[data-template-preview]');
    if (!b) return;
    const f = b.closest('form'), box = b.closest('[data-template-try]');
    const status = box.querySelector('[data-template-status]'), out = box.querySelector('[data-template-out]');
    const v = k => f.elements.namedItem(k).value, topic = v('topic').trim(), name = v('name').trim();
    out.replaceChildren();
    if (!topic || !name) return void (status.textContent = 'Fill in the topic and name first.');
    let payload;
    try { payload = JSON.parse(box.querySelector('[data-template-payload]').value); } catch { return void (status.textContent = 'The sample is not valid JSON.'); }
    const headers = {};
    for (const line of box.querySelector('[data-template-headers]').value.split('\n')) {
      const i = line.indexOf(':');
      if (i > 0) headers[line.slice(0, i).trim()] = line.slice(i + 1).trim();
    }
    const template = {};
    for (const k of ['each', 'title', 'message', 'priority', 'tags', 'click', 'id', 'resolve']) if (v(k).trim()) template[k] = v(k);
    status.textContent = 'Running...';
    const r = await fetch(`/api/v1/topics/${encodeURIComponent(topic)}/templates/${encodeURIComponent(name)}/preview`,
      { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ payload, headers, template }) }).catch(() => null);
    const j = r ? await r.json().catch(() => ({})) : {};
    if (!r || !r.ok) return void (status.textContent = r ? j.error || `Not run (${r.status}).` : 'Not run: no connection.');
    const ns = j.notifications || [];
    status.textContent = ns.length ? `A post like this makes ${ns.length} ${ns.length === 1 ? 'notification' : 'notifications'}.` : 'A post like this makes no notifications.';
    for (const n of ns) {
      const card = document.createElement('div'), h = document.createElement('p'), m = document.createElement('p'), meta = document.createElement('p');
      card.className = 'bg-card grid gap-1 rounded-xl border p-4';
      h.className = 'text-sm font-semibold [overflow-wrap:anywhere]';
      m.className = 'text-body text-sm [overflow-wrap:anywhere]';
      meta.className = 'meta [overflow-wrap:anywhere]';
      h.textContent = n.title || '(no title)';
      m.textContent = n.message || '';
      meta.textContent = [n.status === 'resolved' ? 'Resolves' : `Priority ${n.priority || 3}`, n.external_id && `ID ${n.external_id}`, n.tags?.length && `Tags ${n.tags.join(', ')}`, n.click].filter(Boolean).join('. ');
      card.append(h, m, meta);
      out.append(card);
    }
  });

  // Settings: message display switches, each saved on its own.
  disp && disp.addEventListener('change', async e => {
    const sw = e.target, status = $('[data-display-status]'), t = sw.dataset.topic;
    const body = t ? { topic_previews: { [t]: sw.value ? sw.value === 'on' : null } } : { [sw.name]: sw.checked }; // null: back to the default
    lock(true);
    const r = await fetch('/api/v1/settings', { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null);
    if (r && r.ok) return location.reload();
    if (t) sw.value = [...sw.options].find(o => o.defaultSelected).value; else sw.checked = !sw.checked;
    lock(false);
    status.textContent = r ? ((await r.json().catch(() => ({}))).error || `Not saved (${r.status}).`) : 'Not saved: no connection.';
  });

  // Settings: a topic's icon (#199), saved when the box loses focus or on Enter.
  const ticons = $('[data-topic-icons]');
  // Enter is caught on keydown: with several URL boxes and no submit button the
  // browser does not submit the form.
  ticons && ticons.addEventListener('submit', e => e.preventDefault());
  ticons && ticons.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); e.target.blur(); } });
  ticons && ticons.addEventListener('change', async e => {
    const box = e.target, status = $('[data-topic-icon-status]');
    const r = await fetch(`/api/v1/topics/${encodeURIComponent(box.dataset.topic)}`, { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ icon: box.value.trim() }) }).catch(() => null);
    if (r && r.ok) { box.defaultValue = box.value; status.textContent = `Saved the icon for ${box.dataset.topic}.`; return; }
    status.textContent = r ? ((await r.json().catch(() => ({}))).error || `Not saved (${r.status}).`) : 'Not saved: no connection.';
  });

  // An icon that fails to load (gone, host down) is hidden, not shown broken (#199), so no gap is left (#309).
  const icons = 'img[data-icon], img.favicon';
  document.addEventListener('error', e => { if (e.target.matches?.(icons)) e.target.hidden = true; }, true);
  // An icon that failed before this script ran.
  document.querySelectorAll(icons).forEach(i => { if (i.complete && !i.naturalWidth) i.hidden = true; });

  // Share and Copy (SPEC section 2): title, message and click, from the card.
  // Share needs a secure page (https or localhost); a plain http LAN address has
  // no navigator.clipboard either, so Copy falls back to a hidden text area there.
  document.documentElement.classList.toggle('no-share', !navigator.share);
  document.documentElement.classList.toggle('no-copy', !navigator.clipboard && !document.queryCommandSupported?.('copy'));
  const copyText = async text => {
    if (navigator.clipboard) return navigator.clipboard.writeText(text);
    const ta = document.createElement('textarea');
    ta.value = text; ta.readOnly = true; ta.style.position = 'fixed'; ta.style.opacity = '0';
    document.body.append(ta); ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    if (!ok) throw new Error('copy refused');
  };
  async function shareCard(act, c) {
    const d = c.querySelector('[data-share]'), title = d.value, text = d.dataset.message, url = d.dataset.click;
    try {
      if (act === 'share') return await navigator.share({ title, text, ...(url && { url }) });
      await copyText([title, text].filter(Boolean).join('\n\n') + (url ? `\n${url}` : ''));
      say('Copied.');
    } catch (err) {
      if (err.name !== 'AbortError') say(act === 'share' ? 'Could not share.' : 'Could not copy.');
    }
  }

  // Settings, format check (#308): copy a topic's fix prompt.
  document.addEventListener('click', async e => {
    const b = e.target.closest('[data-format-copy]');
    if (!b) return;
    try { await copyText(b.closest('[data-format-topic]').querySelector('[data-format-prompt]').textContent); say('Copied.'); } catch { say('Could not copy.'); }
  });

  // Empty-inbox tip card (#244): previous and next, a slow rotation that pauses
  // on hover or focus, and none at all under reduced motion.
  const tipBox = $('[data-tips]');
  if (tipBox) {
    const ts = [...tipBox.querySelectorAll('[data-tip]')];
    let at = ts.findIndex(t => !t.hidden), stopped = false;
    const show = (i, said) => {
      ts[at].hidden = true; at = (i + ts.length) % ts.length; ts[at].hidden = false;
      $('[data-tip-count]').textContent = `${at + 1} of ${ts.length}`;
      if (said) announce.textContent = ts[at].getAttribute('aria-label');
    };
    tipBox.addEventListener('click', async e => {
      const step = e.target.closest('[data-tip-step]');
      // Stepping by hand stops the rotation for good (WCAG 2.2.2).
      if (step) { stopped = true; return show(at + +step.dataset.tipStep, true); }
      if (!e.target.closest('[data-tip-copy-btn]')) return;
      try { await copyText(ts[at].querySelector('[data-tip-copy]').textContent); say('Copied.'); } catch { say('Could not copy.'); }
    });
    setInterval(() => {
      if (stopped || still.matches || document.hidden || !tipBox.offsetParent || tipBox.matches(':hover') || tipBox.contains(document.activeElement)) return;
      show(at + 1, false);
    }, 9000); // long enough to read one tip (#292)
  }

  const done = { ack: 'Acknowledged', dismiss: 'Cleared', reopen: 'Restored to the inbox' };
  const tell = text => { announce.textContent = text; };
  document.addEventListener('click', async e => {
    const b = e.target.closest('[data-act]');
    if (!b || b.disabled) return;
    e.preventDefault(); // the buttons are also a form for when this script is off
    const act = b.dataset.act;
    if (act === 'clear-all' || act === 'clear-stack') {
      // A stack sends the ids it holds, not its topic: clearing a topic on the
      // server would also clear that topic's snoozed notifications.
      const s = act === 'clear-stack' && b.closest('[data-stack]');
      const ids = cards().filter(c => !s || c.dataset.topic === s.dataset.stack).map(c => c.dataset.id);
      b.disabled = true; // one tap, one clear
      const r = ids.length && await post('/api/v1/notifications/clear', { ids, via: 'web_bulk' });
      b.disabled = false;
      if (!r) return;
      const n = (await r.json().catch(() => ({}))).cleared;
      if (s) { unfolded.delete(s.dataset.stack); leave(s, 'Cleared'); }
      ids.forEach(id => leave(card(id), 'Cleared'));
      // Undo only when we know every id was ours to clear; otherwise it could
      // reopen something another device cleared first.
      say(n === undefined ? 'Cleared.' : `Cleared ${n} ${n === 1 ? 'notification' : 'notifications'}.`, n === ids.length ? ids : undefined);
      return refresh();
    }
    const c = b.closest('.card');
    if (act === 'share' || act === 'copy') return shareCard(act, c);
    let body;
    if (act === 'snooze') {
      const t = !b.dataset.for && snoozeAt(b.dataset.at, c);
      if (!b.dataset.for && !(t > new Date())) return say('Pick a time in the future.');
      body = b.dataset.for ? { for: b.dataset.for } : { until: t.toISOString() };
    }
    if (act === 'reply') body = { reply: b.value, seq: Number(c.dataset.seq) }; // 409 if it changed since this card
    b.disabled = true;
    const r = await post(`/api/v1/notifications/${encodeURIComponent(c.dataset.id)}/${act}`, body, act === 'reply');
    b.disabled = false;
    if (!r) return;
    const n = await r.json().catch(() => ({}));
    if (act === 'reply') {
      // Who may approve (#329): a refused or pending tap says so on the card itself.
      const why = r.status === 202 ? 'Sent for confirmation. An allowed device has to confirm it.'
        : !r.ok && ({ approve_denied: 'This device may not answer approval requests. An allowed device can change that in Settings.',
          own_request: 'This device cannot answer a request its own token sent.',
          conflict: 'Not answered: it was answered, cleared or changed since. Reload to see it now.' }[n.code] || `The server refused that (${r.status}). Nothing changed.`);
      if (!why) { tell(`Answered ${b.textContent}.`); return refresh(); }
      let p = c.querySelector('[data-note]');
      if (!p) { p = document.createElement('p'); p.className = 'meta w-full'; p.dataset.note = ''; p.setAttribute('role', 'status'); b.form.append(p); }
      p.textContent = why;
      say(why); // a live redraw can replace the card, so the toast says it too
      return r.ok && refresh();
    }
    done.snooze = n.snooze_until ? `Snoozed until ${fmt.format(new Date(n.snooze_until))}` : 'Snoozed';
    if (act === 'dismiss' && !(n.state === 'dismissed' && n.state_by_id === shell.dataset.meId)) {
      // Another device or the sender got there first: no Undo, it would reopen their change.
      leave(c, reason(n));
      say(`${reason(n)}.`);
    } else {
      if (act !== 'ack') leave(c, done[act]);
      else tell(`${c.querySelector('.card-title').textContent.trim()}: Acknowledged`);
      if (act === 'dismiss') say('Cleared.', [c.dataset.id]);
    }
    refresh();
  });

  // Unlink a device from Settings, after a confirm (#246).
  const unlinkBox = $('#unlink');
  if (unlinkBox) {
    const status = $('[data-unlink-status]');
    let id = '';
    document.addEventListener('click', e => {
      const b = e.target.closest('[data-unlink]');
      if (b) { id = b.dataset.unlink; $('[data-unlink-name]').textContent = b.dataset.name; status.textContent = ''; unlinkBox.showModal(); }
      if (e.target === unlinkBox || e.target.closest('[data-unlink-close]')) unlinkBox.close();
    });
    unlinkBox.querySelector('form').addEventListener('submit', async e => {
      e.preventDefault();
      if (e.submitter) e.submitter.disabled = true;
      const r = await fetch(`/api/v1/devices/${encodeURIComponent(id)}`, { method: 'DELETE', credentials: 'same-origin' }).catch(() => null);
      if (e.submitter) e.submitter.disabled = false;
      if (r && (r.ok || r.status === 401)) return location.reload();
      status.textContent = r ? (await r.json().catch(() => ({}))).error || `Not unlinked (${r.status}).` : 'Not unlinked: no connection.';
    });
  }

  // Who may approve, per device, in Settings (#329). The page offers no
  // raise for this browser or what it hangs under; lowering one of those
  // cannot be undone from here, so it asks first.
  document.addEventListener('change', async e => {
    const s = e.target.closest('[data-approve]');
    if (!s) return;
    const was = [...s.options].find(o => o.defaultSelected)?.value, label = s.selectedOptions[0].textContent;
    if ('own' in s.dataset && !confirm(`Lower ${s.dataset.name} to ${label}? Only another allowed device can raise it again.`)) { s.value = was; return; }
    s.disabled = true;
    const r = await fetch(`/api/v1/devices/${encodeURIComponent(s.dataset.approve)}/approve`, {
      method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ approve: s.value }),
    }).catch(() => null);
    s.disabled = false;
    if (r && r.status === 401) return location.reload();
    if (r && r.ok) { s.blur(); say(`${s.dataset.name} may approve: ${label}.`); return refresh(); } // blur, or the redraw skips the focused table
    s.value = was;
    say(r ? `Not changed: ${(await r.json().catch(() => ({}))).error || r.status}.` : 'Could not reach the server. Nothing changed.');
  });

  // Send a notification from this browser, with its own token (#150).
  const sendBox = $('#send');
  if (sendBox) {
    const f = sendBox.querySelector('form'), status = $('[data-send-status]');
    let opened = 0; // counts openings: a send that ends after the dialog was reopened leaves the new note alone (#182)
    document.addEventListener('click', e => {
      if (e.target.closest('[data-send-open]')) { opened++; status.textContent = ''; sendBox.showModal(); }
      if (e.target === sendBox || e.target.closest('[data-send-close]')) sendBox.close(); // the dim or Cancel
    });
    f.addEventListener('submit', async e => {
      e.preventDefault();
      const v = name => f.elements[name].value.trim();
      const body = { topic: v('topic'), title: v('title') || undefined, message: v('message'), priority: Number(v('priority')) };
      const btn = e.submitter, mine = opened;
      if (btn) btn.disabled = true; // one tap, one notification
      status.textContent = 'Sending...';
      const r = await fetch('/api/v1/notifications', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null);
      if (btn) btn.disabled = false;
      if (r && r.status === 401) return location.reload();
      if (mine !== opened) return say(r && r.ok ? 'Sent.' : 'Not sent.');
      if (!r || !r.ok) {
        status.textContent = r ? (await r.json().catch(() => ({}))).error || `Not sent (${r.status}).` : 'Not sent: no connection.';
        return;
      }
      f.elements.title.value = f.elements.message.value = '';
      sendBox.close();
      say('Sent.');
    });
  }

  // Command palette (Ctrl or Cmd+K) and single-key shortcuts. The palette
  // lists every shortcut; Basecoat's command.js filters it and runs Enter.
  const palette = $('#palette');
  const mac = /Mac|iPhone|iPad/.test(navigator.platform);
  if (mac) document.querySelectorAll('[data-mod]').forEach(k => { k.textContent = '\u2318 K'; });
  const openPalette = () => {
    if (!palette || palette.open) return;
    const input = palette.querySelector('input');
    input.value = '';
    input.dispatchEvent(new Event('input'));
    palette.showModal();
  };
  palette && palette.addEventListener('click', e => { if (e.target === palette) palette.close(); }); // click on the dim
  const search = $('[data-search]');
  // Each command presses the selected card's own button, so Undo and errors work the same.
  const press = (sel, c = selected() || shown()[0]) => {
    if (stackFocus()) return tell('Open the stack to act on one notification.'); // not the card next to it
    const b = c && c.querySelector(sel);
    if (!b) return tell(c ? 'Not available on this notification.' : 'No notification to act on.');
    select(c);
    b.click();
  };
  const run = {
    dismiss: () => press('[data-act=dismiss]'),
    restore: () => press('[data-act=reopen]'),
    ack: () => press('[data-act=ack]'),
    snooze: () => press('[data-snooze-main]'),
    next: () => move(1),
    prev: () => move(-1),
    search: () => search && (search.focus(), search.select()),
    'clear-all': () => clearAll && !clearAll.hidden && clearAll.click(),
  };
  document.addEventListener('click', e => {
    if (e.target.closest('[data-palette]')) return openPalette();
    const cmd = e.target.closest('[data-cmd]');
    if (cmd && run[cmd.dataset.cmd]) setTimeout(run[cmd.dataset.cmd]); // after the dialog closes and gives focus back
    const c = e.target.closest('.card[data-id]');
    if (c && !c.classList.contains('is-leaving')) select(c, false);
  });
  const keys = { c: 'dismiss', r: 'restore', a: 'ack', s: 'snooze', j: 'next', k: 'prev', '/': 'search' };
  const go = { i: B + '/app', s: B + '/app/snoozed', c: B + '/app/cleared', ',': B + '/app/settings' };
  let g = 0;
  document.addEventListener('keydown', e => {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); return palette && (palette.open ? palette.close() : openPalette()); }
    const t = e.target;
    if (e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented || $('dialog[open]')) return; // the palette or Send is open
    if (t.closest && t.closest('input, textarea, select, [contenteditable], details[open]')) {
      if (e.key === 'Escape' && t === search) search.blur();
      return;
    }
    const k = e.key.toLowerCase();
    if (g && Date.now() - g < 1200 && go[k]) { e.preventDefault(); g = 0; location.href = go[k]; return; }
    g = k === 'g' ? Date.now() : 0;
    if (k === 'g' || e.shiftKey && k !== '?') return;
    if (k === '?') { e.preventDefault(); return openPalette(); }
    if (keys[k] && list) { e.preventDefault(); run[keys[k]](); }
  });

  // Live sync. Replays everything after the last seq on every (re)connect.
  // Retries forever, but after 3 minutes without it says the page may be stale.
  let tries = 0, staleTimer = null;
  const offline = $('[data-offline]');
  const live = on => {
    sync.classList.toggle('is-live', on);
    if (on) { sync.classList.remove('is-down'); refresh(); } // device changes between render (or the drop) and ready send no message (#295)
    conn.textContent = on ? '' : (tries ? 'Reconnecting' : 'Connecting');
    if (on) { clearTimeout(staleTimer); staleTimer = null; offline.hidden = true; }
    else staleTimer ??= setTimeout(() => { offline.hidden = false; }, 180000);
  };
  function connect() {
    const u = new URL(B + '/api/v1/ws', location.href);
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
    u.searchParams.set('since', seq);
    const ws = new WebSocket(u);
    let ready = false;
    ws.onmessage = e => {
      let m;
      try { m = JSON.parse(e.data); } catch { return; }
      const reset = () => { ws.onclose = null; ws.close(); location.reload(); };
      if (m.type === 'ready') {
        // Another epoch is another database (server reinstalled or moved), so
        // this page's seq and cards mean nothing there: same as reset.
        if (epoch && m.epoch && m.epoch !== epoch) return reset();
        epoch ||= m.epoch || '';
        tries = 0; if (m.seq > seq) seq = m.seq; live(true); ready = true;
      }
      else if (m.type === 'reset') reset();
      else if (m.type === 'devices') refresh(); // a device connected, caught up or dropped: redraw the dots (#295)
      else if (m.type === 'settings') {
        // Snooze times or formatting changed on another device: the cards are stale.
        // Snooze edits not saved here yet are kept; saving them replaces the other change (#182).
        const st = m.settings || {};
        if (st.sound) { serverSound = st.sound; if (soundSel && !localSound()) soundSel.value = st.sound; } // set here, on another device or by an agent (#297)
        const stale = () => snzDirty() ? ($('[data-snooze-status]').textContent = 'Settings changed on another device. Saving here replaces that change.') : reset();
        if (st.snooze_default && [st.snooze_default, ...(st.snooze_intervals || [])].join(',') !== shell.dataset.snooze) stale();
        else if ('keep_cleared_days' in st && (`${st.keep_cleared_days}` !== shell.dataset.keep || Object.entries(st.topic_keep_cleared_days || {}).sort().map(([k, v]) => `${k}=${v};`).join('') !== shell.dataset.tkeep)) reset(); // a stale form would save the old period back
        else if ('render_formatting' in st &&(`${st.render_formatting},${st.autolink},${st.favicons},${st.link_previews}` !== shell.dataset.fmt || Object.entries(st.topic_previews || {}).sort().map(([k, v]) => `${k}=${v};`).join('') !== shell.dataset.tprev)) stale();
      }
      else if (m.notification) {
        // Live news only: the replay before ready is catch-up. A scheduled send rings when it wakes, not when queued.
        if (ready && m.notification.state === 'active' && (m.type === 'created' || m.type === 'repeat' || m.via === 'snooze_end')) alertSound(m.notification);
        onEvent(m);
      }
    };
    ws.onclose = e => {
      // Unlinked or revoked from another device: show the sign-in page now (#246).
      if (e.reason === 'token revoked') return location.reload();
      // A refused reconnect gives no reason, so ask: signed out means reload too.
      fetch('/api/v1/topics', { credentials: 'same-origin' }).then(r => r.status === 401 && location.reload(), () => {});
      live(false);
      sync.classList.add('is-down');
      setTimeout(connect, Math.min(30000, 1000 * 2 ** tries++));
    };
  }
  live(false);
  connect();
})();
