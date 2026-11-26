// The installed worker contains the offline shell. It never stores inbox HTML.
self.addEventListener('install', e => e.waitUntil(self.skipWaiting()));
self.addEventListener('activate', e => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', e => {
  if (e.request.mode !== 'navigate' || e.request.method !== 'GET') return;
  const app = new URL(self.registration.scope);
  const url = new URL(e.request.url);
  if (url.origin !== app.origin || (url.pathname !== app.pathname && !url.pathname.startsWith(app.pathname + '/'))) return;
  if (url.searchParams.has('auth')) return; // preserve the browser's sign-in redirect

  e.respondWith(fetch(e.request, { cache: 'no-store' }).catch(() => new Response(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>clrd | Lost the live connection</title>
<style>
  body { font: 1rem/1.5 system-ui, sans-serif; background: #f5f3ee; color: #0f1114; margin: 2rem auto; padding: 0 1rem; max-width: 34rem; }
  a { color: #0b7a55; }
  @media (prefers-color-scheme: dark) {
    body { background: #0f1114; color: #f5f3ee; }
    a { color: #89d7b3; }
  }
</style>
<main>
  <p>clrd</p>
  <h1>Lost the live connection</h1>
  <p>Reconnect to load your inbox. <a href="">Reload</a></p>
</main>
</html>`, { status: 503, headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' } })));
});
