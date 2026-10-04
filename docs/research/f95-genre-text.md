# F95 Genre text and tag formats (ticket #3)

Status: **answered.** All 42 sampled threads were read logged-in (0 unreadable). Genre text is in `samples.jsonl` (`genre_text`, status `read-logged-in`); the rule set is executable in `docs/research/f95-genre-text/parse_genre.py`.

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

## Genre text, logged-in (42/42 read)

Loaded each of the 42 threads once while signed in (about 45 F95 page loads at 4.3 s spacing, read-only), plus one extra load of 101484 to check tag visibility (see "F95 tag list differs by login").

### Where the Genre text is in the DOM

First post only: `article.message--post .bbWrapper` (first match). Direct children of `.bbWrapper` are `<b>Genre</b>`, a text node `:`, `<br>`, then the spoiler. Rule used (works 42/42): find the first direct child whose text starts with "Genre" and is shorter than 60 chars (it is `<b>` in 42/42), then take the next direct child with class `bbCodeSpoiler`; the text is `.bbCodeSpoiler-content > .bbCodeBlock--spoiler > .bbCodeBlock-content`. Line breaks are `<br>`; paragraphs are blank lines (double `<br>`). The spoiler button title is empty in 42/42 (label "SPOILER"), so there is no title to key on; the heading `<b>Genre</b>` is the anchor. The next block is `Installation` 41/42, `Changelog` 1/42 (97871). Nested spoilers appear inside the Genre block 1/42 (90433; `.bbCodeSpoiler` inside the content).

### Layouts (42 threads)

| Layout | Threads |
|---|---|
| One comma-separated line, nothing else | 33 |
| Comma list + blank line + `Planned:`-style heading + second comma list | 5 (37986, 80371, 93994, 98051, 186046) |
| Planned list as one `-item` per line, blank-line separated (bullets) | 1 (176520, 6 items) |
| Planned list inside a nested spoiler under `Future tags:` | 1 (90433) |
| Several headed sections (`Currently in Chapter 1 & 2:`, `Planned:`, `Possible (...)`) | 1 (177547) |
| Comma list + free-text paragraph describing a feature | 1 (74436, `Gender Bender(...)` prose with its own optional clause) |

Counting blocks by blank lines: 1 block 33, 2 blocks 5, 3 blocks 2, 6 blocks 1, 8 blocks 1 (sums to 42; the six-and-eight-block ones are 177547 and 176520). Small formatting noise: all-lowercase list 12/42; trailing comma 6; trailing period 4; double spaces / leading spaces 8; duplicated phrase 4 (110316 Male Protagonist x2, 151505 creampie x2, 176520 voyeurism/masturbation appear both present and planned).

Item counts: 785 phrases across 42 threads (147 distinct normalized phrases); per thread median 16.

### Planned / optional markers

No marker was found for `toggleable`, `(WIP)`, `not yet`, `can be disabled` (0/42). What exists:

| Marker | Form | Threads (examples) | Items |
|---|---|---|---|
| `Planned:` heading | section until next heading | 37986, 80371, 98051, 177547 | 3+4+9+8 (+3 optional in 177547) |
| `Planned content:` | section | 93994 | 16 |
| `Planned Tags:` + bullets | section | 176520 | 6 |
| `Planned tags:` | section | 186046 | 1 planned + 2 optional |
| `Future tags:` + nested spoiler | section | 90433 | 3 |
| `(optional)` / `(Optional)` | trailing paren on an item, with or without space | 39572 (`Futa/trans (optional)`, `Polyamory (optional).`), 49572 (`Bloody Deflowering (Optional)`), 61893 (3), 177547 (3), 186046 (`Pregnancy(optional)`, `Footjob(optional)`) | 11 |
| `(avoidable)` | trailing paren, means optional | 177547 `Lesbian (avoidable)` | 1 |
| `optional ` prefix | leading word | 273938 `optional trap` | 1 |
| prose `... is optional)` | inside a paragraph | 74436 `Gender Bender(...)` | 1 (not parseable; manual) |
| `Possible (Most to least possible):` | section, not planned/optional | 177547 (4 items) | 4 |
| `Currently in Chapter 1 & 2:` | section = present | 177547 | 20 |
| `(soft)` / `'Soft'`, `Mild`, `Light` | severity modifier, not a qualifier | 39572 `Vore (soft)`, 74436 `'Soft' Horror`, 50488 `Mild BDSM`, 45823 `Light Humiliation` | 4 |
| `... for now` | trailing prose | 177547 `Stripping... for now` | 1 |

