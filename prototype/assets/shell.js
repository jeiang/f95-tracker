// PROTOTYPE shell: theme bootstrap, shared nav, variant switcher, htmx file:// fallback.
// Loaded synchronously in <head> after data.js.

(function initTheme() {
  const saved = localStorage.getItem('proto-theme');
  const prefersLight = window.matchMedia('(prefers-color-scheme: light)').matches;
  document.documentElement.dataset.theme = saved || (prefersLight ? 'light' : 'dark');
})();

function toggleTheme() {
  const next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
  document.documentElement.dataset.theme = next;
  localStorage.setItem('proto-theme', next);
}

// Call from an inline <script> right after <div id="nav"></div>.
function renderNav(active) {
  const q = queueGames().length + (qs('skipped') && !gameById(qs('skipped'))?.tagReview ? 1 : 0);
  const imp = importReviewGames().length;
  const link = (key, href, label, count) => {
    const on = key === active;
    const badge = count
      ? `<span class="ml-1 rounded-full bg-accent px-1.5 text-[11px] font-semibold leading-4 text-accent-ink" aria-label="${count} waiting">${count}</span>`
      : '';
    return `<a href="${href}" ${on ? 'aria-current="page"' : ''}
      class="inline-flex shrink-0 items-center rounded-md px-3 py-2 text-sm font-medium ${on ? 'bg-raised text-ink' : 'text-muted hover:text-ink'}">${label}${badge}</a>`;
  };
  document.getElementById('nav').outerHTML = `
    <div class="proto-strip px-4 py-1 text-xs text-ink">
      <div class="mx-auto flex max-w-6xl flex-wrap items-center gap-x-2">
        <strong class="font-semibold tracking-wide">PROTOTYPE</strong>
        <span class="text-muted">sample data from the CSV plus invented tags · nothing is saved · ticket #14</span>
      </div>
    </div>
    <header class="border-b border-line bg-surface">
      <div class="mx-auto flex max-w-6xl flex-wrap items-center gap-x-4 px-4 py-2">
        <a href="index.html" class="mr-auto flex items-baseline gap-1 py-2 font-semibold sm:mr-0">
          <span class="font-mono text-accent">f95</span><span>tracker</span>
        </a>
        <nav aria-label="Primary" class="order-last -mx-4 flex w-full gap-1 overflow-x-auto px-4 pb-1 sm:order-none sm:mx-0 sm:w-auto sm:flex-1 sm:px-0 sm:pb-0">
          ${link('games', 'index.html', 'Games')}
          ${link('add', 'add.html', 'Add Game')}
          ${link('queue', 'queue.html', 'Tags to review', q)}
          ${link('import', 'import.html', 'Import review', imp)}
          ${link('settings', 'settings.html', 'Settings')}
        </nav>
        <button type="button" onclick="toggleTheme()" class="btn btn-ghost btn-sm" aria-label="Toggle light or dark theme">
          <span aria-hidden="true">◐</span><span class="hidden sm:inline">Theme</span>
        </button>
      </div>
    </header>`;
}

// Floating variant switcher (prototype chrome, not part of the design).
// variants: [{ key: 'A', name: 'Table' }, ...]
function renderSwitcher(variants, current) {
  const i = Math.max(0, variants.findIndex((v) => v.key === current));
  const go = (d) => {
    const v = variants[(i + d + variants.length) % variants.length];
    const p = new URLSearchParams(location.search);
    p.set('variant', v.key);
    location.search = p.toString();
  };
  const bar = document.createElement('div');
  bar.className = 'proto-switcher';
  bar.setAttribute('role', 'group');
  bar.setAttribute('aria-label', 'Prototype variant switcher');
  bar.innerHTML = `
    <button type="button" aria-label="Previous variant">←</button>
    <span class="px-2">Variant ${variants[i].key} · ${variants[i].name}</span>
    <button type="button" aria-label="Next variant">→</button>`;
  const [prev, next] = bar.querySelectorAll('button');
  prev.onclick = () => go(-1);
  next.onclick = () => go(1);
  document.addEventListener('keydown', (e) => {
    const t = e.target;
    if (t.closest('input, textarea, select, [contenteditable]')) return;
    if (e.key === 'ArrowLeft') go(-1);
    if (e.key === 'ArrowRight') go(1);
  });
  document.body.appendChild(bar);
}

// htmx can't fetch partials over file://. Say so in the target instead of failing silently.
document.addEventListener('htmx:sendError', (e) => {
  const target = e.detail.target;
  if (target && location.protocol === 'file:') {
    target.innerHTML = '<span class="chip chip-warn">htmx partials need the local server — see prototype/README.md</span>';
  }
});
