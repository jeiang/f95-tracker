# F95 Tracker

Single-user tracker for adult games played from F95zone and manual sources.

## Language

**Game**:
A title the user tracks, with its own ratings, versions, and statuses.
_Avoid_: thread, entry

**Source**:
Where a Game's information comes from: an F95zone thread, an itch.io page, or a manual link the user maintains.

**Play status**:
The user's own relationship to a Game (for example planned, playing, finished, dropped, on hold). Set only by the user.
_Avoid_: status (unqualified)

**Dev status**:
The developer's state of a Game as reported by its Source (ongoing, completed, abandoned, on hold). Fetched, never set by the user for F95zone Games.
_Avoid_: status (unqualified)

**Game tag**:
A tag attached to a Game, with an origin (F95zone tag list, parsed Genre text, both, or entered by hand), a qualifier (present, planned, or optional), and a verification (unverified, confirmed, or wrong). Refers to either an F95zone tag or a **Custom tag**.

**Custom tag**:
A tag for a Genre phrase or hand-entered concept that has no F95zone tag (for example BBW, Threesome).

**Synonym**:
A user-editable mapping from a Genre phrase to an F95zone tag or Custom tag (for example Yuri to lesbian).

**Tag review**:
The user's post-play pass that marks a Game's present tags confirmed or wrong. Games with a skipped or pending review are in the tags-to-review queue.

**Genre text**:
The free text under the Genre heading of an F95zone thread, parsed into Game tags. Markers in it set the planned or optional qualifier.

**Play log**:
An append-only record of (date, version) entries made when the user marks a version played. The newest entry is the last played version. Entries from the CSV import have no date and the origin "imported".

**Behind**:
A Game whose last played version differs from its latest version at the Source, ignoring case, surrounding whitespace, and a leading "v".

**Update**:
A change of the latest version at a Game's Source since the last check. Alerts are sent only for Games whose Play status is in the configured alert set.

## Relationships

- A **Game** has exactly one **Play status** and at most one **Dev status**.
- A **Game** has one primary **Source**. An F95zone thread is exactly one **Game**; a remake, rework, or part 2 posted on the same thread is the same **Game**. A sequel on a different thread is a different **Game**.
- A **Game** has a rating from 0.5 to 5 in 0.5 steps, or no rating.
- A **Game tag** is confirmed or marked wrong by the user only in a **Tag review**, after play. Confirming the parse when a Game is added does not verify any tag.

## Flagged ambiguities

- The legacy CSV columns Abandoned / Completed / On Hold record **Dev status**, not **Play status**.
