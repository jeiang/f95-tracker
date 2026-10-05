// PROTOTYPE DATA — names, CSV versions and ratings come from "F95 Tracker - Sheet1.csv";
// latest versions, tags, Genre text, dates and statuses are invented for the prototype.
// Nothing is persisted: every page reloads this file.

const PLAY_STATUSES = ['planned', 'playing', 'on hold', 'finished', 'dropped'];
const DEV_STATUSES = ['ongoing', 'completed', 'on hold', 'abandoned'];
const TODAY = '2026-10-04';
const ALERT_SET = ['playing', 'on hold']; // Play statuses that alert on Update (Settings)

// Game tag helper. kind: 'f95' (an F95zone tag slug) or 'custom' (Custom tag).
// q: present | planned | optional; v: unverified | confirmed | wrong;
// origin: genre | f95 | both | hand; flags: 'new' | 'removed'.
function T(label, origin, extra = {}) {
  return {
    label,
    kind: extra.kind || 'f95',
    q: extra.q || 'present',
    v: extra.v || 'unverified',
    origin,
    note: extra.note || '',
    flags: extra.flags || [],
  };
}
const C = (label, origin, extra = {}) => T(label, origin, { ...extra, kind: 'custom' });

const f95 = (id) => ({ kind: 'f95', id, url: `https://f95zone.to/threads/${id}/` });
const itch = (url) => ({ kind: 'itch', url });
const imported = (version) => ({ date: null, version, origin: 'imported' });
const played = (date, version) => ({ date, version, origin: 'played' });

