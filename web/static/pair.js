// Sign in this browser by scanning a QR code with a phone that is signed in (issue #74).
// Without this script the token form still works.
(() => {
  const box = document.querySelector('[data-pair]');
  if (!box) return;
  box.hidden = false;
  const btn = box.querySelector('[data-pair-start]'), img = box.querySelector('[data-pair-qr]'), msg = box.querySelector('[data-pair-msg]');
  const post = (url, body) => fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });
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
      const r = await post('/api/v1/pair');
      const p = await r.json();
      if (!r.ok) return stop(p.error || 'Could not make a code.');
      img.src = p.qr;
      img.hidden = false;
      btn.hidden = true;
      msg.textContent = 'Waiting for your phone. The code works for 2 minutes.';
      const until = Date.parse(p.expires_at);
      const poll = async () => {
        if (Date.now() > until) return stop('The code expired.');
        try {
          const c = await post('/api/v1/pair/claim', { code: p.code });
          if (c.status === 200) { msg.textContent = 'Approved. Signing in.'; return location.reload(); }
          if (c.status === 410) return stop((await c.json()).error || 'The code expired.');
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
})();
