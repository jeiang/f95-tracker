// Run: node userscript/extract.test.js (from repo root)
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const { extractLinks } = require('./f95-tracker-links.user.js');

const html = fs.readFileSync(path.join(__dirname, '../testdata/f95/114650.html'), 'utf8');
const { masked, direct } = extractLinks(html);
assert(masked.length > 0 && masked.every((u) => u.includes('/masked/')), 'masked hrefs found');
assert(direct.every((l) => !l.url.includes('/masked/')), 'masked never in direct');
const pc = direct.find((l) => l.url.includes('datanodes.to/02uq7i30qipg'));
assert.deepStrictEqual(pc, { url: pc.url, platform: 'win_linux', label: 'DATANODES' });
const mac = direct.find((l) => l.url.endsWith('-mac.zip'));
assert.strictEqual(mac.platform, 'mac');
assert(!direct.some((l) => /patreon|discord/.test(l.url)), 'links before DOWNLOAD ignored');

const snip = '<b>DOWNLOAD</b><br/><b>Windows</b>: <a href="https://x.io/a?b=1&amp;c=2">X</a><br/>Other <a href="https://x.io/o">O</a>';
assert.deepStrictEqual(extractLinks(snip).direct, [
  { url: 'https://x.io/a?b=1&c=2', platform: 'win', label: 'X' },
  { url: 'https://x.io/o', label: 'O' },
]);
console.log('ok', masked.length, 'masked,', direct.length, 'direct');
