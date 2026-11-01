# In-play data contract (backend → web, Flutter, and any later client)

Status: specification. Implementation status per section is marked
**[built]**, **[partial]** or **[to build]**.
Owner: backend. Consumers: `apps/web`, `apps/mobile`.

This file is the contract only. It deliberately does not describe screens or
components — UI design lives elsewhere and is owned by another workstream.
Where the UI needs an input this contract does not yet provide, that is a gap to
raise against this file rather than a thing to invent client-side.

## 0. The two record types, and why they are separate

This is the most important structural decision here, and getting it wrong is
expensive in both directions.

| | `engine_predictions` | `inplay_snapshots` |
|---|---|---|
| Purpose | the auditable, settleable **published prediction** | the **time series** a client draws |
| Grain | one row per market per publication | one row per fixture per tick |
| Lifecycle | superseded, settled, reversible | append-only, never settled |
| Consumers | settlement, accuracy, audit | graphs, before/after figures, tracker |
| Volume | unchanged content is touched rather than versioned | ~24k rows/match-day |

A client asking "how did this match's probabilities move" must read snapshots,
not predictions. A settlement or accuracy question must read predictions, not
snapshots.

**Why not one table.** The 60-second timer requirement (§2) multiplied by the
dynamic market ladder is roughly 75 market rows per fixture per tick — about
1.8M rows on a Saturday. As a snapshot header carrying its markets as a JSONB
payload it is ~24,000 rows instead, and the graph reads one row per point.
Settlement keeps its own row-per-market grain because each published market
needs its own terminal state, supersede timestamp and audit envelope. Merging
the two would either make the graph unaffordable or make settlement
un-auditable.

One healthy regeneration supplies the complete current set of publishable
market keys. `inplay_publication_state` records that set and its generation time
atomically with the prediction writes. Readers serve only its member keys;
older unsuperseded rows remain available to settlement and audit, but are no
longer current display predictions. An unchanged member can keep its older row
and cutoff when its content is identical to the new engine read. Until a fixture
has a publication state, readers conservatively show only rows from its newest
published generation, avoiding a mixed-vintage ladder during migration.

## 1. Identity — one id per thing, no exceptions

**[built]** and load-bearing. This is where an earlier defect cost real money:
team history was split across two ids, so the engine saw 6 Bayern matches
instead of 15 and the quality gate skipped fixtures entirely.

- **Match id.** There is exactly one. `fixtures.id` is a `BIGINT PRIMARY KEY
  REFERENCES matches(id)` (`000010_prediction_engine_contract.up.sql:2`), so the
  engine fixture and the match share a primary key by database constraint, not
  by convention. Every in-play table keys on it: `fixture_live_snapshots`,
  `fixture_events`, `engine_predictions`. **Clients use this id and no other.**
- **Team id.** Canonical `teams.id`. Provider team ids are *not* canonical and
  must never be used as such.
- **Resolution is mandatory.** Provider ids resolve through
  `provider_entity_maps` via `EntityMaps.LookupCanonicalEntityID(ctx, provider,
  "team"|"match", providerID)`. `statistics_job.go:216` is the reference
  implementation.
- **An unmapped entity is skipped, never guessed.** `statistics_job.go:220`
  logs and skips. `ReconcileScoringEvents` bails out entirely on a goal it
  cannot attribute. Inventing an id is how the split happened.

**Rule for the `fixtures?ids=` work (§7):** the embedded `statistics`,
`lineups` and `players` blocks all carry **provider** team ids. Every one goes
through `LookupCanonicalEntityID` before it touches a canonical table. No
exceptions, and no `int(providerTeamID)` shortcuts.

### Team crests are decoration, not identity

**[built]** 2026-10-15 (task `F1`). The fixture payload on every
`/v1/inplay/` route carries two extra keys:

| Key | Type | Meaning |
|---|---|---|
| `home_team_logo` | string, always present | crest URL, `""` when the team has no logo on record |
| `away_team_logo` | string, always present | as above |

Three properties a client may rely on, and a fourth it must not assume away:

1. **Ungated.** A crest is not model output, so it is served identically to
   both viewer tiers. The payload still differs only in `locked` and the
   presence of `prediction`.
2. **Always present, empty rather than null.** Same convention as `acca.Leg`:
   the clients key on the empty string to choose between an `<img>` and a drawn
   initials shield, and an absent key would make them handle one gap two ways.
