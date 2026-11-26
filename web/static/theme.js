// Appearance (#583) and the inbox's saved palette (#584). Runs in the head.
(() => {
  const sys = matchMedia('(prefers-color-scheme: dark)');
  const root = document.documentElement;
  const keys = ['bg', 'card', 'text', 'clear', 'urgent', 'snooze'];
  const colors = (...values) => Object.fromEntries(keys.map((k, i) => [k, values[i]]));
  const contrastThemes = [
    colors('#ffffff', '#ffffff', '#000000', '#006444', '#ad3000', '#805300'),
    colors('#121212', '#1b1b1b', '#ffffff', '#55ffb2', '#ff9b58', '#ffd168'),
  ];
  const midnightThemes = [
    colors('#edf1f8', '#ffffff', '#111b2e', '#007354', '#ad380b', '#87570f'),
    colors('#000000', '#101014', '#f5f3ee', '#3ddc97', '#f97316', '#f2c66d'),
  ];
  const starter = colors('#f5f3ee', '#ffffff', '#0f1114', '#0b7a55', '#c2410c', '#9d640e');
  const rgb = hex => [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16));
  const light = hex => rgb(hex).reduce((total, byte, i) => {
    const n = byte / 255;
    return total + [0.2126, 0.7152, 0.0722][i] * (n <= 0.04045 ? n / 12.92 : ((n + 0.055) / 1.055) ** 2.4);
  }, 0);
  const ratio = (a, b) => {
    const l = light(a), r = light(b);
    return (Math.max(l, r) + 0.05) / (Math.min(l, r) + 0.05);
  };
  const mix = (fg, bg, amount) => '#' + rgb(fg).map((n, i) =>
    Math.round(n * amount + rgb(bg)[i] * (1 - amount)).toString(16).padStart(2, '0')).join('');
  const near = (a, b) => rgb(a).reduce((sum, n, i) => sum + (n - rgb(b)[i]) ** 2, 0) < 80 ** 2;
  function validate(c) {
    if (!c || !keys.every(k => /^#[a-f\d]{6}$/i.test(c[k] || ''))) return 'Pick all six colors.';
    for (const [label, fg, bg, min] of [
      ['Text on background', c.text, c.bg, 4.5], ['Text on cards', c.text, c.card, 4.5],
      ['Clear on cards', c.clear, c.card, 3], ['Urgent on cards', c.urgent, c.card, 3], ['Snooze on cards', c.snooze, c.card, 3],
    ]) {
      const r = ratio(fg, bg);
      if (r < min) return `${label} has too little contrast (${r.toFixed(2)}:1, needs ${min}:1).`;
    }
    return near(c.clear, c.urgent) ? 'Urgent and Clear colors are too similar.' : '';
  }

  let shown, written = [];
  function paint(c, high) {
    for (const token of written) root.style.removeProperty(token);
    written = [];
    if (!c) return;
    const { bg, card, text, clear, urgent, snooze } = c;
    const tint = mix(text, bg, 0.06);
    const soft = ratio(text, tint) >= 4.5 ? tint : bg;
    const ink = (color, backgrounds) => backgrounds.every(b => ratio(color, b) >= 4.5) ? color : text;
    const inkOn = b => light(b) < 0.179 ? '#ffffff' : '#000000';
    const line = mix(text, bg, high ? 0.65 : 0.12);
    const set = (value, names) => {
      for (const token of names.split(' ')) {
        root.style.setProperty(token, value);
        written.push(token);
      }
    };
    set(bg, '--background --sidebar --primary-foreground --sidebar-primary-foreground');
    set(card, '--card --popover');
    set(text, '--foreground --card-foreground --popover-foreground --primary --secondary-foreground --accent-foreground --sidebar-foreground --sidebar-primary --sidebar-accent-foreground');
    set(soft, '--secondary --muted --accent --sidebar-accent');
    set(clear, '--clear --ring --sidebar-ring');
    set(inkOn(clear), '--clear-foreground');
    set(urgent, '--destructive');
    set(line, '--border --sidebar-border');
    set(mix(text, bg, high ? 0.65 : 0.22), '--input');
    set(ink(mix(text, bg, 0.60), [bg, card, soft]), '--muted-foreground');
    set(ink(mix(text, bg, 0.85), [bg, card, soft]), '--body');
    for (const [state, color] of [['active', urgent], ['ack', text], ['res', clear], ['snz', snooze]]) {
      const fill = mix(color, card, 0.15);
      set(fill, '--chip-' + state + '-bg');
      set(ratio(color, fill) >= 4.5 ? color : inkOn(fill), '--chip-' + state);
    }
  }
  function apply() {
    let pick = null;
    try { pick = localStorage.getItem('clrd.theme'); } catch {}
    let dark = pick ? pick === 'dark' : sys.matches;
    const override = shown?.device_theme;
    const choice = override === 'custom' && !shown.theme_colors ? shown.theme : override || shown?.theme;
    let palette;
    if (choice === 'high-contrast') palette = contrastThemes[Number(dark)];
    if (choice === 'midnight') palette = midnightThemes[Number(dark)];
    if (choice === 'custom' && !validate(shown.theme_colors)) {
      palette = shown.theme_colors;
      dark = light(palette.bg) < 0.179;
    }
    root.classList.toggle('dark', dark);
    paint(palette, choice === 'high-contrast');
    for (const m of document.querySelectorAll('meta[name="theme-color"]')) m.content = palette?.bg || (dark ? '#0F1114' : '#F5F3EE');
  }
  const initial = JSON.parse(document.currentScript.dataset.palette || '{}');
  shown = initial;
  apply();
  sys.addEventListener('change', apply);
  addEventListener('storage', e => { if (e.key === 'clrd.theme') apply(); });
  window.clrdTheme = apply;
  window.clrdPalette = { initial, starter, validate, show: st => { shown = st; apply(); } };
})();
