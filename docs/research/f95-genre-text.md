# F95 Genre text and tag formats (ticket #3)

Status: **partially answered.** The F95 tag vocabulary and F95-tag statistics are done. The Genre text itself could not be read: F95zone hides it from logged-out visitors and the signed-in browser relay was not available this run. See "Blocked on logged-in access".

## What was observed

### Genre text is locked for guests (42/42)

Fetched 42 threads from the CSV as a guest (stratified by rating, seed 7; ~44 F95 page loads, 3.2 s apart, no account). In every one, the first post reads `Genre:` followed by a `Spoiler` block whose content is replaced with "You don't have permission to view the spoiler content. Log in or register now." Example: `https://f95zone.to/threads/31912/` (Corrupted Kingdoms). The guest HTML also omits the text from the embedded JSON (`Genre:\n\n\nInstallation:`), so nothing leaks.

- 42/42 sampled threads: Genre is inside a Spoiler block (so "spoiler block" is the dominant layout, at least for this list).
- 41/42 follow the order `Genre → Installation`; 1/42 (97871 UFO) follows `Genre → Changelog` (no Installation spoiler).
- The F95Checker indexer (`https://api.f95checker.dev/full/<id>`) returns tags as numeric ids, description, changelog, etc., but no Genre field (checked on 31912).
- Wayback Machine has no snapshot for thread 31912 (`https://archive.org/wayback/available?url=f95zone.to/threads/31912/` returned empty).
- Browser relay: `browser.open({app:{relay:true}})` failed with "omp browser relay is serving at http://127.0.0.1:9224 but its extension never connected". No user tab was touched.

Consequently there is no data for: layouts, planned/optional markers and their counts, Genre-to-tag mapping stats, "F95 tag absent from Genre" and the reverse.

### F95 tag vocabulary (done)

- Source: `Tag` enum in `https://raw.githubusercontent.com/WillyJL/F95Checker/main/common/structs.py` (line ~400). 154 entries: 153 real tags + `unknown` (id 154). Saved to `docs/research/f95-genre-text/f95-tag-vocabulary.tsv` (slug, label, F95Checker id).
- 38 of them are `asset-*` tags (for asset threads, irrelevant to games). So ~115 are game tags.
- Slugs are the `/tags/<slug>/` URL segment; the parser in `common/parser.py` (~line 390) reads tags from `href="/tags/..."` links and records unknown slugs separately, i.e. the site tag list can grow beyond the hardcoded enum.
- Validation: across the 42 sampled threads, all 82 distinct tag slugs were in the vocabulary (0 unknown). So the hardcoded list covers this list fully, but it is only a snapshot; keep the vocabulary table editable/refreshable.
- The `label` column in the TSV is the slug with `-` replaced by space and is only a rough display label (F95 slugs such as `maledomination`, `femaledomination` have no hyphen; the human label is "male domination"). The F95Checker `text` field per tag holds the exact label. Slugs matched the live pages: `/tags/maledomination/` (8 threads) and `/tags/femaledomination/` (11) appear in the sampled HTML and in the vocabulary.

### F95 tag lists in the sample (done)

Per thread: min 7, mean 20.8, max 37 tags. Raw per-thread lists in `docs/research/f95-genre-text/samples.jsonl` (`genre_text` is null, status `locked-for-guests`). Most frequent: male-protagonist 41/42, vaginal-sex 39, 3dcg 35, animated 34, big-tits 31, oral-sex 31, masturbation 28, mobile-game 27, big-ass 26, harem 26.

Engine mix in the sample is not diverse: titles start with Ren'Py 40 (18 with a `VN -` prefix), Unity 2. The CSV is Ren'Py-heavy, so engine coverage is limited by the data, not by the sample size.

Rating mix: 5:8, 4.5:6, 4:10, 3.5:5, 3:7, 2.5:1, 2:3, 1:1, none:1.

## Blocked on logged-in access

Needed (all signed-in only), to finish this ticket:

1. Open the first post of the 42 threads in `samples.jsonl` (ids listed there) while logged in, expand the Genre spoiler, and save the text (text only) into `genre_text`. About 42 page loads at ≥3 s spacing; one dedicated named tab via the relay.
2. Then: catalog layouts and planned/optional markers with counts; map phrases to the vocabulary (exact / synonym / none); compute both-direction absence rates; tune the parser below.

Alternative that avoids the relay: the user pastes or exports the Genre spoiler text for the sampled ids.

## Recommendation

Cannot be answered yet on data; what holds today and what to do:

1. The Genre text is invisible to guests, so the app's scraper **must use a signed-in session** (cookies/token stored as secrets) to read it. A guest fetch still gives title, version, tag list, and the Overview. This affects the F95 access design (ticket on F95 access).
2. Keep F95 tags (from `/tags/<slug>/` links, public, 100% in the vocabulary for 42/42 threads) as the baseline Game tag origin, and parsed Genre text as a second, optional origin, which matches the glossary (origin: F95 list, Genre text, or both).
3. Parser plan to validate once samples exist: split the Genre spoiler on commas, newlines and `/`; lowercase and strip; detect qualifier markers by regex on a phrase or its parent line/heading (candidates to count: `(planned)`, `planned:`, `optional`, `toggleable`, `(WIP)`, `not yet`); look up the phrase in the 153-slug vocabulary, then in a hand-kept synonym table; anything with no match, a marker nested in prose, or a heading line goes to a manual-confirm queue. Tags in the F95 list but absent from the Genre text stay unverified per the glossary.
4. Do not claim a parser accuracy figure until the 42 Genre texts are collected. The ticket should stay open.