3. **Never identity.** A fixture is `fixture_id` and a side is resolved from the
   fixture's own `teams.id` values, exactly as §1 requires. Nothing keys on a
   logo URL, and a fixture whose teams have no crest is still served — a missing
   crest never removes a row from the board.
4. **No team names here.** The in-play payload still carries no `home_team` /
   `away_team`; a client that needs the names reads them from the match payload
   and joins on `fixture_id`, which is what `apps/web` already does. If a client
   needs names on this payload, raise it here rather than deriving them.

## 2. Snapshots — when one is written

**[built]** — `inplay_snapshots` and `inplay_final_snapshots` (migration 000079), written on the live path by the in-play runner on its own 60-second cadence, and frozen at full time by the `predictions.snapshots.finalise` job.

A snapshot is written on each of:

| Trigger | `trigger` value | Note |
|---|---|---|
| Timer, every 60s of play | `timer` | unconditional while the match is live |
| Immediately before a goal | `pre_goal` | see below |
| Immediately after a goal | `post_goal` | |
| Before / after a red card | `pre_red_card` / `post_red_card` | second yellow counts as a red |
| Before / after a VAR decision | `pre_var` / `post_var` | includes the retraction case |
| Period change | `period_change` | half time, second half start |
| Full time | `final` | **frozen and kept permanently** |

**"Just before" is a link, not a synthesis, in the normal case.** With a 60s
timer the most recent prior snapshot is at most 60s stale, so the before-state
is the newest snapshot whose `data_cutoff_at` is strictly earlier than the
event's `observed_at`. A snapshot is synthesized only when no such snapshot
exists within the timer interval — at kickoff, or after a gap in the feed.
Synthesizing unconditionally would write two rows per event that say the same
thing as the timer row next to them.

**The final snapshot is retained forever.** Every other snapshot is subject to
partition retention; `trigger = 'final'` is not. It is the permanent record of
what the model said at the whistle, and it is what a "how did we do" view reads
long after the partitions are dropped.

**Retention.** Partitioned by `captured_at` by month, matching
`fixture_live_snapshots` and `fixture_events`, so retention is a partition drop
rather than a bulk delete on a VPS tight on memory.

## 3. Sequence and stable ids

**[built]** — one global `inplay_stream_sequence`, drawn by both
`inplay_snapshots` and `fixture_events`, so a single cursor orders the whole
stream. Global monotonic is also monotonic per fixture, which is what the rule
below requires.

- Every snapshot and every event carries `sequence`: a **strictly increasing
  integer per fixture**, shared across both record types, allocated on write.
- `sequence` is the **stream resume token** (§6). A client that reconnects sends
  its last `sequence` and receives everything after it.
- Strictly increasing, not gapless. A client must not infer a dropped message
  from a gap.
- **Event stable id.** `fixture_events.identity_key` is the stable identity:
  derived from fixture, minute, extra, type, detail, team and player, because
  the provider sends **no event id at all**
  (`000070_fixture_events.up.sql`). Clients key markers on `identity_key`, never
  on the surrogate `id`, which is per-observation.
- **VAR-overturned goals are marked void, never deleted.** `is_retracted` plus
  `retracted_at` already exist for exactly this, and the row stays for audit. A
  client removes the marker when it sees `is_retracted: true` — it does **not**
  need to diff lists to discover a disappearance, and must not, because a goal
  absent from a partial feed is not a retracted goal (§5).

## 4. Probabilities — integer basis points

**[to build]** at the API boundary.

- Wire format is an **integer out of 10,000**. `6412` means 64.12%.
- **Storage stays floating point.** Conversion happens at the API boundary, not
  in the database. Rounding on write would lose precision the model and the
  calibration work need, and would make a stored probability disagree with the
  matrix it came from.
- **Selections sum to exactly 10,000 per mutually exclusive GROUP, not per
  market.** Most markets are one group. **A line ladder is not**: it carries an
  over/under pair per line, so a five-line market sums to 50,000 and each pair
  sums to 10,000. Reading a ladder as one group is a real defect that shipped
  once -- an `over_1.5` at 81.65% went out as 1633 basis points.
- **Within a group, selections must sum to exactly 10,000.** Naive per-selection
  rounding does not: three selections at 33.333% round to 3333 each and sum to
  9999, and a client rendering 99.99% looks broken. Use **largest remainder**:
  floor every selection, then distribute the shortfall one basis point at a time
  to the largest fractional parts. Assert the sum in a test.
- Determined markets are not sent as probabilities at all; they are sent as
  settled outcomes (§5).

