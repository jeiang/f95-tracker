#!/usr/bin/env python3
"""Reference rule set for parsing F95 Genre text (ticket #3).

Usage: python3 parse_genre.py [samples.jsonl]   -> prints per-thread parse and totals.
The rules here are the ones documented in ../f95-genre-text.md ("Recommendation").
"""
import csv, json, re, sys, os

HERE = os.path.dirname(os.path.abspath(__file__))

def key(s):
    """Normalization key: lowercase, drop everything but letters/digits."""
    return re.sub(r'[^a-z0-9]', '', s.lower())

# Synonym table seed: normalized key -> F95 slug (None = known to have no F95 tag).
SYN = {
    'futa': 'futa-trans', 'futanari': 'futa-trans', 'futatrans': 'futa-trans',
    '3dgc': '3dcg', 'haren': 'harem', 'oral': 'oral-sex', 'blowjob': 'oral-sex',
    'anal': 'anal-sex', 'vaginal': 'vaginal-sex', 'femdom': 'femaledomination',
    'netori': 'ntr', 'netorare': 'ntr', 'scifi': 'sci-fi', 'toys': 'sex-toys',
    'largebreasts': 'big-tits', 'boobjob': 'titfuck', 'boobsjob': 'titfuck',
    'titjob': 'titfuck', 'superpower': 'superpowers', 'possesion': 'possession',
    'violence': 'graphic-violence', 'group': 'group-sex', 'yuri': 'lesbian',
    'datingsim': 'dating-sim', 'pointandclick': 'point-click',
    'femaleprotagonist': 'female-protagonist', 'futaprotagonist': 'futa-trans-protagonist',
    'deflowering': None, 'defloration': None,   # F95 has no tag: keep as unmatched
}

OPT_WORD = r'(?:optional|toggleable|avoidable|can be disabled)'
OPT_PAREN = re.compile(r'\(\s*' + OPT_WORD + r'\s*\)', re.I)     # "Harem (Optional)", "Pregnancy(optional)"
OPT_PREFIX = re.compile(r'^\s*' + OPT_WORD + r'\s+', re.I)       # "optional trap"
MODIFIER = re.compile(r"^(?:light|mild|soft|'soft')\s+", re.I)    # "Mild BDSM" -> bdsm + note
HEAD = re.compile(r'^(?P<h>(?:planned|future|possible|currently)\b[^:\n]*?)\s*:\s*(?P<rest>.*)$', re.I)

def section_q(h):
    h = h.lower()
    if h.startswith(('planned', 'future')): return 'planned'
    if h.startswith('currently'): return 'present'
    return 'possible'   # "Possible (Most to least possible):" -> human decision

def load_vocab():
    v = {}
    with open(os.path.join(HERE, 'f95-tag-vocabulary.tsv')) as f:
        for row in csv.DictReader(f, delimiter='\t'):
            if not row['slug'].startswith('asset-') and row['slug'] != 'unknown':
                v[key(row['slug'])] = row['slug']
    return v

def lookup(base, vocab):
    k = key(base)
    if k in vocab: return [vocab[k]], 'exact'
    if SYN.get(k): return [SYN[k]], 'synonym'
    return [], 'none'

def tokenize(line):
    """Split on commas / sentence periods / ellipses, but never inside parentheses."""
    out, depth, cur = [], 0, ''
    for i, ch in enumerate(line):
        if ch == '(': depth += 1
        if ch == ')': depth = max(0, depth - 1)
        sep = depth == 0 and (ch == ',' or (ch == '.' and (line[i+1:i+2] in ('', ' ', '.')) or ch == '.' and line[i-1:i] == '.'))
        if sep: out.append(cur); cur = ''
        else: cur += ch
    out.append(cur)
    return out

def parse(text, vocab):
    """-> list of dict(raw, phrase, slugs, match, qualifier, flags). Section state persists across blank
    lines and nested [[spoiler]] wrappers until the next heading line."""
    items, q = [], 'present'
    for line in text.split('\n'):
        line = line.strip()
        if not line or re.fullmatch(r'\[\[/?spoiler[^\]]*\]\]', line):
            continue
        m = HEAD.match(line)
        if m:
            q = section_q(m.group('h')); line = m.group('rest').strip()
            if not line: continue
        line = re.sub(r'^[-*\u2022]\s*', '', line)       # bullet
        for tok in tokenize(line):
            raw = tok.strip().strip('.').strip()
            flags, qual = [], q
            if re.search(r'\bfor now\b', raw, re.I):
                raw = re.sub(r'\bfor now\b', '', raw, flags=re.I).strip()
            if not raw: continue
            base = raw
            if OPT_PAREN.search(base): qual = 'optional'; base = OPT_PAREN.sub(' ', base)
            if OPT_PREFIX.match(base): qual = 'optional'; base = OPT_PREFIX.sub('', base)
            for p in re.findall(r'\(([^)]*)\)', base): flags.append('note:' + p.strip())
            base = re.sub(r'\([^)]*\)', ' ', base)
            base = re.sub(r'\s+', ' ', base).strip()
            if re.search(r'\w\([^)]*\)|\b(is|are|etc|as)\b', raw) and len(raw.split()) > 5: flags.append('prose')
            slugs, match = lookup(base, vocab)
            if match == 'none' and MODIFIER.match(base):
                slugs, match = lookup(MODIFIER.sub('', base), vocab)
                if slugs: flags.append('modifier:' + MODIFIER.match(base).group(0).strip(" '").lower()); match = 'synonym'
            if match == 'none' and '/' in base:
                parts = [lookup(p, vocab) for p in base.split('/')]
                if all(p[0] for p in parts):
                    slugs = [p[0][0] for p in parts]; match = 'synonym'; flags.append('compound')
            if q == 'possible': flags.append('possible')
            if 'prose' in flags: qual = q; slugs, match = [], 'none'
            items.append(dict(raw=raw, phrase=base, slugs=slugs, match=match, qualifier=qual, flags=flags))
    return items

if __name__ == '__main__':
    vocab = load_vocab()
    recs = [json.loads(l) for l in open(sys.argv[1] if len(sys.argv) > 1 else os.path.join(HERE, 'samples.jsonl'))]
    for r in recs:
        for it in parse(r['genre_text'], vocab):
            print(r['thread_id'], json.dumps(it, ensure_ascii=False))
