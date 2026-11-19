// clrd inbox: live updates on top of the server-rendered page. No framework.
// The page works without this file; it adds the buttons, the WebSocket and the toasts.
(() => {
  document.documentElement.classList.remove('no-js');
  const shell = document.querySelector('[data-inbox]');
  if (!shell) return;

  const show = { inbox: ['active', 'acknowledged'], snoozed: ['snoozed'], cleared: ['dismissed', 'resolved'] }[shell.dataset.view] || [];
  const $ = s => document.querySelector(s);
  const list = $('[data-list]'), empty = $('[data-empty]'), clearAll = $('[data-act=clear-all]');
  const toast = $('[data-toast]'), announce = $('[data-announce]'), conn = $('[data-conn]'), sync = $('#sync');
  let seq = Number(shell.dataset.seq) || 0;

  // Times are rendered in UTC on the server; show them in the reader's zone.
  const fmt = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  const localize = el => el.querySelectorAll('time[datetime]').forEach(t => { t.textContent = fmt.format(new Date(t.dateTime)); });
  localize(document);

  const card = id => list && [...list.children].find(c => c.dataset.id === id);
  const cards = () => list ? [...list.querySelectorAll('.card:not(.is-leaving)')] : [];
  const tidy = () => {
    if (!list) return;
    const none = cards().length === 0 && !list.querySelector('.is-leaving');
    if (empty) empty.hidden = !none;
    if (clearAll) clearAll.hidden = cards().length === 0;
  };

  // A card that no longer belongs in this view: green line and the reason, then it folds away.
  function leave(c, why) {
    if (!c || c.classList.contains('is-leaving')) return;
    const p = document.createElement('p');
    p.className = 'gone';
    p.textContent = why;
    c.append(p);
    c.classList.add('is-leaving');
    c.querySelectorAll('button').forEach(b => { b.disabled = true; });
    announce.textContent = `${c.querySelector('.card-title').textContent.trim()}: ${why}`;
    setTimeout(() => {
      c.classList.add('is-folding');
      setTimeout(() => { c.remove(); tidy(); }, 260);
    }, 1400);
  }

  const reason = n => {
    const by = n.state_by ? (n.state === 'resolved' ? ' by ' : ' on ') + n.state_by : '';
    return ({ dismissed: 'Cleared', resolved: 'Resolved', snoozed: 'Snoozed', acknowledged: 'Acknowledged', active: 'Back in the inbox' }[n.state] || 'Moved') + by;
  };

  // Re-fetch this page and patch it in: cards keyed by id and seq, plus the sidebar.
  let pending;
  const refresh = () => { clearTimeout(pending); pending = setTimeout(patch, 250); };
  async function patch() {
    if (list && list.querySelector('.is-leaving')) return refresh();
    let doc;
    try {
      const r = await fetch(location.href, { credentials: 'same-origin', headers: { Accept: 'text/html' } });
      if (!r.ok) throw r.status;
      doc = new DOMParser().parseFromString(await r.text(), 'text/html');
    } catch {
      // seq has already moved past this change, so the WebSocket will not
      // replay it: keep trying until the page loads.
      clearTimeout(pending);
      pending = setTimeout(patch, 5000);
      return;
    }
    document.querySelectorAll('[data-live]').forEach(cur => {
      const next = doc.querySelector(`[data-live="${cur.dataset.live}"]`);
      if (next && !cur.contains(document.activeElement)) cur.replaceWith(document.adoptNode(next));
    });
    const nextList = doc.querySelector('[data-list]');
    if (!list || !nextList) return tidy();
    const keep = [...nextList.children].map(n => {
      const old = card(n.dataset.id);
      if (old && old.dataset.seq === n.dataset.seq) return old;
      const fresh = document.adoptNode(n);
      localize(fresh);
      return fresh;
    });
    if (keep.some(c => c.contains(document.activeElement)) || keep.length !== list.children.length || keep.some((c, i) => c !== list.children[i])) {
      list.replaceChildren(...keep);
    }
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

  // Toast, with Undo when the action can be reversed.
  let toastTimer;
  function say(text, undoIds) {
    const span = document.createElement('span');
    span.textContent = text;
    toast.replaceChildren(span);
    if (undoIds) {
      const b = document.createElement('button');
      b.type = 'button';
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
    toastTimer = setTimeout(() => { toast.hidden = true; }, undoIds ? 10000 : 6000);
  }

  async function post(url, body) {
    try {
      const r = await fetch(url, {
        method: 'POST',
        credentials: 'same-origin',
        headers: body ? { 'Content-Type': 'application/json' } : {},
        body: body ? JSON.stringify(body) : undefined,
      });
      if (r.status === 401) { say('You are signed out. Reload the page to sign in.'); return null; }
      if (!r.ok) { say(`The server refused that (${r.status}). Nothing changed.`); return null; }
      return r;
    } catch {
      say('Could not reach the server. Nothing changed.');
      return null;
    }
  }

  const done = { ack: 'Acknowledged', snooze: 'Snoozed for 1 hour', dismiss: 'Cleared', reopen: 'Restored to the inbox' };
  document.addEventListener('click', async e => {
    const b = e.target.closest('[data-act]');
    if (!b || b.disabled) return;
    const act = b.dataset.act;
    if (act === 'clear-all') {
      const ids = cards().map(c => c.dataset.id);
      const r = ids.length && await post('/api/v1/notifications/clear', { ids });
      if (!r) return;
      const n = (await r.json().catch(() => ({}))).cleared ?? ids.length;
      ids.forEach(id => leave(card(id), 'Cleared'));
      // Undo only when we know every id was ours to clear; otherwise it could
      // reopen something another device cleared first.
      say(`Cleared ${n} ${n === 1 ? 'notification' : 'notifications'}.`, n === ids.length ? ids : undefined);
      return refresh();
    }
    const c = b.closest('.card');
    b.disabled = true;
    const ok = await post(`/api/v1/notifications/${encodeURIComponent(c.dataset.id)}/${act}`, act === 'snooze' ? { for: '1h' } : undefined);
    b.disabled = false;
    if (!ok) return;
    if (act !== 'ack') leave(c, done[act]);
    if (act === 'dismiss') say('Cleared.', [c.dataset.id]);
    else say(`${done[act]}.`);
    refresh();
  });

  // Live sync. Replays everything after the last seq on every (re)connect.
  let tries = 0;
  const live = on => {
    sync.classList.toggle('is-live', on);
    conn.textContent = on ? '' : (tries ? 'Reconnecting' : 'Connecting');
  };
  function connect() {
    const u = new URL('/api/v1/ws', location.href);
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
    u.searchParams.set('since', seq);
    const ws = new WebSocket(u);
    ws.onmessage = e => {
      let m;
      try { m = JSON.parse(e.data); } catch { return; }
      if (m.type === 'ready') { tries = 0; if (m.seq > seq) seq = m.seq; live(true); }
      else if (m.type === 'reset') { ws.onclose = null; ws.close(); location.reload(); }
      else if (m.notification) onEvent(m);
    };
    ws.onclose = () => {
      live(false);
      setTimeout(connect, Math.min(30000, 1000 * 2 ** tries++));
    };
  }
  live(false);
  connect();
})();