### Model health

Every snapshot carries `model_health`, derived from the existing
`SuppressionReason` rather than a second opinion:

| `model_health` | From | Meaning for a client |
|---|---|---|
| `ok` | `SuppressionReason.NONE` | show the numbers |
| `stale` | `STALE_DATA` | show last known, marked stale; do not imply it is current |
| `suspended` | `NOT_IN_PLAY`, `EXTRA_TIME_NOT_MODELLED`, `MISSING_SCORE`, `MISSING_PRE_MATCH_RATES` | show no probabilities |

§14 of the architecture is that a prediction on stale data is **suppressed,
never extrapolated**. `stale` therefore means "this is what we last knew", and a
client must not present it as live. Extra time is genuinely not modelled today.

## 5. Settlement state a client can render

**[built]** for the settlement arms; **[to build]** for reversal.

Each published market carries a terminal state once decided:

- `won` / `lost` — graded.
- `void` — a market rule voided it (a drawn draw-no-bet).
- `unsettled_data_unavailable` — **closed, not pending.** Full time closes every
  market. A corners or fouls market on a fixture whose provider never supplied
  the statistic closes here: excluded from accuracy, never left hanging. This is
  the deliberate answer to per-league coverage gaps — publish the market, close
  it honestly.
- `settlement_reversed` — **[to build]** the provider withdrew the evidence. The
  prior settlement row is retained; the reversal is shown, not hidden. This can
  arrive **after full time** with no time bound.

### How settlement reaches a client

**[built] 2026-10-15.** The vocabulary above predates the wire field: the
grader has written these states to `prediction_results` since migration
`000077`, but nothing served them, so "full time closes every market" was true
of the database and unobservable to a reader. Each market in the `/v1/inplay/`
payload now carries:

```json
{ "key": "btts", "label": "btts", "locked": false,
  "prediction": { "selection": "yes", "probability_bp": 6200, "...": "..." },
  "settlement": "won" }
```

Four rules, each of which a client should rely on:

- **`settlement` is one of `won`, `lost`, `void`,
  `unsettled_data_unavailable`.** It is the four words above, not the
  database's `settlement_status` — that column separates "did we grade it" from
  "were we right", which is the right shape for a grader (a void is not a wrong
  answer and must never be counted as one) and the wrong shape for a chip.
- **The key is absent while a market is ungraded**, which is most of its life.
  Absent means "not graded yet". It is never sent as an empty string, because
  that would make each client invent a meaning for `""`.
- **It is gated with `prediction`, and that is forced rather than chosen.** The
  score is public. An anonymous reader who could see `"settlement": "won"` next
  to a visible 1-1 would know the BTTS pick was Yes — the gate would leak the
  exact selection it exists to protect. A locked market therefore carries
  `locked: true` and neither a prediction nor a grade. Pinned by
  `TestSettlementIsNeverVisibleToAnAnonymousViewer`.
- **A grade can change after full time, so do not cache it as final.**
  Settlement is reversible with no time bound (the `settlement_reversed` arm
  above). The field reflects the *current* grade; `prediction_result_revisions`
  is the history, and is not on this wire.

**Known gap, deliberate:** the grade is keyed on the **currently published
prediction's own id**, so it is only ever shown beside the pick it graded. If a
market was graded and that generation was later superseded, this reads ungraded
rather than attributing the older generation's result to the selection now on
display. Under-reporting is safe; mis-attributing is not. Closing it properly
means deciding what "the market's settlement" means across generations — a
question for the in-play workstream, not for a read path to answer on its own.

**Markets are only offered where the data exists.** A market whose underlying
statistic is absent for that fixture is not published for that fixture. Where it
is present, it is published. Decided per fixture on observed data, not per
league from a static list.

**A line's market key carries the line.** A published ladder row is
`market: "total_goals_match_9.5"`, `prediction: "over"` — not
`total_goals_match` with `over_9.5`. This is required rather than incidental:
supersede keys on `(fixture_id, market)`, so lines sharing one key would
supersede each other and only one could be current. A client grouping a family
of lines should match on the key prefix.

**Published rows are filtered by material change.** A selection whose
probability moved less than ~2 percentage points does not get a new row; the
standing row stays current and un-superseded, carrying the cutoff at which we
last had reason to revise it. So the absence of a new row means "unchanged", not
"withdrawn". **Snapshots are not filtered** — the series has a point for every
tick.

