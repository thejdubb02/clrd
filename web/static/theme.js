// Appearance (#583). Runs in <head> before first paint: puts .dark on <html>
// for a saved Dark choice, or for System when the device is dark. Settings
// saves the choice per browser; other tabs follow through the storage event.
(() => {
  const sys = matchMedia('(prefers-color-scheme: dark)');
  const apply = () => {
    let pick = null;
    try { pick = localStorage.getItem('clrd.theme'); } catch {}
    const dark = pick ? pick === 'dark' : sys.matches;
    document.documentElement.classList.toggle('dark', dark);
    for (const m of document.querySelectorAll('meta[name="theme-color"]')) m.content = dark ? '#0F1114' : '#F5F3EE';
  };
  apply();
  sys.addEventListener('change', apply);
  addEventListener('storage', e => { if (e.key === 'clrd.theme') apply(); });
  window.clrdTheme = apply;
})();
