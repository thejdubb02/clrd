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

  // The card or closed stack the keyboard works on (J and K). Kept by id, or
  // by topic for a stack: a refresh swaps the nodes.
  const nav = () => list ? [...list.querySelectorAll('.card[data-id]:not(.is-leaving):not([hidden]), [data-stack]:not(.is-leaving)')] : [];
  const key = c => c.dataset.id || 'stack:' + c.dataset.stack;
  let selId = null;
  const selected = () => (selId && nav().find(c => key(c) === selId)) || null;
  function select(c, focus = true) {
    list && list.querySelectorAll('.is-selected').forEach(x => x.classList.remove('is-selected'));
    selId = c ? key(c) : null;
    if (!c) return;
    c.classList.add('is-selected');
    if (focus) { c.focus({ preventScroll: true }); c.scrollIntoView({ block: 'nearest' }); }
  }
  const move = by => {
    const all = nav(), i = all.indexOf(selected());
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
  // The ids of a Clear all waiting for its reply. Until it comes, live events
  // and refreshes leave the screen alone, so the stacks fade as one (#387).
  const clearing = new Set();
  let stacking = false;
  try { stacking = localStorage.getItem('clrd.stack') === '1'; } catch {}
  const stackOn = () => stacking && !new URLSearchParams(location.search).has('topic');
  const byTopic = cs => { const m = new Map(); for (const c of cs) m.set(c.dataset.topic, [...(m.get(c.dataset.topic) || []), c]); return m; };
  // Where the cards go with stacking on: each topic's cards together, at its newest one's place.
  const stackOrder = cs => stackOn() ? [...byTopic(cs).values()].flat() : cs;
  // Focus inside a stack, as the topic and which button, so a rebuild can put it back.
  const stackFocus = () => {
    const el = document.activeElement, s = el && el.closest && el.closest('[data-stack]:not(.is-leaving)');
    return s && { t: s.dataset.stack, sel: el === s ? null : el.matches('[data-act]') ? '[data-act]' : '[data-stack-toggle]' };
  };
  // A stack's count, newest title and priority dot, from its topic's cards g.
  const fill = (s, g) => {
    s.querySelector('[data-stack-count]').textContent = `${g.length} notifications`;
    s.querySelector('[data-stack-latest]').textContent = g[0].querySelector('.card-title').textContent.trim();
    const top = Math.max(...g.map(c => Number(c.dataset.priority)));
    const dot = s.querySelector('[data-stack-dot]');
    dot.classList.remove('bg-destructive', 'bg-foreground', 'bg-border');
    dot.classList.add(top >= 5 ? 'bg-destructive' : top === 4 ? 'bg-foreground' : 'bg-border');
    const side = [...document.querySelectorAll('[data-topic-icon]')].find(a => a.dataset.name === s.dataset.stack);
    const icon = (side && side.dataset.topicIcon) || g[0].querySelector('img[data-icon]')?.getAttribute('src');
    const img = s.querySelector('img[data-icon]');
    if (icon) Object.assign(img, { src: icon, hidden: false }); else img.hidden = true;
  };
  function restack(focus = stackFocus()) {
    if (!list || !stackTpl) return;
    list.querySelectorAll('[data-stack]:not(.is-leaving)').forEach(s => s.remove());
    const groups = byTopic(cards());
    cards().forEach(c => { c.hidden = false; });
    if (!stackOn()) return;
    for (const [t, g] of groups) {
      if (g.length < 2) {
        // A selected stack down to one card hands the selection to that card.
        if (selId === 'stack:' + t) { select(g[0], focus && focus.t === t); }
        continue;
      }
      const s = stackTpl.content.firstElementChild.cloneNode(true), open = unfolded.has(t);
      s.dataset.stack = t;
      s.querySelector('[data-stack-topic]').textContent = t;
      fill(s, g);
      const tg = s.querySelector('[data-stack-toggle]');
      tg.setAttribute('aria-expanded', open);
      tg.textContent = open ? 'Hide' : 'Show all';
      g[0].before(s);
      // The topic's cards sit under its stack. Move only what is out of place:
      // moving a node drops the focus inside it.
      g.reduce((prev, c) => { if (prev.nextElementSibling !== c) prev.after(c); return c; }, s);
      g.forEach(c => { c.hidden = !open; });
      if (selId === key(s)) s.classList.add('is-selected');
      if (focus && focus.t === t) (focus.sel ? s.querySelector(focus.sel) : s).focus();
    }
  }
  restack();
  // Tap a stack to open or close it in place.
  list && list.addEventListener('click', e => {
    const s = e.target.closest('[data-stack]');
    if (!s || e.target.closest('[data-act]') || s.classList.contains('is-leaving')) return;
    const t = s.dataset.stack;
    selId = key(s); // the rebuilt stack keeps the selection
    if (!unfolded.delete(t)) unfolded.add(t);
    restack({ t, sel: '[data-stack-toggle]' });
  });
  // The switch in Settings, and the same switch flipped in another tab.
  const stackSw = $('[data-stack-setting]');
  if (stackSw) {
    stackSw.checked = stacking;
    stackSw.addEventListener('change', () => { try { localStorage.setItem('clrd.stack', stackSw.checked ? '1' : '0'); } catch {} });
  }
  // Appearance (#583): theme.js applies it; this browser keeps the choice.
  const themeSel = $('[data-theme-setting]');
  if (themeSel) {
    try { themeSel.value = localStorage.getItem('clrd.theme') || ''; } catch {}
    themeSel.addEventListener('change', () => {
      try { themeSel.value ? localStorage.setItem('clrd.theme', themeSel.value) : localStorage.removeItem('clrd.theme'); } catch {}
      window.clrdTheme?.();
    });
    addEventListener('storage', e => { if (e.key === 'clrd.theme') themeSel.value = e.newValue || ''; });
  }
  addEventListener('storage', e => { if (e.key === 'clrd.stack') { stacking = e.newValue === '1'; restack(); tidy(); } });

  // Settings search (#575): match the typed text in a setting's label, its
  // description or its keywords; hide the rest. Enter goes to the first match.
  const sq = $('[data-settings-q]');
  if (sq) {
    const sets = [...document.querySelectorAll('[data-setting]')].map(s => [s, `${s.querySelector('h3').textContent} ${[...s.querySelectorAll(':scope > p')].map(p => p.textContent).join(' ')} ${s.dataset.keywords}`.toLowerCase()]);
    const none = $('[data-settings-none]'), said = $('[data-settings-n]');
    const shown = s => s.style.display !== 'none' && !s.hidden; // hidden: not on this device
    const filter = () => {
      const t = sq.value.trim().toLowerCase();
      for (const [s, text] of sets) s.style.display = text.includes(t) ? '' : 'none';
      for (const g of document.querySelectorAll('[data-settings-group]')) g.style.display = !t || sets.some(([s]) => g.contains(s) && shown(s)) ? '' : 'none';
      const n = sets.filter(([s]) => shown(s)).length;
      none.hidden = !t || n > 0;
      $('[data-settings-term]').textContent = sq.value.trim();
      said.textContent = !t ? '' : n ? `${n} ${n === 1 ? 'setting matches' : 'settings match'}. Press Enter to go to the first.` : 'No settings match';
    };
    sq.addEventListener('input', filter);
    // A setting another script reveals later (pair.js: Link a phone) joins a search already typed (#591).
    const seen = new MutationObserver(filter);
    for (const [s] of sets) seen.observe(s, { attributeFilter: ['hidden'] });
    sq.addEventListener('keydown', e => {
      if (e.key !== 'Enter' || e.isComposing) return;
      e.preventDefault();
      const hit = sets.find(([s]) => shown(s))?.[0];
      if (!hit) return;
      history.replaceState(null, '', '#' + hit.id);
      hit.tabIndex = -1;
      hit.focus();
    });
    $('[data-settings-clear]').addEventListener('click', () => { sq.value = ''; filter(); sq.focus(); });
    addEventListener('hashchange', () => { if (sq.value) { sq.value = ''; filter(); } }); // a link to a setting the search hid
  }

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

  // Web Push opt-in (#164, SPEC 9): nothing asks for permission until the
  // button is tapped. Server and browser must agree; when one side lost the
  // subscription, or the site is blocked, both are tidied to off.
  const pushBtn = $('[data-push]');
  if (pushBtn) {
    const hint = $('[data-push-hint]');
    const scope = `${B}/static/`;
    const blockedPush = 'Notifications are blocked for this site in the browser settings.';
    let key = '';
    const show = (on, why) => { pushBtn.dataset.on = on ? '1' : ''; pushBtn.textContent = on ? 'Turn off notifications' : 'Turn on notifications'; hint.textContent = why; };
    const local = async () => (await navigator.serviceWorker.getRegistration(scope))?.pushManager.getSubscription();
    const save = s => fetch('/api/v1/push', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(s) });
    // True once the server stopped pushing; the browser's own copy is best effort.
    const off = async () => { await (await local().catch(() => null))?.unsubscribe().catch(() => {}); return !!(await fetch('/api/v1/push', { method: 'DELETE' }).catch(() => null))?.ok; };
    if (!('PushManager' in window) || !navigator.serviceWorker) show(false, 'This browser cannot show notifications from clrd.');
    else fetch('/api/v1/push').then(r => r.ok ? r.json() : Promise.reject()).then(async g => {
      key = g.public_key;
      const s = await local().catch(() => null);
      if (Notification.permission === 'denied') { if (g.subscribed || s) await off(); return show(false, blockedPush); }
      if (g.subscribed && s && Notification.permission === 'granted') { await save(s).catch(() => {}); show(true, 'On for this device.'); } // the server keeps the browser's current endpoint
      else { if (g.subscribed || s) await off(); show(false, ''); }
      pushBtn.disabled = false;
    }).catch(() => show(false, 'Could not reach the server. Reload to try again.'));
    pushBtn.addEventListener('click', async () => {
      pushBtn.disabled = true;
      if (pushBtn.dataset.on) { (await off()) ? show(false, 'Off for this device.') : show(true, 'Could not turn notifications off. Try again.'); pushBtn.disabled = false; return; }
      const p = await Notification.requestPermission(); // first, while the tap still counts
      if (p !== 'granted') { pushBtn.disabled = p === 'denied'; return show(false, p === 'denied' ? blockedPush : 'Not turned on: the browser was not allowed to show notifications.'); }
      try {
        const reg = await navigator.serviceWorker.register(`${scope}sw.js`);
        const w = reg.installing || reg.waiting || reg.active; // ready never settles: this page is outside the worker's scope
        if (w.state !== 'activated') await new Promise((ok, no) => w.addEventListener('statechange', () => w.state === 'activated' ? ok() : w.state === 'redundant' && no()));
        const s = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: Uint8Array.from(atob(key.replace(/-/g, '+').replace(/_/g, '/')), c => c.charCodeAt(0)) });
        if (!(await save(s)).ok) { await s.unsubscribe(); throw new Error(); }
        show(true, 'On. New notifications show on this device.');
      } catch { show(false, 'Could not turn notifications on. Try again.'); }
      pushBtn.disabled = false;
    });
  }

  // A card that no longer belongs in this view: the cleared moment (clrd.css).
  // Green washes across it with a check and the reason, then it folds away.
  // Timers, not animation events: with reduced motion there are none.
  const still = matchMedia('(prefers-reduced-motion: reduce)');
  const check = '<svg class="size-4 shrink-0" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>';
  function leave(c, why, wait = 0) {
    if (!c || c.classList.contains('is-leaving')) return;
    if (c.hidden) {
      // Folded into a closed stack: nothing to watch leave, so the stack's
      // count and newest title change now, not after the exit timer (#227).
      // While a Clear all waits for its reply, a full restack would bring back
      // the stacks it is clearing, so only this card's own stack changes, and
      // only when that clear holds none of its cards (#397).
      announce.textContent = `${c.querySelector('.card-title').textContent.trim()}: ${why}`;
      c.remove();
      if (!clearing.size) restack();
      else {
        const t = c.dataset.topic, g = cards().filter(x => x.dataset.topic === t);
        const s = list.querySelector(`[data-stack="${CSS.escape(t)}"]:not(.is-leaving)`);
        if (s && !g.some(x => clearing.has(x.dataset.id))) {
          if (g.length > 1) fill(s, g);
          else { // down to one card: it stands on its own, as restack would leave it
            const sel = selId === key(s), focused = s.contains(document.activeElement);
            g.forEach(x => { x.hidden = false; }); // shown first, so it can take the focus
            s.remove();
            if (sel) select(g[0] || null, focused);
          }
        }
      }
      return tidy();
    }
    if (key(c) === selId) { const all = nav(), i = all.indexOf(c); select(all[i + 1] || all[i - 1] || null, c.contains(document.activeElement)); }
    const p = document.createElement('p');
    p.className = 'gone';
    p.innerHTML = check;
    p.append(why);
    c.append(p);
    c.classList.add('is-leaving');
    c.querySelectorAll('button').forEach(b => { b.disabled = true; });
    announce.textContent = `${c.querySelector('.card-title').textContent.trim()}: ${why}`;
    // It slides the way it was swiped, or toward the inline end (SPEC 10.1).
    const rtl = getComputedStyle(c).direction === 'rtl';
    const x = c.x || c.out; delete c.out;
    c.style.setProperty('--out', x ? (x > 0) === rtl ? 1 : -1 : rtl ? -1 : 1);
    const gone = () => { c.remove(); tidy(); };
    setTimeout(() => {
      if (still.matches) return gone();
      c.classList.add('is-sliding');
      setTimeout(() => {
        c.classList.add('is-folding');
        c.animate([{ height: `${c.offsetHeight}px` }, { height: '0px', marginBottom: '-0.5rem', borderWidth: 0 }],
          { duration: 200, easing: 'cubic-bezier(.4, 0, .2, 1)', fill: 'forwards' });
        setTimeout(gone, 200);
      }, 200);
    }, 1400 + wait);
  }

  const reason = n => {
    const by = n.state_by ? (n.state === 'resolved' ? ' by ' : ' on ') + n.state_by : '';
    return ({ dismissed: 'Cleared', resolved: 'Resolved', snoozed: 'Snoozed', acknowledged: 'Acknowledged', active: 'Back in the inbox' }[n.state] || 'Moved') + by;
  };

  // Re-fetch this page and patch it in: cards keyed by id and seq, plus the sidebar.
  let pending, gen = 0, arrived = [];
  const unarrive = c => c.classList.remove('is-arriving', 'is-newest');
  // A change makes any page already on its way stale: drop it, or it puts back
  // what the change just took off the screen (#402).
  const refresh = () => { gen++; clearTimeout(pending); pending = setTimeout(patch, 250); };
  async function patch() {
    if (clearing.size || (list && list.querySelector('.is-leaving'))) return refresh();
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
      // A Clear all that started while this page was loading: its reply comes
      // first, then a fresh load (#397).
      if (clearing.size || (list && list.querySelector('.is-leaving'))) return refresh();
    } catch {
      if (mine !== gen) return; // a newer refresh is already on its way
      // seq has already moved past this change, so the WebSocket will not
      // replay it: keep trying until the page loads.
      clearTimeout(pending);
      pending = setTimeout(patch, 5000);
      return;
    }
    document.title = doc.title; // the open count in the tab
    document.querySelectorAll('[data-live]').forEach(cur => {
      const next = doc.querySelector(`[data-live="${cur.dataset.live}"]`);
      if (next && !cur.contains(document.activeElement) && !next.isEqualNode(cur)) cur.replaceWith(document.adoptNode(next)); // same colors: no reload flash
    });
    const nextList = doc.querySelector('[data-list]');
    if (!list || !nextList) return tidy();
    const sf = stackFocus();
    let typing = null;
    const keep = stackOrder([...nextList.children].map(n => {
      const old = card(n.dataset.id);
      if (old && old.dataset.seq === n.dataset.seq) { // a color rule change recolors in place, so an open menu and its draft stay (#696)
        const look = el => [...el.classList].filter(c => /^c[cs]-/.test(c));
        old.classList.remove(...look(old)); old.classList.add(...look(n)); old.dataset.look = n.dataset.look;
        old.querySelector('[data-unsub]')?.toggleAttribute('data-approvals', !!n.querySelector('[data-unsub][data-approvals]')); // a topic flagged since (#700)
        return old;
      }
      const fresh = document.adoptNode(n);
      // A live change to a still-open question keeps a half-typed answer (#153).
      const was = old && old.querySelector('[data-text-reply] [name=text]'), box = fresh.querySelector('[data-text-reply] [name=text]');
      if (was && box && was.value) box.value = was.value;
      const k = !was && box ? kept.findIndex(([id]) => id === n.dataset.id) : -1; // a draft kept from a reload, its card back live (#687)
      if (k >= 0) box.value = kept.splice(k, 1)[0][1];
      if (was && box && was === document.activeElement) typing = box;
      localize(fresh);
      previews(fresh);
      return fresh;
    }));
    const now = [...list.children].filter(c => !c.matches('[data-stack]'));
    if (keep.some(c => c.contains(document.activeElement)) || keep.length !== now.length || keep.some((c, i) => c !== now[i])) {
      // Moving a card restarts its animation, so last patch's arrivals stop here.
      list.querySelectorAll('.is-arriving').forEach(unarrive);
      const had = list.contains(document.activeElement) && !document.activeElement.closest('[data-stack]') && document.activeElement;
      list.replaceChildren(...keep);
      if (typing) typing.focus();
      else if (had && had.isConnected) had.focus({ preventScroll: true }); // moving a node drops its focus mid-typing (#517)
      else if (had) select(selected()); // focus was on a node the refresh replaced
    }
    restack(sf);
    // Only cards still in sight: not folded into a stack, not off screen.
    const inn = document.visibilityState === 'visible' ? arrived.map(card).filter(c => {
      const r = c && !c.hidden && c.getBoundingClientRect();
      return r && r.bottom > 0 && r.top < innerHeight;
    }) : [];
    arrived = [];
    inn.forEach(c => c.classList.add('is-arriving'));
    inn.at(-1)?.classList.add('is-newest'); // a burst wiggles only the newest
    // Done once its animations have played, so reopening a stack later never
    // plays them again. Not a timer: a busy page can draw no frame in 500ms (#714).
    const anims = c => c.getAnimations().filter(a => a.animationName);
    inn.forEach(c => { const run = anims(c); Promise.allSettled(run.map(a => a.finished)).then(() => anims(c).every(a => run.includes(a)) && unarrive(c)); });
    const s = selected();
    if (s) select(s, false); else selId = null;
    tidy();
  }

  function onEvent(m) {
    const n = m.notification;
    if (m.seq > seq) seq = m.seq;
    const c = card(n.id);
    if (c && m.seq <= Number(c.dataset.seq)) return;
    if (c && !show.includes(n.state) && !clearing.has(n.id)) leave(c, reason(n));
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
        if (typeof undoIds === 'function') return undoIds();
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
    // One menu open per card: Mark would cover Snooze.
    const d = e.target;
    if (d.open && d.matches && d.matches('article details.popover')) d.closest('article').querySelectorAll('details.popover[open]').forEach(o => { if (o !== d) o.open = false; });
  }, true);

  // Enter in the date box would submit the card form with its first button
  // (Ack). Enter in a typed answer sends it; Shift+Enter makes a new line.
  document.addEventListener('keydown', e => {
    if (e.key === 'Enter' && e.target.matches && e.target.matches('[data-until]')) {
      e.preventDefault();
      e.target.closest('.snooze-custom').querySelector('[data-act]').click();
    } else if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.target.matches && e.target.matches('[data-text-reply] textarea')) {
      e.preventDefault();
      e.target.form.querySelector('[data-act]').click();
    }
  });

  // Long press (or right click) on Snooze opens the other times instead of
  // snoozing for the default; the click that ends a long press is dropped.
  let hold = 0, held = null, down = null;
  const more = b => { held = b; b.nextElementSibling.open = true; };
  document.addEventListener('pointerdown', e => {
    const b = e.button === 0 && e.target.closest('[data-snooze-main]');
    held = null;
    down = b && { b, at: e.timeStamp };
    if (b) hold = setTimeout(() => more(b), 500);
  });
  // A busy page can handle the release before the timer, so the release's own
  // time decides too: held 500 ms or more is a long press, never a tap (#476).
  document.addEventListener('pointerup', e => { if (down && !held && e.timeStamp - down.at >= 500) more(down.b); }, true);
  ['pointerup', 'pointercancel', 'pointerleave'].forEach(t => document.addEventListener(t, () => { clearTimeout(hold); down = null; }, true));
  document.addEventListener('contextmenu', e => {
    const b = e.target.closest('[data-snooze-main]');
    if (b) { e.preventDefault(); clearTimeout(hold); more(b); }
  });
  document.addEventListener('click', e => {
    if (held && e.target.closest('[data-snooze-main]') === held) { e.preventDefault(); e.stopImmediatePropagation(); }
    held = null;
  }, true);
  document.addEventListener('keydown', () => { held = null; }, true); // a touch long press may end with no click

  // Swipe on a touch screen (#161): a part swipe on a card slides it to show
  // the buttons Settings picked for that side, and nothing changes until one
  // is tapped. The slide stops at the buttons, so a full swipe never acts; a
  // mostly vertical drag scrolls as before. Mouse and keyboard keep the card's
  // own buttons, which the revealed ones press, so Clear keeps its Undo.
  const words = s => (s || '').split(' ').filter(Boolean);
  const sides = { start: words(shell.dataset.swipeStart), end: words(shell.dataset.swipeEnd) };
  const swipeFor = { clear: ['[data-act=dismiss]', 'Clear'], ack: ['[data-act=ack]', 'Ack'], snooze: ['[data-snooze-main]', 'Snooze'] }; // no Resolve control here, so it is skipped
  let opened = null, drag = null; // the card showing its buttons; the touch moving a card
  const slide = (card, x) => { // x > 0 slides toward the start of the line
    const rtl = getComputedStyle(card).direction === 'rtl';
    for (const c of card.children) if (c !== card.tray) c.style.transform = x ? `translateX(${rtl ? x : -x}px)` : '';
    card.x = x;
    if (card.tray) card.tray.style.width = `${Math.abs(x)}px`;
  };
  const shut = card => {
    if (!card) return;
    slide(card, 0);
    card.tray?.remove();
    card.tray = null;
    if (opened === card) opened = null;
  };
  // The buttons for one side, the first nearest the edge; null when none fit this card.
  const tray = (card, side) => {
    const bs = sides[side].map(a => [a, swipeFor[a] && (a !== 'snooze' || shell.dataset.view === 'inbox') && card.querySelector(swipeFor[a][0])])
      .filter(([, t]) => t).map(([a, t]) => {
        const b = document.createElement('button');
        Object.assign(b, { type: 'button', className: 'btn shrink-0', textContent: swipeFor[a][1] });
        b.dataset.variant = a === 'clear' ? 'clear' : 'outline';
        b.addEventListener('click', () => { const x = card.x; shut(card); card.out = x; t.click(); }); // leave() slides it the way it was swiped
        return b;
      });
    if (!bs.length) return null;
    if (side === 'start') bs.reverse(); // that tray sits at the end of the line
    const t = document.createElement('div');
    t.dataset.swipeTray = side;
    Object.assign(t.style, { position: 'absolute', insetBlock: '0', [side === 'start' ? 'insetInlineEnd' : 'insetInlineStart']: '0', display: 'flex', alignItems: 'center',
      justifyContent: side === 'start' ? 'flex-end' : 'flex-start', gap: '0.5rem', paddingInline: '0.75rem', overflow: 'hidden', width: 'max-content' });
    t.append(...bs);
    card.style.position = 'relative';
    card.prepend(t);
    card.tray = t;
    card.trayW = t.offsetWidth;
    return t;
  };
  if (list && (sides.start.length || sides.end.length)) list.style.touchAction = 'pan-y pinch-zoom';
  document.addEventListener('pointerdown', e => {
    if (drag?.on) return; // a second finger leaves the first one's swipe alone
    const card = e.pointerType === 'touch' && list?.contains(e.target) && e.target.closest('article[data-id]');
    if (opened && opened !== card) shut(opened); // a touch or click anywhere else closes it
    drag = card && !e.target.closest('input, textarea, select, [data-swipe-tray]') ? { card, id: e.pointerId, x: e.clientX, y: e.clientY, from: card.x || 0, on: false } : null;
  });
  document.addEventListener('pointermove', e => {
    if (!drag || e.pointerId !== drag.id) return;
    const { card } = drag;
    const rtl = getComputedStyle(card).direction === 'rtl';
    const dx = (e.clientX - drag.x) * (rtl ? 1 : -1), dy = e.clientY - drag.y; // dx > 0: toward the start
    if (!drag.on) {
      if (Math.abs(dx) < 10 && Math.abs(dy) < 10) return;
      if (!(card.tray || tray(card, dx > 0 ? 'start' : 'end'))) { drag = null; return; } // nothing on that side (touch-action hands a vertical drag to the scroll, which cancels the pointer)
      drag.on = true;
      clearTimeout(hold); down = null; // not a long press on Snooze
      card.setPointerCapture(e.pointerId);
    }
    const w = card.trayW, x = drag.from + dx;
    slide(card, card.tray.dataset.swipeTray === 'start' ? Math.min(Math.max(x, 0), w) : Math.max(Math.min(x, 0), -w));
  });
  const release = e => {
    if (!drag || e.pointerId !== drag.id) return;
    const { card, on, from } = drag;
    drag = null;
    if (!on) return;
    if (e.type === 'pointercancel') from ? slide(card, from) : shut(card); // the browser took the gesture: back to how it was
    else if (Math.abs(card.x) > card.trayW / 2) { slide(card, Math.sign(card.x) * card.trayW); opened = card; } else shut(card);
  };
  document.addEventListener('pointerup', release);
  document.addEventListener('pointercancel', release);

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
  // Topic subscriptions (SPEC 2.3). A change reloads the page, so Undo
  // rides in sessionStorage, written before the PUT so a reply that lands
  // mid-reload still offers it. The new page checks the change landed (#649)
  // and is still the owner's last one, tagged with this tab's request id (#702).
  const subsPut = body => fetch('/api/v1/subscriptions', { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null);
  const subsWhy = async r => r ? ((await r.json().catch(() => ({}))).error || `Not saved (${r.status}).`) : 'Not saved: no connection.';
  const approvalsOff = t => confirm(`Agents send their approval requests to ${t}. Unsubscribe, and those requests stop reaching you. Unsubscribe anyway?`);
  const store = (k, v) => { try { v == null ? sessionStorage.removeItem(k) : sessionStorage.setItem(k, v); } catch { /* private window: no Undo after the reload */ } };
  let unsubDone = null;
  try { unsubDone = JSON.parse(sessionStorage.getItem('clrd-unsub')); } catch { /* as above */ }
  if (Array.isArray(unsubDone)) {
    store('clrd-unsub', null);
    const [t, id] = unsubDone;
    fetch('/api/v1/subscriptions', { credentials: 'same-origin' }).then(r => r.json()).then(s => {
      // the PUT never landed, or another device changed the set since
      if (s.request_id !== id || !(s.topics || []).some(x => x.topic === t && !x.subscribed)) return;
      say(`Unsubscribed from ${t}`, async () => {
        const r = await subsPut({ topics: { [t]: true } });
        if (r && r.ok) return location.reload();
        say(await subsWhy(r));
      });
    }, () => {});
  }
  // A reload (a subscriptions change on another device, a reset) keeps a
  // half-typed answer, as a live update does (#153, #649). A draft whose card
  // is not on this page (its topic unsubscribed) is kept for the tab's life (#687).
  const reply = id => card(id)?.querySelector('[data-text-reply] [name=text]');
  let kept = [];
  try { kept = (JSON.parse(sessionStorage.getItem('clrd-drafts')) || []).filter(([id, v]) => { const b = reply(id); if (b) b.value = v; return !b; }); } catch { /* as above */ }
  addEventListener('pagehide', () => {
    const d = [...kept.filter(([id]) => !reply(id)), ...[...document.querySelectorAll('[data-text-reply] [name=text]')].filter(b => b.value).map(b => [b.closest('[data-id]').dataset.id, b.value])];
    store('clrd-drafts', d.length ? JSON.stringify(d) : null);
  });
  document.addEventListener('click', async e => {
    const b = e.target.closest('[data-unsub], [data-subscribe]');
    if (!b) return;
    e.preventDefault();
    const t = b.dataset.unsub ?? b.dataset.subscribe, on = 'subscribe' in b.dataset;
    if (!on && document.querySelector(`[data-unsub="${CSS.escape(t)}"][data-approvals]`) && !approvalsOff(t)) return;
    const id = Date.now().toString(36) + Math.random().toString(36).slice(2);
    if (!on) store('clrd-unsub', JSON.stringify([t, id]));
    const r = await subsPut({ topics: { [t]: on }, request_id: id });
    if (r && r.ok) return location.reload();
    store('clrd-unsub', null);
    if (r && r.status === 401) return location.reload();
    say(await subsWhy(r));
  });
  const subsForm = $('[data-subs-form]');
  if (subsForm) {
    $('[data-subs-save]').hidden = true; // each switch saves as it changes
    subsForm.addEventListener('change', async e => {
      const c = e.target;
      if (c.type !== 'checkbox') return;
      if (c.name === 'on' && !c.checked && 'approvals' in c.dataset && !approvalsOff(c.value)) { c.checked = true; return; }
      const body = c.name === 'new_topics' ? { new_topics: c.checked } : { topics: { [c.value]: c.checked } };
      c.disabled = true;
      const r = await subsPut(body);
      if (r && r.ok) return location.reload();
      c.disabled = false;
      c.checked = !c.checked;
      $('[data-subs-hint]').textContent = await subsWhy(r);
    });
    $('[data-subs-q]')?.addEventListener('input', e => {
      const q = e.target.value.trim().toLowerCase();
      subsForm.querySelectorAll('tr[data-sub]').forEach(tr => { tr.hidden = !tr.dataset.sub.toLowerCase().includes(q); });
    });
  }

  // A saved setting reloads the page, so while one save runs the snooze and
  // display forms are locked: an edit made then would be lost (#214), and an
  // older save cannot land after a newer one.
  const disp = $('[data-display-settings]');
  const keep = $('[data-keep-settings]');
  const sil = $('[data-silence-form]');
  const tkeep = $('[data-topic-keep]');
  const swp = $('[data-swipe-settings]');
  const forms = [snz, disp, keep, sil, tkeep, swp];
  const lock = on => forms.forEach(f => f && [...f.elements].forEach(c => { c.disabled = on; }));
  // Settings: swipe buttons (#161), three places per side. A sign-in that may
  // not change settings gets 403, and the form then stays read-only.
  swp?.querySelectorAll('[data-side]').forEach(d => d.replaceChildren(...[0, 1, 2].map(i => {
    const s = document.createElement('select');
    Object.assign(s, { className: 'input min-w-0 flex-1', name: d.dataset.side });
    s.setAttribute('aria-label', `${d.parentElement.firstChild.textContent}, button ${i + 1}`);
    s.append(...[['', 'None'], ['clear', 'Clear'], ['snooze', 'Snooze'], ['ack', 'Ack'], ['resolve', 'Resolve']].map(([v, t]) => new Option(t, v, false, (sides[d.dataset.side][i] || '') === v)));
    return s;
  })));
  swp && swp.addEventListener('submit', async e => {
    e.preventDefault();
    const pick = side => [...swp.querySelectorAll(`select[name=${side}]`)].map(s => s.value).filter(Boolean);
    const status = $('[data-swipe-status]');
    lock(true);
    const r = await fetch('/api/v1/settings', { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ swipe_start: pick('start'), swipe_end: pick('end') }) }).catch(() => null);
    if (r && r.ok) return location.reload();
    if (r && r.status === 403) forms.pop(); // swp is last: lock(false) leaves it disabled
    lock(false);
    status.textContent = r ? ((await r.json().catch(() => ({}))).error || `Not saved (${r.status}).`) : 'Not saved: no connection.';
  });
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
    const c = await fetch(`/api/v1/counts?keep_cleared_days=${days ?? 'default'}&topic=${encodeURIComponent(t)}`, { credentials: 'same-origin' }).then(r => r.ok ? r.json() : null).catch(() => null);
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
    // Sent as typed, not re-encoded: JSON.parse rounds whole numbers over 2^53, and a real post keeps them.
    const payload = box.querySelector('[data-template-payload]').value.trim();
    try { JSON.parse(payload); } catch { return void (status.textContent = 'The sample is not valid JSON.'); }
    const headers = {};
    for (const line of box.querySelector('[data-template-headers]').value.split('\n')) {
      const i = line.indexOf(':');
      if (i > 0) headers[line.slice(0, i).trim()] = line.slice(i + 1).trim();
    }
    const template = {};
    for (const k of ['each', 'title', 'message', 'priority', 'tags', 'click', 'id', 'resolve']) if (v(k).trim()) template[k] = v(k);
    status.textContent = 'Running...';
    const run = box.dataset.run = String(+(box.dataset.run || 0) + 1); // only the latest click shows
    const r = await fetch(`/api/v1/topics/${encodeURIComponent(topic)}/templates/${encodeURIComponent(name)}/preview`,
      { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: `{"payload":${payload},"headers":${JSON.stringify(headers)},"template":${JSON.stringify(template)}}` }).catch(() => null);
    const j = r ? await r.json().catch(() => ({})) : {};
    if (box.dataset.run !== run) return;
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
  // Settings, add an agent (#456): copy the invite string pair.js shows.
  document.addEventListener('click', async e => {
    if (!e.target.closest('[data-agent-copy]')) return;
    try { await copyText(document.querySelector('[data-agent-invite]').textContent); say('Copied.'); } catch { say('Could not copy.'); }
  });
  // Settings, webhook body template (#495): show what a delivery would send.
  // The server renders it against a sample event and sends nothing (#241).
  document.querySelectorAll('[data-hook-preview]').forEach(d => { d.hidden = false; });
  document.addEventListener('click', async e => {
    const b = e.target.closest('[data-hook-preview] button');
    if (!b) return;
    const f = b.closest('form'), out = f.querySelector('[data-hook-preview-out]'), topic = f.elements.topic.value.trim();
    // Only the latest press may write; the region is shown before it is filled
    // so a screen reader hears the first result too.
    const n = f.previewN = (f.previewN || 0) + 1;
    const show = (text, bad) => {
      if (n !== f.previewN) return;
      out.hidden = false;
      requestAnimationFrame(() => { out.textContent = text; out.classList.toggle('text-destructive', bad); });
    };
    if (!topic) return show('Pick a topic first.', true);
    if (!f.elements.body.value.trim()) return show('Empty: each delivery sends the event as it is.', false);
    const r = await fetch(`/api/v1/topics/${encodeURIComponent(topic)}/hooks/preview`, {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ body: f.elements.body.value.trim() }),
    }).catch(() => null);
    if (r && r.status === 401) return location.reload(); // signed out: show the sign-in page
    const j = r ? await r.json().catch(() => ({})) : {};
    if (r && r.ok) show(j.body, false);
    else show(r ? j.error || `No preview (${r.status}).` : 'No preview: no connection.', true);
  });

  // Empty-inbox tip card (#244): previous and next, a slow rotation that pauses
  // on hover or focus, and none at all under reduced motion.
  const tipBox = $('[data-tips]');
  if (tipBox) {
    const ts = [...tipBox.querySelectorAll('[data-tip]')];
    let at = ts.findIndex(t => !t.hidden), stopped = false, played = false; // played: Play pressed, so hover and focus no longer hold it
    const show = (i, said) => {
      ts[at].hidden = true; at = (i + ts.length) % ts.length; ts[at].hidden = false;
      $('[data-tip-count]').textContent = `${at + 1} of ${ts.length}`;
      if (said) announce.textContent = ts[at].getAttribute('aria-label');
    };
    // Pause and Play (WCAG 2.2.2), for readers who never hover or focus the
    // card. No button under reduced motion: nothing rotates there.
    const pause = $('[data-tip-pause]');
    const sync = () => {
      pause.hidden = still.matches;
      pause.setAttribute('aria-label', stopped ? 'Play tips' : 'Pause tips');
      pause.querySelector('[data-tip-playing]').hidden = stopped;
      pause.querySelector('[data-tip-paused]').hidden = !stopped;
    };
    sync();
    still.addEventListener('change', sync);
    tipBox.addEventListener('click', async e => {
      if (e.target.closest('[data-tip-pause]')) { stopped = !stopped; played = !stopped; return sync(); }
      const step = e.target.closest('[data-tip-step]');
      // Stepping by hand stops the rotation until Play (WCAG 2.2.2).
      if (step) { stopped = true; played = false; sync(); return show(at + +step.dataset.tipStep, true); }
      if (!e.target.closest('[data-tip-copy-btn]')) return;
      try { await copyText(ts[at].querySelector('[data-tip-copy]').textContent); say('Copied.'); } catch { say('Could not copy.'); }
    });
    setInterval(() => {
      if (stopped || still.matches || document.hidden || !tipBox.offsetParent || (!played && (tipBox.matches(':hover') || tipBox.contains(document.activeElement)))) return;
      show(at + 1, false);
    }, 9000); // long enough to read one tip (#292)
  }

  // Card marks (#451): seen or actioned, with an optional note and link, for
  // everyone on the topic. A mark changes no state; Remove takes yours off.
  async function markCard(b, c) {
    const f = b.form.elements, url = `/api/v1/notifications/${encodeURIComponent(c.dataset.id)}/mark`;
    b.disabled = true;
    const r = b.value ? await post(url, { mark: b.value, note: f.note.value.trim(), link: f.link.value.trim() }, true)
      : await fetch(url, { method: 'DELETE', credentials: 'same-origin' }).catch(() => say('Could not reach the server. Nothing changed.'));
    b.disabled = false;
    if (!r) return;
    if (r.status === 401) return location.reload();
    if (!r.ok) return say(`${b.value ? 'Not marked' : 'Not removed'}: ${(await r.json().catch(() => ({}))).error || r.status}.`);
    b.closest('details').open = false;
    tell(b.value ? `Marked ${b.value}.` : 'Your mark is removed.');
    refresh();
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
      ids.forEach(id => clearing.add(id)); // two clears at once share it
      const r = ids.length && await post('/api/v1/notifications/clear', { ids, via: 'web_bulk' });
      b.disabled = false;
      const done = () => ids.forEach(id => clearing.delete(id));
      if (!r) { done(); arrived = arrived.filter(id => !ids.includes(id)); return; } // they never left, so nothing slides in (#656)
      const n = (await r.json().catch(() => ({}))).cleared;
      // Clear all takes every stack with it, so none shrinks card by card.
      // Each one goes a little after the one above it (SPEC 10.1).
      const order = nav(), lag = el => Math.min(Math.max(order.indexOf(el), 0), 8) * 40;
      for (const st of s ? [s] : list.querySelectorAll('[data-stack]')) { unfolded.delete(st.dataset.stack); leave(st, 'Cleared', lag(st)); }
      ids.forEach(id => leave(card(id), 'Cleared', lag(card(id))));
      done();
      // Undo only when we know every id was ours to clear; otherwise it could
      // reopen something another device cleared first.
      say(n === undefined ? 'Cleared.' : `Cleared ${n} ${n === 1 ? 'notification' : 'notifications'}.`, n === ids.length ? ids : undefined);
      return refresh();
    }
    const c = b.closest('.card');
    if (act === 'share' || act === 'copy') return shareCard(act, c);
    if (act === 'mark') return markCard(b, c);
    let body;
    if (act === 'snooze') {
      const t = !b.dataset.for && snoozeAt(b.dataset.at, c);
      if (!b.dataset.for && !(t > new Date())) return say('Pick a time in the future.');
      body = b.dataset.for ? { for: b.dataset.for } : { until: t.toISOString() };
    }
    const typed = act === 'reply' && b.form.elements.text; // a typed answer (#153)
    if (typed && !typed.value.trim()) { typed.focus(); return say('Type an answer first.'); }
    if (act === 'reply') body = { reply: b.value, text: typed ? typed.value : undefined, seq: Number(c.dataset.seq) }; // 409 if it changed since this card
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
          // Too long or empty: the server says which; what was typed stays.
          bad_request: typed && n.error && `Not answered: ${n.error}`,
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
      // Chosen devices (#82): ticked ids, or none for every device.
      const picked = [...f.querySelectorAll('[name=devices]:checked')];
      const body = { topic: v('topic'), title: v('title') || undefined, message: v('message'), priority: Number(v('priority')),
        devices: picked.length ? picked.map(c => c.value) : undefined };
      const btn = e.submitter, mine = opened;
      if (btn) btn.disabled = true; // one tap, one notification
      status.textContent = 'Sending...';
      const r = await fetch('/api/v1/notifications', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null);
      if (btn) btn.disabled = false;
      if (r && r.status === 401) return location.reload();
      if (mine !== opened) return say(r && r.ok ? 'Sent.' : 'Not sent.');
      if (!r || !r.ok) {
        // The server names a refused device by its id; say its name instead.
        const err = r && ((await r.json().catch(() => ({}))).error || `Not sent (${r.status}).`);
        status.textContent = r ? picked.reduce((s, c) => s.replaceAll(`"${c.value}"`, `"${c.dataset.name}"`), err) : 'Not sent: no connection.';
        return;
      }
      f.elements.title.value = f.elements.message.value = '';
      f.querySelectorAll('[name=devices]').forEach(c => { c.checked = false; }); // the next send alerts every device again
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
    if (stackFocus() || c && c.matches('[data-stack]')) return tell('Open the stack to act on one notification.'); // not the card next to it
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
    const c = e.target.closest('.card[data-id], [data-stack]');
    if (c && c.isConnected && !c.classList.contains('is-leaving')) select(c, false); // a toggled stack was rebuilt
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
    if (k === 'enter' && t.matches && t.matches('[data-stack]')) { e.preventDefault(); return t.querySelector('[data-stack-toggle]').click(); }
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
        if (st.color_rules) refresh(); // recolor the cards (#165)
        if (st.sound) { serverSound = st.sound; if (soundSel && !localSound()) soundSel.value = st.sound; } // set here, on another device or by an agent (#297)
        const stale = () => snzDirty() ? ($('[data-snooze-status]').textContent = 'Settings changed on another device. Saving here replaces that change.') : reset();
        if (st.snooze_default && [st.snooze_default, ...(st.snooze_intervals || [])].join(',') !== shell.dataset.snooze) stale();
        else if ('keep_cleared_days' in st && (`${st.keep_cleared_days}` !== shell.dataset.keep || Object.entries(st.topic_keep_cleared_days || {}).sort().map(([k, v]) => `${k}=${v};`).join('') !== shell.dataset.tkeep)) reset(); // a stale form would save the old period back
        else if ('render_formatting' in st &&(`${st.render_formatting},${st.autolink},${st.favicons},${st.link_previews}` !== shell.dataset.fmt || Object.entries(st.topic_previews || {}).sort().map(([k, v]) => `${k}=${v};`).join('') !== shell.dataset.tprev)) stale();
        else if ('swipe_start' in st && [st.swipe_start, st.swipe_end].map(s => (s || []).map(a => `${a} `).join('')).join('|') !== `${shell.dataset.swipeStart}|${shell.dataset.swipeEnd}`) reset(); // the buttons and the form would be stale
      }
      else if (m.notification) {
        // Live news only: the replay before ready is catch-up. A scheduled send rings when it wakes, not when queued.
        if (ready && m.notification.state === 'active' && (m.type === 'created' || m.type === 'repeat' || m.via === 'snooze_end')) alertSound(m.notification);
        // A card that comes in live while this tab is in front slides in (SPEC 10.1).
        // Its old copy may still be leaving (a snooze that ended at once), or
        // be about to with a Clear all's reply: that one does not count (#611, #656).
        if (ready && document.visibilityState === 'visible' && (m.type === 'created' || m.type === 'repeat' || m.via === 'snooze_end')
          && show.includes(m.notification.state) && (clearing.has(m.notification.id) || !cards().some(c => c.dataset.id === m.notification.id))) arrived.push(m.notification.id);
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