**Early settlement is normal, not exceptional.** A market settles the moment its
outcome is invariant under every reachable future — FT over 1.5 with two goals at
minute 10 is *decided*, and a client should render it as won immediately while
every other market on that fixture keeps updating. Settlement is per market.

## 6. Transport

**[built]** — three routes, all under `/v1/inplay/`, all through the same filter.

| Route | Purpose |
|---|---|
| `GET /v1/inplay/overview` | combined first load: board, series, timeline, cursor |
| `GET /v1/inplay/stream` | resumable SSE; the `id` of each frame IS the cursor |
| `GET /v1/inplay/changes?since=N` | polling fallback, same query as the stream |

Resume with `?since=N` or the `Last-Event-ID` header — the header is what a
browser `EventSource` sends by itself. A valid header takes precedence over
`since`, because an automatic reconnect reuses its original URL while updating
the header. Without a valid header, a valid `since` supplies the initial cursor;
if neither is valid, replay starts at zero.

`changes` advertises `X-Poll-After-Seconds: 15`, matching the stream's own poll.
A connection is bounded (30 min by default) and announces `event: reconnect`
with the cursor, so a deliberate close is distinguishable from a network drop.
The browser resumes from the last change it successfully applied. A malformed
frame or deliberate reconnect switches to polling from that applied cursor;
the reconnect notification alone must not advance it. Database reads inherit
the stream lifetime and a shorter query deadline. Each write has its own
deadline, so ordinary HTTP response timeouts do not terminate a healthy stream
and a slow reader cannot hold an unbounded write.
`more: true` means the limit truncated the set and the client should ask again
immediately rather than fall further behind.

1. **Combined first load.** One call returns fixture state, the current market
   set, the event timeline, the snapshot series for the graph, and the current
   `sequence`. A cold client must not need to fan out.
2. **Live stream**, resumable. The client sends its last `sequence`; the server
   replays from there. Sequence is the only resume token — no timestamps, which
   are not totally ordered across a restart.
3. **Polling fallback at 15s**, returning everything after a supplied
   `sequence`, so the fallback and the stream share one server-side query and
   cannot disagree.

15s matches the provider's own refresh for `/fixtures` and `/fixtures/events`
(confirmed; see the scale plan §3b). Polling faster returns identical bytes.

**Everything in-play uses the same read policy and serializer.** The whole
`/v1/inplay/` prefix routes to one handler (`httpserver/inplay_handlers.go`).
Production explicitly enables public reads under [ADR0007](../architecture/decisions/0007-public-inplay-predictions.md):
guests can see published predictions without a session. The legacy gated policy
remains separately tested. Both policies retain suppression of unavailable or
suspended values; public reads grant no account or admin capability. New
transports must remain under this prefix so they receive the same filtering.

## 7. Ingestion cadence this contract assumes

**[partial]** — `live=all` is built. The `ids=` statistics poll is **built and
switched off**, waiting on one capture.

| Poll | Cadence | Cost | Supplies |
|---|---|---|---|
| `fixtures?live=all` | **60s** (`QUOTA_LIVE_POLL_SECONDS`) | **1 request, any number of matches** | score, clock, status, events, cards |
| `fixtures?ids=` in 20s | 60s, **switched off** | 1 per 20 live fixtures | statistics, xG, lineups, players |

The live cadence is 60s, not the 15s this table used to claim — and 15s was
never what ran, so the correction is to the document rather than a change of
behaviour. The provider does refresh `/fixtures` every 15 seconds, so 15s is
the fastest *useful* poll; it is not the affordable one. A full day at 15s is
5,760 requests against a 7,500/day package, and at 60s it is 1,440. See
`docs/operations/api-football-quota-budget.md`.

**This bounds what §6's 15s polling fallback can deliver.** A client polling
our API every 15 seconds is not wrong to do so — it costs us no provider quota
and the `sequence` cursor makes an unchanged response cheap — but three polls
in four will return nothing new. Treat 60s as the freshness this contract
promises for live score, clock and events, and 15s as the latency with which a
client learns about a change once we have it.

Full coverage of every match costs ~6,800 requests/day, insensitive to peak
concurrency. Against the retired 75,000/day plan that was affordable. Against
the 7,500/day package it is the whole package, so the batched `fixtures?ids=`
poll cannot be enabled for full coverage at this plan size -- its budget has
to come from somewhere, and there is nowhere for it to come from. See
`docs/operations/api-football-quota-budget.md`.

### What the statistics poll stores