const GAMES = [
  {
    id: 'ck', name: 'Corrupted Kingdoms', source: f95(31912),
    latest: '0.19.0', update: true, updatedAt: '2026-10-03', lastChecked: '2026-10-04 06:00',
    dev: 'ongoing', play: 'playing', rating: 5,
    log: [imported('0.18.5')],
    genre: 'Fantasy, RPG, Corruption, Harem, Monster girls, Yuri, BDSM (mild), Threesome, Possible: Netorare (avoidable). Planned: Pregnancy. Lots of choices that actually matter.',
    tags: [
      T('fantasy', 'both', { v: 'confirmed' }),
      T('rpg', 'both'),
      T('corruption', 'both', { v: 'confirmed' }),
      T('harem', 'genre'),
      T('monster girl', 'both', { v: 'confirmed' }),
      T('lesbian', 'both', { note: 'via synonym “Yuri”' }),
      T('bdsm', 'both', { note: 'mild' }),
      C('Threesome', 'genre'),
      T('male protagonist', 'f95', { v: 'confirmed' }),
      T('sandbox', 'f95', { v: 'wrong' }),
      T('vaginal sex', 'f95', { flags: ['new'] }),
      T('humor', 'f95', { flags: ['removed'] }),
      T('netorare', 'genre', { q: 'optional', note: 'avoidable' }),
      T('pregnancy', 'genre', { q: 'planned' }),
    ],
  },
  {
    id: 'hh', name: 'Harem Hotel', source: f95(12760),
    latest: 'v0.17.2', update: false, updatedAt: '2026-08-21', lastChecked: '2026-10-04 06:00',
    dev: 'ongoing', play: 'playing', rating: 5,
    log: [imported('0.17.2')],
    genre: 'Harem, Romance, Comedy, Big tits, Male protagonist.',
    tags: [
      T('harem', 'both', { v: 'confirmed' }),
      T('romance', 'both', { v: 'confirmed' }),
      T('humor', 'both', { note: 'via synonym “Comedy”' }),
      T('big tits', 'both'),
      T('male protagonist', 'both', { v: 'confirmed' }),
      T('3dcg', 'f95'),
      T('vaginal sex', 'f95'),
    ],
  },
  {
    id: 'et', name: 'Eternum', source: f95(93340),
    latest: '0.4.1', update: false, updatedAt: '2026-09-28', lastChecked: '2026-10-04 06:00',
    dev: 'ongoing', play: 'playing', rating: 4,
    log: [imported('0.3.0'), played('2026-09-12', '0.3.5')],
    tagReview: { state: 'skipped', version: '0.3.5', at: '2026-09-12' },
    genre: 'Sci-fi, Romance, Mystery, Harem, Humor, Animated scenes. Possible: Group sex (later).',
    tags: [
      T('sci-fi', 'both'),
      T('romance', 'both'),
      T('mystery', 'both'),
      T('harem', 'genre'),
      T('humor', 'both'),
      T('animated', 'both'),
      T('3dcg', 'f95'),
      T('male protagonist', 'f95'),
      T('group sex', 'genre', { q: 'optional', note: 'later' }),
    ],
  },
  {
    id: 'tx', name: 'ToxiCity', source: f95(237702),
    latest: '0.22.0', update: false, updatedAt: '2026-09-30', lastChecked: '2026-10-04 06:00',
    dev: 'ongoing', play: 'playing', rating: 5,
    log: [imported('0.21.0'), played('2026-10-02', '0.22.0')],
    tagReview: { state: 'pending', version: '0.22.0', at: '2026-10-02' },
    genre: 'Dystopian setting, Corruption, Female domination, BBW, Threesome, Male protagonist.',
    tags: [
      T('dystopian setting', 'both'),
      T('corruption', 'both'),
      T('female domination', 'both'),
      C('BBW', 'genre'),
      C('Threesome', 'genre'),
      T('male protagonist', 'both'),
      T('3dcg', 'f95'),
      T('sandbox', 'f95'),
    ],
  },
  {
    id: 'id', name: 'Interim Domain', source: f95(114650),
    latest: '0.31.0', update: false, updatedAt: '2026-05-02', lastChecked: '2026-10-04 06:00',
    dev: 'completed', play: 'finished', rating: 5,
    log: [imported('0.31.0')],
    genre: 'Fantasy, Mind control, Harem, Male protagonist.',
    tags: [
      T('fantasy', 'both', { v: 'confirmed' }),
      T('mind control', 'both', { v: 'confirmed' }),
      T('harem', 'both', { v: 'confirmed' }),
      T('male protagonist', 'both', { v: 'confirmed' }),
      T('2dcg', 'f95', { v: 'confirmed' }),
    ],
  },
  {
    id: 'fu', name: 'Furina', source: f95(92250),
    latest: '0.3a', update: false, updatedAt: '2025-11-14', lastChecked: '2026-10-04 06:00',
    dev: 'abandoned', play: 'finished', rating: 5,
    log: [imported('0.3a')],
    importReview: { derived: 'finished', rule: 'Rating set + CSV Abandoned = TRUE' },
    genre: 'Fantasy, Monster girls, Female protagonist.',
    tags: [T('fantasy', 'both'), T('monster girl', 'both'), T('female protagonist', 'both')],
  },
  {
    id: 'mm', name: 'Maid and Maidens', source: f95(59416),
    latest: '0.12.0', update: false, updatedAt: '2026-01-09', lastChecked: '2026-10-04 06:00',
    dev: 'on hold', play: 'on hold', rating: 5,
    log: [imported('0.12.0')],
    importReview: { derived: 'on hold', rule: 'CSV On Hold = TRUE' },
    genre: 'Harem, Fantasy, Romance, Possible: Netorare.',
    tags: [T('harem', 'both'), T('fantasy', 'both'), T('romance', 'both'), T('netorare', 'genre', { q: 'optional' })],
  },
  {
    id: 'ot', name: 'Out of Touch', source: f95(67494),
    latest: 'v3.92.0', update: true, updatedAt: '2026-10-04', lastChecked: '2026-10-04 06:00',
    dev: 'ongoing', play: 'playing', rating: null,
    log: [imported('v3.91.1')],
    importReview: { derived: 'playing', rule: 'Version recorded, no rating' },
    genre: 'Romance, Drama, Slice of life.',
    tags: [T('romance', 'both'), C('Drama', 'genre'), T('slice of life', 'both'), T('3dcg', 'f95')],
  },
  {
    id: 'wa', name: 'Wartribe Alliances', source: f95(254874),
    latest: null, update: false, updatedAt: null, lastChecked: '2026-10-01 06:00',
    dev: null, play: 'planned', rating: null, detailsPending: true,
    log: [],
    importReview: { derived: 'planned', rule: 'No version, no rating' },
    genre: null,
    tags: [],
  },
  {
    id: 'lr', name: 'Lycoris Radiata (SFW Rework)', source: itch('https://kuro-kai.itch.io/lycoris-radiata'),
    latest: 'Ch. 1', update: false, updatedAt: null, lastChecked: null,
    dev: null, play: 'playing', rating: 5,
    log: [imported('Ch. 1')],
    genre: null,
    tags: [T('fantasy', 'hand', { v: 'confirmed' }), C('SFW', 'hand', { v: 'confirmed' })],
  },
  {
    id: 'as', name: 'Alchemy Shop', source: itch('https://jjambong.itch.io/alchemy-shop'),
    latest: 'Demo', update: false, updatedAt: null, lastChecked: '2026-10-04 06:00',
    dev: 'completed', play: 'finished', rating: 3, sourceUnavailable: true,
    log: [imported('Demo')],
    genre: null,
    tags: [C('Management', 'hand')],
  },
  {
    id: 'ca', name: 'Campo', source: f95(135682),
    latest: '0.5', update: false, updatedAt: '2026-07-19', lastChecked: '2026-10-04 06:00',
    dev: 'ongoing', play: 'dropped', rating: 1,
    log: [imported('0.2')],
    importReview: { derived: 'dropped', rule: 'Rating ≤ 2' },
    genre: 'Farming, Sandbox, Male protagonist.',
    tags: [C('Farming', 'genre'), T('sandbox', 'both'), T('male protagonist', 'both')],
  },
  // Only reachable from the Add Game flow (not listed until "added").
  {
    id: 'ap', name: 'Apocalypse 2059', source: f95(238127), addDemo: true,
    latest: '0.3', update: false, updatedAt: '2026-09-22', lastChecked: TODAY + ' 10:41',
    dev: 'ongoing', play: 'planned', rating: null,
    log: [],
    genre: 'Post-apocalyptic, Survival, Harem, Yuri, BBW, Corruption (light). Possible: Netorare (avoidable). Planned: Pregnancy, Futa. Great story with tons of choices.',
    tags: [
      T('post apocalyptic', 'both'),
      T('survival', 'genre'),
      T('harem', 'both'),
      T('lesbian', 'genre', { note: 'via synonym “Yuri”' }),
      C('BBW', 'genre'),
      T('corruption', 'both', { note: 'light' }),
      T('male protagonist', 'f95'),
      T('3dcg', 'f95'),
      T('netorare', 'genre', { q: 'optional', note: 'avoidable' }),
      T('pregnancy', 'genre', { q: 'planned' }),
      T('futa/trans', 'genre', { q: 'planned', note: 'via synonym “Futa”' }),
    ],
  },
];

