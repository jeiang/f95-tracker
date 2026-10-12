// ==UserScript==
// @name         F95 Tracker: send download links
// @namespace    https://github.com/jeiang/f95-tracker
// @version      1.0.0
// @description  Sends the download links of an F95zone thread to your F95 Tracker when a download job is waiting for them.
// @match        https://f95zone.to/threads/*
// @grant        GM_xmlhttpRequest
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        GM_registerMenuCommand
// @connect      f95.proxy.jeiang.dev
// @connect      *
// @run-at       document-idle
// ==/UserScript==
// `@connect *` is needed only because the tracker URL is user-configurable;
// Violentmonkey still asks before the first request to a new host. Edit it to
// your tracker host if you prefer.

const DEFAULT_TRACKER = 'https://f95.proxy.jeiang.dev';

const PLATFORMS = [
  [/^win\s*\/\s*linux\b/i, 'win_linux'],
  [/^linux\b/i, 'linux'],
  [/^win(dows)?\b/i, 'win'],
  [/^mac(os)?\b/i, 'mac'],
  [/^android\b/i, 'android'],
];

function stripTags(s) {
  return s.replace(/<[^>]*>/g, '').replace(/&nbsp;/g, ' ').replace(/&amp;/g, '&').replace(/\s+/g, ' ').trim();
}

function decodeAttr(s) {
  return s.replace(/&amp;/g, '&').replace(/&#34;|&quot;/g, '"').replace(/&#39;/g, "'");
}

function isMasked(url) {
  try {
    const u = new URL(url);
    return /(^|\.)f95zone\.to$/i.test(u.hostname) && u.pathname.startsWith('/masked/');
  } catch {
    return false;
  }
}

// postHtml: innerHTML of the first post body. Returns masked hrefs and direct
// external links (with platform/label) found at or after the DOWNLOAD heading.
function extractLinks(postHtml) {
  const marker = postHtml.search(/>\s*DOWNLOAD\s*</i);
  const html = marker >= 0 ? postHtml.slice(marker) : postHtml;
  const masked = [];
  const direct = [];
  const seen = new Set();
  const anchor = /<a\b[^>]*?\bhref="([^"]*)"[^>]*>([\s\S]*?)<\/a>/gi;
  let m;
  while ((m = anchor.exec(html))) {
    const url = decodeAttr(m[1]);
    if (!/^https?:\/\//i.test(url) || seen.has(url)) continue;
    seen.add(url);
    if (isMasked(url)) {
      masked.push(url);
      continue;
    }
    if (/^https?:\/\/([^/]*\.)?f95zone\.to(\/|$)/i.test(url)) continue;
    const lineStart = Math.max(
      html.lastIndexOf('<br', m.index),
      html.lastIndexOf('</li', m.index),
      html.lastIndexOf('</div', m.index),
    );
    const line = stripTags(html.slice(lineStart < 0 ? 0 : lineStart, m.index).replace(/^<[^>]*>/, ''));
    const hit = PLATFORMS.find(([re]) => re.test(line));
    const link = { url };
    if (hit) link.platform = hit[1];
    const label = stripTags(m[2]);
    if (label) link.label = label.slice(0, 100);
    direct.push(link);
  }
  return { masked, direct };
}

if (typeof module !== 'undefined') module.exports = { extractLinks, isMasked };

if (typeof GM_getValue === 'function') {
  (function main() {
    const cfg = () => ({
      url: String(GM_getValue('tracker_url', DEFAULT_TRACKER)).replace(/\/+$/, ''),
      token: String(GM_getValue('submit_links', '')),
    });

    GM_registerMenuCommand('F95 Tracker: set tracker URL', () => {
      const v = prompt('Tracker URL', cfg().url);
      if (v) GM_setValue('tracker_url', v.trim());
    });
    GM_registerMenuCommand('F95 Tracker: set submit_links token', () => {
      const v = prompt('submit_links token (f95t_...)', '');
      if (v !== null) GM_setValue('submit_links', v.trim());
    });

    const api = (method, path, body) =>
      new Promise((resolve, reject) => {
        const { url, token } = cfg();
        if (!token) return reject(new Error('No token set (userscript menu: set submit_links token).'));
        GM_xmlhttpRequest({
          method,
          url: url + path,
          headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' },
          data: body ? JSON.stringify(body) : undefined,
          onload: (r) => {
            let json = null;
            try { json = JSON.parse(r.responseText); } catch {}
            resolve({ status: r.status, json });
          },
          onerror: () => reject(new Error('Tracker unreachable.')),
          ontimeout: () => reject(new Error('Tracker timed out.')),
        });
      });

    const threadId = () => {
      const key = document.querySelector('[data-content-key^="thread-"]');
      const m = (key && key.getAttribute('data-content-key').match(/thread-(\d+)/)) ||
        location.pathname.match(/\/threads\/(?:[^/]*\.)?(\d+)/);
      return m ? Number(m[1]) : null;
    };

    const panel = document.createElement('div');
    panel.style.cssText = 'position:fixed;right:12px;bottom:12px;z-index:99999;max-width:320px;padding:10px 12px;' +
      'background:#1f2937;color:#f9fafb;border-radius:8px;font:13px/1.4 sans-serif;box-shadow:0 2px 10px #0008';
    const msg = document.createElement('div');
    const btn = document.createElement('button');
    btn.style.cssText = 'margin-top:6px;padding:4px 10px;cursor:pointer';
    panel.append(msg, btn);

    // Unmask via F95's own endpoint with the page session. Never solves a captcha.
    async function unmask(maskedUrl) {
      const r = await fetch(maskedUrl, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'X-Requested-With': 'XMLHttpRequest' },
        body: 'xhr=1&download=1',
      });
      try { return await r.json(); } catch { return { status: 'error' }; }
    }

    async function send(tid) {
      btn.disabled = true;
      try {
        const post = document.querySelector('article.message--post .bbWrapper');
        if (!post) throw new Error('First post not found.');
        const { masked, direct } = extractLinks(post.innerHTML);
        const links = direct.slice();
        for (const mu of masked) {
          const res = await unmask(mu);
          if (res.status === 'captcha') {
            msg.textContent = 'F95 wants a captcha. Open one masked download link, solve the captcha there, then press the button again.';
            return;
          }
          if (res.status === 'ok' && typeof res.msg === 'string' && /^https?:\/\//i.test(res.msg) && !isMasked(res.msg)) {
            links.push({ url: res.msg });
          }
          await new Promise((r) => setTimeout(r, 400));
        }
        if (!links.length) throw new Error('No links found.');
        const r = await api('POST', '/api/v1/links', { thread_id: tid, links });
        if (r.status === 200) {
          msg.textContent = `Sent: ${r.json.accepted} accepted, ${r.json.manual} manual.`;
        } else {
          msg.textContent = 'Tracker error: ' + ((r.json && r.json.error) || r.status);
        }
      } catch (e) {
        msg.textContent = e.message;
      } finally {
        btn.disabled = false;
      }
    }

    (async () => {
      const tid = threadId();
      if (!tid || !cfg().token) return;
      let r;
      try { r = await api('GET', '/api/v1/links/pending?thread=' + tid); } catch { return; }
      if (r.status !== 200 || !r.json) return;
      msg.textContent = `Send links for ${r.json.game_name} ${r.json.target_version}`;
      btn.textContent = 'Send links';
      btn.addEventListener('click', () => send(tid));
      document.body.appendChild(panel);
    })();
  })();
}
