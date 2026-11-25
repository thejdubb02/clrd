// QR codes that connect a device by approval (issue #74): sign in this browser
// from a signed-in phone ([data-pair]), or link a phone from this signed-in
// browser ([data-link]). Without this script the token form still works.
(() => {
  const B = document.documentElement.dataset.base || ''; // the sub-path of CLRD_BASE_URL, if any
  const fetch = (u, o) => window.fetch(u.startsWith('/') ? B + u : u, o);
  const post =(url, body) => fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });

  // This browser's own id, sent at sign-in so signing in again replaces its
  // old row in the device list instead of adding one (#765).
  let install = '';
  try {
    // getRandomValues, unlike randomUUID, works on a plain-http LAN address too.
    install = localStorage.getItem('clrd-install') || [...crypto.getRandomValues(new Uint8Array(16))].map(b => b.toString(16).padStart(2, '0')).join('');
    localStorage.setItem('clrd-install', install);
  } catch { /* no storage: each sign-in is a new row, as before */ }
  for (const i of document.querySelectorAll('input[name="install"]')) i.value = install;

  // On click, make a code at url and show its QR, then run step every 1.5 s
  // until it returns true or the code expires. step gets the code and stop.
  const qrFlow = (box, url, waiting, step) => {
    box.hidden = false;
    const btn = box.querySelector('[data-pair-start]'), img = box.querySelector('[data-pair-qr]'), msg = box.querySelector('[data-pair-msg]');
    let timer;
    const stop = text => {
      clearTimeout(timer);
      img.hidden = true;
      btn.hidden = false;
      btn.textContent = 'Show a new code';
      msg.textContent = text;
    };
    btn.addEventListener('click', async () => {
      btn.disabled = true;
      try {
        const r = await post(url);
        const p = await r.json();
        if (!r.ok) return stop(p.error || 'Could not make a code.');
        img.src = p.qr;
        img.hidden = false;
        btn.hidden = true;
        msg.textContent = waiting;
        // Time left by the server's clock, so a wrong clock here does not expire it early.
        const until = performance.now() + (Date.parse(p.expires_at) - (Date.parse(r.headers.get('Date')) || Date.now()));
        const poll = async () => {
          if (performance.now() > until) return stop('The code expired.');
          try {
            if (await step(p.code, stop)) return;
          } catch { /* offline for a moment: keep trying until it expires */ }
          timer = setTimeout(poll, 1500);
        };
        timer = setTimeout(poll, 1500);
      } catch {
        stop('Could not reach the server.');
      } finally {
        btn.disabled = false;
      }
    });
    return { img, msg };
  };

  const pair = document.querySelector('[data-pair]');
  if (pair) {
    const { msg } = qrFlow(pair, '/api/v1/pair', 'Waiting for your phone. The code works for 2 minutes.', async (code, stop) => {
      const c = await post('/api/v1/pair/claim', { code, install });
      if (c.status === 200) { msg.textContent = 'Approved. Signing in.'; location.reload(); return true; }
      if (c.status === 410) { stop((await c.json()).error || 'The code expired.'); return true; }
    });
  }

  const link = document.querySelector('[data-link]');
  if (link) {
    const ask = link.querySelector('[data-link-ask]');
    let current;
    const { img, msg } = qrFlow(link, '/api/v1/link', 'Scan this from the clrd app on your phone. The code works for 2 minutes.', async (code, stop) => {
      current = { code, stop };
      ask.hidden = true;
      const r = await fetch('/api/v1/link/' + code);
      if (r.status === 404) { stop('The code expired.'); return true; }
      const s = await r.json();
      if (s.state !== 'claimed') return false;
      link.querySelector('[data-link-name]').textContent = s.name; // from the phone: text only
      link.querySelector('[data-link-check]').textContent = s.check;
      img.hidden = true;
      msg.textContent = '';
      ask.hidden = false;
      return true; // wait for Approve or Deny
    });
    const decide = async verb => {
      ask.hidden = true;
      const { code, stop } = current;
      try {
        const r = await post(`/api/v1/link/${code}/${verb}`);
        if (verb === 'deny') return stop('Denied. That phone was not connected.');
        if (!r.ok) return stop('The code expired.');
        stop(`Connected: ${(await r.json()).name}.`);
      } catch {
        stop('Could not reach the server.');
      }
    };
    link.querySelector('[data-link-approve]').addEventListener('click', () => decide('approve'));
    link.querySelector('[data-link-deny]').addEventListener('click', () => decide('deny'));
  }

  // Add an agent (#456): a one-use invite string. It lives only in this page:
  // never in a URL, storage or the console, and it leaves the page when it runs out.
  const agent = document.querySelector('[data-agent]');
  if (agent) {
    agent.hidden = false;
    const $ = s => agent.querySelector(s);
    const start = $('[data-agent-start]'), form = $('[data-agent-form]'), done = $('[data-agent-done]'), status = $('[data-agent-status]'), gone = $('[data-agent-gone]');
    let timer;
    const open = () => {
      clearTimeout(timer);
      gone.textContent = '';
      $('[data-agent-invite]').textContent = '';
      done.hidden = true; start.hidden = true; form.hidden = false;
      form.reset(); status.textContent = '';
      form.elements.name.focus();
    };
    start.addEventListener('click', open);
    $('[data-agent-again]').addEventListener('click', open);
    form.addEventListener('submit', async e => {
      e.preventDefault();
      const f = form.elements;
      const topics = [...new Set([...form.querySelectorAll('[name=topic]:checked')].map(c => c.value)
        .concat(f.more.value.split(',').map(t => t.trim()).filter(Boolean)))];
      if (!topics.length) { status.textContent = 'Pick at least one topic.'; return; }
      const btn = form.querySelector('button');
      btn.disabled = true; status.textContent = '';
      try {
        const r = await post('/api/v1/agents/invite', { name: f.name.value.trim(), topics, expires_in: f.expires_in.value });
        const p = await r.json();
        if (!r.ok) { status.textContent = p.error || 'Could not make an invite.'; return; }
        $('[data-agent-name]').textContent = p.name;
        $('[data-agent-invite]').textContent = p.url;
        const until = $('[data-agent-until]');
        until.dateTime = p.expires_at;
        until.textContent = new Date(p.expires_at).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
        form.hidden = true; done.hidden = false;
        $('[data-agent-invite]').parentElement.focus(); // the form had focus; keep keyboard and screen readers on the invite
        // By the server's clock, as for link codes above.
        const ms = Date.parse(p.expires_at) - (Date.parse(r.headers.get('Date')) || Date.now());
        timer = setTimeout(() => {
          $('[data-agent-invite]').textContent = '';
          done.hidden = true; start.hidden = false;
          gone.textContent = 'That invite ran out. If the agent used it in time, it is in Linked devices.';
        }, ms);
      } catch {
        status.textContent = 'Could not reach the server.';
      } finally {
        btn.disabled = false;
      }
    });
  }
})();