const SYNONYMS = [
  { phrase: 'Yuri', target: 'lesbian', kind: 'f95' },
  { phrase: 'Comedy', target: 'humor', kind: 'f95' },
  { phrase: 'Futa', target: 'futa/trans', kind: 'f95' },
  { phrase: 'Monster girls', target: 'monster girl', kind: 'f95' },
  { phrase: 'Post-apocalyptic', target: 'post apocalyptic', kind: 'f95' },
  { phrase: 'Chubby', target: 'BBW', kind: 'custom' },
  { phrase: 'MMF / FFM', target: 'Threesome', kind: 'custom' },
];

// ---------- domain helpers ----------

const qs = (k) => new URLSearchParams(location.search).get(k);
const gameById = (id) => GAMES.find((g) => g.id === id);
const listedGames = () => GAMES.filter((g) => !g.addDemo);

// Behind: last played differs from latest, ignoring case, surrounding whitespace and a leading "v".
const normVersion = (v) => (v || '').trim().toLowerCase().replace(/^v/, '');
const lastPlayed = (g) => (g.log.length ? g.log[g.log.length - 1].version : null);
const lastPlayedDate = (g) => [...g.log].reverse().find((e) => e.date)?.date || null;
const isBehind = (g) => !!(lastPlayed(g) && g.latest && normVersion(lastPlayed(g)) !== normVersion(g.latest));

const queueGames = () => GAMES.filter((g) => g.tagReview);
const importReviewGames = () => GAMES.filter((g) => g.importReview);

function coverClass(g) {
  let h = 0;
  for (const ch of g.id) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return 'cover-' + ((h % 6) + 1);
}
const initials = (g) => g.name.replace(/[^A-Za-z0-9 ]/g, '').split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0]).join('').toUpperCase();

// "★★★★½" style; null → "unrated"
function stars(r) {
  if (r == null) return '';
  return '★'.repeat(Math.floor(r)) + (r % 1 ? '½' : '');
}

function sourceLabel(g) {
  if (g.source.kind === 'f95') return `F95 #${g.source.id}`;
  if (g.source.kind === 'itch') return 'itch.io';
  return 'manual link';
}

const ORIGIN_LABEL = { both: 'Genre + F95', genre: 'Genre', f95: 'F95 only', hand: 'by hand' };
const VERIFY_LABEL = { unverified: 'unverified', confirmed: 'confirmed', wrong: 'wrong' };

const playChip = (s) => ({ planned: 'chip-muted', playing: 'chip-accent', 'on hold': 'chip-warn', finished: 'chip-ok', dropped: 'chip-bad' }[s] || 'chip-muted');
const devChip = (s) => ({ ongoing: 'chip-info', completed: 'chip-ok', 'on hold': 'chip-warn', abandoned: 'chip-bad' }[s] || 'chip-muted');