Totals: threads with any planned/future section 8 (50 planned items); threads with inline optional 6 (+74436 prose) with 13 optional items; threads with "possible" 1.

Not in the glossary: the qualifier "possible" (177547) and the "currently in" scoping. Note too that the Genre section is not always about the current version: planned lists describe future versions, and 177547 scopes the present list to chapters 1 and 2.

### Mapping Genre phrases to the F95 vocabulary (115 game slugs, assets excluded)

Of 785 phrases: **719 exact** (91.6%), **31 synonym/variant** (3.9%), **35 no F95 tag** (4.5%; 33 distinct). Exact means normalized key (lowercase, only letters/digits) equals the key of a vocabulary slug, which absorbs case, spacing, hyphens and slash variants (`Male protagonist`, `Hand job`, `DatingSim`, `Futa/trans`, `Sex Toys`, `Female domination` vs slug `femaledomination`). 86 distinct vocabulary slugs are hit by Genre text.

Synonym pairs found (phrase -> slug; "ok" = slug is also in that thread's F95 list):
- spelling/typo: `haren` -> harem (ok), `3DGC` -> 3dcg (ok), `Possesion` -> possession (ok), `Superpower` -> superpowers (planned, F95 lacks it)
- short forms: `futa` x4 / `Futanari` -> futa-trans (ok, 5/5), `Oral` -> oral-sex, `Anal` -> anal-sex (ok), `Vaginal` -> vaginal-sex (ok), `Group` -> group-sex (ok), `Toys` -> sex-toys (ok), `point and click` -> point-click (ok), `Femdom` -> femaledomination (ok), `Large breasts` -> big-tits (ok)
- different word: `Netorare`/`Netori` -> ntr (1 ok / 1 not in F95 list), `Blowjob` -> oral-sex (ok), `Boobjob`/`Boobs Job`/`Titjob` -> titfuck (ok x3), `Tentacle Sex` -> tentacles (ok), `Yuri` -> lesbian (ok), `Violence` -> graphic-violence (F95 lacks), `futa protagonist` -> futa-trans-protagonist (F95 has futa-trans instead)
- modifier + tag: `Mild BDSM`, `Light Humiliation`, `'Soft' Horror` -> bdsm / humiliation / horror (ok x3)
- compound: `Group/Harem` -> group-sex + harem (planned, 176520)

Synonym corroboration: 25 of 31 synonym hits have their slug in the same thread's F95 list (the other 6: 4 are in "planned" sections, which F95 would not list yet; `Violence` and `futa protagonist` are real ambiguity).

No F95 tag (35 items, 33 distinct): 2D, BBW, Weight Gain, Breast Expansion, Big Dick, Elves, Nuns, Witches, Squirting, Swallow, Threesome, Impregnation, Small tits, Hand-Holding (x2), Defloration/Bloody Deflowering (x3), Marathon Sex, Partially Voiced, Uncensored, Male Prostitution, Reverse Rape, Face Sitting, Anallingus, Cunnilingus, Feet, Psychedelic, Vanilla, Visual Novel, Polyamory, Mind break, Fetish, Dom/Sub, Gender Bender(prose). Several are real F95 tags we do not have: the 154-entry vocabulary is a snapshot of F95Checker's enum, which is clearly missing live tags (`Threesome`, `Impregnation`, `Elves`... exist on F95 [INFERENCE: F95 tags beyond the enum, not verified]). Others are not tags at all (`Visual Novel`, `Uncensored`, `Partially Voiced`).

### F95 tag list differs by login (finding)

The F95 tag list in `samples.jsonl` came from guest HTML. Logged in, thread 101484 shows 19 tags vs 17 for a guest: `incest` and `school-setting` are hidden from guests (observed: `/tags/incest/`, `/tags/school-setting/` links in the logged-in page; absent in guest list). Across the 42 guest lists these slugs never appear even though Genre text lists them: incest (10 threads), school-setting (8), twins (4), mind-control (4), monster (4), sleep-sex (3), rape (2), sexual-harassment (2), drugs (2), blackmail, slave, bestiality, vore, futa-trans-protagonist (1 each). So **guest tag lists omit a class of tags, probably the "adult/extreme" ones**; the scraper must read tags logged in or "absent from F95 tags" figures will be inflated. [INFERENCE for every slug other than incest and school-setting; I did not have page budget to reload the other threads.] For 101484 `samples.jsonl` has `f95_tags_logged_in` (19). All figures below use the guest lists.

### Absence measurements (guest F95 lists vs parsed Genre text)

- F95 tags absent from Genre text (any qualifier): 233 of 874 tag instances (26.7%); per thread min 0, mean 5.5, max 26 (thread 67494: 4-phrase Genre, 30 F95 tags). 4 threads have 0 absent (36056, 135682, 136746, 150543).
- Genre slugs absent from F95 list: 106 of 747 (14.2%); per thread mean 2.5, max 17. Counting only present (non-planned) Genre items: 59 (mean 1.4); 44 of them are slugs that never appear in any guest F95 list (the hidden-tag list above) and 15 are tags that other threads' F95 lists do have (real Genre/F95 disagreement).
- Planned, optional and possible items account for the other 47 of the 106.
- After counting only present Genre items, F95 tags with no Genre mention: 247 (28.3%).
- Direction note: F95 tag absent from Genre means "unverified" per CONTEXT.md; with 27% of tag instances in that state the unverified queue is large for this sample.

## Parser rules (executed)

`parse_genre.py` implements them; run over the 42 samples it produced the numbers above.

1. Take text, split lines. Blank lines and `[[spoiler]]` wrappers never reset the current section.
2. Heading line = `^(planned|future|possible|currently)...:` ; section qualifier: planned/future -> planned, currently -> present, possible -> `possible` (human). The text after the colon is the first item row. Initial section is present.
3. Strip bullets (`-`). Tokenize on `,`, `. ` and `...` outside parentheses (parentheses may contain commas: 74436). Drop empty tokens, trailing `.`, `for now`.
4. Marker rules: `(optional)`/`(avoidable)`/`(toggleable)`/`(can be disabled)` as a full parenthetical, or a leading `optional `, sets `optional`. Other parentheticals are kept as a note (`Vore (soft)`). Phrases longer than 5 words with parentheses/prose go to manual.
5. Normalize key = lowercase, only a-z/0-9 (kills case, spaces, hyphens, `/`). Match key to vocabulary slug keys (exact), then to the synonym table, then retry after stripping a leading `light|mild|soft` modifier, then split on `/` and match each side.
6. Synonym table seed: see `SYN` in the script (29 entries, 2 of them explicit `None` = known unmatched).

### Accuracy against manual reading

Method: I read all 42 Genre texts and checked every parser output that was not (exact match, present): 124 items (including 4 modifier/compound forms), plus scanned the 661 exact-present items. Result: 785/785 phrases got the slug set and qualifier I would assign by hand, after fixing 3 bugs found in the first pass (commas inside parentheses split 74436; `optional` in prose wrongly set the qualifier; modifiers like `Mild BDSM` unmatched). 12 items I would not auto-accept (below). Caveat: the same person wrote the rules and the gold, so this is not independent; the exact-match items are only spot-checked.

## Recommendation

1. **Use the first-post DOM rule above, signed in.** Genre is hidden from guests and so are some tags (incest, school-setting at least). The scraper must use a logged-in session for both. (This affects the F95 access design.)
2. Parser: apply the six rules above. Auto-accept: exact match (91.6%) and synonym with a note; qualifier from section/marker. Keep the synonym table editable and the vocabulary refreshable; seed it from `SYN`.
3. Store phrases with no match (35, 33 distinct) as free-text Genre tags with `unmatched` origin rather than dropping them; most are real concepts (BBW, Threesome, Elves).
4. Needs human confirmation: `Possible` section items (177547, 4); prose clauses (74436 `Gender Bender(...)`); `2D` (2d-game vs 2dcg); `Dom/Sub`; `Group/Harem` compound; `Feet` (footjob?); `Violence` -> graphic-violence; `futa protagonist` -> futa-trans-protagonist (F95 list has futa-trans); `Netori` / `Superpower` / `Oral` (not corroborated by the F95 list); `Defloration` x3 (no tag); items after `for now`.
5. Planned sections apply to future versions; qualifier `planned` for the whole section, and mark `Currently in ...` sections present. Ignore duplicates within a thread (4 seen); if the same slug is both present and planned (176520), keep present.
6. Do not treat F95 "tag absent from Genre" as wrong; with guest lists the absence rate (26.7%) is inflated by hidden tags, and the logged-in rate is not measured (needs another ~41 loads).
