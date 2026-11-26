const { copyFileSync } = require('node:fs');
const { join } = require('node:path');

copyFileSync(join(__dirname, '..', 'js', 'index.js'), join(__dirname, 'dist', 'client.mjs'));
for (const name of ['clrd.svg', 'clrd.dark.svg']) {
  copyFileSync(join(__dirname, 'nodes', name), join(__dirname, 'dist', 'nodes', name));
}
