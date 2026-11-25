// The This computer page. It only shows what the app's core says and passes
// button presses on: the token never comes to this page.
const invoke = window.__TAURI_INTERNALS__.invoke;
const $ = (id) => document.getElementById(id);

function show([view, status, noted, offer, closing]) {
  $('close-clears').checked = closing;
  const waiting = view.check !== '';
  const linked = view.server !== '';
  // A link the browser handed over. Any page can send one, so it only goes
  // in the box, with the server it names said plainly: the user presses the
  // button, or does not.
  if (offer) {
    $('url').value = offer[0];
    $('offer').textContent = `Your browser opened a link to ${offer[1]}. If that is your clrd server and you asked for this, press Link this computer.`;
    $('plain').hidden = true;
  }
  $('status').textContent = status;
  $('error').textContent = view.error;
  $('unlinked').hidden = linked || waiting;
  $('waiting').hidden = !waiting;
  $('linked').hidden = !linked;
  $('check').textContent = view.check;
  $('server').textContent = view.server;
  $('name').textContent = view.name;
  $('refused').hidden = !view.refused;
  $('retry').hidden = !view.refused && !noted && (view.error === '' || waiting);
}

// Asking for the state is also what moves a waiting link forward.
async function refresh() {
  try {
    show(await invoke('state'));
  } catch (e) {
    $('error').textContent = String(e);
  }
}

async function run(command, args) {
  try {
    return await invoke(command, args);
  } catch (e) {
    // What went wrong is in the state, which the refresh below shows.
  } finally {
    await refresh();
  }
}

async function start(plainOk) {
  $('plain').hidden = true;
  $('offer').textContent = '';
  const link = $('url').value;
  // The warning is for this link only: not for one that came in meanwhile.
  if ((await run('start', { link, plainOk })) === false && $('url').value === link) {
    $('plain').hidden = false;
  }
}

$('link').addEventListener('submit', (e) => {
  e.preventDefault();
  start(false);
});
$('plain-yes').addEventListener('click', () => start(true));
$('plain-no').addEventListener('click', () => { $('plain').hidden = true; });
$('cancel').addEventListener('click', () => run('cancel'));
$('retry').addEventListener('click', () => run('retry'));
// A change that could not be saved is said, and the switch goes back to
// what is kept. One change at a time, so the last click is the one kept.
$('close-clears').addEventListener('change', async (e) => {
  e.target.disabled = true;
  $('setting-error').textContent = await invoke('set_close_clears', { on: e.target.checked }).then(() => '', String);
  await refresh();
  e.target.disabled = false;
});
$('unlink').addEventListener('click', () => { $('sure').hidden = false; });
$('sure-no').addEventListener('click', () => { $('sure').hidden = true; });
$('sure-yes').addEventListener('click', () => {
  $('sure').hidden = true;
  $('url').value = '';
  run('unlink');
});

// Updates: shown only in a build that has them. What went wrong is said in
// the note, and the switch goes back to what is kept.
async function updates(note) {
  const has = await invoke('updates').catch(() => null);
  $('updates').hidden = !has;
  if (has) [$('version').textContent, $('auto-update').checked, $('auto-update').disabled] = has;
  $('update-note').textContent = note || (has && has[2] ? "Set by this computer's administrator." : '');
}
$('auto-update').addEventListener('change', async (e) => {
  updates(await invoke('set_auto_update', { on: e.target.checked }).then(() => '', String));
});
$('check-update').addEventListener('click', async (e) => {
  e.target.disabled = true;
  $('update-note').textContent = 'Checking.';
  await updates(await invoke('check_update').catch(String));
  e.target.disabled = false;
});

updates();
refresh();
setInterval(refresh, 1000);