`fixture_statistics_snapshots` (migration 000081) is an **append-only series**,
one row per fixture per team per capture, with exactly the columns and units of
`fixture_statistics`. A client does not read it; the in-play model and grading
do.

It is a second table rather than the existing one because `fixture_statistics`
is upserted and answers "what are this match's statistics". In-play asks how
many corners had been taken when a market was published at minute 30, and by
full time the upserted row has been overwritten with the final count.

**The live poll never writes `fixture_statistics`.** That table is what
settlement grades against at full time; a live write puts mid-match counts in
it, which is harmless only while the post-match pass always runs afterwards. A
market with no final statistics closes `unsettled_data_unavailable` (§5) rather
than being graded against a minute-70 number.

Both teams of one batch share a `captured_at`, so home and away are comparable
at the same instant. A payload with no clock records a NULL minute, never 0.

### Why it is off, and what turns it on

`INPLAY_STATISTICS_POLL_ENABLED` defaults to **false**. API-Football documents
`fixtures?ids=` as embedding statistics, but the nesting has never been observed
on a real response from this account, and `apifootball.fixtureStatisticsBlock`
is that assumption written down. A wrong assumption **does not error — it
decodes to zero teams**, so the poll would spend quota, store nothing, and make
every league read as having no corner or card data.

To enable it:

1. Run `services/worker/cmd/capturefixtureids` with a real key against fixtures
   that are live at that moment. It reports whether the assumed shape held and
   writes `testdata/fixtures-ids-embedded.captured.json`.
2. If the shape differs, `fixtureStatisticsBlock` is the one type that changes;
   the parser, writer, job and table are shape-independent.
3. Account for the poll's spend in the quota planner. Until then it is scheduled
   outside it, which is only safe because nothing is scheduled at all while the
   flag is off.
4. Set the flag.

The job warns loudly if fixtures come back and not one carries statistics —
the one symptom the gate itself cannot catch.

## 8. Pre-match picks and the tracker

**[built]** — `prediction_tracker_state` (migration 000080), written by
`refresh-trackers` over the live scope. Served as `tracker_state` on each entry
of a match payload's `predictions` array, and on the headline `prediction`
object of every match list item.

### `null` and `unknown` are different answers

`tracker_state` is **always present**. It is `null` when nothing has judged the
pick: the in-play pass has never covered this fixture, or it has not kicked
off. `unknown` is different — it is a verdict the engine reached, meaning it
looked and the match state cannot decide the pick either way.

Three rules, deliberately split between what the backend owes and what a client
owes:

1. **On the wire they are distinct, and both are sent.** The backend must never
   collapse one into the other. A system that cannot tell "never looked" from
   "looked and cannot decide" cannot tell a quiet night from a dead feed.
2. **A client must not treat `unknown` as "no verdict exists" in logic.** Not in
   a tally, not in an "all picks judged" check, and never as a settled `void`
   (§5). The web app's `TrackerTally` already depends on this: it excludes
   `unknown` from its counts, which is only possible because the value arrives.
3. **Presentation is the client's call.** The recommended default is no chip —
   a reader cares how their pick is doing, not that our corner feed is thin —
   and a surface with room may say something like "can't call this one yet".
   The contract does not specify pixels. It specifies that rule 2 holds
   whichever rendering you pick.

This replaces an earlier line reading "Show no tracker for `unknown`, not a
colour", which contradicted the paragraph above it: a value rendered as nothing
is indistinguishable from an absent one, so the contract was asking clients to
tell the two apart and to display them identically at the same time.

**Expect `unknown` to become common, not rare.** It is uncommon today only
because goals and cards are the markets producing trackers. The corner, shot
and foul ladders are built and waiting on one provider capture (§7), and every
fixture in a league without that data will read `unknown` for those markets at
scale. A rule of "render it as nothing" would then hide a systematic data gap
behind silence, which is why rule 2 exists and why the value stays on the wire.

`tracker_state` is also independent of `outcome` — a pick can read `won` on the
tracker while `outcome` is still `pending`, because settlement writes a
different record.

- A pre-match pick is **locked at kickoff**. Nothing published after
  `kickoff_at` may be served as a pre-match pick. This is not cosmetic: a leak
  here handed the paid in-play asset to anonymous readers through
  guest-readable `/v1/analysis/{id}` before it was fixed, and the guard is
  `generated_at <= kickoff_at`.
- **Tracker state is computed server-side** and sent as a value, so web and
  Flutter cannot derive it differently. Deriving it client-side guarantees the
  two apps disagree eventually.
