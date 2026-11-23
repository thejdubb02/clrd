// The This computer page. It only shows what the app's core says and passes
// button presses on: the token never comes to this page.
const invoke = window.__TAURI_INTERNALS__.invoke;
const $ = (id) => document.getElementById(id);

function show([view, status, noted]) {
  const waiting = view.check !== '';
  const linked = view.server !== '';
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
  if ((await run('start', { link: $('url').value, plainOk })) === false) {
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
$('unlink').addEventListener('click', () => { $('sure').hidden = false; });
$('sure-no').addEventListener('click', () => { $('sure').hidden = true; });
$('sure-yes').addEventListener('click', () => {
  $('sure').hidden = true;
  $('url').value = '';
  run('unlink');
});

refresh();
setInterval(refresh, 1000);
