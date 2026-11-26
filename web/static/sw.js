// Web Push for the inbox (#164, SPEC 9). Registered only after the user turns
// notifications on in Settings. A notification here only signals a message:
// closing it changes nothing on the server, and a tap opens the inbox.
// Relative URLs resolve against this file, so a server on a sub-path works.
self.addEventListener('push', e => e.waitUntil((async () => {
  let d = {};
  try { d = e.data.json(); } catch {}
  if (d.id && !d.title && !d.message) { // too big for a push: fetch the rest
    const r = await fetch(`../api/v1/notifications/${encodeURIComponent(d.id)}`).catch(() => null);
    if (r && r.ok) d = { ...(await r.json().catch(() => ({}))), ...d };
  }
  // Always show one: a push with nothing shown breaks the browser's rules.
  await self.registration.showNotification(d.title || d.topic || 'clrd', {
    body: d.message || '', icon: '../apple-touch-icon.png', data: { id: d.id, seq: d.seq },
    ...(d.id && { tag: d.id, renotify: true }), // a repeat rings again
  });
  const app = new URL('../app', self.location).pathname;
  const windows = await clients.matchAll({ type: 'window', includeUncontrolled: true });
  windows.forEach(w => {
    const path = new URL(w.url).pathname;
    if (path === app || path.startsWith(app + '/')) w.postMessage('clrd-push-shown');
  });
})()));

self.addEventListener('notificationclick', e => {
  e.notification.close();
  const app = new URL('../app', self.location).href;
  e.waitUntil(clients.matchAll({ type: 'window', includeUncontrolled: true }).then(ws => {
    // The inbox itself (any view or search), not Settings or another page under it.
    const w = ws.find(w => w.url === app || (w.url.startsWith(app) && /^[?#]/.test(w.url.slice(app.length))));
    return w ? w.focus() : clients.openWindow(app);
  }));
});