- States: `on_track`, `off_track`, `won`, `lost`, `void`, `unknown`.
- **`on_track` means "would win if the match ended now"** — a report on the pick
  already made, not a second prediction. `off_track` includes a draw under a
  home pick: it would not win.
- **A decided pick is terminal, never `on_track`.** FT over 1.5 with two goals at
  minute 10 reads `won`. Only counting markets decide early, and only upward
  since a count never falls. BTTS decides **both** sides at once.
- **`unknown` is not `void`.** Void is a settled outcome; unknown means we
  cannot judge it from what we hold — a corners pick before any corner data, or
  a settlement that was reversed and is not yet re-decided. How to render it is
  settled above, under "`null` and `unknown` are different answers".
- The tracker grades through the same code as settlement, so it cannot contradict
  the grade the same state settles to.

## 9. Open gaps against this contract

1. ~~`inplay_snapshots`, its sequence allocator and the writer~~ **built**
   (000079, and the runner's own 60-second cadence). The full-time freeze runs as
   `predictions.snapshots.finalise` on the prediction cadence.
2. ~~Basis-point serialisation~~ **built**, in Go
   (`internal/predictions/basispoints`) and Python (`inplay_model.basis_points`),
   asserted against the same cases so the two cannot disagree.
3. ~~Stream and combined endpoint~~ **built** — §6. `Snapshot` gates its markets
   with unexported fields and a hand-written `MarshalJSON`, the same construction
   as `Market`, and the invariant battery now parses SSE frames so the stream is
   held to the same assertions as every JSON route.
4. ~~Settlement reversal and rollup recomputation~~ **built end to end**
   (§5). A retracted goal re-settles the affected rows and the accuracy rollup
   recounts them, as two sweeps attached to every settlement cycle.
   **This entry said "open" until 2026-10-02 and it was half right in the worst
   way**: every piece existed — the classifier, the revision history,
   `resettle_fixture`, `recount_reversed_results`, the `needs_recount` ledger
   column, both CLI commands, all tested — and no job ever invoked them, so a
   goal withdrawn by VAR left its settled result standing and its accuracy
   figures counting it. The sweeps were not late; they did not exist. They run
   in order now (re-settle marks `needs_recount`, recount clears it) on the
   settlement schedule, because splitting dependent sweeps across schedules is
   how one runs and the other silently does not.
5. `fixtures?ids=` capture — the **only** remaining step for Tier 2
   statistics. The parser, the series table (000081), the writer and the batched
   60-second poll are all built; the poll is switched off behind
   `INPLAY_STATISTICS_POLL_ENABLED` until a live capture confirms the embedded
   shape. See §7.
6. ~~Dynamic market ladder~~ **built** for goals. Lines are generated from the
   live count: a nine-goal match is offered `over_9.5` and reads `over_0.5`
   through `over_8.5` as won. Cards, corners, shots and fouls use the same
   generator and are waiting on Tier 2 statistics (§7).
7. ~~Server-side tracker state~~ **built end to end** (§8, migration 000080):
   computed by the engine, stored, and served as `tracker_state` on the match
   payload.
8. ~~The in-play model reading the statistics series~~ **built**. The model
   conditions counting markets on a Gamma-Poisson rate update (the pre-match
   expectation blended with the match's own observed rate, weighted by minutes
   elapsed) and publishes a ladder per statistic.
   - **`total_cards` publishes today.** Cards come from the event list in every
     bulk poll, so the card ladder needs nothing switched on. Market keys are
     `total_cards_<line>`, selection `over` or `under`, exactly like the goals
     ladder.
   - `total_corners`, `total_shots`, `shots_on_target` and `total_fouls` are
     built and publish nothing until the statistics poll is on (item 5).
   - **A statistic with no defensible rate publishes no market**, which is the
     same rule as §5's absent-market case: too little play to infer a rate and
     no prior, or a count still at zero by the half hour (indistinguishable from
     a feed that is not reporting it). Expect these markets to appear and
     disappear between fixtures; that is the contract working, not a bug.

The 60-second unconditional timer in §2 is finer than the current Tier A
two-minute / Tier B five-minute regeneration cadence. Those are different
things and both are kept: the timer governs **snapshots** (cheap, JSONB, for the
graph), regeneration governs **published predictions** (settleable, per-market,
one complete current membership set). Unchanged prediction content is touched,
not re-versioned. Conflating snapshots with prediction publications would make
the write volume unaffordable.
