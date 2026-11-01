# Prediction Engine — Core Architecture & Build Specification (v2)

**This document extends the already-implemented `prediction-engine-architecture-v1.md` — it is not a from-scratch redesign.** The v1 engine has been built and is running: fixtures are being gated, Poisson/Dixon-Coles and form-adjusted predictions are being generated across the lifecycle stages, the best-bet pool and acca engine are live, and settlement/accuracy tracking is operating. Everything below assumes that system exists, has real data flowing through it, and — critically — has real historical predictions and settlements already sitting in the database under the v1 schema. This document's job is to specify the **delta**: what gets added, what gets migrated, and what genuinely new work (calibration, additional models, governance) sits on top of what's already running. Section 1.1 makes this explicit before anything else, because it changes how almost every phase below should be read — as an extension plan against live infrastructure, not a greenfield build plan.

In the model-governance vocabulary this document itself defines in Section 3 and Section 21: the currently-running v1 model stack is not being retroactively relabeled as a mistake — it becomes this document's **initial Champion**, the real, live baseline every new model introduced below (Elo, the ML model, the calibrated ensemble) must actually outperform on real evidence before it's allowed to take over. That is a stronger, more honest starting point than a document written before anything existed could have offered.

**What changed and why, in one paragraph:** v1 built a genuinely solid engine skeleton — lifecycle staging, fixture independence, concurrency, gating, the confidence/probability split, an unlimited best-bet pool, an acca engine with real correlation handling — and it's proven itself by actually running. What v1 does not yet have is what separates a system that *looks* statistically sound from one that actually *is*: calibrated probabilities instead of raw model output, genuinely independent models instead of one model wearing two hats, point-in-time-correct historical evaluation instead of a backtest that can accidentally see the future, and the governance machinery (model registry, champion/challenger, drift monitoring) that catches the moment any of this quietly stops being true. This revision adds all of that, as an extension, without simplifying anything v1 already got right or disrupting what's already live.

**Scope of this document:** the prediction engine only. Not the Go backend, not the worker, not the frontend, not infrastructure. This assumes fixtures, statistics, odds, standings, injuries, and lineups already exist in PostgreSQL, populated by an upstream ingestion worker. The engine's job starts at "read from the database" and ends at "write predictions back to the database."

**Audience:** an autonomous coding agent (Codex) executing this as a build plan against the existing SokaLens prediction engine codebase. Every phase has a definition of done and a testing gate. Do not proceed to the next phase until the current phase's tests pass. Where this document says a validation gate must pass before proceeding, that is not a suggestion — a coding agent that ships a phase with a failing statistical validation gate has not completed that phase, regardless of whether the code runs without error. Where a phase touches a table or code path that already exists in production, treat it as a migration: additive, backward-compatible, and never destructive to already-settled historical data (Section 1.1, Section 28).

**Language:** Python 3.11+. Recommended libraries: `numpy`, `scipy`, `pandas`, `psycopg2` or `asyncpg`, `pydantic` (for config/schema validation), `pytest` for testing, `scikit-learn` (isotonic/Platt calibration, Elo utilities), `lightgbm` or `xgboost` (Model C), `shap` (explanation factors for Model C).

**What "60%+ accuracy" means for this document, stated once up front so it isn't misread anywhere below:** this document does not claim the engine will achieve 60%+ accuracy. It specifies a system designed so that *if* 60%+ on selectively published high-confidence recommendations is achievable, the system can prove it honestly — and if it isn't achievable for a given market or league, the system detects that and suppresses publishing there rather than manufacturing the appearance of success. Section 26 defines this precisely. Every other section that touches accuracy should be read with that framing in mind, not as a promise.

---

## 1.1 Relationship to the Existing Implementation — Read This Before Section 0

This section exists so that neither a human reviewer nor the coding agent executing this document mistakes it for a rebuild. It isn't one.

**What already exists and is not being redone:** the database tables in Section 3.1 (fixtures, fixture_statistics, team_season_stats, head_to_head, standings, player_availability, odds, referee_stats, league_coverage, api_predictions), the core `predictions` table and its v1 field set (Section 3.3's "Core fields" group), the match quality gate (Section 5), the full lifecycle/concurrency/horizon-embargo machinery (Sections 5A–5C), Model A (Poisson/Dixon-Coles, Section 8.1), the v1 form-adjustment mechanism, the v1 ensemble blend, v1 confidence scoring, the best-bet pool, the acca engine, and settlement/accuracy tracking as v1 specified them. None of this is being torn out. Where a later section changes how one of these pieces behaves (the ensemble, confidence scoring, and the recommendation gate all change meaningfully), that section says so explicitly and explains why, exactly as v1's own governance principle (Design Principle 14, below) demands of the running system itself.

**What this means concretely for migrations:** every new field added to an existing table (Section 3.3's new field groups) is added via an additive, nullable migration — `ALTER TABLE ... ADD COLUMN`, never a destructive rewrite. Existing historical rows are **not** backfilled with fabricated values for fields that didn't exist when they were generated — a prediction generated under v1 genuinely has no `ensemble_variance`, because no ensemble in the v2 sense produced it, and pretending otherwise by computing one retroactively from data the original prediction never used would itself be a point-in-time violation (Section 3.4). Those fields stay `NULL` on legacy rows, and every downstream consumer of them (the ledger, drift monitoring, champion/challenger evaluation) must treat `NULL` here as "pre-v2 legacy prediction," a real and expected state, not a data-quality error to fix.

**What this means for the model registry specifically:** Section 3.3's `model_registry` table does not start empty. Its first row, written as part of Phase 0 (Section 28), represents the model that is *already in production* — the v1 Poisson/Dixon-Coles-plus-form engine — with `status = "production"` and best-effort values for `training_start`/`training_end`/`training_dataset_hash` where exact historical values aren't recoverable (approximate and clearly flagged as approximate is correct here; fabricated precision is not). This is what makes Section 21's champion/challenger framework meaningful from day one instead of theoretical: the very first champion/challenger cycle this system ever runs is the new calibrated, multi-model ensemble (Sections 8–9) competing against the real, already-running v1 output on real, already-flowing fixtures — not a hypothetical baseline.

**What this means for the phased build plan (Section 28):** phases are written as extension work against a live system, not fresh-build steps. Where a phase's subject matter already exists and is running, that phase's job is verification and any necessary additive migration, not construction — this is stated explicitly at the start of each such phase rather than left ambiguous. Where a phase's subject matter is genuinely new (calibration, Model B, Model C, OOD detection, the governance layer), it's a real build phase, same rigor as v1's phases.

---
## 0. Design Principles

These principles govern every decision in this document. When a build choice isn't explicitly covered below, fall back to these.

1. **Probability and confidence are two different numbers.** Probability answers "how likely is this outcome." Confidence answers "how much should we trust this probability." A 70% probability built on three matches of data is not the same as a 70% probability built on three seasons of data. The engine must never collapse these into one number.

2. **No market is hardcoded as primary.** The system in earlier drafts treated 1X2, goals, and BTTS as "the real markets" and corners/cards as secondary. That is wrong for this build. Every market API-Football can support statistically gets the same engineering rigor: a defined data requirement, a model, a confidence calculation, and a settlement rule. Section 2 enumerates all of them.

3. **Silence is a valid output.** If the data doesn't support a confident prediction, the correct output is "no recommendation," not a low-confidence guess dressed up as a pick. The match quality gate (Section 5) and per-market eligibility gate (Section 9) both exist to enforce this.

4. **Every prediction must be explainable in plain language using only the inputs that actually drove it.** Explanations are generated from the same feature values used in scoring — never from a separate, decorative text generator. See Section 11.

5. **Everything tunable lives in configuration, not in code.** Thresholds, weights, decay rates, market eligibility rules — all externalized. See Section 4.

6. **The engine is a pure function of its inputs at any given point in time.** Given the same database state, two runs of the engine must produce identical predictions. No hidden randomness, no uncontrolled external calls mid-run. This is what makes the system testable.

7. **The engine never calls API-Football.** All upstream data arrives pre-fetched in PostgreSQL. This is a hard boundary, not a suggestion — the engine has no API-Football client code in it at all.

8. **A prediction is not a single fact computed once — it is the current state of an ongoing estimate that gets refined as better data arrives.** Lineups, late injury news, and other near-kickoff data are real signals, not edge cases to bolt on. The engine is designed from the ground up to be invoked multiple times per fixture across its pre-match lifecycle, each time honestly reflecting what was knowable at that moment. See Section 5A.

9. **Every fixture's prediction work is independent of every other fixture's.** Nothing about predicting Match A may read, wait on, lock, or share mutable state with the prediction work for Match B, except the read-only, pre-computed league-average baselines from Section 6.2. This is what allows fifty matches kicking off at the same time to be processed without one slow fixture blocking another or the whole batch becoming a bottleneck. See the Concurrency & Execution Model section.

10. **A raw model probability is not what gets shown to anyone.** Between "the model computed 68%" and "the user sees 68%" sits a calibration step whose entire job is to check whether the model's 68% actually behaves like a 68% historically, and correct it if not. Skipping this step and calling raw model output "probability" is the single most common way a prediction system looks rigorous while quietly being wrong. See Section 9.

11. **Agreement between models is only meaningful if the models could have disagreed.** A second "model" built by nudging the first model's own output is not an independent check — it will always seem to agree, because it's structurally incapable of an independent opinion. Genuine ensemble agreement requires models built on different statistical machinery reaching the same place on their own. See Section 8.

12. **A historical prediction may only be evaluated using information that existed at the moment it claims to have been made.** This sounds obvious and is violated constantly, almost always by accident — a backtest that queries "today's" fully-populated database for a fixture from two seasons ago is silently grading the model against information it never actually had. Every feature query, every backtest, every historical reconstruction is point-in-time or it is not trustworthy. See Section 3.4.

13. **Coverage is not a side effect to report after the fact — it is a first-class design target, chosen deliberately, not maximized blindly.** A confidence threshold set so high that almost nothing gets published can post an excellent headline accuracy number while being commercially useless and, worse, can look rigorous while actually just being sample-starved. A threshold set so low that the "Strong" tier stops meaning anything erodes the trust the whole recommendation system depends on. The engine's job is to make this tradeoff visible — accuracy at every coverage level, not just the one currently chosen — so the threshold is a deliberate, revisitable business decision, not an accident of whatever number looked good once. This is what growth-oriented means for a system whose core asset is user trust: publishing more is only good when it's honestly earned, because a wrong "Strong" pick costs more in the users who stop trusting the label than a missed opportunity costs in impressions not shown. See Section 26.

14. **No production model, weight, or threshold is ever silently replaced.** Every model has a version, a status (`candidate` → `shadow` → `production` → `retired`), and a record of what it replaced and why. A challenger earns promotion by demonstrating it outperforms the champion on held-out evidence — it is never swapped in because it's newer, more sophisticated, or because someone was confident it would be better. See Section 3 (model registry) and Section 21 (champion/challenger).

---

## 1. What "Core" Means Here — System Boundary

```
        ╔═══════════════════════════════════════════════════════════╗
        ║                 PREDICTION ENGINE (THIS DOC)                ║
        ║                                                              ║
PostgreSQL║  Read (point-in-time) → Feature build → Quality/OOD gate  ║PostgreSQL
(read-only║  → Models A/B/C/D → Ensemble → Calibrate → Confidence      ║ (write
 to engine║  → Recommendation gate → Market select → Rank → Explain    ║ predictions,
  + model ║  → Settle → Shadow-evaluate → Drift-monitor                ║ results,
 registry)║                                                              ║ accuracy,
        ║                                                              ║ registry,
        ╚═══════════════════════════════════════════════════════════╝ drift log)
```

**In scope:**
- Reading raw match/team/league data from PostgreSQL, strictly point-in-time correct (Section 3.4)
- Computing derived features, including hierarchical fallback for new/promoted teams and manager/regime awareness
- Deciding whether a fixture is predictable at all (the quality gate) and whether it is statistically out-of-distribution (the OOD gate)
- Running four genuinely independent model families per market group and combining them through a real ensemble with measurable agreement, not a single model wearing two hats
- Calibrating raw model probability against historical outcomes before it is trusted anywhere downstream
- Scoring confidence independently of probability, from evidence rather than hand-authored weights alone
- Refining predictions as later-arriving data (lineups, late injury news) becomes available, across a defined pre-match lifecycle, and tracking whether that refinement actually improves accuracy
- Executing fixture predictions concurrently and independently, so high-volume kickoff windows do not bottleneck
- Selecting, per fixture, which of several eligible markets is best-supported, and ranking/exposing predictions as a continuously available pool, sliceable by date range and confidence tier — not a single capped daily list
- Building accumulator (acca) selections on demand from the existing prediction pool, given user-supplied constraints, with correlation handled honestly rather than assumed away
- Generating explanations from structured model factors, never inventing rationale after the fact
- Settling predictions against actual results after matches finish, including shadow settlement of pre-LOCKED lifecycle stages for analytics purposes
- Maintaining the engine's own accuracy, calibration, and statistical-significance ledger
- Governing its own models: registry, versioning, champion/challenger evaluation, drift monitoring, and a defined retraining policy

**Explicitly out of scope (mentioned only where the boundary needs to be drawn):**
- Fetching data from API-Football (that's the ingestion worker's job)
- Serving HTTP requests to a frontend (that's the Go backend's job)
- Database schema definition beyond the tables the engine itself owns (Section 3 defines only what the engine reads and writes)
- Scheduling / cron orchestration beyond stating when the engine should be invoked (Section 5A and the Concurrency & Execution Model section)
- Acting as a bookmaker: the acca engine (Section 20) selects and combines legs from the engine's own modeled probabilities and exposes a fair (model-derived) price; it does not source, hold, or guarantee real bookmaker odds. If real bookmaker odds are reliably ingested for a fixture/market (the `odds` table from Section 3.1), the acca engine may display them alongside the fair price as a reference, but the combined acca price it computes is always derived from the engine's own probabilities unless explicitly told otherwise — this keeps the acca price internally consistent with every other number the engine produces
- Training infrastructure beyond what a single production-scale box needs to run walk-forward validation and train Model C on a scheduled cadence (Section 27) — this document does not assume a distributed training cluster, and Section 28's retraining policy is deliberately scoped to fit on the same infrastructure the rest of the engine runs on

---

## 2. Market Catalogue — Every Market API-Football Data Can Support

This is the deliberately exhaustive list. The instruction was: do not limit ourselves to a handful of markets. Below, every market is tied to the specific API-Football data point(s) it depends on, so there is no ambiguity about whether it's buildable from what the ingestion worker is already collecting.

API-Football's `fixtures/statistics` endpoint returns, per team, per fixture: Shots on Goal, Shots off Goal, Shots insidebox, Shots outsidebox, Total Shots, Blocked Shots, Fouls, Corner Kicks, Offsides, Ball Possession, Yellow Cards, Red Cards, Goalkeeper Saves, Total Passes, Passes Accurate, Passes %. Combined with fixtures (scores, status, venue, referee), teams/statistics (season aggregates, form, biggest win/loss, clean sheets, failed-to-score counts), standings, head-to-head, injuries, lineups, odds, and API-Football's own `/predictions` endpoint, this is the full surface area.

**Important constraint to design around:** not every statistic is populated for every league. Lower-tier leagues frequently return null for advanced stats (xG, detailed shot zones) and sometimes for basic ones. The market catalogue below is split into tiers by data reliability so the gate logic (Section 5) and per-market eligibility (Section 9) can apply the right strictness to each.

### 2.1 Tier A — Core Goal-Based Markets (highest data reliability, every league has goals)

| Market | Outcomes | Source data |
|---|---|---|
| Match Result (1X2) | Home / Draw / Away | Fixture result history |
| Double Chance | Home-or-Draw / Draw-or-Away / Home-or-Away | Derived from 1X2 |
| Draw No Bet | Home / Away (draw voided) | Derived from 1X2 |
| Total Goals Over/Under | Over/Under at 0.5, 1.5, 2.5, 3.5, 4.5, 5.5 | Goals scored history |
| Both Teams To Score (BTTS) | Yes / No | Goals scored/conceded both sides |
| BTTS & Over/Under combo | e.g. "BTTS & Over 2.5" | Combined goal matrix |
| BTTS & 1X2 combo | e.g. "Home Win & BTTS Yes" | Combined goal matrix |
| Correct Score | Specific scorelines (top-N only) | Full goal probability matrix |
| Half-Time/Full-Time | 9 combinations (e.g. Draw/Home) | Half-time + full-time goal history |
| Half-Time Result | Home/Draw/Away at HT | Half-time score history |
| Highest Scoring Half | First half / Second half / Equal | Half-time vs full-time goal split |
| Team To Score First | Home / Away / No goals | Event-timestamped goal data |
| Team Total Goals (per team O/U) | Over/Under 0.5–3.5 per team | Per-team goals scored history |
| Win To Nil | Home / Away win without conceding | Goals scored + clean sheet history |
| Win Either Half | Home/Away leads after either half | Half-time + full-time results |
| Clean Sheet | Home Yes/No, Away Yes/No | Goals conceded history |
| Both Teams To Score In Both Halves | Yes / No | Half-time goal split, both teams |
| Odd/Even Total Goals | Odd / Even | Goal totals history |

### 2.2 Tier B — Set-Piece & Discipline Markets (good reliability in top 5-6 leagues, patchier elsewhere — gate must check per-league coverage)

| Market | Outcomes | Source data |
|---|---|---|
| Total Corners Over/Under | Lines at 7.5, 8.5, 9.5, 10.5, 11.5, 12.5 | `Corner Kicks` stat, season history |
| Team Corners Over/Under | Per-team corner lines | `Corner Kicks` stat, split by team |
| Race to N Corners | First team to reach N corners | Corner stat + timing if available |
| Corner Handicap | Asian-style corner handicap | Derived from corner distribution |
| Total Cards Over/Under | Lines at 2.5, 3.5, 4.5, 5.5 (using weighted card points: yellow=1, red=2) | `Yellow Cards`, `Red Cards` |
| Team Cards Over/Under | Per-team card lines | Cards stat, split by team |
| Booking Points Over/Under | Total weighted card points line | Yellow/red weighted sum |
| Total Fouls Over/Under | Lines at 18.5, 21.5, 24.5 | `Fouls` stat |
| Team Fouls Over/Under | Per-team foul lines | Fouls stat, split by team |
| Total Offsides Over/Under | Lines at 1.5, 2.5, 3.5 | `Offsides` stat |
| Red Card In Match | Yes / No | Red Cards history (rare event — needs special handling, see 9.4) |
| Player Sent Off | Yes / No (team-level proxy, not player-specific in this engine) | Red Cards history |

### 2.3 Tier C — Shot & Possession-Based Markets (reliability varies most by league; explicitly mark these as "use with stricter gate")

| Market | Outcomes | Source data |
|---|---|---|
| Total Shots Over/Under | Lines based on league average | `Total Shots` |
| Shots on Target Over/Under | Lines based on league average | `Shots on Goal` |
| Team Shots on Target O/U | Per-team lines | `Shots on Goal`, split by team |
| Possession Over/Under (per team) | e.g. "Home possession over 55%" | `Ball Possession` |
| Possession Winner | Which team has more possession | `Ball Possession` |
| Pass Accuracy Over/Under | League-relative line | `Passes %` |
| Goalkeeper Saves Over/Under | Lines based on league average | `Goalkeeper Saves` |

### 2.4 Tier D — Combination & Derived Markets (built by combining Tier A/B outputs, not independently modeled)

| Market | Outcomes | Built from |
|---|---|---|
| 1X2 & Total Goals combo | e.g. "Away Win & Under 2.5" | 1X2 model × Goals model joint probability |
| 1X2 & Corners combo | e.g. "Home Win & Over 9.5 Corners" | 1X2 model × Corners model joint probability |
| Multi-market parlay confidence | Composite probability of a user-defined bet slip | Product of independent market probabilities (engine exposes the building blocks; combining into slips is a frontend/API concern, not engine concern, but the engine must expose clean independent probabilities to make this possible downstream) |

### 2.5 Markets Explicitly Excluded From This Engine (with reasons — revisit only if upstream data changes)

| Market | Why excluded now |
|---|---|
| Asian Handicap (goal-based) | Requires modeling against a specific bookmaker handicap line; doable later as a Tier D derived market once odds ingestion is mature, not in v1 |
| Throw-ins | Not present in API-Football's `fixtures/statistics` field list (confirmed: Shots on/off Goal, Shots inside/outside box, Total/Blocked Shots, Fouls, Corner Kicks, Offsides, Possession, Yellow/Red Cards, Goalkeeper Saves, Passes, Passes Accurate, Passes %). If a future API-Football tier adds it, re-open this market. |
| Player-specific markets (anytime scorer, player shots, player cards) | Requires `fixtures/players` per-player statistics, which is a materially different data pipeline (player-level time series, transfer tracking, lineup-confirmed minutes). Out of scope for the core engine; candidate for a v2 player-prediction module. |
| Exact minute of first goal | Statistically near-impossible to model reliably from aggregate stats; would require event-level historical modeling. Excluded. |
| In-play / live markets | This document covers pre-match prediction only. Live market re-pricing during a match is a separate real-time system with different latency requirements. Out of scope. |

**Instruction to the build agent:** implement Tier A fully in Phase 4, Tier B in Phase 8's applicable market family, Tier C similarly, Tier D in the acca/combination work (Section 20). Phase numbers are defined in Section 28; do not skip tiers, and do not merge them into one phase — they have different data reliability profiles and need separate test suites.

### 2.6 Per-market data-support audit — every market needs a real metadata record, not just a table row

The tables above say what a market *conceptually* needs. They do not, by themselves, prove the current read contract (Section 3.1) actually supplies that need at the granularity required. Two markets in the Tier A/B tables above fail this check today and must be corrected, not quietly left as-is:

- **Team To Score First** is listed against "event-timestamped goal data." Section 3.1's `fixture_statistics` table stores per-fixture, per-team **aggregate totals** — final goal counts, not a timestamped sequence of individual goal events. Without knowing *when* each goal happened, "who scored first" cannot be determined from the current contract at all, aggregate or otherwise.
- **Race to N Corners** has the same problem — `fixture_statistics.corners` is a final total, not an ordered/timestamped sequence.

Both are corrected the same way: a new optional table, **`fixture_events`** (fixture_id, team_id, event_type — goal/card/substitution/VAR, minute, extra_time_minute, player_name, detail, sequence_order), sourced from API-Football's own events endpoint where a league's coverage includes it. This is added to Section 3.1 as an **optional** table — most leagues will have it, some won't, and `league_coverage` gets a new flag, `has_event_timestamps`, to make that explicit per league-season rather than inferred from whether the table happens to be empty.

Every market in the catalogue tables above must carry a formal metadata record before it is enabled in production, with these fields:

| Field | Meaning |
|---|---|
| `required_data_fields` | Exact table.column list the market's model needs |
| `minimum_history` | Minimum matches/samples required before the market is even attempted |
| `required_event_granularity` | `aggregate` (season/fixture totals suffice) or `event_timestamped` (needs `fixture_events`) |
| `supported_leagues` | Which leagues currently have the required coverage flags true — not "all," computed |
| `settlement_fields` | Exactly which fields the settlement process (Section 17) reads to determine the actual outcome |
| `model_family` | Which of Model A/B/C/D (Section 8) or which auxiliary distribution produces this market's probability |
| `confidence_cap` | The market's `max_confidence_tier` ceiling if one applies (Section 11.3), or "uncapped" |
| `best_bet_eligible` | Whether this market may ever enter the best-bet pool (Section 15), or is display-only |

Populating this record for all ~45 markets in the catalogue is a Phase 0 deliverable (Section 28), not something this document exhaustively hand-fills — most records are mechanically derivable from the tier tables above (a Tier A market's `required_event_granularity` is `aggregate`, its `model_family` is Model A/ensemble, etc.). What this document *does* fix explicitly, because it's the kind of error that's easy to miss and expensive to publish: **Team To Score First and Race to N Corners are marked `required_event_granularity: event_timestamped` and are disabled (`best_bet_eligible: false`, excluded from the general-prediction catalogue entirely) for any league where `league_coverage.has_event_timestamps` is not explicitly true.** They are not deleted from the catalogue — they re-enable automatically, league by league, the moment `fixture_events` coverage is confirmed for that league, with no code change required. This is the general pattern the audit exists to enforce: a market is either provably supported for a given league or it is off, and "off" is a coverage-flag state, not a permanent decision.

---

## 3. Data the Engine Reads (Contract With Upstream)

The engine assumes the following data already exists in PostgreSQL when it runs. This section is a **read contract**, not a schema you need to build from scratch — but if any of these tables/fields don't exist yet, flag it before Phase 1 starts, because the engine cannot produce meaningful output without them.

### 3.1 Required tables (minimum fields the engine queries)

- **fixtures** — id, league_id, season_id, home_team_id, away_team_id, kickoff_at, status, venue_name, referee, round, home_goals_ft, away_goals_ft, home_goals_ht, away_goals_ht
- **fixture_statistics** — fixture_id, team_id, shots_on_target, shots_total, shots_blocked, shots_inside_box, shots_outside_box, possession_pct, corners, fouls, offsides, yellow_cards, red_cards, goalkeeper_saves, passes_total, passes_accurate, pass_accuracy, xg (nullable — not all leagues provide this)
- **team_season_stats** — team_id, league_id, season_id, home/away split of played/wins/draws/losses/goals_for/goals_against/clean_sheets/failed_to_score, plus derived averages for corners/cards/fouls/shots where the ingestion worker has computed them
- **head_to_head** — home_team_id, away_team_id, fixture_id, played_at, home_goals, away_goals, venue
- **standings** — league_id, season_id, team_id, rank, points, played, wins, draws, losses, goals_for, goals_against, goal_diff, form (string)
- **player_availability** — fixture_id, team_id, player_name, reason (Injury/Suspension), status (Doubtful/Out)
- **odds** — fixture_id, bookmaker, market, outcome, value, implied_prob, fetched_at
- **referee_stats** — name, matches_officiated, avg_yellow_cards, avg_red_cards, avg_fouls
- **league_coverage** — league_id, season_id, has_statistics, has_xg, has_injuries, has_odds, has_lineups, **has_event_timestamps** (new — whether `fixture_events` below has real per-league coverage; see Section 2.6) (this mirrors API-Football's own per-league `coverage` flags — the engine must check this before trusting any field from a given league, since lower-tier leagues frequently return null for advanced stats)
- **api_predictions** (optional but recommended) — fixture_id, predicted_winner, win_or_draw, under_over, percent_home, percent_draw, percent_away, advice — API-Football's own native `/predictions` endpoint output, stored as a cross-validation signal (see Section 10.3)
- **fixture_events** (new, optional — see Section 2.6) — fixture_id, team_id, event_type (goal/card/substitution/var), minute, extra_time_minute, player_name, detail, sequence_order. Only populated where `league_coverage.has_event_timestamps` is true. Unlocks Team To Score First, Race to N Corners, and any future market genuinely requiring event order rather than final totals.
- **team_manager_history** (new — see Section 5E) — team_id, manager_name, appointed_at, departed_at (nullable — null means current). Used to compute manager tenure and detect recent regime changes; a team with no rows here is treated as having no known regime-change signal, not as an error.
- **odds** — extended from v1: bookmaker, observed_at (was `fetched_at` — renamed for clarity and made a mandatory, indexed field, not an incidental timestamp), market, line (nullable, for lined markets like O/U and corners), raw_odds, implied_probability, de_vigged_probability (computed at ingestion or on read — see Section 14). Every row is immutable once written; a later odds movement is a **new row**, never an update to an existing one, so opening and closing lines are both permanently queryable (see Section 14.1).

### 3.2 What the engine must NOT assume

- That every field is populated. Null-handling is mandatory everywhere, not optional defensive code. A league with `has_xg = false` must never have xg-dependent logic silently fall through to a default value that looks like real data — it must explicitly degrade (see Section 5.3).
- That `team_season_stats` has a full season of history early in a season. Early-season fixtures (first 5-6 gameweeks) have structurally thinner data and the gate must treat them more cautiously regardless of league tier.
- That referee data exists for the assigned referee. Many fixtures will have `referee_stats = null`. The set-piece model must have a defined fallback (league-average card/foul rate) rather than crashing or skipping the whole fixture.
- That a fixture within the 7-day horizon is automatically eligible for prediction. A fixture may be within the window but blocked by the interleaved-match embargo (Section 5C). The engine must check both conditions before entering prediction work.

### 3.3 Tables the engine writes (write contract)

These are the tables the engine owns and writes to. The ingestion worker must never write to these tables, and the Go backend must treat all of them as read-only. Schema details below are fields the engine is responsible for populating — not the full table DDL, which is the infrastructure layer's concern.

**predictions** — the engine's primary output table. One row per fixture × market × lifecycle stage version. Substantially extended from v1 to support calibration, genuine ensemble, OOD detection, stability tracking, and value analysis — every new field below exists because a specific section later in this document writes or reads it; none are speculative.

**A naming clarification that governs every other section in this document:** the field literally called `probability` — the one every downstream component (ranking, market selection, acca leg eligibility, the public API) reads — is `calibrated_probability`. `raw_probability` is retained purely for audit and calibration-quality monitoring; nothing outside Section 9 (Calibration) ever makes a decision based on it. Wherever the rest of this document says "the prediction's probability" without qualification, it means the calibrated value.

*Core fields (present in v1, unchanged):*
- `id` (UUID, engine-generated), `fixture_id`, `market`, `prediction`, `confidence` (float 0.00–1.00), `confidence_tier` ("strong"/"medium"/"low"/"none"), `is_best_bet` (boolean, set by Section 15's pool logic, not the model layer), `lifecycle_stage` ("EARLY"/"PRE_MATCH"/"LINEUP_CONFIRMED"), `data_completeness` (float 0.0–1.0, Section 5.3), `features_used` (JSON snapshot, Section 7), `generated_at`, `superseded_at` (nullable), `gate_tier` ("SKIP"/"BASIC"/"FULL"), `horizon_eligible`, `embargo_active` (Section 5C), `model_version`

*Probability & calibration fields (new):*
- `raw_probability` (float 0.01–0.99 — the ensemble's output before calibration)
- `calibrated_probability` (float 0.01–0.99 — the field every downstream consumer reads; see naming clarification above)
- `calibration_model_version`, `calibration_training_window`, `calibration_method` (Section 9)

*Ensemble fields (new — one row per model, per Section 8.5):*
- `poisson_probability`, `elo_probability`, `ml_probability` (Model A/B/C's individual outputs)
- `other_model_probabilities` (JSON — auxiliary Model D output where applicable)
- `ensemble_mean`, `ensemble_variance`, `max_model_spread`, `model_agreement_score` (Section 8.5)

*Out-of-distribution fields (new, Section 5D):*
- `ood_score`, `feature_distance_score`, `unseen_team_flag`, `unseen_league_flag`, `major_squad_change_flag`

*Lifecycle stability fields (new, Section 5A.6):*
- `previous_probability`, `probability_delta`, `max_lifecycle_probability_shift`, `prediction_direction_changed` (boolean), `lifecycle_stability_score`

*Point-in-time audit fields (new, Section 3.4 — mandatory on every row, no exceptions):*
- `prediction_as_of` (the timestamp this prediction claims to represent knowledge as of — for a live prediction this is `generated_at`; for a backtested historical prediction this is the simulated historical moment)
- `data_cutoff_at` (the latest timestamp of any data actually used to build the feature vector — must never be later than `prediction_as_of`)
- `latest_source_event_at` (the timestamp of the single most recent underlying event — a match result, an odds update, an injury report — that fed this prediction, for fast leakage auditing without reconstructing the full feature snapshot)
- `feature_version` (which version of the feature-engineering logic produced this row's `features_used`)

*Market selection, odds, and value fields (new, Sections 13–14):*
- `ranking_score` (Section 15.2's composite ranking output)
- `is_strongest_market_for_fixture` (boolean, Section 13)
- `bookmaker_implied_probability`, `de_vigged_market_probability`, `model_edge`, `closing_market_probability`, `closing_line_value` (Section 14 — all nullable, populated only where odds coverage exists for this fixture/market)

*Recommendation status fields (new, Section 12):*
- `recommendation_status` ("recommended" / "shown_general_only" / "no_prediction")
- `no_prediction_reason` (nullable — one of the enumerated codes in Section 12.3, populated only when `recommendation_status` is "no_prediction")

*Explanation fields (extended, Section 16):*
- `explanation` (text — now rendered from `explanation_factors`, never generated independently of them)
- `explanation_factors` (JSON — structured drivers: for Model C, top positive/negative SHAP contributors; for Model A, attacking/defensive-strength/home-advantage/injury/lineup/league-baseline contributions; see Section 16)

**prediction_results** — written by the settlement process (Section 12) after each fixture completes. One row per prediction row that was the LOCKED-stage version for its fixture. Required fields: `prediction_id`, `fixture_id`, `market`, `predicted`, `actual`, `is_correct`, `probability`, `confidence`, `confidence_tier`, `lifecycle_stage_at_settlement`, `league_id`, `settled_at`.

**accuracy_snapshots** — written by the accuracy aggregation process (Section 19.1) on its scheduled cadence, not on every individual settlement. One row per snapshot_date × scope. Required fields: `snapshot_date`, `scope` (e.g. "overall", "market:1X2", "league:39", "tier:strong"), `scope_id`, `total_predictions`, `correct`, `accuracy`, `roi_simulated`, `computed_at`.

**acca_selections** — written by the acca engine (Section 20) when it builds an accumulator selection. One row per acca. Required fields: `id` (UUID), `requested_at`, `legs_requested` (integer), `target_odds` (float, nullable — the user's combined odds target, if any), `markets_filter` (JSON array of market strings the user requested), `start_datetime`, `end_datetime`, `legs` (JSON array of {fixture_id, market, prediction, probability, confidence, fair_odds}), `combined_fair_odds` (float), `combined_fair_probability` (float), `leg_count` (integer), `any_leg_embargoed` (boolean — true if any leg had an active embargo at selection time, which must be shown as a caveat), `generated_at`.

**fixture_prediction_horizon** — written by the engine's pre-run eligibility check (Section 5C) to record, for each fixture, its horizon status and embargo status at the time of each engine sweep. Required fields: `fixture_id`, `checked_at`, `kickoff_at`, `days_to_kickoff`, `horizon_eligible` (boolean), `embargo_reason` (text, nullable — populated when `horizon_eligible` is false due to embargo, explaining which interleaving match is blocking prediction and why), `embargo_blocking_fixture_id` (integer, nullable — the specific midweek-fixture ID that is blocking this weekend fixture).

**model_registry** (new — Section 21) — one row per trained model artifact, across all of Model A/B/C/D and the calibration models. Required fields: `model_id` (UUID), `model_version` (string), `market_family` (which market(s) this artifact serves), `training_start`, `training_end`, `validation_window`, `feature_version`, `hyperparameters` (JSON), `calibration_version` (nullable — set once this model has an associated calibration model), `training_dataset_hash`, `code_commit`, `created_at`, `promoted_at` (nullable), `retired_at` (nullable), `status` ("candidate"/"shadow"/"production"/"retired"). Exactly one row per `market_family` may have `status = "production"` at any time; the registry itself enforces this (a unique partial index on `market_family` where `status = 'production'`), not just application logic.

**calibration_models** (new — Section 9) — one row per fitted calibration model. Required fields: `calibration_id` (UUID), `market`, `confidence_band` (nullable — some calibration models are fit per band, some globally per market), `method` ("isotonic"/"platt"/"beta"), `training_window_start`, `training_window_end`, `brier_score`, `log_loss`, `expected_calibration_error`, `calibration_slope`, `calibration_intercept`, `sample_count`, `fitted_at`, `status` ("candidate"/"production"/"retired").

**shadow_settlements** (new — Section 17.2/18) — settlement results for lifecycle stages *other than* LOCKED, kept separate from `prediction_results` so the headline user-facing accuracy ledger (which only ever grades LOCKED) is never contaminated by shadow analytics. Required fields: mirrors `prediction_results` (`prediction_id`, `fixture_id`, `market`, `predicted`, `actual`, `is_correct`, `probability`, `confidence`, `lifecycle_stage`, `brier_contribution`, `log_loss_contribution`) plus `settled_at`.

**drift_alerts** (new — Section 22) — one row per detected drift event. Required fields: `alert_id` (UUID), `drift_type` ("feature"/"prediction_distribution"/"calibration"/"accuracy"/"league_performance"), `scope` (e.g. "market:btts", "league:61"), `detected_at`, `metric_name`, `expected_range`, `observed_value`, `severity` ("warn"/"action_required"), `resolution_status` ("open"/"acknowledged"/"resolved"), `resolved_at` (nullable), `resolution_note` (nullable).

**champion_challenger_evaluations** (new — Section 21) — one row per completed evaluation cycle. Required fields: `evaluation_id` (UUID), `market_family`, `champion_model_id`, `challenger_model_id`, `evaluation_start`, `evaluation_end`, `sample_count`, `champion_accuracy`, `challenger_accuracy`, `champion_brier`, `challenger_brier`, `champion_log_loss`, `challenger_log_loss`, `champion_calibration_error`, `challenger_calibration_error`, `champion_coverage`, `challenger_coverage`, `champion_simulated_roi`, `challenger_simulated_roi`, `decision` ("promote"/"reject"/"extend_evaluation"), `decided_at`, `decided_by` (nullable — human override reference, if applicable).

**market_metadata** (new — Section 2.6) — the per-market audit registry. One row per market. Fields exactly as defined in Section 2.6's table: `market`, `required_data_fields` (JSON), `minimum_history`, `required_event_granularity`, `supported_leagues` (JSON array, computed not hand-maintained), `settlement_fields` (JSON), `model_family`, `confidence_cap`, `best_bet_eligible` (boolean), `updated_at`.

---

## 3.4 Point-in-Time Data Correctness

This is the single most important correctness property in this document, because it is the one most likely to be violated by accident, and a violation is invisible in ordinary testing — code that leaks future information into a historical prediction runs without error, produces plausible-looking output, and quietly invalidates every accuracy claim built on top of it.

### 3.4.1 The principle, stated precisely

Every feature query, for every prediction — live or historical — must conceptually answer **"what was knowable at this exact timestamp?"**, never **"what does the database currently know about this fixture?"** These are the same question only for a live prediction being made right now. For a historical prediction being reconstructed, backtested, or audited, they are very different questions, and conflating them is data leakage.

`as_of_timestamp` is a mandatory, explicit, first-class parameter to every feature-generation call in this engine — not an implicit "now" that happens to be correct in production and silently wrong in a backtest. A feature-building function that does not take `as_of_timestamp` as an explicit argument is, by definition, not point-in-time-safe and may not be used anywhere in this engine, including ad hoc analysis scripts.

### 3.4.2 Specific leakage vectors this engine must prevent

Each of the following is a concrete, plausible way future information reaches a historical prediction if `as_of_timestamp` filtering is not applied at every read:

- Future fixtures (a rolling-average feature that scans "the team's next 10 matches" instead of "the team's 10 matches before this one")
- Post-match statistics for the fixture being predicted (the exact target leak — the fixture's own final stats must never appear in its own feature vector)
- Later table/standings positions (a team's rank as of *today* rather than as of the prediction moment)
- End-of-season aggregates computed after the season the prediction is being made within
- Future injuries, future lineups, future player availability
- Subsequent manager changes (Section 5E) — a regime-change feature must reflect who the manager *was* at `as_of_timestamp`, not who they are now
- Future Elo ratings (Model B, Section 8.2, must use the Elo rating as it stood immediately before this fixture, not the version updated by this fixture's own result or any later result)
- Revised historical aggregates (if a data provider corrects a past statistic retroactively, a historical prediction must be reconstructed using the data *as it existed* at prediction time, not the corrected value — see 3.4.4 on snapshot storage)
- Later bookmaker prices — an EARLY-stage prediction must only ever be compared against odds observed by the EARLY-stage timestamp, never against closing odds (Section 14.1)
- Future player availability beyond what was reported by `as_of_timestamp`
- Derived rolling averages that, through an off-by-one error, include the target fixture itself in its own rolling window

### 3.4.3 Point-in-time-safe query pattern

Every feature query that reads from a table with a natural time dimension (`fixtures.kickoff_at`, `odds.observed_at`, `player_availability` reports, `standings` snapshots, `team_manager_history` appointments) must filter to rows whose relevant timestamp is strictly before `as_of_timestamp`. Where a table does not naturally version itself over time (e.g., `team_season_stats` as a single mutable row per team-season, updated in place by the ingestion worker as the season progresses), point-in-time correctness cannot be achieved by filtering that table directly — it requires either: a periodic **feature snapshot** table that records the computed feature values as of specific checkpoints (see 3.4.4), or reconstructing the aggregate from the underlying per-fixture rows filtered by `as_of_timestamp`, which is slower but always correct. Prefer reconstruction from per-fixture data wherever performance allows; use snapshotting only where reconstruction is demonstrably too slow for the lifecycle stage's latency budget (Section 5B).

### 3.4.4 Point-in-time-safe feature snapshot storage

For features that are expensive to reconstruct on demand (league-average baselines, Section 6.2; team-season aggregates spanning hundreds of fixtures), the engine maintains periodic snapshots — a `feature_snapshots` table keyed by `(scope, as_of_date, feature_name, value)` — computed on a schedule (nightly is sufficient for season-level aggregates, since these move slowly) rather than reconstructed from scratch on every prediction. A historical backtest or audit reconstructing a prediction from three months ago reads the snapshot whose `as_of_date` is the closest one at or before the historical `as_of_timestamp` being simulated, never the current live aggregate.

### 3.4.5 What this means for live production versus backtesting

In live production, `as_of_timestamp` is simply "now" (or, more precisely, the moment the lifecycle-stage trigger fired — Section 5A), and correctness risk is genuinely low because "now" and "what the database currently knows" are almost always the same thing by construction. **The overwhelming majority of real leakage risk is in backtesting, historical replay, and audit reconstruction** (Section 24), where "now" (today, when the backtest is being run) and "as_of_timestamp" (some historical moment being simulated) are deliberately different, and it is exactly that difference a leakage bug erases. This document does not overstate live-production risk to justify the rigor here — it is precise about where the risk actually concentrates, and Sections 3.4.3–3.4.4 exist primarily so that historical replay (Section 24) is trustworthy, with live production correctness as a natural, low-cost byproduct of using the same code path both ways.

### 3.4.6 Mandatory validation

Section 27.2's temporal-leakage test category exists specifically to catch violations of this section automatically: adding a future fixture to the database must provably not alter any already-computed historical `as_of`-scoped feature vector. This is not optional test coverage — no phase past Phase 2 (Section 28) may be considered complete without this test passing.

---

## 4. Configuration Layer

Every threshold, weight, and decay constant referenced anywhere in this document lives in one configuration module, never hardcoded inline in logic. This is what allows tuning without redeploying logic and what allows the testing phase (Section 14) to run the same test suite against multiple config variants.

### 4.1 Configuration groups required

**Confidence thresholds**
- Strong tier minimum (default 0.75)
- Medium tier minimum (default 0.65)
- Low tier minimum (default 0.55)
- Below Low → no recommendation

**Match gate thresholds**
- Minimum data completeness to attempt any prediction (default 0.35)
- Minimum data completeness to be eligible for "full" tier vs "basic" tier (default 0.55)
- Minimum number of historical matches per team required (default 5)
- Injury-impact-both-teams skip threshold (default 0.50 each)

**Per-market-tier gate strictness multipliers**
- Tier A markets use the base thresholds above
- Tier B markets require a higher data completeness floor (default +0.10 over base) because corner/card data is less consistently available
- Tier C markets require an even higher floor (default +0.15 over base) and additionally require `league_coverage.has_statistics = true` explicitly, not just inferred from non-null fields

**Model weights (ensemble blending)**
- Per market-group, the blend weights across Models A (Poisson/Dixon-Coles), B (Elo), and C (ML) in Section 8.5's genuine ensemble — starting defaults set here, validated/optimized via Section 24's walk-forward process rather than fixed permanently by hand (Design Principle 14)
- Season-long-versus-recent-form blend ratio used inside Model A's own attack/defence calculation (Section 8.1 step 1–2; default 70% season-long / 30% recent-form) — a separate weight from the cross-model ensemble weights above, since this one blends two data windows within a single model rather than blending across models

**Form calculation**
- Lookback window sizes (default last 5 and last 10 matches, both computed)
- Per-feature-group decay rates — see the dedicated "Per-feature-group recency decay" group below (Section 6.6); there is no longer a single global decay constant

**Dixon-Coles correction**
- Rho parameter for low-score bias correction (default -0.10)
- Max goals modeled per side in the score matrix (default 8, giving a 9×9 matrix)

**Ranking weights**
- The composite weights used in Section 11's ranking formula

**Best bet pool controls (no daily cap — see Section 11)**
- Minimum confidence tier to qualify for best-bet status (default: Medium and above)
- Maximum predictions surfaced per fixture in any single best-bet API response (default 2, for display variety — this is a per-response presentation cap, not a production cap on how many valid predictions the pool actually contains)
- Probability floor for best-bet eligibility, below which a prediction is shown as general only regardless of confidence (default 0.55)

**Prediction horizon window**
- Rolling window length in days counted forward from the current calendar date (default 7, meaning today through the same day next week inclusive)
- Configured as a number of calendar days, not a number of kickoffs — all fixtures whose kickoff falls within the window are in scope, regardless of how many that is
- This value may be tuned upward as the product matures and user demand for longer-horizon predictions is validated; it must never be set below 1 (today-only) or above a value the team has validated the model's accuracy holds reasonably for

**Interleaved-match embargo**
- The embargo is on by default and cannot be disabled globally — only per-league exclusions are allowed (see Section 5C.5)
- Minimum gap between a team's interleaving match and their subsequent fixture, below which the subsequent fixture's prediction is embargoed (default: the subsequent fixture's kickoff must be more than 72 hours after the interleaving match's kickoff, otherwise the embargo holds — adjust based on operational experience)
- The embargo lifts the moment the interleaving match reaches full-time status and all relevant statistics are ingested, regardless of whether the 72-hour gap has elapsed (see 5C.3) — the gap threshold is only relevant when the interleaving match has not yet completed

**Accumulator (acca) engine controls**
- Minimum number of legs permitted in an acca (default 2)
- Maximum number of legs permitted in an acca (default 8 — beyond this the combined probability becomes so small as to produce effectively meaningless fair-odds estimates)
- Minimum confidence tier for a prediction to be eligible as an acca leg (default: Low and above — accas by design combine multiple legs and the multi-leg combination already filters toward better expected value; using Medium as a floor here is an option for a stricter variant, configurable)
- Maximum legs sourced from the same fixture in a single acca (default 2 — same joint-probability reason as the per-fixture cap in best-bet presentation; see Section 20.4)

**Worker pool and execution**
- Maximum concurrent fixture-processing workers (default: number of available CPUs minus 2, reserving headroom for PostgreSQL and the Go backend; minimum 1; set explicitly, never derived dynamically at runtime)
- Lifecycle stage priority ordering weights for the worker pool queue (5B.4)

**Per-feature-group recency decay (new — Section 6.6)**
- A separate decay rate per feature group, not one global rate: match-result form, xG-based form, cards/discipline form, corners form, and lineup/player-availability recency each get their own configured decay constant, defaulted from Section 6.6's starting values and refined by walk-forward validation (Section 24)

**Market-specific recommendation thresholds (new — Section 12.4, replaces the single global "Medium and above" rule)**
- Per market family (1X2, double chance, BTTS, over/under, corners, cards, and so on), a separately configured, separately validated confidence-tier-to-recommendation mapping — never one universal 0.65 applied blindly across every market

**Calibration (new — Section 9)**
- Calibration method per market family (default: isotonic regression for markets with sufficient sample size, Platt/sigmoid as the fallback for low-sample markets, beta calibration where bounded-proportion targets — Section 8.4.6 — make it the better statistical fit)
- Minimum sample size before a calibration model is trusted in production (default 500 settled predictions for that market; below this, calibration falls back to a coarser parent scope — see Section 9.5)
- Calibration re-fit cadence (default: monthly, or immediately upon a drift alert per Section 22)

**Out-of-distribution thresholds (new — Section 5D)**
- `ood_score` threshold above which confidence is capped, and a separate, higher threshold above which the gate forces BASIC or NO_RECOMMENDATION regardless of what confidence scoring alone would have produced

**Hierarchical shrinkage (new — Section 5D.4)**
- Bayesian shrinkage strength (how heavily an early-season or newly-promoted team's estimate is pulled toward the league prior) as a function of matches-played, tuned per league tier

**Drift monitoring (new — Section 22)**
- Rolling window size for drift detection per metric (default 200 predictions for accuracy drift, 30 days for calibration drift)
- Alert thresholds per drift type, separate from the retraining-trigger thresholds (an alert can fire well before retraining is warranted)

**Retraining policy (new — Section 23)**
- Minimum new settled sample count before a retraining candidate is even generated (default 500 new settled predictions since the production model's training cutoff, per market family)
- Scheduled evaluation cadence independent of drift triggers (default monthly, so a slowly-decaying model doesn't go unevaluated indefinitely just because it never crosses a hard drift threshold)

**Model registry / promotion (new — Section 21)**
- Minimum evaluation sample count before a champion/challenger decision may be made (default 500 settled predictions in shadow, consistent with Section 25's minimum validation sample)
- Statistical significance requirement for promotion (default: challenger's improvement over champion must be significant at a configured confidence level, not just numerically higher — Section 18)

### 4.2 Configuration validation requirement

On engine startup, validate that:
- All blend ratios per market sum to 1.0
- All confidence tier thresholds are strictly descending (Strong > Medium > Low > 0)
- No negative weights anywhere
- Decay rate is strictly between 0 and 1
- Horizon window length is a positive integer (at least 1)
- Acca minimum legs is at least 2 and less than or equal to acca maximum legs
- Worker pool size is at least 1
- Embargo minimum gap is a positive integer number of hours
- Every per-feature-group decay rate is strictly between 0 and 1
- Every market family has an assigned calibration method and a defined minimum sample threshold
- OOD thresholds are strictly ordered (the confidence-cap threshold is lower than the force-BASIC threshold)
- Retraining and champion/challenger minimum sample counts are positive integers

If validation fails, the engine must refuse to run and log a clear error — never silently fall back to defaults mid-run, since that would make a run non-reproducible (violates Design Principle 6).

---

## 5. The Match Quality Gate

This is the component that decides, before any model runs, whether a fixture should be: skipped entirely, given basic-only output, or given the full prediction treatment. This logic runs once per fixture and its output governs everything downstream.

### 5.1 Three possible gate outcomes

- **SKIP** — No predictions generated for this fixture at all, for any market. Reasons logged. The fixture still appears in "today's matches" listings (a downstream concern) but carries no prediction data.
- **BASIC** — Tier A markets only, shown as plain probability percentages (no confidence tier, never eligible for "best bet" status). This is the "show only basic stats with probabilities" behavior from the original product brief.
- **FULL** — All applicable market tiers run, confidence scored, best-bet eligible.

### 5.2 Gate evaluation order (run in this exact sequence; first matching SKIP condition stops evaluation)

0. **Horizon and embargo pre-check (evaluated before the gate)** — If the fixture is outside the 7-day rolling prediction window (Section 5C.1), do not enter the gate at all — stop with no output and no log entry, since fixtures outside the window are not predictions to be skipped but simply not yet in scope. If the fixture is within the window but under an active interleaved-match embargo (Section 5C.3), defer — do not enter the gate, but do log the embargo status to `fixture_prediction_horizon` so the orchestration layer can re-trigger once the embargo lifts (5C.5). Only after both horizon and embargo checks pass does the gate evaluation below begin.

1. **Fixture status check** — If status is Cancelled, Postponed, or Abandoned, SKIP immediately. No data quality calculation needed.
2. **Minimum sample size check** — If either team has fewer than the configured minimum historical matches (default 5) in the relevant competition context, SKIP. A team with 2 matches of data cannot produce a statistically meaningful Poisson rate.
3. **Base data completeness check** — Compute the data completeness score (Section 5.3). If below the SKIP floor (default 0.35), SKIP.
4. **Dual-injury check** — If both teams show injury impact scores above the configured threshold (default 0.50 each) simultaneously, SKIP. Two severely weakened teams produce an unreliable matchup regardless of how good their historical data is, because the historical data no longer represents the team that will play.
5. **Tier assignment** — If none of the above triggered a SKIP, but data completeness is below the FULL floor (default 0.55), assign BASIC. Otherwise assign FULL.
6. **Soft-flag logging (does not change tier, but is stored alongside the prediction for transparency)** — fixture congestion above threshold, early-season flag (fewer than 6 gameweeks played this season), missing referee data, missing odds data. These are recorded as caveats attached to the explanation (Section 11), not silent.

### 5.3 Data completeness score — exact composition

This single 0.0–1.0 score is the backbone of the entire gate. Compute it as a weighted sum:

- **Team history depth (40% weight)** — average of (matches available for home team ÷ 10, capped at 1.0) and the same for away team
- **Season stats presence (20% weight)** — full points if both teams have a `team_season_stats` row for the current season, half points if only one does, zero if neither
- **Head-to-head depth (15% weight)** — full points if 4+ prior meetings exist, partial (about half) if 2-3 exist, zero if fewer than 2
- **Referee data presence (10% weight)** — full points if `referee_stats` exists for the assigned referee, zero otherwise (does not block the fixture, just lowers completeness, which the set-piece model fallback then compensates for separately)
- **Lineup confirmation (10% weight)** — full points if lineups are confirmed (only meaningful close to kickoff — earlier runs of the engine will legitimately score lower here and that's expected, not a bug)
- **Odds availability (5% weight)** — full points if at least one bookmaker's 1X2 odds are present for this fixture

This score is stored against every prediction row (not just computed transiently) so that historical predictions can later be audited against the data completeness they were made with.

### 5.4 Critical design note on per-market-tier strictness

The base gate above governs whether a fixture gets predictions at all. But Tier B and Tier C markets need an **additional, stricter check on top of fixture-level gating**, because a fixture can have perfectly good goal-history data while having patchy or absent corner/card/shot data (this happens constantly in lower-tier leagues). This second-level check happens at Section 10 (per-market eligibility), not here — the fixture-level gate in this section only ever blocks Tier A. A fixture passing FULL tier here is a necessary but not sufficient condition for, say, a corners market to be shown.

### 5.5 Relationship to the out-of-distribution gate

Section 5.2's evaluation order above governs *data sufficiency* — does enough history exist at all. It says nothing about whether the fixture, despite having enough raw data volume, is statistically *unlike* anything the models were validated on (a newly promoted club with five matches of top-flight data behaves nothing like an established mid-table side with five matches of data, even though both pass the sample-size check identically). That is a separate concern, evaluated immediately after this gate and before any model runs — see Section 5D. A fixture can pass this section's gate at FULL and still be forced down to BASIC, or to no recommendation, by the OOD gate. The two gates are sequential and independent: this section asks "is there enough data," Section 5D asks "is this data trustworthy for what the models were built to handle."

---

## 5A. The Incremental Prediction Lifecycle (Late-Arriving Data)

This section exists because of a specific, real constraint: confirmed lineups typically don't arrive until roughly an hour before kickoff, injury news can change in the hours before a match, and a prediction engine that only ever runs once per fixture, far in advance, is either stale by kickoff or has to ignore signal that genuinely changes the picture (a key striker confirmed out, a backup goalkeeper starting). The fix is not "wait until the last possible moment to predict" (that breaks every downstream use case that needs predictions hours or days ahead — best-bet listings, weekend preview pages, acca building) and it is not "predict once and never touch it again" (that throws away real signal). The fix is treating every fixture's prediction as something that passes through defined stages, each producing a complete, fully valid, separately confidence-scored prediction set — never a partial or broken one.

### 5A.1 Lifecycle stages (named windows relative to kickoff)

- **Stage EARLY (kickoff minus several days down to roughly kickoff minus 6 hours)** — the first stage at which a fixture can produce a FULL-tier prediction, once it's passed the Section 5 gate. Lineups are never available here. Confidence scoring (Section 11.2) naturally scores the lineup-confirmation factor at zero for every prediction produced in this stage, which is expected and correct, not a defect.
- **Stage PRE-MATCH (roughly kickoff minus 6 hours down to roughly kickoff minus 90 minutes)** — injury/availability data (`player_availability`) is most likely to have firmed up by this point even though lineups themselves haven't been announced. A re-run here picks up any injury news that's emerged since the EARLY stage and reflects it in the injury-impact features (Section 6.1, Group 5) and therefore in the model outputs and confidence.
- **Stage LINEUP-CONFIRMED (triggered by data arrival, not a fixed time offset — typically lands somewhere around kickoff minus 60 to 75 minutes for competitions that publish lineups early, but the trigger is the data itself, never a clock)** — this is the highest-confidence stage achievable pre-match. Once `lineups_confirmed` flips true in the upstream data for a fixture, that fixture becomes eligible for a lineup-aware re-run regardless of what wall-clock time it happens to be. The lineup-confirmation confidence factor (10.1) now scores full points where it previously scored zero, and if the confirmed lineup shows an unexpected absence not already reflected in `player_availability` (a key player simply left out of the squad without a formally logged injury), the injury-impact feature must be recomputed using the actual named starting XI versus each team's established regular-starter baseline, not just the formally logged injuries table — a quietly rested star striker affects the match exactly as much as a publicly announced injury does, and the engine must treat it the same way.
- **Stage LOCKED (kickoff itself onward)** — no further pre-match re-runs are issued for this fixture. The last LINEUP-CONFIRMED (or, if lineups never arrived for this competition/league, the last PRE-MATCH) prediction set is what stands and is what gets settled post-match per Section 12. This stage exists so that "the prediction" has an unambiguous, final, citable version once the match has actually started — there is no value in re-predicting a match that's already kicked off, and doing so would also violate Design Principle 6 (the engine predicts outcomes, it does not chase a moving target mid-event, which is explicitly out of scope per Section 1's exclusion of in-play markets).

### 5A.2 What "re-running" actually means

A re-run for a fixture already in EARLY or PRE-MATCH stage is not a patch, a diff, or a partial update applied to the previous prediction set. It is the entire pipeline (gate re-evaluation, Section 6's full feature re-assembly, Section 8's full model run, Section 10's full confidence scoring, Section 11's re-ranking) executed again from scratch against the database's current state, consistent with Design Principle 6 (pure function of current state). The only thing that changes between two runs for the same fixture is that the database now contains more or better information than it did before. This matters for a concrete reason: if re-runs were partial patches, subtle bugs could leave a prediction set in an inconsistent state (e.g., the 1X2 market reflecting confirmed lineups while the BTTS market for the same fixture still reflects the pre-lineup estimate). A full re-run guarantees every market in a fixture's prediction set is always internally consistent with the same snapshot of knowledge.

### 5A.3 Versioning requirement

Every prediction row (Section 3's schema, written by the engine) must carry a `lifecycle_stage` value (one of EARLY, PRE_MATCH, LINEUP_CONFIRMED) and a `generated_at` timestamp. When a re-run produces a new prediction set for a fixture, the new rows do not overwrite the old ones in place — they are inserted as a new version, and the previous version is marked superseded (a `superseded_at` timestamp set, row retained, never deleted). Two things depend on this: first, the accuracy ledger (Section 12) needs to be able to state, honestly, which lifecycle stage's prediction is the one being graded for accuracy (this document's default rule: **the LOCKED-stage prediction — i.e., the last version produced before kickoff — is the one settlement always grades**, since that's the most-informed prediction the engine ever produced and grading an early, deliberately-less-confident version would understate the value of the lineup-aware refinement); second, any API surface built on top of this engine (out of scope for this document, but the contract must support it) can choose to show "what we said this morning" versus "what we say now" if that's ever a desired product feature, because the history is genuinely preserved rather than overwritten.

### 5A.4 Confidence must reflect lifecycle stage honestly, not just data completeness in the abstract

Section 11.2 already scores a lineup-confirmation factor, and Section 5.3's data completeness score already includes a lineup-confirmation sub-weight. This section adds one explicit rule on top: a prediction produced in EARLY stage must never be displayed or treated identically to a prediction produced in LINEUP_CONFIRMED stage for the same fixture, even if every other feature happens to be identical, because the two predictions carry genuinely different epistemic weight. This is already mechanically true given how the scoring works (the lineup factor differs), but it's stated here explicitly because it's the kind of distinction a build agent could accidentally erode over time (for instance, by caching an EARLY-stage confidence score and reusing it across stages as a shortcut) — that must never happen.

### 5A.5 What does NOT trigger a re-run

To avoid re-running fixtures needlessly (which directly feeds into the concurrency/load concerns addressed next), a re-run is only ever triggered by one of: the stage-boundary schedule itself (Section 29 defines the invocation pattern), or an explicit data-arrival event for that specific fixture (lineups newly confirmed, an injury status newly logged). Cosmetic or unrelated database writes (e.g., another fixture's data being ingested, a standings refresh for a different matchday) must never cascade into unnecessary re-runs of fixtures whose own relevant data hasn't changed. This is an orchestration-layer responsibility to enforce (the engine itself, per Design Principle 6, simply does the right thing whenever it's invoked) but it's stated here so the build agent designing the invocation triggers understands why over-triggering is a real cost, not just an inefficiency — see 5B.4 below for the resource impact specifically.

### 5A.6 Prediction stability as a first-class signal

Because a fixture passes through multiple lifecycle stages, each producing a complete, independently-computed prediction, the engine has something most prediction systems never get: a natural check on itself. A prediction that lands on Home Win at 64% in EARLY, 63% in PRE_MATCH, and 65% at LINEUP_CONFIRMED is a prediction that new information kept confirming. A prediction that goes 67% → 58% → 49% across those same three stages is telling you something too — that this fixture's outcome is genuinely sensitive to information that only firms up close to kickoff, and treating the EARLY-stage 67% with the same trust as a stable prediction's EARLY-stage number would be a mistake.

**Computation:** on every re-run past the first (i.e., every PRE_MATCH and LINEUP_CONFIRMED version), compute `probability_delta` as the signed difference between this stage's `calibrated_probability` and the immediately preceding stage's, for the same fixture and market. Track `max_lifecycle_probability_shift` as the largest absolute delta observed across all stages so far for this fixture/market. Set `prediction_direction_changed` to true if the leading outcome itself flipped (e.g., Home Win was the most probable outcome in EARLY, Draw becomes most probable in PRE_MATCH) rather than just moving within the same leading outcome. Compute `lifecycle_stability_score` (0.0–1.0, 1.0 being maximally stable) as a function of `max_lifecycle_probability_shift` relative to a configured tolerance band — small, expected shifts (a few percentage points as injury news firms up) barely move this score; large or direction-flipping shifts move it substantially.

**Use:** `lifecycle_stability_score` becomes one input to confidence scoring (Section 11.2) — for any prediction past its first lifecycle version. A first-ever EARLY-stage prediction has no prior stage to compare against and is scored as neutral on this factor (neither rewarded nor penalized) rather than being disadvantaged simply for being new. This is a case where more information (a second and third data point on the same fixture) makes the confidence system strictly better over the course of a fixture's own lifecycle — an EARLY prediction that turns out stable through PRE_MATCH is *more* trustworthy at LINEUP_CONFIRMED than an otherwise-identical prediction with no stability history, and the confidence score should reflect that.

### 5A.7 Shadow settlement across every stage, not only LOCKED

Section 17 (Settlement) grades the LOCKED-stage version of every prediction — that remains the official, user-facing, headline number the accuracy ledger reports. But EARLY, PRE_MATCH, and LINEUP_CONFIRMED versions are not simply discarded once superseded (5A.3 already establishes they're retained, marked superseded, never deleted) — they are also **shadow-settled** against the same actual outcome, written to the separate `shadow_settlements` table (Section 3.3) rather than mixed into the public `prediction_results` ledger. This is what makes it possible to eventually answer a genuinely valuable question this document couldn't answer before: does waiting for lineups actually improve accuracy, and for which markets, by how much. See Section 18 for the full methodology this unlocks.

---

## 5B. Concurrency & Execution Model (Many Simultaneous Kickoffs Without Bottlenecking)

This section exists because of a specific, real operational fact: football fixtures cluster heavily around a small number of kickoff times (Saturday 15:00 in England being the canonical example, but every league has its own clustering pattern), so the engine must be able to process dozens of fixtures whose EARLY, PRE_MATCH, or LINEUP_CONFIRMED stage boundaries land in the same narrow window, without one slow or problematic fixture delaying or blocking the others, and without the aggregate load spiking resource usage in a way that starves the rest of the system (the Go backend, the database itself, other concurrent workloads on the same VPS).

### 5B.1 The foundational invariant: per-fixture independence

Design Principle 9 states this already; this section is its operational consequence. Every step from Section 6 (feature assembly) through Section 11 (ranking, for that fixture's own predictions) must be expressible as a unit of work that takes a single fixture ID and the current database state as input, and produces that fixture's complete prediction set as output, with no dependency on any other fixture's prediction work happening concurrently. This is what makes the "many matches at once" problem tractable: if fixtures are independent, the system can simply run many of them at the same time, bounded only by available compute, rather than needing any fixture to wait its turn behind another.

### 5B.2 The one deliberate, controlled exception: league-average baselines

Section 6.2 already establishes that league-average baselines (for goals, corners, cards, fouls — every Poisson-modeled statistic) are computed once per league-season per engine run, not per-fixture, specifically to avoid wasteful and potentially inconsistent recomputation. This is the only shared state in the entire pipeline, and it must be handled deliberately rather than accidentally: compute all required league-average baselines for a given run as a distinct, fast, read-only precomputation pass before fixture-level work begins, store the results in an in-memory structure (or a lightweight cache) that is read-only for the remainder of that run, and never allow any fixture's processing to write to or mutate this shared structure. Read-only shared state accessed concurrently by many independent fixture-processing units is safe and does not reintroduce the coupling Design Principle 9 prohibits; the prohibition is specifically against fixtures depending on or blocking each other, not against fixtures legitimately sharing a precomputed, immutable reference value.

### 5B.3 Execution pattern: bounded worker pool, not unbounded parallelism and not serial processing

Two naive approaches both fail here: processing fixtures one at a time in a loop means fifty fixtures sharing a kickoff window could take fifty times as long as one fixture, which directly risks predictions for later-processed fixtures in the batch not being ready in time, especially for the time-sensitive LINEUP_CONFIRMED stage where the whole point is reacting quickly once lineups land. Spawning unlimited concurrent processes for every fixture simultaneously risks overwhelming the VPS's CPU, memory, and PostgreSQL connection pool all at once, which on the resource-constrained VPS tier this platform runs on (a handful of CPU cores, not a large cluster) would create exactly the kind of strain and bottleneck this update is asking to avoid, just shifted from "too slow" to "resource exhaustion."

The correct pattern is a **bounded worker pool**: a fixed, configured maximum number of fixtures are processed concurrently at any moment (the exact number is a deployment/infrastructure tuning concern, not something this document hardcodes, but it must be configured and must be meaningfully lower than the total core count available, leaving headroom for PostgreSQL itself, the Go backend, and other concurrent workloads on the same machine). As soon as one fixture's processing completes, the next queued fixture begins. This bounds peak resource usage to a known, controlled ceiling regardless of whether 5 or 500 fixtures need processing in a given window, while still processing many fixtures genuinely concurrently rather than one at a time.

### 5B.4 Priority ordering within the worker pool

When more fixtures need processing than the worker pool can run simultaneously, they must not be processed in arbitrary or purely first-come order. Order the queue by urgency: fixtures whose LINEUP_CONFIRMED-stage data has just arrived (time-sensitive, since the value of lineup-aware re-prediction decays the closer it gets to kickoff without being reflected) take priority over fixtures still in their EARLY-stage initial run for a match that's still days away. Within the same urgency tier, order by proximity to kickoff (a fixture kicking off in 90 minutes is processed before one kicking off in 5 hours, all else equal). This priority ordering is itself a configuration concern (Section 4 should include a `lifecycle_stage_priority` weighting), not hardcoded logic, consistent with the rest of this document's approach to tunables.

### 5B.5 Database access pattern under concurrent load

Each concurrently-executing fixture's feature-assembly step (Section 6) issues its own read queries against PostgreSQL independently. To avoid this concurrent read load itself becoming a bottleneck: queries must be scoped as tightly as possible to exactly the fixture (and that fixture's two teams, league, and relevant referee) being processed, never broader; the database connection pool available to the engine must itself be bounded and sized deliberately relative to the worker pool size from 5B.3 (a worker pool of N concurrent fixture-processors needs at most N actively-used connections at a time for engine reads, plus headroom — connection pool exhaustion is a common, avoidable source of exactly the kind of strain this update is asking the design to prevent); and the engine's final write step (persisting the new prediction version per 5A.3) should be a small, fast, focused write transaction per fixture, not a long-held lock, so that one fixture's write never blocks another fixture's concurrent write to a different row.

### 5B.6 Idempotency and safe retries

Because many fixtures are processed concurrently and because re-runs are triggered by data-arrival events (5A.5) that could in principle fire more than once or overlap with a scheduled stage-boundary run, every fixture-processing unit of work must be safely re-runnable: if the same fixture is (correctly or by a triggering glitch) processed twice in close succession, the result must be the same complete, valid prediction set either way (consistent with Design Principle 6), and the versioning scheme from 5A.3 must handle two near-simultaneous completions for the same fixture gracefully — the later `generated_at` timestamp wins and becomes the current version, the earlier one is marked superseded, and no partial or corrupted intermediate state is ever visible to anything reading from the predictions table.

### 5B.7 Failure isolation

If feature assembly, a model, or any other step throws an error for one specific fixture (malformed or unexpectedly missing data for that fixture, a numerical edge case, anything), that failure must be caught, logged with the specific fixture ID and stage it failed at, and must not propagate to or affect any other fixture currently being processed in the same worker pool batch. A batch of fifty fixtures with one bad fixture should yield forty-nine successful prediction sets and one logged failure — never a batch-wide failure, and never a silent skip that goes unlogged. This directly follows from Design Principle 9: if fixtures are truly independent, then one fixture's failure has no mechanism by which it could affect another's success.

---

## 5C. Prediction Horizon Window & Interleaved-Match Embargo

This section governs which fixtures the engine is even permitted to produce predictions for, based entirely on their position in time relative to today and relative to each team's own fixture schedule. It is logically prior to the match quality gate (Section 5) — a fixture that fails this section's eligibility check never reaches the gate at all. This is a deliberate separation: the match quality gate answers "is the data good enough to predict this fixture," while this section answers "is now the right time to predict this fixture at all, regardless of data quality."

### 5C.1 The 7-day rolling prediction horizon — definition and rationale

The engine only produces predictions for fixtures whose kickoff timestamp falls within a rolling window of 7 calendar days from the current date, calculated from midnight at the start of today through midnight at the end of the day 7 days from now. If today is Wednesday the 4th, the window covers Wednesday the 4th through Wednesday the 11th inclusive.

**Why 7 days and not more:** prediction accuracy for football matches degrades meaningfully with lookahead time, for reasons the engine cannot compensate for with better statistics: squads rotate, injuries develop, managers rest players before big games, and the relative importance of matches shifts as other results arrive around them. Predicting 10 or 14 days out with the same displayed confidence as predicting tomorrow would mislead users. Seven days is the range at which the data the engine has access to (recent form, injuries, standings, H2H) remains plausibly current and relevant to the actual lineup and motivation level the teams will bring to the pitch. If the product later validates that accuracy holds further out for specific leagues and specific market types, the configured window can be extended — but the default must be conservative.

**Why 7 days and not fewer:** a window shorter than 7 days breaks the weekend-preview use case (viewing Saturday and Sunday fixtures from a Monday or Tuesday), the "fixtures this week" use case, and the acca engine's ability to build multi-day accumulators across a weekend programme. Seven days specifically covers one complete football programme cycle: midweek fixtures plus the following weekend's full schedule, which is the natural planning horizon for how football fans engage with prediction content.

**Implementation — what "within the window" means precisely:** compare `fixture.kickoff_at` against `current_date` (midnight UTC start of today) and `current_date + 7 days` (midnight UTC start of the day 8 days from now). A fixture whose kickoff is exactly at midnight on the 8th day is NOT within the window. Store the horizon check result on `fixture_prediction_horizon.horizon_eligible` (per the write contract in Section 3.3).

### 5C.2 The interleaved-match embargo — purpose and the problem it solves

The embargo is the specific rule that handles a scenario that would otherwise silently produce misleading predictions: Team A plays on Wednesday night and again on Saturday. If the engine predicts Saturday's match on Monday morning, before Wednesday's match has happened, the Wednesday result is genuinely unknown. What happens Wednesday matters for Saturday — fatigue from an extra-time match, possible suspensions from cards received, the psychological effect of a big win or a humiliating defeat, and simply the factual update to recent form that Wednesday's result will represent. Predicting Saturday with Monday's data, then displaying that prediction on Friday afternoon after Wednesday's result is already known and in the database, makes the prediction look less informed than it actually could be, because it wasn't re-triggered after the interleaving match completed.

The embargo rule solves this by deferring prediction of the subsequent fixture (Saturday) until after the interleaving fixture (Wednesday) has reached full-time and its data is ingested. The Saturday prediction is not cancelled — it is **deferred**, and the moment Wednesday's match settles, Saturday's prediction is automatically queued for its first or updated run with the fresh data already available.

### 5C.3 Embargo — exact eligibility logic

For every fixture F in the horizon window, before any gate or prediction work begins, evaluate the following check for both the home team and the away team:

1. Query all competitive fixtures involving this team that have kickoff timestamps **before F's kickoff** and **within the same 7-day horizon window** (or within the configured embargo gap window before F's kickoff — see 5C.4). Call any such fixture an "interleaving fixture" I.
2. If any interleaving fixture I exists AND I has not yet reached full-time status (status is not "FT" or equivalent final states in the API-Football status vocabulary), then fixture F is under embargo for that team. Since both teams' statuses must be checked, the embargo fires if **either** team has an interleaving match that has not yet completed.
3. Mark `fixture_prediction_horizon.embargo_active = true` for fixture F, populate `embargo_reason` with a human-readable explanation of which interleaving fixture is blocking and why (e.g. "Team Arsenal has a fixture vs. Man City on Wednesday 8th at 19:45 UTC that has not yet concluded — Saturday's prediction will be released once that result is available"), and populate `embargo_blocking_fixture_id` with the interleaving fixture's ID.
4. Do not begin any feature engineering, gate evaluation, or model runs for fixture F until the embargo lifts.

### 5C.4 The gap threshold — when the embargo is relevant within the window

Not every fixture that precedes another in the same window triggers an embargo. A team playing Tuesday and Saturday has a substantial gap; a team playing Thursday and Saturday has a minimal gap. The configured embargo gap (default 72 hours from Section 4's interleaved-match embargo config) determines the threshold below which the engine considers the two fixtures "interleaved" in a way that justifies deferral. Specifically:

- If the gap between the interleaving fixture's kickoff and the subsequent fixture's kickoff is **greater than** the configured embargo gap threshold, no embargo is applied. There is enough time between the two matches that: the interleaving match will be complete, statistics will be ingested, and a full prediction run for the subsequent fixture can be completed before it becomes meaningfully close to kickoff. In this scenario the engine simply runs both fixtures independently, with the subsequent fixture's run naturally incorporating the interleaving match's result if it has already been played.
- If the gap is **less than or equal to** the configured embargo gap threshold, the embargo applies and the subsequent fixture's prediction is deferred until the interleaving fixture completes.
- The 72-hour default is a starting point reflecting that a Wednesday 20:00 UTC match and a Saturday 12:00 UTC match have roughly a 64-hour gap, which is below threshold, while a Tuesday 20:00 UTC match and a Saturday 15:00 UTC match have roughly an 91-hour gap, which is above threshold. These are the canonical "midweek and weekend" cases the rule is designed around. Tune the threshold based on actual league schedules once operational.

### 5C.5 Embargo lift — the automatic re-trigger mechanism

The embargo for fixture F lifts the moment the blocking interleaving fixture I reaches full-time and its core statistics (at minimum: goals scored, result, yellow/red cards for discipline-relevant markets) are confirmed in the database. The specific trigger is: `fixtures.status` for fixture I transitions to "FT" (or equivalent) AND `fixture_statistics` contains at least one row for fixture I. Both conditions must be true — status alone is not sufficient since statistics ingestion may lag by a few minutes after the live status update.

When the embargo lifts for fixture F, the orchestration layer (Section 15) must automatically queue fixture F for its first prediction run (if it has never been predicted before) or for a full re-run at the current lifecycle stage if it was already in an EARLY-stage prediction set produced before the interleaving match occurred. This re-trigger is what ensures the Saturday prediction, when it is eventually shown to users, has Wednesday's result already incorporated into the form features (Section 6.1 Group 1) and any card/suspension implications incorporated into the player availability features (Section 6.1 Group 5).

**The embargo lift re-trigger must use the normal worker pool priority mechanism from 5B.4** — an embargo-lift re-run is treated as higher priority than a routine EARLY-stage sweep, since it often happens close to kickoff of the newly-unblocked fixture, but lower priority than a LINEUP_CONFIRMED trigger for a match about to kick off within the hour.

### 5C.6 Products built on top of the embargo — what they must show

This is an engine-level document and presentation is out of scope, but the engine's data contract (Section 3.3) must support the following downstream requirements unambiguously:

- Any fixture currently under embargo must have a clearly accessible status that downstream services can read and display accurately ("prediction coming after Wednesday's result")
- Any fixture that was under embargo and has since been released must have the `embargo_active = false` and a populated `embargo_blocking_fixture_id` that downstream services can use to explain the prediction's timing ("prediction updated after Wednesday's result")
- The `generated_at` timestamp on each prediction row is the proof of when that prediction was made and what lifecycle stage it was in, which is the engine's primary accountability mechanism

### 5C.7 Edge cases and the explicit decisions made for each

**What if both teams in Saturday's fixture each have a midweek game?** The embargo applies if either team has an outstanding interleaving match. If both do, the embargo lifts when both interleaving matches have concluded and both have statistics ingested — whichever happens last determines the lift time.

**What if the interleaving fixture I itself gets postponed?** A postponed match's status is not "FT" — it is "PST" or similar. The embargo on the subsequent fixture F must not be treated as lifted just because the interleaving match will now not happen before F kicks off. The embargo logic must re-evaluate: if the interleaving match is now confirmed postponed (and will not now happen before F), the embargo lifts immediately, because there is no longer a match result to wait for. Postponed means the interleaving data that was being waited for will never come — the best available prediction using current data is better than no prediction at all.

**What if the subsequent fixture F is itself within 24 hours of kickoff and the embargo is still active?** The embargo is not waived on grounds of urgency. However, the engine must log a prominent warning in this case: a fixture is approaching kickoff without a finalized prediction because the interleaving match has not yet completed. This scenario (e.g., Wednesday night match finishes at 22:30 UTC and Thursday 14:00 UTC is the subsequent fixture) is unavoidable if the gap between the two fixtures is very small. The engine's response is to produce the best prediction it can with the data it has the moment the interleaving match's statistics arrive, regardless of how little pre-kickoff time remains — even a prediction produced one hour before kickoff is better than none.

**What if a team has three matches within the 7-day window?** Apply the embargo to each pair in turn: the second fixture is embargoed pending the first, and the third fixture is embargoed pending the second. There is no chain shortcutting — the engine does not try to predict the third fixture even if the first is already complete, until the second is also complete, because the second's result will itself affect the third.

**What if the horizon window is set to something shorter than 7 days (e.g., 3 days for a future product variant)?** The embargo logic still applies within whatever window is configured. The two rules (horizon window and embargo) are independent; both must pass for a fixture to be eligible.

### 5C.8 Products that depend on the 7-day window — and what each gets

This section clarifies how the horizon window maps to the product surfaces the engine is expected to support:

| Product surface | Date range queried from the prediction pool | Embargo interaction |
|---|---|---|
| Today's predictions | Current calendar day only | Embargoed fixtures are absent; today's matches are almost always within their embargo-free window since any interleaving match would have been earlier in the week |
| Tomorrow's predictions | Next calendar day | Embargo may apply if a team plays today |
| This weekend's predictions | Nearest Saturday + Sunday | Embargo most commonly applies here — teams playing Wednesday-Thursday before a weekend fixture |
| Saturday picks | Nearest Saturday only | Same embargo behaviour as weekend |
| Sunday picks | Nearest Sunday only | Same |
| Next week's predictions | Days 4–7 from today | Embargo will mostly not apply at this range since interleaving matches are themselves within the window and will complete before the subsequent fixtures kick off, but the logic still runs and may defer |
| Full 7-day window | All 7 days | Full pool; embargoed fixtures appear with embargo status visible |
| Accumulators (Section 20) | User-specified start/end within the 7-day window | Embargoed fixtures are excluded from acca leg candidates entirely — an acca must never include a leg whose prediction was built on incomplete information relative to what will be available before the match |

---

## 5D. Out-of-Distribution Detection & New/Promoted-Team Handling

Every gate so far in this document asks whether enough data exists. This section asks a different, harder question: even with enough raw volume, is this fixture statistically *like* the fixtures the models were built and validated against — or is it the kind of case where the model's usual accuracy simply doesn't apply, no matter how confident its output looks?

### 5D.1 Why this is a distinct concern from the data-completeness gate

A newly promoted club's first five top-flight matches pass the minimum-sample-size check (Section 5.2) exactly as cleanly as an established mid-table side's five matches do. The two situations are not remotely comparable: the established side's five matches are five noisy observations of a reasonably stable underlying team; the promoted side's five matches are five observations of a team whose entire statistical profile — quality of opposition, tempo, physicality — just changed discontinuously. Data completeness scoring (Section 5.3) has no way to see this difference, because it only counts rows, not context. Out-of-distribution detection exists specifically to catch the cases where "enough rows" and "trustworthy rows" diverge.

### 5D.2 What counts as out-of-distribution

- A newly promoted club, or a club newly entering a competition the models have never been trained on
- A team with only a handful of matches in the current competitive context, even if technically above the minimum-sample floor
- Major squad turnover (a large fraction of the regular starting XI has changed since the training data was collected — see Section 5F for how this is measured)
- A statistical profile (goals, shots, cards, possession — any feature group) that sits far outside the range the training data covered for that league/tier
- A league the production model was never trained or validated on at all
- A team returning after a long inactivity gap (relegation-then-promotion cycles, a suspended competition resuming, a club that folded and reformed)

### 5D.3 Computed signals

- `unseen_team_flag` — true if this team has no meaningful history in the training data the current production model was built from, independent of how much *recent* data exists
- `unseen_league_flag` — true if this league was not part of the production model's training scope at all
- `major_squad_change_flag` — true if Section 5F's squad-turnover measure exceeds a configured threshold since the reference period the team's historical stats represent
- `feature_distance_score` — a distance metric (e.g., Mahalanobis distance, or a simpler standardized-distance measure to start) between this fixture's feature vector and the centroid/distribution of the training data for the relevant league tier — the further out, the higher the score
- `ood_score` — a single combined 0.0–1.0 score rolling up the above signals, computed the same way for every fixture so it can be thresholded consistently in Section 4.1's OOD configuration group

### 5D.4 What high OOD does

Per the configured thresholds (Section 4.1): a moderate `ood_score` caps the achievable confidence tier for this fixture regardless of what every other confidence factor (Section 11) would otherwise produce — the fixture can still get a FULL-tier prediction, but it cannot be labeled Strong no matter how clean the raw numbers look, because the model has genuine reason to distrust its own applicability here. A high `ood_score` forces the fixture down to BASIC (numbers shown, no recommendation) or, past a second, higher configured threshold, to NO_RECOMMENDATION entirely (Section 12.3's `HIGH_OOD_SCORE` reason code). This is enforced as a hard cap applied *after* normal confidence scoring, the same pattern already established for market-specific confidence ceilings (Section 11.3) — the underlying score stays visible for audit, the cap prevents it from ever being presented as more trustworthy than it honestly is.

### 5D.5 Hierarchical fallback and Bayesian shrinkage for new/promoted teams

Detection alone isn't enough — the feature layer needs a principled way to still produce *something* reasonable for a new or promoted team rather than either refusing outright or using two or three matches of extreme, noisy data at face value.

**Hierarchical fallback chain**, applied in this order, falling through only as far as necessary:

1. Current team-season data, where it exists
2. Previous season's data for this team, adjusted for the strength difference between the tier they're now in and the tier that data was collected in (a promoted side's second-tier scoring rate is not a neutral estimate of their top-flight scoring rate — it needs a tier-adjustment factor, itself estimated empirically from how previously-promoted teams' rates shifted, and refined over time as more promotion cycles are observed)
3. League baseline for the tier the team is currently competing in, where neither of the above exists (a genuinely new entrant with no prior-tier history at all)
4. Global prior across all covered leagues, as the final fallback, used only when nothing more specific is available

**Bayesian shrinkage**, applied on top of whichever level of the fallback chain is active: an early-season or thin-history team's raw observed rate (goals scored per match, cards per match, whatever the feature is) is not used directly. It is shrunk toward the relevant prior (the league baseline, most commonly) in proportion to how little evidence exists — a team with 2 matches of current-tier data gets pulled heavily toward the prior; a team with 15 matches gets pulled only slightly. The shrinkage strength is a configured function of matches-played (Section 4.1), tuned per league tier since the appropriate amount of shrinkage differs by how variable that tier's teams typically are. The practical effect: a promoted team that has, by chance, scored 4 goals in their first 2 matches is *not* projected forward at a 2-goals-per-match rate — their estimate sits much closer to the league's promoted-team baseline until more evidence accumulates, exactly the failure mode this section exists to prevent.

---

## 5E. Manager & Squad-Regime Awareness

A club is not a statistically stationary object across its entire history. A team's underlying scoring rate, defensive solidity, and tactical tendencies can shift discontinuously at a managerial change in a way that gradual, decay-weighted form (Section 6.1) does not fully capture — decay weighting still treats every match as coming from "the same team," just recently or less recently, when a regime change means some of that history is arguably describing a different team in important respects.

### 5E.1 What's tracked

From the new `team_manager_history` table (Section 3.1): `manager_tenure` (days since current manager's appointment, as of `as_of_timestamp` — point-in-time correct per Section 3.4), `manager_change_recently` (boolean, true within a configured recency window — default 90 days), `matches_under_current_manager` (count, as of `as_of_timestamp`).

### 5E.2 How this feeds the models

Rather than a hard cutover (discard all pre-change history, a fragile approach for a manager who's been in charge only 2 matches), historical observations before a confirmed managerial change receive **gradually reduced weight**, layered on top of the ordinary recency decay from Section 6.1/6.6 — a second, regime-aware decay multiplier that only activates when `manager_change_recently` is true, so a stable, long-tenured manager's history is unaffected by this mechanism entirely. `matches_under_current_manager` also feeds directly into Section 5D's OOD assessment: a team with a very new manager and very few matches under them is closer to "new entity" than "known quantity," even though the club itself has years of history.

### 5E.3 Deliberately not over-engineered for v1

This document explicitly does not require, for the first production version: modeling *why* a managerial change matters (tactical system detection, playing-style clustering), or extending the same regime-change concept to individual milestone signals (a new goalkeeper, a star departure, a major transfer-window turnover) with the same depth as manager changes get. Those are real, valid extensions — a major transfer-window squad turnover or a goalkeeper change absolutely can matter the same way a managerial change does — but they are **feature-versioned extension points** (Section 7's `feature_version` exists precisely so a future feature-set revision can add these without restructuring the whole model architecture), not v1 requirements. Track `manager_change_recently` and `matches_under_current_manager` now; leave transfer-window/goalkeeper-change signals as a documented, explicitly-named future feature addition in Section 5F.3 and Section 28's later phases rather than building them speculatively now.

---

## 5F. Player-Strength & Lineup-Strength Model

v1's injury-impact approach — treating a player as "important" purely because they started at least 60% of available matches — is a genuinely crude proxy: it can't distinguish a nominal starter who contributes little from a rotation player who happens to be decisive, and it produces no meaningful *degree* of impact, only a binary important/not-important signal.

### 5F.1 Design, from crude proxy toward a richer signal

The replacement is a graded player-impact system, built from whatever combination of the following the current data actually supports (not all of these will be available for every league — this is explicitly built to degrade gracefully rather than require the full set):

- Minutes share and starts share (how much of the team's available game-time this player actually occupies, a strictly better signal than a binary started/didn't-start threshold)
- Goals and assists contribution relative to the team's total output
- xG/xA contribution where the league has xG coverage (Section 3.1's `has_xg` flag)
- Defensive actions, for defensively-relevant positions
- Goalkeeper-specific impact, treated separately from outfield players entirely (a goalkeeper change is not comparable to a winger change and must not be scored on the same scale)
- Team performance with this player versus without them, where enough matches of both exist to compute it meaningfully (this is the single strongest signal when available, and the weakest to compute reliably on a thin sample — weight it accordingly, shrinking toward the simpler minutes/goals-based signals when the with/without sample is small)
- Position importance (a like-for-like center-back absence is not equivalent to a like-for-like striker absence in every context — position-specific weighting, not a flat per-player score)
- Replacement strength (who actually comes in matters — losing a starter to a similarly-capable deputy is a smaller true impact than losing a starter to a clearly weaker one, even though a crude binary signal would score both absences identically)

### 5F.2 Outputs

- `expected_starting_XI_strength` — computed pre-lineup-confirmation, from each team's most-likely starting XI given historical selection patterns
- `confirmed_starting_XI_strength` — recomputed once lineups are confirmed (Section 5A.1's LINEUP_CONFIRMED stage), using the *actual* named XI rather than the expectation
- `missing_player_strength` — the aggregate impact of players who are unavailable (injury, suspension, rest, or — critically — simply omitted from a confirmed lineup with no formal injury logged, which Section 5A.1 already established must be treated identically to a logged absence)
- `bench_strength` — an aggregate signal for squad depth, used mainly as a secondary confidence input rather than a primary probability driver
- `lineup_strength_delta` — the difference between `expected_starting_XI_strength` and `confirmed_starting_XI_strength`, which is the single number that answers "how much did the actual lineup surprise us relative to what we assumed" and is exactly the kind of signal that should visibly move a prediction between PRE_MATCH and LINEUP_CONFIRMED (and register in Section 5A.6's stability tracking if the shift is large)

### 5F.3 What's required for v1 versus what the architecture must support later

A full player-level machine-learning model (per-player value ratings, transfer-market-informed strength estimates) is explicitly not required for the first production version — Section 26's pragmatism principle applies here directly. What v1 requires is the graded, multi-factor scoring above, computed from box-score-level data the engine already has access to (minutes, goals, starts, and xG where available), replacing the binary 60%-started proxy with something that actually varies in magnitude and degrades gracefully where richer data (with/without splits, defensive actions) isn't available. The output contract (5F.2's five fields) is designed so a future, more sophisticated player-strength model can replace the scoring *method* without changing what any downstream consumer (Model A's injury adjustment, Model C's feature set, the confidence scorer) expects to receive.

---
## 6. Feature Engineering

Every model in Section 8 consumes the same feature vector — there is one feature-building pass per fixture, not one per market. This avoids redundant computation and, more importantly, keeps every model looking at a consistent picture of the match.

### 6.1 Feature groups and their exact contents

**Group 1 — Recent form (computed separately for last-5 and last-10 windows, for each team)**
- Points-per-game, exponentially decay-weighted (most recent match weighted highest, weight = decay_rate^(games_ago); uses match-result form's own decay rate from Section 6.6, not one global constant shared across every feature)
- Goals scored average, decay-weighted (its own decay rate, per 6.6)
- Goals conceded average, decay-weighted (its own decay rate, per 6.6)
- Expected-goals-for average, decay-weighted (its own decay rate, per 6.6), falling back to actual goals scored when xG is null for that league (and flagging that fallback occurred — see 6.4)
- Clean sheet rate, decay-weighted
- BTTS rate (matches where both teams scored), decay-weighted
- Over-2.5 rate, decay-weighted
- Sample size used (raw count, separate from the weighted figures — this feeds the confidence scorer directly)

**Group 2 — Home/away venue splits (season-to-date, not decay-weighted — these are season aggregates)**
- Home team's home win rate, home goals-for average, home goals-against average, home clean sheet rate
- Away team's away win rate, away goals-for average, away goals-against average, away clean sheet rate
- Home/away xG averages where available

**Group 3 — Head-to-head (last 6 meetings, or fewer if that's all that exists)**
- Count of home wins, draws, away wins within this H2H sample
- Average combined goals across these meetings
- BTTS rate across these meetings
- Most recent meeting's result and date (recency-weighted signal, not a raw average — a H2H result from 4 years ago with squad turnover means less than one from 8 months ago)

**Group 4 — Standings context**
- Current league rank for each team
- Rank differential (home rank − away rank)
- Points-per-game across the season (distinct from recent-form points-per-game — this is season-long, recent form is short-window)

**Group 5 — Situational context**
- Injury/lineup impact score per team (0.0–1.0), sourced from Section 5F's graded player-strength model rather than a binary started-60%-of-matches proxy — this feature is `missing_player_strength` from Section 5F.2, not a hand-rolled recomputation
- Fixture congestion per team (derived from days since each team's last competitive match; capped scoring so a 3-day gap is treated more severely than a 6-day gap, with diminishing impact beyond 7 days)
- Match importance score (0.0–1.0; elevated for matches affecting title races, relegation battles, European qualification places, or designated rivalry/derby fixtures — exact computation defined in 6.3)
- Derby/rivalry flag (boolean, sourced from a maintained list — see 6.5)
- Early-season flag (boolean — fewer than 6 gameweeks played this season for either team)

**Group 6 — Set-piece and discipline base rates**
- Corners-for and corners-against season averages per team, split home/away
- Cards (weighted: yellow=1, red=2) season averages per team, split home/away
- Fouls season averages per team, split home/away
- Referee average cards-per-match and fouls-per-match (league-average fallback applied and flagged if referee-specific data is absent)

**Group 7 — Shot and possession base rates (Tier C support)**
- Shots-on-target season averages per team, split home/away
- Total shots season averages
- Possession averages per team, split home/away
- Pass accuracy averages per team

**Group 8 — Market cross-reference (only populated when data exists; never fabricated)**
- Bookmaker implied probabilities for 1X2, over/under 2.5, BTTS where odds data exists for this fixture, filtered to odds observed at or before this prediction's `as_of_timestamp` (Section 3.4) — never a later, closer-to-kickoff price
- API-Football's own native prediction percentages, if the `api_predictions` table has a row for this fixture (used purely as a cross-validation signal in confidence scoring — never as a direct input to the engine's own probability output, to avoid the engine just parroting someone else's model)

**Group 9 — Data quality meta-features**
- The full data completeness score from Section 5.3
- Per-group completeness sub-scores (e.g., "Group 6 completeness" specifically, since a fixture might have excellent goals data but poor corners data) — these sub-scores are what drive the Tier B/C per-market gating in Section 10
- Section 5D's out-of-distribution signals (`ood_score`, `feature_distance_score`, `unseen_team_flag`, `unseen_league_flag`, `major_squad_change_flag`) — computed as part of feature assembly, consumed by the gate immediately afterward

**Group 10 — Manager and squad-regime signals (Section 5E)**
- `manager_tenure`, `manager_change_recently`, `matches_under_current_manager`, all point-in-time correct as of this prediction's `as_of_timestamp`

**Group 11 — Lineup and player-strength signals (Section 5F)**
- `expected_starting_XI_strength` (always available), `confirmed_starting_XI_strength` and `lineup_strength_delta` (only once lineups are confirmed — null before then, exactly like the existing `lineups_confirmed` flag's own timing), `bench_strength`

### 6.2 League-average baselines (required for every Poisson-style calculation)

Before any team-level rate can be turned into an attack/defence strength multiplier, the engine needs the league's average rate for that same statistic, computed per league per season, separately for home and away (since home/away scoring rates differ systematically). This applies to goals, corners, cards, and fouls — every Poisson-modeled market needs its own league baseline. Compute and cache these once per league-season at the start of an engine run, not per-fixture, since recomputing them per fixture is wasteful and risks subtle inconsistencies if computed slightly differently each time within the same run.

### 6.3 Match importance scoring — exact method

This is one of the few feature groups that needs explicit method documentation since "importance" isn't a raw stat.

- Compute each team's mathematical distance from the relevant table thresholds (title-winning position, European qualification cutoff, relegation cutoff), based on current points, games remaining, and maximum possible points
- A team within realistic mathematical range of a major threshold (defined as: could reach or fall from that threshold within games-remaining × 3 points swing) contributes to a higher importance score
- Cup matches (knockout, single-elimination) automatically score at the higher end regardless of league position, since elimination stakes differ from league-table stakes
- Final score is 0.0 (dead rubber, both teams safe and out of contention) to 1.0 (title decider, relegation six-pointer, or cup final)
- This score modifies model behavior in two places: it slightly increases variance/upset probability in the goals model (cup upsets and "danger of complacency" effects are real and documented in football analytics literature), and it factors into the confidence scorer as a context flag rather than a probability shifter

### 6.4 Fallback and degradation rules (must be explicit, must be logged, must be visible downstream)

Whenever a feature falls back from its ideal source to a substitute, that fallback must be recorded as a flag attached to the fixture's feature set, not silently absorbed:

- xG unavailable → fall back to actual goals scored/conceded as a proxy, flag `xg_fallback_used = true`
- Referee stats unavailable → fall back to league-average cards/fouls rate, flag `referee_fallback_used = true`
- One team's season stats missing (new team, promoted, etc.) → fall back to last season's stats for that competition tier if available, heavily down-weighted, flag `cross_season_fallback_used = true`; if no prior-season data exists either, this should already have triggered a gate SKIP via the minimum sample size check
- H2H history below 2 meetings → treat H2H group as fully absent (zero-weighted in confidence, not partially weighted), flag `h2h_insufficient = true`

These flags feed two places: the confidence scorer (each fallback used reduces confidence) and the explanation generator (the explanation should be honest about caveats — see Section 16.3).

### 6.5 Derby/rivalry list maintenance

This is a maintained reference list, not a computed feature — rivalries are geographic/historical facts, not statistical patterns. The build agent should create this as a simple configuration table (league_id, team_id_a, team_id_b, rivalry_name) seeded with well-known rivalries for the leagues in initial scope, with a documented process for adding more as league coverage expands. Do not attempt to infer rivalries from data (e.g., "teams that play extra-aggressively against each other") — that's a fragile proxy for what is actually a known, listable fact.

### 6.6 Per-feature-group recency decay — one rate does not fit every feature

v1 used a single exponential decay rate (default 0.85) applied uniformly to every recency-weighted feature. That is a reasonable starting point but not a defensible permanent choice: match results, discipline statistics, and lineup availability are not equally volatile, and treating them as if they were forces a compromise rate that's wrong for all of them to some degree.

**Why these differ:** a team's underlying quality (reflected in match results) genuinely does drift over a season but rarely swings sharply match-to-match, so a slower decay (older matches still carry real signal) is defensible. Cards and fouls are more strongly influenced by referee assignment and single-match circumstance, arguably warranting faster decay so a run of unusually strict referees doesn't get treated as a lasting team trait. Corners sit somewhere between the two. Lineup and player-availability information is, by contrast, almost entirely about *right now* — who was fit and selected two months ago says close to nothing about who's fit and selected this week, warranting the fastest decay of any feature group, weighted almost entirely toward the present.

**Configuration:** Section 4.1's per-feature-group decay config gives each of the following its own independently tunable rate, rather than inheriting one global constant: match-result form, xG-based form, cards/discipline form, corners form, fouls form, and lineup/availability recency. Starting defaults (subject to the same walk-forward validation as everything else in this document — Section 24): match-result form 0.90 (slower — more history retained), xG-based form 0.88, cards/fouls 0.80 (faster), corners 0.85 (matching v1's original single rate, since corners' volatility profile is closest to what that rate was originally tuned for), lineup/availability recency effectively near-immediate (a very low decay constant, since Section 5F.2's `confirmed_starting_XI_strength` already supersedes older lineup information entirely once available rather than blending it).

**Validation requirement:** Section 24's walk-forward backtesting must include decay-rate sensitivity as one of the things it validates, not just model weights — running the same backtest across a small grid of candidate decay rates per feature group and confirming the chosen defaults are not meaningfully worse than nearby alternatives before they're locked in as production values.

---

## 7. Feature Vector Assembly — Output Contract

The output of Section 6 is a single structured object per fixture containing every feature above, fully computed, with all fallback flags set. This object is what gets passed into both the gate (Section 5, though gate completeness is actually computed first and is a prerequisite input to this same object) and every model in Section 8. Treat this as a strict schema — every model declares which fields it consumes, and the assembly step must guarantee all declared fields are present (using explicit defaults/nulls where genuinely absent, never omitting the key).

This is also the object that gets persisted (as a snapshot, e.g. structured JSON) alongside each prediction, so that any historical prediction can be fully reconstructed and audited later — this is what "features_used" means operationally, and it is non-negotiable for the accuracy-tracking and future ML phases.

**What is persisted as `features_used` is broader than just this section's input vector.** It is the input feature vector from Section 6 *plus* any model-internal computed artifact that a later section of this document explicitly declares as a required output. Section 6's feature vector is the assembly step's own output; a model in Section 8 may compute additional structured artifacts during its own run (not inputs, but intermediate or full computational results) that must also be captured at persistence time if any downstream component depends on reading them back later. Do not treat "features_used" as scoped only to Section 6 — treat it as the single, complete, append-as-needed audit record for everything that went into and came out of a fixture's prediction run. Section 8.1 specifies one concrete required artifact of this kind; any future model added to this engine that produces an intermediate structure another component depends on must declare that requirement the same explicit way.

---
## 8. The Model Layer

**What changed from v1, stated up front:** v1 ran Model A (Poisson/Dixon-Coles) and a second component it called "Model B" that was actually a bounded adjustment to Model A's own output, computed from Model A's own inputs — never a genuinely independent view of the fixture. Treating that as ensemble "agreement" was circular: it could never disagree with itself in a way that meant anything. This section keeps everything Model A did well, folds recent form into it honestly as a feature rather than a pretend second opinion, and adds two genuinely independent models — Elo (B) and a machine-learning tabular model (C) — plus the market-specific auxiliary family (D) for everything that isn't goals. Section 8.6 explains this change in full; read Section 8.1 first, since it now looks slightly different from v1's version for exactly this reason.

Four model families run per fixture. Models A, B, and C independently produce probabilities for the same goal-based (Tier A) markets; Model D independently covers the non-goal market families (Tier B/C), each using whichever distribution its own diagnostics justify, never inherited from another market's choice. Section 8.5 combines A/B/C into a genuine ensemble with measurable agreement; Model D's outputs pass through largely on their own, per family, as v1 already established and this revision preserves.

### 8.1 Model A — Poisson / Dixon-Coles Goals Model (drives all of Tier A, revised)

**Purpose:** produce a full probability matrix over scorelines (home goals × away goals, 0 through the configured max, default 8 each side), from which every Tier A market is derived. Unchanged from v1.

**Method:**
1. Compute each team's attack rate as a configured blend of their **season-long** goals-scored rate (Section 6.1 Group 2, home/away split) and their **recent-form** decay-weighted goals-scored rate (Section 6.1 Group 1) — default blend 70% season / 30% recent-form, tunable per Section 4.1, validated by Section 24's walk-forward process the same as every other weight in this document. This is the one substantive change from v1's Model A: recent form is now a bounded input to this single calculation, not a delta applied afterward by a separate component. Divide by the league's average goals-scored rate for that venue context to get attack strength.
2. Compute defence strength the same way — blended season/recent-form goals-conceded rate, divided by the league's average goals-conceded rate for that venue context.
3. Compute the expected goals (lambda) for each team as: their attack strength × the opponent's defence strength × the league's average goals rate for that venue context.
4. Apply situational adjustments to the lambdas: reduce home lambda proportionally to home injury impact (now sourced from Section 5F's graded player-strength model), reduce away lambda proportionally to away injury impact, apply a smaller reduction for fixture congestion on each side, apply a small variance widening (not a lambda shift, a matrix-shape adjustment) when match importance is high to reflect upset risk.
5. Build the full scoreline probability matrix using independent Poisson distributions for home and away goals.
6. Apply the Dixon-Coles correction to the low-scoring cells specifically (0-0, 1-0, 0-1, 1-1), which corrects a well-documented bias where naive independent Poisson underestimates draws and tight low-scoring outcomes. Use the configured rho parameter.
7. Normalize the full matrix so all cells sum to exactly 1.0.

**Markets derived from this single matrix (all of Tier A, computed by summing the relevant cells):** unchanged from v1 — 1X2, double chance, draw no bet, over/under at every configured line, BTTS, correct score (top-N only), half-time/full-time and highest-scoring-half (via the secondary half-time-lambda calculation v1 already specified), team total goals, win to nil, odd/even, clean sheet.

**Mandatory output requirement, unchanged and still critical:** the full, normalized, Dixon-Coles-corrected scoreline matrix is a required output, persisted in `features_used`, because Section 20.4's acca correlation handling depends on reading it back exactly as it was at prediction time.

**Distribution justification:** Poisson-with-Dixon-Coles-correction for goals is not chosen by convention here — Section 8.4.1 requires this justification to be made explicitly for every count-based market, and goals is the one case where the correction is well-established in football analytics literature specifically (the Dixon-Coles low-score adjustment exists because standard match data demonstrably shows exactly the bias it corrects). Where league-level dispersion diagnostics (Section 8.4.1) show a given league's goal distribution is meaningfully overdispersed relative to what Poisson predicts, a Negative Binomial variant is the documented fallback for that league specifically — this is not a global switch, it's a per-league diagnostic outcome.

### 8.2 Model B — Elo / Dynamic Team-Strength Model (new, genuinely independent)

**Purpose:** provide a second, structurally different view of match strength — one that doesn't route through goals-scored/conceded rates at all, so it can genuinely agree or disagree with Model A rather than being a variation on the same calculation.

**Method:**
1. Maintain a running Elo-style rating per team, initialized from a league-tier baseline for any team with no prior rating (consistent with Section 5D.5's hierarchical fallback — a newly promoted team's initial Elo rating is not invented from nothing, it inherits from the promotion-adjusted prior the same way its other features do).
2. After each settled fixture, update both teams' ratings using the standard Elo update rule: actual result versus expected result (derived from the pre-match rating differential), scaled by a K-factor. Point-in-time correctness applies exactly as everywhere else (Section 3.4) — a fixture's own Elo update happens *after* that fixture, never before, and a historical prediction for fixture N must only ever see ratings as they stood immediately before fixture N, never a rating that already reflects fixture N's own result.
3. Maintain separate home and away rating adjustments (a fixed home-advantage offset applied when computing expected result, consistent with football-specific Elo variants rather than a generic single-rating implementation) and, where sample size supports it, separate attacking-strength and defensive-strength Elo components rather than one blended rating, since a team can be a strong attacking side with a weak defense and a single combined rating obscures that.
4. Apply a league-strength adjustment when comparing ratings across teams from different leagues (relevant for cup competitions with cross-league fixtures) — a per-league strength offset, itself estimated from historical cross-league results where they exist, defaulting to a neutral (no adjustment) baseline where they don't.
5. Convert the rating differential (plus home advantage) into 1X2 and, where the specific Elo variant supports it, over/under and BTTS probabilities via the model's own standard conversion function (a logistic mapping from rating differential to win probability is the standard approach and the recommended starting point).

**Why this counts as genuinely independent:** Elo's rating only moves based on match *results* (win/draw/loss and, in the goal-difference-aware variant, margin), not on the same goals-scored/conceded rate calculations Model A uses as its core inputs. A team can have a mediocre Elo trajectory while still posting decent goals-scored numbers against weak opposition, and the reverse. When Model A and Model B agree, it's because two differently-constructed views of the same team happen to point the same way — which is exactly the kind of agreement Section 8.5 needs to be able to measure honestly.

### 8.3 Model C — Machine-Learning Tabular Model (new, genuinely independent)

**Purpose:** a third, structurally different view — a supervised model (LightGBM or XGBoost, gradient-boosted trees on tabular features) trained to predict match outcomes directly from the full feature vector (Section 6, Section 7), rather than from a hand-specified statistical formula like Models A and B.

**Feature set:** the complete feature vector from Section 6/7 is available to Model C — every group, including the OOD signals (Group 9), the manager/regime signals (Group 10), and the lineup/player-strength signals (Group 11). This is the one model in the ensemble that can learn nonlinear interactions between features the hand-specified models can't express (e.g., "fixture congestion matters much more when combined with a recent managerial change" is the kind of interaction a tree-based model can discover from data that neither Model A nor Model B is structurally capable of representing).

**Training methodology — this is where point-in-time correctness (Section 3.4) and walk-forward validation (Section 24) are not optional add-ons but the entire reason the model can be trusted at all:**
1. Training data is assembled exclusively from point-in-time-correct feature snapshots (Section 3.4.4) paired with the eventual settled outcome — never from live, current-database feature values paired with historical outcomes, which would leak.
2. Model C is trained per Section 24's walk-forward methodology: expanding or rolling windows, never a random train/test split (random splitting on temporal football data lets the model implicitly learn from matches chronologically after the ones it's being tested on, since team strength is autocorrelated across nearby fixtures — this is a well-documented and easy-to-miss leakage source specific to time-series-like sports data).
3. Hyperparameter tuning happens only against the validation fold(s), never the final holdout test period (Section 24.3) — this is a hard rule, not a preference, because tuning against the final test set is exactly how a model's reported performance stops meaning anything.
4. One trained artifact per market family it serves (1X2 is a natural first target; extending Model C to additional Tier A markets is a documented later phase, not a v1 requirement — see Section 28).
5. Every trained artifact is registered in `model_registry` (Section 3.3) before it may be evaluated as a challenger (Section 21) — there is no path for a Model C artifact to reach production without going through registry + champion/challenger, same as every other model.

**Explanation requirement:** Model C's per-prediction explanation factors are SHAP values (or an equivalent local feature-contribution method) — top positive and top negative contributing features for this specific prediction, stored in `explanation_factors` (Section 3.3) and rendered per Section 16's structured-factors-first rule. Model C is never asked to generate its own rationale text; it produces numbers, the same as Models A, B, and D do.

### 8.4 Model D — Market-Specific Auxiliary Models (expanded — every non-goals market gets a distribution chosen on evidence, not convention)

**The governing rule, stated as plainly as possible: no market gets assigned a statistical distribution simply because another football statistic used that distribution successfully.** Poisson worked well for goals because goals, empirically, behave close enough to Poisson (with the Dixon-Coles correction for the low-score bias). That does not make Poisson the default for corners, cards, fouls, shots, possession, or anything else — each of those gets its own empirical check before a model family is chosen for it.

#### 8.4.1 Distribution diagnostics — run once per market family, before a model is chosen

For every statistical market family (corners, cards, fouls, shots/shots-on-target, possession, pass accuracy, red cards), compute against the historical data for that family:
- Mean and variance
- The dispersion ratio (variance ÷ mean) — a ratio near 1.0 is consistent with Poisson; a ratio meaningfully above 1.0 indicates overdispersion, where a Negative Binomial distribution is the standard, empirically justified correction
- Zero frequency — how often the statistic is exactly zero, checked against what the candidate distribution would predict; a mismatch (far more zeros in the real data than the distribution predicts) indicates a zero-inflated or hurdle model is more appropriate than a plain count distribution
- Tail behaviour — whether extreme values occur more often than the candidate distribution's tail would predict
- Goodness-of-fit (a standard test appropriate to count data, run against each candidate distribution)

This diagnostic is run **per league tier**, not once globally, since dispersion genuinely differs by competition (a physical, high-intensity league may show different card dispersion than a technical, low-foul league) — Section 8.4.6 covers how this interacts with the hierarchical league-model approach.

#### 8.4.2 Corners

Test Poisson against Negative Binomial per 8.4.1's diagnostics; choose per league tier based on the dispersion ratio and goodness-of-fit, not a fixed a priori choice. Where Poisson is adequate, v1's method (Section 8.4's original combined-total and per-team split approach) carries forward unchanged. Where Negative Binomial fits better, the same structural approach (combined-total distribution, separate per-team distributions rather than proportional splitting) applies with the Negative Binomial's two parameters (mean and dispersion) estimated from the same team-rate inputs v1 already computes.

#### 8.4.3 Cards

Cards show meaningfully different statistical behavior from corners in most leagues: a substantial zero-card-per-team frequency (many matches, many teams, zero cards), driven partly by referee variation. Test Negative Binomial, a hurdle model, and a zero-inflated model against the diagnostics; choose empirically. **Red cards are handled entirely separately, never as part of the yellow/total-cards count model** — they are a rare binary event (did a red card occur, yes/no) and are modeled as a direct probability from team and league base rates, exactly as v1 already specified for this specific case, not lambda-then-derive.

#### 8.4.4 Fouls

Test Poisson against Negative Binomial per 8.4.1; fouls are less commonly zero-inflated than cards but should not be assumed Poisson without the same check.

#### 8.4.5 Shots and shots on target

Count models, with Negative Binomial the likely fit wherever overdispersion diagnostics confirm it (shot counts in football commonly show more variance than a pure Poisson process would predict, driven by match-flow effects — a team chasing a game generates a burst of late shots that a memoryless Poisson process doesn't naturally capture). Choose per 8.4.1's diagnostics, per league tier.

#### 8.4.6 Possession and pass accuracy — explicitly not count models

Possession and pass accuracy are **bounded proportions** (0–100%), not counts, and must never be modeled as Poisson regardless of how convenient that would be given the rest of this section's machinery. The appropriate family is a bounded regression method for proportion data — **Beta regression** is the recommended starting point (it models a proportion directly, respects the 0–1 bound, and naturally represents the fact that a proportion's variance depends on how close it sits to the bounds). Where Beta regression's assumptions don't fit well in practice, a validated alternative bounded-proportion method is acceptable, but a standard unbounded regression or a count model is not — this is a hard rule, not a preference, precisely because it's the kind of shortcut that's easy to take and produces nonsensical outputs (a "possession" prediction that isn't bounded to 0–100% is not usable no matter how good its point estimate looks).

#### 8.4.7 What this means operationally

Model D is not one model — it's the diagnostic procedure in 8.4.1 plus a small family of count/proportion/binary models, applied per statistical market, per league tier, with the choice of *which* model family serves a given market/league combination stored explicitly (this is exactly what `market_metadata.model_family` from Section 2.6/3.3 records, and it can differ by league for the same market — corners in the Premier League and corners in a data-sparse lower-tier league are not guaranteed to use the same distribution family, because the diagnostics might not agree).

### 8.5 Genuine Ensemble & Model Agreement (Models A, B, C)

This is the step that replaces v1's blend of "Model A plus a pretend second opinion" with an honest combination of three structurally independent views, plus a real measure of whether they actually agree.

**Method, per Tier A market:**
1. Collect `poisson_probability` (Model A), `elo_probability` (Model B), and `ml_probability` (Model C) for this market and outcome.
2. Compute `ensemble_mean` — the weighted average of the three, using configured per-market weights (Section 4.1). These weights start at reasonable defaults and are validated/optimized via Section 24's walk-forward backtesting — they are not permanently hand-set, per Design Principle 14's prohibition on treating configuration as a substitute for evidence once evidence exists.
3. Compute `ensemble_variance` — the variance of the three individual model probabilities around `ensemble_mean`. Low variance means the three structurally different models landed in a similar place independently; that's real signal.
4. Compute `max_model_spread` — the maximum pairwise difference between any two of the three model probabilities for this outcome. This is a simpler, more interpretable companion to variance for the confidence scorer and for explanation purposes ("our three models ranged from 52% to 68% on this one" is a more human-legible caveat than a raw variance number).
5. Compute `model_agreement_score` (0.0–1.0, 1.0 being maximal agreement) as a normalized function of `max_model_spread` relative to a configured tolerance — three models landing within a few points of each other scores high; three models spread across 20+ percentage points scores low, regardless of what `ensemble_mean` happens to be.
6. Renormalize 1X2 outcomes so they sum to exactly 1.0 after weighting (independent weighting can introduce rounding drift, same requirement v1 already established).
7. Every output probability is clipped to the configured safe range (default 0.01–0.99), unchanged from v1.
8. `ensemble_mean` (renamed `raw_probability` once it exits this step, per Section 3.3's naming) proceeds to Section 9 (Calibration) — it is never shown to a user or used in ranking directly. `ensemble_variance`, `max_model_spread`, and `model_agreement_score` all persist alongside it and all feed Section 11's confidence scoring directly.

**The worked example that motivated this section:** three models landing at 67%, 65%, and 64% for the same outcome is meaningful consensus — three independently-reasoned views converged. Three models landing at 68%, 49%, and 52% might average to a similar-looking `ensemble_mean`, but it represents the models fundamentally disagreeing about this fixture, and `model_agreement_score` is what stops that disagreement from being invisible once it's been averaged away. This is precisely the distinction v1's pseudo-ensemble could never make, because its second "model" was mathematically incapable of landing far from the first.

**Tier B/C markets** continue to pass through from Model D directly, per family, exactly as v1 established — recent form's relationship to set-piece behavior remains a deliberately unstudied v1-and-v2 simplification (Section 8.6), not something this ensemble step blends in.

### 8.6 What changed from v1's Model B, and why

Stated explicitly, per this document's own governance principle (Design Principle 14) that changes are never silent: v1's "Model B" computed a bounded adjustment delta from the same recent-form features Model A's own attack/defence calculation could have used directly, then blended that delta back into Model A's output during the ensemble step. Nothing about that delta could ever come from a source Model A didn't already have access to — it was Model A's own inputs, reprocessed, and calling the result an independent model made the resulting "ensemble agreement" circular by construction. That mechanism's actual, useful behavior — recent form providing a bounded correction on top of season-long rates, capped so a short streak can't overwhelm a full season of data — is fully preserved, just relocated: it is now Section 8.1 step 1's blended attack/defence rate calculation, an honest feature input to Model A rather than a disguised second model. The letter "B" is reassigned to Elo (Section 8.2), which is a genuinely different calculation with no structural dependency on Model A's inputs. No downstream section that referenced "Model B" retains that old meaning; every reference below means Elo.

---
## 9. Probability Calibration

This is the pipeline stage v1 didn't have at all, and its absence is the single biggest gap between "the models produce numbers that look like probabilities" and "the models produce numbers that behave like probabilities." A model can have excellent classification accuracy — correctly picking the more likely outcome most of the time — while being badly calibrated, meaning the specific number it attaches to that pick (68%, say) doesn't actually happen 68% of the time historically. Both properties matter for this engine; only one of them was previously being checked.

### 9.1 Where calibration sits in the pipeline

Raw model probability (Section 8.5's `ensemble_mean`, stored as `raw_probability`) → **calibrated probability** (this section) → confidence assessment (Section 11) → recommendation gate (Section 12). Calibration is not optional and not skippable for any market that reaches the recommendation gate — an uncalibrated probability never reaches a user, full stop.

### 9.2 Calibration methods, chosen per market family

- **Isotonic regression** — the default choice wherever sample size supports it (Section 4.1's minimum, default 500 settled predictions for that market). Isotonic makes no assumption about the shape of the miscalibration, which is a genuine advantage when there's enough data to estimate a flexible mapping without overfitting.
- **Platt (sigmoid) scaling** — the fallback for markets with a smaller settled sample, since it estimates far fewer parameters than isotonic and is less prone to overfitting a limited sample.
- **Beta calibration** — the appropriate choice specifically where the underlying probability itself comes from a bounded-proportion model (Section 8.4.6's possession/pass-accuracy family) rather than a classification-style probability; Beta calibration is designed for exactly this case, where standard isotonic/Platt calibration (built assuming a classifier's score) is a worse structural fit.

### 9.3 Training discipline — this is where calibration can quietly become a leakage vector if built carelessly

**Calibration is trained only on historical, out-of-sample, settled predictions — never on the same data used to train the underlying models, and never anywhere near the final holdout test set (Section 24.3).** A calibration model fit using the same predictions that trained Model A/B/C, or fit using data from the final test window, produces a calibration curve that looks excellent purely because it's been shown the answers — this is a subtler, easy-to-miss version of the exact leakage problem Section 3.4 exists to prevent, and it is called out here explicitly because calibration is new enough to this engine that there's no existing habit of guarding against it. Section 24.4 defines the exact dataset boundaries (training / validation-tuning / calibration / final holdout) that keep this separation real rather than aspirational.

### 9.4 What's stored, per calibration model and per calibrated prediction

Per calibration model (the `calibration_models` table, Section 3.3): `method`, `market`, `confidence_band` (nullable), `training_window_start`/`training_window_end`, and the full set of quality metrics from 9.5 below. Per prediction (the `predictions` table): `raw_probability`, `calibrated_probability`, `calibration_model_version`, `calibration_training_window`, `calibration_method` — all four of the last group point back to exactly which calibration model produced this specific row's calibrated value, so any prediction can be traced to the exact calibration artifact responsible for it.

### 9.5 Calibration quality metrics

- **Brier Score** — mean squared error between predicted probability and the binary outcome; the standard proper scoring rule for probabilistic binary forecasts, lower is better.
- **Log Loss** — penalizes confident-and-wrong predictions more heavily than Brier Score does; tracked alongside it, not instead of it, since the two metrics can disagree about which of two models is "better" and both perspectives are useful.
- **Expected Calibration Error (ECE)** — buckets predictions by predicted probability and measures the average gap between predicted probability and actual observed frequency within each bucket; this is the most directly interpretable calibration metric, and it's the one Section 19.5's confidence-band validation table is built from.
- **Reliability/calibration curves** — the visual form of ECE, predicted probability on one axis, observed frequency on the other; a perfectly calibrated model sits on the diagonal. Stored as the underlying bucketed data (predictions per bucket, mean predicted probability per bucket, observed frequency per bucket), not just a rendered image, so any consumer downstream can reconstruct or re-bucket the curve.
- **Calibration slope and intercept** — where useful as a compact summary (a slope near 1.0 and intercept near 0.0 indicates good calibration; a slope well below 1.0 indicates the model is systematically overconfident, spreading its predictions too far from the neutral midpoint relative to what actually happens).

### 9.6 The hard validation requirement

**If predictions labelled approximately 70% historically succeed materially less or more than approximately 70%, calibration is not acceptable — even if the underlying classification accuracy looks good.** This is not a soft guideline; it's the specific, mandatory check Section 19.5's confidence-band validation table exists to run, and a market/band combination that fails it is not eligible for the recommendation gate (Section 12) regardless of how good `ensemble_mean`'s raw discriminative accuracy is. Classification accuracy answers "did the model usually pick correctly." Calibration answers "does the number attached to the pick mean what it claims to mean." This engine requires both, and good classification accuracy never substitutes for failing calibration.

### 9.7 Calibration quality must be evaluated per scope, not globally

A single global calibration curve can look fine while hiding real miscalibration in a specific slice — evaluate and report separately per: market, confidence band, league (where sufficient settled sample exists to support a league-specific view — Section 18's minimum-sample rules apply here too), and model version (so a calibration regression introduced by a model update is caught at the version boundary, not blended invisibly into a long-running aggregate). Where a league lacks sufficient sample for its own calibration evaluation, it inherits the market-level (not league-specific) calibration curve, explicitly flagged as inherited rather than locally validated — consistent with Section 5D's hierarchical fallback pattern applied here to calibration itself.

---
## 10. Per-Market Eligibility — The Second Gate

Section 5's gate decides whether a fixture gets predictions at all. This section governs which *specific markets within that fixture* are actually eligible to be shown, given that data reliability varies by market tier even within a single fixture that otherwise passed the fixture-level gate. Unchanged in structure from v1; extended to reference the Section 2.6 market-metadata registry directly rather than re-deriving eligibility logic ad hoc.

### 10.1 Eligibility check sequence (run once per candidate market, after the fixture has already passed the Section 5 gate and the Section 5D OOD gate)

1. Look up this market's record in `market_metadata` (Section 2.6/3.3). If `best_bet_eligible` is false for this league (including the event-granularity-driven disablement Section 2.6 defines for Team To Score First / Race to N Corners where `has_event_timestamps` isn't confirmed), stop here — the market is not attempted for this fixture at all.
2. Confirm `league_coverage` explicitly supports the statistic family this market depends on (e.g., a Tier B corners market requires `league_coverage.has_statistics = true` for this league-season — do not infer support from "the field happened to be non-null for one fixture," since that can be a data entry fluke rather than systematic coverage).
3. Confirm the market-tier-specific data completeness sub-score (from Section 6.1 Group 9's per-group completeness) meets the elevated floor for that tier (base floor + the tier's configured strictness addition from Section 4.1).
4. Confirm the underlying team/league sample size for this specific statistic family meets `market_metadata.minimum_history` — a league might have excellent goals history but have only just started reporting corner statistics this season, which a pure "is the field non-null" check would miss but a "how many matches of corner data exist" check catches.
5. If any check fails, this specific market is excluded from this fixture's output — the fixture's gate tier (BASIC/FULL) and other markets are unaffected.

### 10.2 Why this two-layer gate structure matters

Without this distinction, you get one of two bad outcomes: either the engine is overly conservative and skips entire fixtures because corner data happens to be thin (even though the goals predictions would have been perfectly sound), or it's overly permissive and shows a confident-looking corners prediction built on three matches of patchy data just because the fixture overall looked fine. Two layers solve both failure modes at once.

### 10.3 Cross-validation against API-Football's native predictions and bookmaker odds

Where the `api_predictions` table has a row for this fixture, and where odds data exists (filtered to `observed_at` at or before this prediction's `as_of_timestamp`, per Section 3.4), both are used exclusively as confidence-scoring inputs (Section 11), never as direct probability inputs into Models A/B/C. The reasoning: if the engine's own models are allowed to directly average in someone else's model's output, the engine stops being its own model and starts being a wrapper around a competitor's predictions — which is both a product differentiation problem and makes the engine's stated explanations (Section 16) dishonest, since the explanation would claim "based on form and historical data" while actually partially reflecting an opaque third-party model. Cross-validation strictly means: "does our independently-computed probability roughly agree with this external reference," which is a confidence signal, not a probability input. This is a separate, narrower use of odds than Section 14's value framework, which does use odds directly — but only ever to measure edge/value *after* the probability is finalized, never to shape the probability itself. Section 14.3 states this distinction precisely.

### 10.4 Special eligibility rule for rare-event and structurally-low-confidence markets

Certain markets (Red Card In Match, exact Correct Score, Win To Nil for a heavy underdog, and now — per Section 8.4's distribution diagnostics — any market where the chosen distribution family itself signals high inherent noise, such as a market a league's diagnostics assigned a zero-inflated model to) have a hard confidence ceiling configured regardless of how complete the underlying data is, recorded as `market_metadata.confidence_cap`. This is an honest acknowledgment that some markets are inherently noisier than others, and the engine should never claim false precision on them. Apply this explicitly to: Correct Score (cap at Medium), Red Card In Match (cap at Low), any individual Over/Under line more than two standard deviations from the league mean for that statistic (cap at Medium).

### 10.5 Correct Score handling — specific rule

Never expose the full scoreline matrix as "predictions." Always expose only the top-N highest-probability scorelines (configured, default top 8), and always attach a caveat in the explanation noting that correct score is inherently the hardest market to call accurately (this is also reflected honestly in the accuracy ledger — Section 19 — where Correct Score's historical hit rate will be visibly, structurally lower than 1X2's, and that's expected and should be shown as such, not hidden).

---

## 11. Confidence & Meta-Confidence

This is the component that answers "how much should this probability be trusted," fully independent of the probability value itself. Substantially revised from v1: H2H's contribution is sharply reduced, several new evidence-based inputs replace what used to be implicit or absent, and the section now states explicitly where this is heading — toward a trained meta-model rather than a permanently hand-authored weight table.

### 11.1 Why H2H's weight is being cut, explicitly

v1 gave head-to-head history up to 15 points (10 base + 5 extended-sample bonus) — meaningful enough to materially affect whether a prediction reached Strong. That's a bigger role than H2H history actually deserves: a set of historical meetings between two clubs can span different managers, different squads, different tactical eras, and different league circumstances on both sides. "These two teams drew their last four meetings" is real information, but it's weak information relative to current team strength, and treating it as a near-15%-of-total-confidence signal overweights it. **H2H's confidence contribution is reduced to approximately 0–5% of the total score, and the old extended-sample bonus is eliminated entirely** — a deep H2H history is still recorded and still shown to users as context (it remains a real feature, Section 6.1 Group 3, and can still inform the explanation), it simply no longer meaningfully moves whether a prediction reaches Strong.

### 11.2 Confidence factors and their weights (sum to 100, default weights — tunable per Section 4, subject to the same walk-forward validation as every other weight in this document)

- **Data completeness (20 points)** — the fixture-level score from Section 5.3, scaled directly
- **Sample size sufficiency (12 points)** — scaled from the minimum of home/away team match counts, capped at a configured ceiling
- **Genuine model consensus (25 points, up from v1's 20)** — driven directly by Section 8.5's `model_agreement_score`, now meaningful in a way v1's pseudo-agreement never could be, since Models A/B/C can genuinely diverge; this factor grew because it is now a real, structurally independent signal rather than a circular one
- **Calibration reliability (15 points, new)** — how well-calibrated the specific market/confidence-band this prediction falls into has historically proven to be (Section 9.7's per-scope calibration evaluation), so a market with a known, validated calibration curve contributes more confidence than one whose calibration is still thin or unvalidated
- **Lineup and player-strength quality (10 points, up from v1's lineup-confirmation-only factor)** — full points once lineups are confirmed and Section 5F's `confirmed_starting_XI_strength` is available; partial credit for a strong `expected_starting_XI_strength` estimate pre-confirmation; naturally time-dependent across the lifecycle, exactly as v1's lineup factor was
- **Prediction stability (8 points, new)** — Section 5A.6's `lifecycle_stability_score`, neutral-scored (half credit) for a fixture's first-ever prediction version with no prior stage to compare against
- **Market cross-validation alignment (5 points, down from v1's 15)** — reduced because genuine model consensus (above) now carries most of what this factor used to compensate for; still scored only when odds and/or `api_predictions` data exists, per Section 10.3
- **Out-of-distribution penalty (up to −15 points, new — a penalty, not a positive-weighted factor)** — scaled from Section 5D's `ood_score`; this is what lets a fixture with otherwise-excellent scores on every factor above still land at a lower confidence tier because the model has genuine reason to distrust its own applicability here, layered underneath the hard OOD cap from Section 5D.4 rather than replacing it
- **Head-to-head consistency (0–5 points, reduced per 11.1)** — only scored when sufficient H2H history exists (4+ meetings), zero-weighted when insufficient

### 11.3 Per-market-tier confidence ceiling enforcement

After computing the raw weighted confidence score, apply the `max_confidence_tier` cap from `market_metadata` (Section 2.6/10.4) if this market has one configured, and separately apply Section 5D.4's OOD-driven hard cap if that threshold is crossed. Both are hard ceilings applied after scoring, not weight adjustments during scoring — the underlying score stays visible for audit while the caps prevent the system from ever claiming more certainty than it has honestly earned.

### 11.4 Confidence tier assignment

Apply the thresholds from Section 4.1 directly to the final (post-cap) score: Strong at or above the configured Strong floor, Medium at or above the Medium floor, Low at or above the Low floor, otherwise no recommendation for this market on this fixture — Section 12 formalizes exactly what "no recommendation" means and why, with an explicit reason code rather than silent absence.

### 11.5 Why confidence and probability must never be combined into one displayed number

A 58% probability at Strong confidence and a 58% probability at Low confidence are genuinely different pieces of information, and collapsing them destroys exactly the distinction that makes this engine more trustworthy than a naive one. Both numbers must always be computed, stored, and exposed separately. Unchanged from v1 — restated here because it remains the single most common implementation shortcut a build agent might be tempted to take under time pressure.

### 11.6 The path toward a meta-model, and why v1's/v2's hand-authored weights are the right starting point anyway

The weights in 11.2 above are hand-authored, same as v1's were — this is a deliberate, honest starting point, not a placeholder pretending to be more sophisticated than it is. The longer-term direction (a genuine extension target, not a v1/v2 requirement — see Section 28's later phases) is a trained meta-model whose job is exactly "how likely is this prediction's probability to be reliable" — trained on the same evidence these hand-authored weights use as inputs (data completeness, model agreement, calibration reliability, stability, OOD score) but learning the actual relationship between those inputs and observed reliability from data, rather than having that relationship hand-specified. This is not built now for the same reason Model C's scope is deliberately bounded in Section 28: a meta-model needs a real body of settled, calibrated, ensemble-tagged predictions to train on, which this system does not yet have on day one of this revision — the hand-authored version here is what generates that training data. Building the meta-model before that data exists would mean training it on nothing, which isn't a shortcut, it's building on air.

---

## 12. Recommendation Gate & NO-PREDICTION Reason Codes

v1 already had the right instinct — silence is a valid output (Design Principle 3), and the SKIP/BASIC/FULL gate tiers already exist. This section formalizes what "no recommendation" means at the individual prediction level (as opposed to the whole-fixture SKIP), with explicit, enumerated, machine-readable reasons rather than an implicit absence.

### 12.1 The three recommendation statuses, per prediction

- **`recommended`** — passed every gate (fixture-level, OOD, per-market eligibility), reached at least the configured minimum confidence tier for its market (Section 4.1's market-specific thresholds — see 12.4), and is eligible for the best-bet pool (Section 15) if its ranking score is strong enough.
- **`shown_general_only`** — computed successfully, passed the fixture-level and per-market gates, but didn't reach the confidence/probability floor for a recommendation. Still shown to users as a plain probability percentage (the v1 "General Match Predictions" tier), never as a best bet.
- **`no_prediction`** — not shown at all, for the specific, enumerated reason recorded in `no_prediction_reason`.

### 12.2 Why formalizing this matters beyond just "the current behavior already does this"

An enumerated, stored reason code turns "why didn't we predict this" from an unanswerable question into a queryable one. This is one of the most valuable long-term analytics capabilities this revision adds for a small amount of implementation cost: a genuinely useful question like "why are 38% of Championship fixtures receiving no strong prediction" becomes a direct aggregate query against `no_prediction_reason` instead of requiring someone to manually reconstruct what happened fixture by fixture.

### 12.3 The reason codes

| Code | Meaning |
|---|---|
| `INSUFFICIENT_HISTORY` | Below Section 5.2's minimum sample size |
| `LOW_DATA_COMPLETENESS` | Below the market-tier-adjusted completeness floor (Section 5.3/10.1) |
| `MODEL_DISAGREEMENT` | Section 8.5's `model_agreement_score` fell below a configured floor for this market |
| `HIGH_OOD_SCORE` | Section 5D.4's hard OOD threshold triggered |
| `LINEUP_UNCERTAINTY` | A market whose confidence structurally depends on lineup confirmation (Section 5F) was evaluated before lineups confirmed and didn't clear the bar without them |
| `INSUFFICIENT_MARKET_DATA` | Section 10.1's per-market eligibility check failed (coverage flag or sample size) |
| `STRUCTURAL_MARKET_UNCERTAINTY` | This market's `max_confidence_tier` cap (Section 10.4) means even a good raw score can't clear the recommendation floor |
| `CALIBRATION_UNRELIABLE` | This market/band's calibration model hasn't met Section 4.1's minimum sample threshold, or Section 9.6's hard validation check has flagged it |
| `PREDICTION_UNSTABLE` | Section 5A.6's `lifecycle_stability_score` fell below a configured floor — the prediction has moved too much across stages to trust yet |
| `ACTIVE_EMBARGO` | Section 5C's interleaved-match embargo is in effect |
| `UNSUPPORTED_LEAGUE` | This league isn't in the production model's training/validation scope (Section 5D's `unseen_league_flag`) |
| `UNSUPPORTED_MARKET_DATA` | Section 2.6's per-market data-support audit has this market disabled for this league (e.g., Team To Score First without confirmed event-timestamp coverage) |

### 12.4 Market-specific recommendation thresholds — no universal cutoff

v1 applied one global "Medium confidence and above" floor for recommendation eligibility across every market. That's the wrong default once markets have genuinely different base rates and genuinely different achievable accuracy ceilings — a 62% calibrated probability means something different for 1X2 than it does for BTTS, and a single universal threshold either under-publishes the markets that are reliably good at moderate-looking probabilities or over-publishes the ones that aren't. Each market family gets its own configured recommendation threshold (Section 4.1), and per Design Principle 14/Section 24, these are tuned from validation data — specifically, Section 19's coverage-versus-accuracy curves (55%/60%/65%/70% threshold comparisons) computed per market, not selected because a round number looked intuitively reasonable.

---

## 13. Market Selector — Best-Supported Market Per Fixture

A single fixture routinely has many eligible, calibrated, confidence-scored predictions across different markets — 1X2, double chance, several over/under lines, BTTS, corners, cards. This section answers a question none of the sections above answer on their own: **for this specific fixture, which one of those is actually the best-supported thing to lead with**, if a product surface wants a single headline recommendation per match rather than a full list.

### 13.1 Relationship to the best-bet pool (Section 15) — these are not the same mechanism

The market selector operates **within a single fixture**, ranking that fixture's own eligible markets against each other. The best-bet pool (Section 15) operates **across every fixture and market currently eligible**, ranking predictions against each other regardless of which fixture they come from, to build the pool product surfaces query from. They are complementary: the market selector's output (`is_strongest_market_for_fixture`, a boolean stored on the winning prediction row) is one *input* the best-bet pool's ranking score can use as a tie-breaker or a coverage-shaping signal — it does not replace pool ranking, and a fixture's strongest market is not automatically a best bet; it still has to clear the pool's own bar.

### 13.2 Selection method

For each fixture with two or more eligible, recommended-tier predictions, rank them by a composite score considering: calibrated probability, confidence, this market's historical model performance (from the accuracy ledger, Section 19), data completeness, `model_agreement_score`, `model_edge` where odds coverage exists (Section 14), and `lifecycle_stability_score`. The market with the highest composite score for this fixture is flagged `is_strongest_market_for_fixture = true`.

**Worked example, illustrating why this shouldn't default to always picking 1X2:** Home win at 64%, double chance at 81%, Over 1.5 at 79%, Over 2.5 at 59%, BTTS at 52%. A system that always leads with 1X2 would surface the weakest of these four. The market selector is what lets the engine surface Double Chance or Over 1.5 instead when the evidence genuinely supports one of them more strongly for this specific fixture — a real, evidence-based answer to "what's the best thing we actually know about this match," not a hardcoded market hierarchy (which Design Principle 2 already prohibited at the tier level; this section extends the same idea to the per-fixture level).

### 13.3 What gets stored

All eligible market predictions for a fixture remain stored and queryable regardless of which one wins the selector — this section never deletes or hides losing candidates, it only adds a flag to the winner. Product surfaces can therefore distinguish, and choose to expose, three different views: all predictions, recommended predictions, and the single strongest market per fixture, without the engine needing to know which view any given surface wants.

---

## 14. Odds & Betting-Value Framework

v1 already had a form of this — the acca engine displays fair (model-derived) odds, and Section 10.3 uses odds/`api_predictions` as a confidence cross-check. This section makes explicit something v1 left implicit: **whether a prediction is likely to occur and whether that prediction represents good betting value are two different questions**, and this engine tracks both without ever conflating them.

### 14.1 Odds must be timestamped, and opening/closing must be distinguishable

Per Section 3.1's extended `odds` table: every row carries `observed_at`, and a later odds movement is a new row, not an update — so the full history of a market's price movement is permanently reconstructable, not just the latest snapshot. This directly serves Section 3.4's point-in-time principle applied specifically to odds: **a prediction made at the EARLY lifecycle stage must only ever be compared against odds observed by the EARLY-stage `prediction_as_of` timestamp — never against closing odds captured just before kickoff.** Comparing an EARLY prediction to a closing price would be comparing a forecast made days out against information that only existed minutes before the match, which overstates or understates edge in a way that has nothing to do with the model's actual quality at the time it made that prediction.

### 14.2 De-vigging

Bookmaker odds embed a margin (the overround/vig) that makes the implied probabilities of all outcomes in a market sum to more than 100%. `de_vigged_probability` removes this margin (proportionally, or via a more sophisticated method if one is validated as materially better) so the market's *true* implied view can be compared apples-to-apples against the engine's own `calibrated_probability`. This computation happens once, at read or ingestion time, and is stored (`odds.de_vigged_probability`) rather than recomputed ad hoc by every consumer.

### 14.3 Model edge — and why a high probability is not the same thing as value

- `model_edge` = the engine's `calibrated_probability` minus the market's `de_vigged_probability`, for the same outcome, using odds observed at or before the prediction's own timestamp (14.1).
- **A high predicted probability does not automatically mean value, and a moderate predicted probability is not automatically low value.** Model at 62%, de-vigged market at 52% → +10 points of edge, genuine value regardless of the raw probability not looking dramatic. Model at 70%, market at 77% → the model is *less* confident than the market here, meaning the market disagrees with the engine in the engine's own unfavorable direction — a high-looking 70% with negative edge is not a value signal, and this framework exists specifically so the engine never conflates "confident-looking probability" with "good value" the way a naive read of the numbers might.
- `closing_market_probability` and `closing_line_value` (the difference between the price available at prediction time and the price the market settled at just before kickoff) are tracked separately, specifically because closing-line value is a widely used, credible signal in sports forecasting for whether a model's edge is real or noise — a model that's consistently on the right side of closing-line movement is demonstrating real predictive skill relative to the market, independent of raw hit-rate.

### 14.4 Two experiments this framework must support, kept clearly separate

- **Model-independent forecasting** — odds are excluded entirely from Models A/B/C/D's own probability computation; odds exist only for the benchmarking/value analysis this section describes. This is the default, and the one every section of this document assumes unless explicitly stated otherwise.
- **Market-informed forecasting** — odds (or the de-vigged market probability specifically) are included as an explicit input feature, most naturally to Model C (Section 8.3), which is structurally suited to incorporating an additional feature without requiring a hand-derived formula change.

**These are not permanently exclusive, and this document does not forbid market-informed forecasting forever.** What it requires is that the two be run and compared as a deliberate, evidenced experiment — a football-data-only model version versus a football-data-plus-market model version, evaluated through the same champion/challenger process (Section 21) as any other model change — rather than odds being quietly folded into the models without ever establishing how much of the resulting accuracy is genuinely the football model's own skill versus the market's already-efficient pricing being partially copied. This distinction matters for the same reason Section 10.3 keeps cross-validation and value analysis separate from probability generation: a system that can't tell you how much of its performance is its own versus borrowed from the market isn't being honest with itself about what it's actually good at.

---
## 15. Recommendation Ranking & The Best Bet Pool

### 15.1 No daily cap, no single daily list — unchanged from v1

There is no hardcoded maximum number of best bets per day, per week, per weekend, or per any other time period. The engine's goal is to produce as many high-quality, confidence-backed, calibration-honest recommendations as the data supports across the entire 7-day horizon window (Section 5C). Every prediction that clears its market's configured recommendation threshold (Section 12.4 — now market-specific, not the single universal floor v1 used) is a best bet. The pool is as large or as small as the day's fixtures and the data behind them honestly support — this is the concrete mechanism behind Design Principle 13's growth-oriented framing: coverage grows exactly as far as the evidence allows, and no further, because a pool inflated past what the data supports is not growth, it's borrowed trust that gets paid back later in eroded confidence-tier accuracy.

### 15.2 Composite ranking score — how predictions are ordered within the pool

Every prediction that has cleared per-market eligibility (Section 10), calibration (Section 9), and confidence scoring (Section 11) gets a composite ranking score governing where it sits within any slice of the pool. Extended from v1 with two new inputs:

Compute the ranking score as a weighted sum of:
- `calibrated_probability` — never `raw_probability` (Section 3.3's naming rule)
- Confidence score, post-cap (Section 11)
- Data completeness (Section 5.3)
- Market-specific historical accuracy from the ledger (Section 19), defaulting to a shrinkage-adjusted neutral baseline early per Section 18.4's minimum-sample rules, never raw small-sample accuracy taken at face value
- League-specific historical accuracy, same shrinkage principle
- `model_edge` where odds coverage exists (Section 14.3) — replaces v1's simpler "edge versus market odds" description with the fully-specified de-vigged version
- `is_strongest_market_for_fixture` (new — Section 13) as a modest tie-breaking boost, not a dominant factor
- `lifecycle_stability_score` (new — Section 5A.6) as a modest additional input, rewarding predictions that have proven stable across multiple lifecycle stages over otherwise-similar ones that haven't yet had the chance to

All weights are in Section 4.1's ranking weights group, validated the same way as every other weight in this document (Section 24).

### 15.3 The best-bet pool — structure and what it contains

The best-bet pool is the complete set of predictions meeting all of the following simultaneously:
- `superseded_at` is null (current version)
- `recommendation_status` is "recommended" (Section 12.1 — this replaces v1's separate confidence-tier-and-probability-floor check with the single, already-market-specific gate from Section 12.4)
- `gate_tier` is "FULL"
- `horizon_eligible` is true, `embargo_active` is false (Section 5C)
- `kickoff_at` is in the future
- `market_metadata.best_bet_eligible` is true for this market/league (Section 2.6 — replaces v1's simpler "not in the configured ineligible list," now driven by the same registry that governs market support generally)

Continuously maintained exactly as v1 specified: no batch "daily refresh" job, the pool is always current as new lifecycle-stage predictions are written and prior versions superseded.

### 15.4 Slice patterns — unchanged from v1, still the engine's full responsibility to support via indexing

Today's best bets, tomorrow's, weekend, Saturday/Sunday, this week, by league, by market, by confidence tier — all still simple filtered/sorted reads against the pool as v1 specified, with the same principle: the engine's only responsibility is ensuring every field these patterns filter or sort on is properly indexed, never building bespoke logic per product surface.

### 15.5 Per-fixture presentation cap — still a display concern, not a pool filter

Unchanged from v1: the configured maximum-per-fixture cap (default 2) applies only when a downstream service assembles a display response, never as a filter on what enters the pool itself.

### 15.6 General (non-best-bet) predictions

Every prediction with `recommendation_status = "shown_general_only"` (Section 12.1) is still stored and exposed with its full `calibrated_probability` and `confidence_tier` visible, used by match-preview and all-markets views regardless of individual-market confidence.

---

## 16. Explanation & Structured Rationale

v1 already required explanations to be generated from real feature values rather than generic templates — that principle is preserved completely. What's added is the missing layer underneath it: **structured explanation factors, computed and stored first, with the natural-language text rendered from those factors afterward** — never the reverse, and never delegated to a language model asked to invent a reason after the fact.

### 16.1 The rule, stated as a hard boundary

Structured model factors → constrained natural-language rendering. **Never:** prediction → ask a language model to invent rationale. If a language model is ever used anywhere in this pipeline for wording (smoothing the final sentence construction, for instance), its only legitimate input is the already-computed structured factors below — it is never given the prediction alone and asked to explain why, because that produces plausible-sounding text with no guaranteed relationship to what the model actually did, which is a fabrication risk this document treats with the same seriousness as a leakage bug: the output looks fine and is not trustworthy.

### 16.2 Structured factors, per model

**For Model C (the ML model):** SHAP values (or an equivalent local feature-contribution method), stored as top positive drivers and top negative drivers for this specific prediction — not global feature importance, which describes the model in general, but the local explanation for this exact fixture.

**For Model A (Poisson/Dixon-Coles):** the components that actually built the lambda and shaped the matrix — attacking-strength contribution, defensive-strength contribution, home advantage, injury adjustment (from Section 5F), lineup adjustment, league scoring baseline. These are already computed as intermediate values in Section 8.1's method; this section requires they be *retained* and stored in `explanation_factors` rather than discarded once the final lambda is computed.

**For Model B (Elo):** the rating differential itself, home-advantage offset, and — where the attacking/defensive-split Elo variant is in use (Section 8.2 step 3) — which of the two components drove the differential.

**For Model D (market-specific auxiliary):** the same pattern as Model A, scaled to whichever statistic is being modeled — team rate contributions, referee modifier where applicable (cards/fouls), league baseline.

### 16.3 Rendering — from factors to text

1. For each market, the primary driver feature groups are already defined (recent form scoring rates and combined xG for Over/Under; team corner averages and possession tendency for Corners; form differential, standings gap, H2H record, and injury impact for 1X2 — unchanged from v1).
2. At generation time, pull the actual stored `explanation_factors` values for this prediction — never placeholder language, never regenerated from a live re-query that might not match what actually drove this specific stored prediction.
3. Select the 2–3 strongest contributing factors (by magnitude, from the structured factors — not re-derived independently by whatever renders the text) and phrase them using actual numbers.
4. **Mandatory caveat inclusion, extended from v1:** any fallback flags from feature assembly (Section 6.4), a `max_confidence_tier` cap in effect (Section 10.4), a recently-lifted embargo (Section 5C.6), a high `ood_score` even if it didn't force BASIC (Section 5D.4), and — new — a low `lifecycle_stability_score` if this prediction has moved meaningfully across lifecycle stages (Section 5A.6), so a user can see when a pick has been volatile leading up to kickoff, not just its current snapshot.
5. Banned phrasing and required probabilistic framing, unchanged from v1: no "guaranteed," "certain," "sure thing," "can't lose"; always "the data suggests," "is favored to," "the model estimates."

### 16.4 Why this ordering is worth the extra storage and computation

Storing structured factors before text costs more than generating text directly from a template. It buys something v1's approach couldn't: every explanation is independently auditable against the exact numbers that produced it, the SHAP/contribution values can themselves be validated (do the "top positive drivers" actually correlate with correct predictions over time — a question the accuracy ledger, Section 19, can now answer), and a future change to how explanations are *worded* never risks silently changing what they claim happened, because the factors and the wording are two separate, independently-versioned layers.

---
## 17. Result Settlement

This is what turns the engine from a one-shot predictor into a system that gets measurably better and more trustworthy over time, and it must be built with the same rigor as the prediction logic itself — an accuracy system that's wrong or inconsistent is worse than no accuracy system at all, because it actively misleads.

### 17.1 Settlement trigger

Once a fixture's status updates to full-time (sourced from the same `fixtures` table the engine reads, populated by upstream ingestion — this is a read of a status the engine itself does not write), settlement runs for that fixture's **LOCKED-stage** prediction for every market, exactly as v1 specified. LOCKED remains the one and only lifecycle stage settlement grades against the public, user-facing accuracy ledger.

### 17.2 Settlement method, per stored prediction for that fixture

1. Compute the actual outcome for this specific market from the fixture's final statistics.
2. Some markets may be unsettleable even after full-time if the underlying statistic never arrived — mark as "unsettled — data unavailable," never silently drop and never guess.
3. Compare predicted against actual, record a binary correct/incorrect flag, and — new — compute this settlement's contribution to `brier_contribution` and `log_loss_contribution` for calibration monitoring (Section 9.5/19).
4. Store this result permanently in `prediction_results`, linked back to the original prediction including confidence tier, calibrated probability, and the full feature snapshot — never overwritten or deleted, unchanged from v1.
5. **New — trigger shadow settlement (Section 18)** for every other, superseded lifecycle-stage version of this same fixture/market that led up to the LOCKED version, writing those results to the separate `shadow_settlements` table rather than mixing them into the public ledger.
6. Ensure settlement is idempotent — a fixture whose settlement job runs twice (a retry, a duplicate trigger) must produce the same result the second time, not a duplicate row or an error; this is a straightforward application of Design Principle 6 to the settlement process specifically, and Section 27's determinism test category checks it explicitly.

---

## 18. Lifecycle Shadow Evaluation

This section formalizes what Section 5A.7 introduced: because the engine produces a complete, independently-scored prediction at multiple points in a fixture's pre-match lifecycle, and every one of those versions is retained rather than overwritten, the system can measure something v1 had no way to measure — **whether waiting for better information actually improves accuracy, and by how much, for which markets.**

### 18.1 What gets tracked, per lifecycle stage

`accuracy_by_lifecycle_stage`, `Brier_by_lifecycle_stage`, `logloss_by_lifecycle_stage`, `calibration_by_lifecycle_stage` — the same core metrics the main ledger (Section 19) tracks for LOCKED predictions, computed identically for EARLY, PRE_MATCH, and LINEUP_CONFIRMED shadow-settled predictions, segmented the same way (market, league, confidence band).

### 18.2 The questions this is built to answer

- How much does confirmed lineup information actually improve 1X2 prediction accuracy — is the accuracy delta between PRE_MATCH and LINEUP_CONFIRMED large enough to justify the operational complexity of waiting, or is it marginal for this particular market?
- Does waiting until PRE_MATCH materially improve BTTS, or does BTTS's accuracy barely move between EARLY and LOCKED, meaning it could safely be published at full confidence far earlier than it currently is?
- Which markets, if any, can safely be published three days early (at EARLY-stage confidence) without a meaningful accuracy cost relative to waiting for LINEUP_CONFIRMED?

### 18.3 Why this is one of the most valuable long-term capabilities in this document

None of this requires new data collection — it's a direct byproduct of retaining every superseded prediction version (something v1 already does per Section 5A.3) and shadow-settling them (the one new piece of machinery this section adds). The payoff compounds over time: once enough shadow-settled history accumulates, Section 12.4's market-specific recommendation thresholds and Section 26's coverage-versus-accuracy tuning can be informed not just by "how accurate is this market at LOCKED" but by "how much of that accuracy is actually attributable to which lifecycle stage's information," which is a materially more actionable question for deciding where to invest further data/model effort (a market where LINEUP_CONFIRMED barely beats EARLY doesn't need a more sophisticated lineup-strength model; a market where the gap is large does).

### 18.4 Reporting boundary — shadow results never leak into the public ledger

`shadow_settlements` is a separate table from `prediction_results` specifically so that the headline, user-facing accuracy figures (Section 19) are never contaminated by pre-LOCKED shadow data — a user-facing "our accuracy is X%" claim always means LOCKED-stage accuracy, full stop, and shadow evaluation is purely an internal analytics capability that informs engine tuning, never a metric blended into what gets shown externally.

---

## 19. Accuracy, Calibration & Statistical-Significance Ledger

This is the most substantially expanded section relative to v1. v1's accuracy ledger tracked overall/market/league/confidence-tier accuracy on a schedule. That's necessary but not sufficient — an accuracy number without a sample size is not evidence, and this section makes that explicit and enforced rather than assumed.

### 19.1 Core aggregation — extended from v1

Maintain running figures, recomputed on a schedule (not per-settlement), across: overall accuracy, accuracy per market, **accuracy per market per confidence band** (not just per confidence tier — Section 19.5 below defines the finer-grained band breakdown this now requires), accuracy per league, and accuracy trend over recent time windows. This remains, as v1 stated, the single most important breakdown for validating whether confidence scoring is actually meaningful — and Section 19.5 now makes that validation mechanical rather than something someone has to notice.

### 19.2 Never report accuracy without sample size and a confidence interval

**This is a hard requirement, not a formatting preference.** Every accuracy figure this ledger produces — for internal tuning or for anything eventually surfaced externally — is stored and reported alongside `sample_count` and a `95_percent_confidence_interval`. 9 correct out of 12 (75%) is not stronger evidence than 2,520 correct out of 4,000 (63%), and a system that reports both as bare percentages invites exactly that misreading. The confidence interval is what makes the difference visible without requiring the reader to intuit it.

### 19.3 Minimum sample requirements and shrinkage toward a neutral prior

A league/market combination is classified as **provisional** (fewer than a configured minimum, default 100 settled predictions), **emerging** (above that floor but below a stronger threshold, default 500), or **statistically mature** (above 500, aligned with Section 4.1's calibration and champion/challenger minimums for consistency). For provisional and emerging scopes, the observed accuracy figure used anywhere downstream (ranking, Section 15.2; market-specific thresholds, Section 12.4) is not the raw observed rate — it is shrunk toward a neutral or parent-scope prior (the market-level accuracy if a league-specific figure is provisional; a global prior if even the market-level figure is thin), using the same Bayesian-shrinkage principle Section 5D.5 already applies to team-level statistics, applied here to accuracy statistics themselves. A provisional league's raw 75% doesn't get to act like a mature 75% anywhere in this system.

### 19.4 Coverage as a first-class KPI

`recommendation_coverage` = published recommendations ÷ eligible candidate opportunities, tracked at the same granularity as accuracy (overall, per market, per league). **A 70% hit rate obtained by recommending 1% of eligible fixtures is not automatically commercially useful, and this ledger reports coverage alongside accuracy specifically so that fact is never hidden inside a headline accuracy number.** Track hit-rate-versus-coverage curves explicitly: for each market, report accuracy and coverage at each of the configured threshold candidates (55%/60%/65%/70%, matching Section 12.4's configuration), so the tradeoff Design Principle 13 describes — confidence threshold up, accuracy potentially up, coverage down — is visible as data, not asserted as a general principle nobody can act on. This is the direct evidence base Section 12.4's market-specific thresholds are tuned from, and it is reviewed on a recurring cadence (not set once and forgotten) as more settled data accumulates, since the right operating point can genuinely shift as calibration and models improve.

### 19.5 Confidence-band validation

Report, for bands 50–55%, 55–60%, 60–65%, 65–70%, 70–75%, 75–80%, 80%+: number of predictions, mean predicted (calibrated) probability, actual observed success rate, and each band's Brier contribution. **If the model says 75% for a band and the observed success rate is 61%, that is a calibration defect, full stop — Section 9.6's hard validation requirement is what this table exists to check mechanically, on an ongoing basis, not just once at launch.**

### 19.6 Statistical significance for any comparison, including champion/challenger

Any claim of the form "X performs better than Y" — a new model beating the old one, one league outperforming another, one confidence tier beating another — is only meaningful with a significance check behind it, not a bare percentage-point difference. This ledger's comparison utilities (used directly by Section 21's champion/challenger evaluation) compute whether an observed difference is statistically significant at a configured confidence level given both samples' sizes, and champion/challenger promotion decisions (Section 21.4) require this, not just "the challenger's number was higher."

### 19.7 Feeding back into ranking — unchanged principle from v1, now shrinkage-aware

The market-level and league-level accuracy figures computed here (now shrinkage-adjusted per 19.3) feed Section 15.2's ranking formula exactly as v1 specified: markets and leagues with a proven track record get a ranking boost over time, weaker-track-record ones are gently deprioritized, never fully excluded, and general predictions are unaffected.

---

## 20. Accumulator (Acca) Engine & Correlation Support Matrix

The acca engine remains, as v1 specified, a selection and combination layer operating entirely on top of the existing best-bet pool (Section 15.3) — it runs no models of its own, re-scores nothing, and modifies no existing prediction. Sections 20.1–20.3 and 20.5–20.6 below are unchanged from v1 in substance (light cross-reference updates only); Section 20.4 is substantially extended per the correlation support matrix this revision adds.

### 20.1 User inputs — unchanged from v1

Number of legs, target combined odds, markets filter, start datetime, end datetime — the same interface contract v1 specified, with the same honest-degradation behavior when a request can't be fully met (return what's actually available, flag clearly, never silently substitute weaker legs to hit a number).

### 20.2 Leg eligibility — unchanged from v1, with one addition

The same five conditions v1 specified (`is_best_bet = true`, `embargo_active = false`, future kickoff, within requested range, market filter match, current non-superseded version) — plus, new: the leg's market must have `market_metadata.best_bet_eligible = true` (Section 2.6), consistent with Section 15.3's pool definition now sourcing eligibility from the same registry.

### 20.3 Leg selection algorithm — unchanged from v1

Query eligible pool → group by fixture → apply per-fixture leg cap (ranked by Section 15.2's composite score) → compute fair odds per leg (1 ÷ `calibrated_probability`, not `raw_probability`) → check target-odds feasibility honestly → build the final leg set → compute combined price via 20.4 → generate explanation from each leg's `explanation_factors` (Section 16) → write to `acca_selections`.

### 20.4 Correlation handling — extended with an explicit per-market-pair support matrix

v1 already got the core principle right: legs from different fixtures are treated as independent (a reasonable approximation), legs from the same fixture are not, and same-fixture goals-derived combinations (Home Win + Under 2.5, BTTS + Over 2.5, and similar) are correctly priced by summing cells of Model A's stored scoreline matrix (Section 8.1) rather than naively multiplying marginal probabilities. That mechanism is unchanged and still correct — it now draws on Model A's matrix specifically, out of the full A/B/C ensemble, since the matrix (not a blended ensemble output) is the object with real joint structure.

**What v1 didn't yet handle: combinations that aren't both goals-derived.** Home Win + Over 9.5 Corners has no joint distribution anywhere in this engine — Model A's matrix covers goals, Model D's corners model (Section 8.4.2) is a separate, independently-fit distribution with no formally established joint relationship to the goals matrix. Silently multiplying these two marginal probabilities together, as a naive implementation might default to, asserts an independence assumption that has never been checked and may well be wrong (a team's dominant Home Win probability plausibly correlates with elevated corner generation, that team pressing for a second or third goal).

**The correlation support matrix (new):** for every pair of market families the acca engine might combine within the same fixture, a stored, explicit record of what correlation handling is actually possible: `exact_joint` (a real joint distribution exists — currently, goals-derived market pairs via Model A's matrix), `empirical_estimate` (a correlation coefficient has been empirically estimated from historical data and validated well enough to use, even without a full joint distribution — a documented future extension, not a v1/v2 requirement), or `unsupported` (no joint handling exists at all — currently, any pairing that crosses out of the goals-derived family, including any combination involving Model D's outputs).

**The v2 rule for `unsupported` pairs:** the acca engine does not silently multiply them. Per the configured policy (Section 4.1): either the combination is **prohibited** — the leg selection algorithm (20.3) never places two legs from the same fixture together unless their pair is `exact_joint` or `empirical_estimate`, falling back to selecting legs from different fixtures instead where the correlation matrix doesn't support a same-fixture pairing — or, where the product explicitly wants same-fixture set-piece combinations anyway, the combination is **explicitly downgraded**: computed via the same-fixture-aware documented approximation v1 already described (treated as independent, with a stored, machine-readable `correlation_approximate = true` flag on that acca), and the resulting combined odds are never presented as an exact fair price — always with the approximation explicitly surfaced, never silently absorbed into a number that looks as precise as the goals-derived combinations. **The default policy for v1/v2 is prohibition, per Section 12.4/Section 24's general preference for suppressing what isn't provably supported over quietly approximating it** — the downgrade path exists and is documented for a product decision to explicitly opt into later, it is not the default.

### 20.5 Named acca products — unchanged from v1

Today's, tomorrow's, weekend, Saturday, Sunday, midweek, full-week, and custom accas, mapped to start/end datetime ranges exactly as v1 specified, still with no acca-engine-level awareness of product naming.

### 20.6 What the acca engine must never do — extended from v1

- Never include a leg whose `embargo_active` was true at selection time (v1, unchanged)
- Never compute combined odds by naive multiplication for same-fixture legs whose pair is `unsupported` in the correlation matrix — either exclude the pairing or explicitly flag the approximation (new, 20.4)
- Never lower the `is_best_bet` eligibility standard to hit a target combined odds (v1, unchanged)
- Never include legs outside the 7-day horizon window (v1, unchanged)
- Never present a combined odds figure without noting it's a fair/model-derived price, and never present a `correlation_approximate = true` combination's odds without that flag visible downstream (extended, 20.4)

---
## 21. Champion/Challenger Framework

### 21.1 Roles

The model currently serving production traffic for a given market family is the **Champion** (`model_registry.status = "production"`). Any newly trained or newly built model competing to replace it is a **Challenger** (`status = "candidate"`, then `"shadow"` once evaluation begins). Exactly one Champion may exist per market family at any time — Section 3.3's unique partial index enforces this at the database level, not just in application logic, precisely so "which model is actually live" is never ambiguous even under a bug or a race condition.

### 21.2 The first cycle this system ever runs is not hypothetical

Because this document extends an already-running system (Section 1.1), the very first champion/challenger cycle has a concrete, real starting point: **the currently-live v1 Poisson/Dixon-Coles-plus-form engine is registered as the initial Champion** for the 1X2 and goals-derived market families (Phase 0, Section 28), with best-effort historical values recorded for its training window where exact figures aren't recoverable. The new calibrated A/B/C ensemble this document specifies (Sections 8–9) is built and evaluated as a **Challenger** against that real Champion, on real, currently-flowing fixtures, not against a synthetic or theoretical baseline. This is a stronger starting position than a pre-implementation design could offer, and it's why this document requires the challenger to *prove* itself rather than simply assuming a more sophisticated model stack is automatically better — sophistication is not evidence, Section 25's baseline comparisons exist precisely to keep that honest.

### 21.3 Shadow evaluation method

A Challenger runs against every fixture the Champion also runs against, for the full lifecycle (EARLY through LOCKED), producing its own complete prediction set — but the Challenger's predictions are never exposed to users, never enter the best-bet pool, and never influence the acca engine. They are stored, settled (via the same settlement process, Section 17, tagged to the challenger model version), and accumulated until Section 21.4's evaluation threshold is met.

### 21.4 Promotion decision

After a sufficient settled sample (minimum per Section 4.1, default 500, aligned with Section 18/19's other minimum-sample thresholds), compare Champion and Challenger across: accuracy, Brier Score, Log Loss, calibration error, coverage, `model_edge`/simulated ROI, per-league and per-market performance, and failure rate (fixtures the model couldn't produce a confident prediction for at all). **Every comparison goes through Section 19.6's statistical significance check — a Challenger with a numerically higher accuracy that isn't significant at the configured confidence level is not promoted.** The decision (`promote` / `reject` / `extend_evaluation`) and full supporting evidence are written to `champion_challenger_evaluations` (Section 3.3), permanently, whichever way it goes — a rejected challenger's evaluation record is kept, not discarded, both for audit and because a rejected challenger's evidence is exactly what the next iteration should learn from.

### 21.5 What promotion actually does

Promotion updates `model_registry`: the new Champion's `status` becomes `"production"` and `promoted_at` is set; the old Champion's `status` becomes `"retired"` and `retired_at` is set. This is the only mechanism by which a production model changes, ever — there is no code path that swaps production models outside this promotion flow, which is the concrete enforcement of Design Principle 14's "never silently replace" rule.

---

## 22. Drift Monitoring

Champion/challenger evaluation catches problems at deliberate evaluation checkpoints. Drift monitoring catches problems continuously, between those checkpoints, before enough evidence has accumulated to trigger a formal challenger cycle.

### 22.1 What's monitored

- **Feature drift** — the distribution of key input features (e.g., a league's average goals rate, a team-tier's typical data-completeness score) shifting meaningfully from what the production model's training data reflected
- **Prediction distribution drift** — the shape of the model's own output (e.g., the proportion of predictions landing in each confidence band) shifting unexpectedly
- **Calibration drift** — Section 9.5's ECE or calibration slope moving out of its historically expected range for a given market/band
- **Accuracy drift** — rolling accuracy over a recent window (default 200 predictions, Section 4.1) dropping below the range that window's size and the market's historical variance would statistically expect
- **League-level performance drift** — a specific league's accuracy diverging from the model's overall pattern in a way that suggests something specific to that league changed (a new season format, a significant change in officiating standards, and similar events with no single clean feature to capture them)

### 22.2 Example alerts, to make this concrete

Strong-confidence accuracy over a rolling window of 200 predictions dropping below the expected range for that tier. A probability bucket that historically read 70–80% now showing an actual hit rate of only 58% over a recent window. A promoted-team feature distribution deviating significantly from what the production model's training distribution represented.

### 22.3 What happens when drift exceeds configured limits

Per Section 4.1's separately-configured alert and retraining-trigger thresholds (an alert threshold is deliberately more sensitive than a retraining trigger, so a warning fires before the situation is severe enough to justify retraining investigation): warn the admin (a `drift_alerts` row, Section 3.3, `severity = "warn"`), lower confidence for the affected scope (an automatic, temporary confidence penalty applied via the same mechanism as Section 11's OOD penalty, until the drift is investigated and resolved), and — past the higher, retraining-trigger threshold — flag a retraining investigation candidate (Section 23), never trigger retraining automatically and unconditionally. **Do not automatically retrain blindly merely because a timer or threshold fired** — drift monitoring's job is to surface the signal reliably; the decision to actually retrain still goes through Section 23's policy, which requires more than a single drift alert on its own.

---

## 23. Retraining Policy

### 23.1 Retraining is scheduled and evidence-triggered, never reflexive

Do not retrain after every match, and do not retrain purely because a drift alert fired once. Two independent triggers exist, and either can generate a retraining *candidate* — neither one, alone, promotes anything to production without going through the full workflow below:

- **Scheduled evaluation** — a recurring cadence (default monthly, Section 4.1) independent of drift signals, so a model that's decaying slowly enough to never trip a hard drift threshold still gets periodically re-examined rather than drifting indefinitely unnoticed.
- **Drift-triggered investigation** — Section 22.3's retraining-trigger threshold being crossed generates a candidate outside the normal schedule, for cases severe enough not to wait for the next scheduled evaluation.

### 23.2 Minimum new-data threshold

A retraining candidate is not generated (regardless of which trigger fired) unless at least the configured minimum amount of new settled data (default 500 new settled predictions since the current production model's training cutoff, per market family, Section 4.1) has accumulated. Retraining on a trivially small increment of new data risks overfitting to recent noise rather than genuinely improving the model.

### 23.3 The full workflow

New data accumulated past the threshold → train challenger (using Section 24's walk-forward methodology, with a training window that now includes the accumulated new data) → walk-forward validation → calibration (Section 9, refit for the new model) → historical benchmark (Section 25) → shadow live evaluation (Section 21.3) → compare against current champion (Section 21.4) → promote or reject. This is the same champion/challenger pipeline as any other model change — retraining does not get a shortcut path to production, because a retrained model is still, structurally, a challenger until it's proven itself.

---

## 24. Walk-Forward Backtesting Methodology

This section formalizes the general method that Section 21 (champion/challenger), Section 23 (retraining), and Section 28's pre-launch validation phase all depend on. It is written once, here, as the authoritative method — every other section that references "walk-forward validation" means exactly this.

### 24.1 Why random train/test splitting is explicitly prohibited

Football outcomes are not independent, identically-distributed samples drawn from a fixed population — a team's underlying strength is autocorrelated across nearby fixtures in time (squad continuity, form, manager tenure), which means a random split can place a match from March in the training set and a match from the preceding February, involving the same team in a similar state, in the test set. A model can then appear to generalize well while actually having implicitly learned from information adjacent in time to what it's being tested on. **Random train/test splitting for this engine's temporal forecasting task is prohibited, without exception**, for exactly this reason.

### 24.2 Expanding-window and rolling-window validation

The replacement is walk-forward validation: train on an initial historical window (e.g., seasons 2021/22 through 2023/24), validate on the next chronological period (2024/25), and — where sample size allows multiple folds — repeat with the training window expanded (or, alternatively, rolled forward at a fixed size) to include the just-validated period, validating again on the next period after that. Each fold's validation period is always strictly chronologically after its training period, with no exceptions, and no fold's training window is ever allowed to include any data chronologically after its own validation window.

### 24.3 The final holdout — untouched by anything upstream of it

A final, most-recent chronological period (e.g., the current or most recently completed season, depending on data volume) is held out completely from feature selection, hyperparameter tuning, ensemble weighting (Section 8.5), confidence weighting (Section 11), calibration fitting (Section 9), and recommendation-threshold tuning (Section 12.4) — every one of those processes only ever sees the training and validation folds from 24.2, never this final period. The final holdout exists solely to be evaluated against once, at the end, by Section 28's pre-launch validation gate and by each champion/challenger cycle's headline comparison. Reusing the final holdout for any tuning decision — even once, even for something that seems minor — invalidates it, and a build agent finding itself tempted to "just check" the final holdout mid-development has found the exact failure mode this section exists to prevent.

### 24.4 Explicit dataset separation

Every model-development process in this document draws from exactly one of these four, never mixing across boundaries without an explicit, documented reason: **training dataset** (24.2's training folds), **tuning/validation dataset** (24.2's validation folds — hyperparameters, ensemble weights, confidence weights, and recommendation thresholds are all tuned here, never against training data directly and never against the final holdout), **calibration dataset** (a further out-of-sample slice, per Section 9.3's requirement that calibration never trains on the same data as the underlying models — in practice this is typically carved from the validation folds specifically, held separate from whatever portion of validation data tunes the models themselves), and **final holdout test dataset** (24.3).

### 24.5 Live shadow-production evaluation — the fifth stage, beyond backtesting

Once a model has passed the backtested stages above, it still isn't promoted directly — it enters Section 21.3's live shadow evaluation, running against real, current fixtures it has never seen in any form, for the sample size Section 21.4 requires, before a promotion decision is made. This is the final check that backtested performance, however carefully validated, actually holds up against genuinely new data the model had no way to have been indirectly shaped by.

### 24.6 Historical replay must behave exactly like production, or it isn't a backtest

A backtest is not a query against today's fully-populated database pretending to be historical — it is a simulation that honors the actual historical timeline: a fixture enters the horizon window (Section 5C) → an EARLY prediction is generated using only data with a timestamp before the simulated EARLY moment → a PRE_MATCH prediction where applicable, same rule → a LINEUP prediction only if historical lineup-confirmation timestamp data genuinely exists for that historical fixture → LOCK → settlement. The replay must honor prediction horizon, embargo, lifecycle timing, data-availability timestamps, odds timestamps, lineup timestamps, injury timestamps, the model version that would genuinely have been in production at that historical moment, and the calibration model that would genuinely have been available at that historical point — never the current, most-recent calibration model applied retroactively to old predictions it was never actually used for. **Loading the final, fully-completed historical database state and treating it as what was available before kickoff is not a backtest — it's the single most common way a backtest silently produces impressive, meaningless numbers**, and Section 3.4's point-in-time infrastructure exists specifically so this replay can be built correctly rather than approximated.

---

## 25. Baseline Comparisons

The engine must prove its complexity earns its keep, not assume it. Every headline model (the production ensemble, and any promoted challenger) is benchmarked, on the same final holdout (Section 24.3), against:

- **Baseline A — league historical frequency** (e.g., the league's raw historical home-win rate, with no team-specific adjustment at all)
- **Baseline B — simple Poisson** (Model A alone, Section 8.1, with no Elo, no ML model, no ensemble)
- **Baseline C — Elo alone** (Model B alone, Section 8.2)
- **Baseline D — bookmaker de-vigged implied probabilities** (Section 14.2), where odds coverage exists for the relevant scope
- **Baseline E — API-Football's own native predictions** (`api_predictions`, Section 3.1), where usable as a benchmark

**If the full ensemble cannot demonstrate a statistically significant (Section 19.6) improvement over simple Poisson or over de-vigged market probabilities, it does not get to remain in production purely because it's more sophisticated.** This is not a hypothetical — it's a required, standing comparison, re-run every time a challenger is evaluated (Section 21.4), and its result is part of every promotion decision's stored evidence.

---

## 26. The 60%+ Target — What It Means and What It Explicitly Does Not Mean

This section is the capstone the rest of the document is built to support honestly, and it's worth stating precisely because "60%" is easy to say and easy to misuse.

### 26.1 The target, stated precisely

High-confidence published recommendations (Strong tier, per Section 11.4) should achieve a rolling hit rate of 60% or higher — but **only when that figure is supported by**: sufficient sample size (Section 19.2/19.3 — a provisional-sample 60% is not the same claim as a statistically-mature-sample 60%), acceptable calibration (Section 9.6's hard requirement — a 60% hit rate on predictions the model itself only claimed 50% for is not a calibration success, it's a coincidence, and Section 19.5's confidence-band validation is designed to catch exactly this), reasonable coverage (Section 19.4 — 60% achieved by recommending almost nothing is not the target this document means), positive, significant performance versus the baselines in Section 25 (a 60% that a simple Poisson model or the market's own de-vigged price would have matched isn't evidence this engine's added complexity is earning anything), and no evidence of leakage (Section 3.4, Section 27.2) or cherry-picking (a single strong 30-day window is not the same claim as stability across multiple, independent time windows and multiple leagues).

### 26.2 Minimum validation sample before any claim, provisional or otherwise

**No claim about hitting 60%, even a provisional one, is made below 500 settled recommendations.** 1,000+ is preferred for a materially stronger claim. Several thousand, spanning multiple seasons and multiple leagues, is what mature evaluation looks like. These thresholds are the same ones already established in Sections 4.1, 18, 19, and 21 — repeated here because this is the section most likely to be read in isolation, and it should carry its own evidentiary bar rather than depending on the reader having cross-referenced everything above it.

### 26.3 Not every market needs to clear 60%

Different markets have genuinely different achievable ceilings — Correct Score's structurally capped confidence (Section 10.4) reflects a market that is not expected to approach 60% at Strong tier, and treating a Correct Score market's lower achievable accuracy as a system failure would be a misunderstanding of what the cap already communicates honestly. The 60% target applies per market, evaluated against that market's own baseline and its own achievable ceiling — not as one number every market is expected to hit uniformly.

### 26.4 What this system is actually designed to guarantee — which is not 60% itself

This document does not claim the engine will achieve 60%+ accuracy. It specifies a system that: makes 60%+ on selectively published high-confidence recommendations a measurable, honestly-evaluated target (this section and Section 19); detects the specific, named failure modes that produce a *false* 60% — leakage (Section 3.4, Section 27.2), miscalibration (Section 9.6), and cherry-picked or sample-starved claims (26.2, Section 19.3) — before they can reach a user-facing number; automatically suppresses markets and leagues where the evidence doesn't support confident publishing (Section 5D's OOD gate, Section 12's NO_PREDICTION codes, Section 10.4's confidence caps); and continuously proves, through the ledger (Section 19) and shadow evaluation (Section 18), where it is genuinely good and where it isn't — rather than asserting a single number once and never checking it again.

---
## 27. Extended Test Categories

v1's phased testing gates (Section 28) already required unit and integration tests per phase. This section adds test *categories* that are fundamentally different in kind from ordinary unit tests — they check properties of the system as a whole, several of them specifically designed to catch the failure modes (leakage, non-determinism, silent behavioral drift) that an ordinary unit test, written to check "does this function return the right answer for this input," structurally cannot catch. These categories are referenced by name from the relevant phases in Section 28 rather than re-described per phase.

### 27.1 Property-based tests

Rather than checking specific input/output pairs, assert properties that must hold for *any* valid input, generated across a wide random/fuzzed range: every probability output is within [0, 1]; mutually exclusive outcome probabilities (1X2, over/under at a given line, BTTS yes/no) sum to exactly 1.0 after normalization; every `calibrated_probability` is a valid probability; the scoreline matrix (Section 8.1) sums to exactly 1.0 across all cells for any valid lambda pair fed into it.

### 27.2 Temporal leakage tests

Directly validate Section 3.4's central correctness property: adding a future fixture to the database must provably not alter any already-computed historical `as_of`-scoped feature vector or prediction. Concretely — compute a historical prediction, insert new data timestamped after that prediction's `as_of_timestamp`, recompute the same historical prediction with the same `as_of_timestamp`, and assert the result is bit-for-bit (or float-tolerance) identical. Any difference is a leakage bug, full stop, and this test category must run as part of every phase from Phase 2 onward, not just once at the end.

### 27.3 Metamorphic tests

Assert that changing an unrelated fixture must not alter another fixture's prediction — a direct test of Design Principle 9's per-fixture independence. Concretely — compute a prediction for fixture A, then insert or modify data for an unrelated fixture B (different teams, different league), recompute fixture A's prediction, and assert it's unchanged. This catches accidental shared-state bugs (a cache key collision, an improperly-scoped query, a global variable that shouldn't exist) that per-fixture unit tests alone wouldn't surface, since each unit test typically only exercises one fixture in isolation.

### 27.4 Determinism tests

Same inputs, same model version, same code path → exactly the same outputs, every time. This is what makes Section 21's champion/challenger comparisons and Section 24's backtests trustworthy — a system with hidden nondeterminism (unseeded randomness, a race condition, a floating-point operation order that varies with concurrency) produces different "backtest results" on different runs of the identical historical replay, which silently undermines every validation gate in this document that assumes a backtest run is reproducible.

### 27.5 Monotonicity sanity tests

Where a directional relationship is genuinely expected, check it holds: increasing a team's expected home scoring strength significantly (holding the opponent's defensive strength fixed) should not *decrease* that team's expected home goals output from Model A. These are sanity checks on the model's internal logic, not accuracy checks — a monotonicity violation usually indicates an implementation bug (a sign error, a misapplied adjustment) rather than a genuine, defensible statistical subtlety, and should be treated as a bug until specifically proven otherwise.

### 27.6 Statistical regression tests

A new engine version (a new model, a new calibration fit, a new feature) must not unexpectedly shift the prediction distribution across a fixed benchmark dataset without an explained, intentional reason. Run the new version against a stored, fixed set of historical fixtures, compare the resulting prediction distribution (mean probability per market, confidence tier distribution, coverage) against the previous version's stored results on the same fixed set, and require an explicit sign-off (a documented reason in the change's record) for any shift beyond a configured tolerance. This is what catches an unintentional, silent behavioral change that technically passes every other test category but still means the engine's output has quietly shifted in a way nobody decided to make happen.

---
## 28. Phased Build Plan With Testing Gates

Each phase below has a Build, a Definition of Done, and a mandatory Testing Gate. **Because this document extends an already-running system (Section 1.1), every phase is marked either `[EXTENSION]`** — the subject matter already exists and is live; this phase's job is verification and additive migration, not construction — **or `[NEW BUILD]`** — genuinely new capability with no v1 equivalent, built with the same rigor v1's original phases specified. Do not begin a phase until the previous phase's tests pass. A phase touching a live table or live code path must never take the system offline or produce a period where predictions stop flowing — every migration in this plan is additive and backward-compatible by construction (Section 1.1), and this is a testing-gate requirement, not just a design preference.

### Phase 0 — Point-in-Time Data Contract Verification & Model Registry Bootstrap `[EXTENSION]`

**Build:** Audit the actual current production schema against Section 3's full contract — confirm what genuinely exists versus what's assumed. Apply the new tables as additive migrations: `fixture_events` (optional), `team_manager_history`, `model_registry`, `calibration_models`, `shadow_settlements`, `drift_alerts`, `champion_challenger_evaluations`, `market_metadata`. Apply the new `predictions` and `odds` columns as nullable `ALTER TABLE` additions (Section 3.3) — existing rows keep `NULL` for every new field, per Section 1.1's explicit rule against fabricating retroactive values. Populate `market_metadata` per Section 2.6 for every market in the catalogue. **Bootstrap `model_registry`** with a row representing the currently-live v1 model, `status = "production"`, best-effort historical training-window values where exact figures aren't recoverable, clearly flagged as approximate.

**Testing gate:**
- Confirm every migration is additive — run it against a copy of the real production database and confirm zero existing rows are altered or deleted, only new nullable columns/tables added
- Confirm the live prediction pipeline continues running, unmodified, against the migrated schema with no behavior change (this phase must not itself change any prediction output)
- Confirm `market_metadata` correctly flags Team To Score First and Race to N Corners as disabled pending `has_event_timestamps` coverage (Section 2.6)
- Confirm the bootstrapped `model_registry` row is the only `status = "production"` row for its market family, and that the unique partial index (Section 21.1) is in place and enforced

### Phase 1 — Configuration & Model Registry Activation `[EXTENSION]`

**Build:** Extend Section 4's configuration layer with every new config group (per-feature decay, market-specific thresholds, calibration, OOD, drift, retraining, promotion). Extend the validation logic (4.2) to cover the new groups. Wire `model_registry` into an active system, not just a populated table — the promotion mechanism (Section 21.5) as real, callable logic, even though nothing will call it to promote anything until Phase 16.

**Testing gate:**
- All of v1's original configuration validation tests still pass, unmodified
- New validation rules (decay bounds, OOD threshold ordering, minimum sample positivity) correctly reject invalid configuration and accept the documented defaults
- Attempting to insert a second `status = "production"` row for a market family that already has one fails at the database level, not just in application code

### Phase 2 — Point-in-Time-Safe Feature Engineering `[EXTENSION, with genuinely new correctness work]`

**Build:** Retrofit the existing feature-assembly code so `as_of_timestamp` is an explicit, mandatory parameter throughout — per Section 3.4, no feature-building function may rely on an implicit "now." Add Section 3.4.4's snapshot storage for expensive-to-reconstruct aggregates. Add hierarchical fallback/shrinkage (Section 5D.5), manager/regime features (Section 5E), and the richer player-strength model (Section 5F), replacing the old binary injury proxy.

**Testing gate:**
- Section 27.2's temporal leakage test category passes for every feature group, not a sample of them
- Confirm the retrofit produces **identical** output to the pre-retrofit live system for current, present-moment predictions (where `as_of_timestamp` equals "now," point-in-time-safe code and the old implicit-now code should agree) — this is the specific regression check that proves the retrofit didn't silently change live behavior while adding historical correctness
- Hierarchical shrinkage produces sensible, bounded estimates for a constructed newly-promoted-team scenario (2 matches of thin data should not produce an extreme rate)

### Phase 2A — Lifecycle Stability & Shadow-Settlement Hooks `[EXTENSION]`

**Build:** Add Section 5A.6 (prediction stability tracking) and the shadow-settlement trigger hook (5A.7) on top of the existing, live lifecycle/concurrency/horizon machinery (Sections 5A–5C), which is otherwise unchanged and continues running as-is.

**Testing gate:**
- `probability_delta` and `lifecycle_stability_score` compute correctly across a fixture's real EARLY→PRE_MATCH→LINEUP_CONFIRMED progression, verified against at least one real, currently in-flight fixture
- The shadow-settlement hook fires correctly without altering the existing LOCKED-stage settlement path's behavior at all

### Phase 3 — Out-of-Distribution Gate `[NEW BUILD]`

**Build:** Section 5D in full — OOD signal computation, the hard confidence-cap and force-BASIC/NO_RECOMMENDATION thresholds, wired in as an additional gate stage running immediately after the existing Section 5 quality gate.

**Testing gate:**
- A constructed newly-promoted-club scenario and a constructed established-mid-table scenario, matched on raw sample size, produce meaningfully different `ood_score` values
- Confirm the OOD gate correctly caps or suppresses without altering the existing quality gate's SKIP/BASIC/FULL logic (Section 5.2) — the two gates remain sequential and independent, per Section 5.5

### Phase 4 — Model A Migration: Blended Season/Form Rate `[EXTENSION]`

**Build:** Modify the live Model A (Section 8.1) to use the configured blend of season-long and recent-form rates directly in its attack/defence calculation (Section 8.1 step 1–2), replacing the old separate form-adjustment-delta mechanism (Section 8.6).

**Testing gate:**
- Section 27.6's statistical regression test, run before this change ships as anything more than a challenger candidate: compare the modified Model A's output distribution against the current live Model A's output distribution on a fixed benchmark set, and confirm the shift is understood and bounded, not a surprise
- The matrix-persistence requirement (Section 8.1) is verified still satisfied post-change

### Phase 5 — Model B: Elo / Dynamic Team-Strength `[NEW BUILD]`

**Build:** Section 8.2 in full, including the point-in-time-correct rating-update discipline and the hierarchical-fallback-consistent initialization for new teams.

**Testing gate:**
- Elo updates are verified point-in-time correct via Section 27.2 (a fixture's own result must not be reflected in the rating used to predict that same fixture)
- Elo-derived probabilities for a set of hand-picked, intuitively-obvious mismatches (a long-time top-of-table side versus a long-time bottom-of-table side) land in a sensible direction and range

### Phase 6 — Model C: Machine-Learning Tabular Model `[NEW BUILD]`

**Build:** Section 8.3 in full — the first genuinely trained ML artifact in this system's life, built using Section 24's walk-forward methodology from day one (this is the first model in the project for which walk-forward validation isn't retrofitted after the fact — it's foundational to how the model is trained at all).

**Testing gate:**
- Section 24.1's prohibition on random splitting is verified structurally enforced in the training code, not just documented
- SHAP-based `explanation_factors` are verified to actually correspond to the features the model demonstrably weighted most heavily (a basic sanity check: perturbing a top-SHAP feature should move the prediction; perturbing a bottom-SHAP feature should barely move it)
- The trained artifact is registered in `model_registry` with `status = "candidate"` before any evaluation begins

### Phase 7 — Market-Specific Auxiliary Models: Distribution Diagnostics `[EXTENSION, with genuinely new diagnostic rigor]`

**Build:** Run Section 8.4.1's diagnostics against the real historical corners/cards/fouls/shots/possession/pass-accuracy data the live system has already accumulated — this is a genuine advantage of extending a running system rather than starting fresh, since real distributional evidence already exists rather than needing to be simulated. Rebuild or adjust each market family's model per what the diagnostics actually show (confirming Poisson where it already fits, moving to Negative Binomial/hurdle/zero-inflated/Beta regression where the diagnostics indicate).

**Testing gate:**
- Diagnostics are run and recorded per league tier, not just once globally, per Section 8.4.1
- For any market/league where the diagnostics indicate the existing live model's distribution choice is a poor fit (e.g., corners with a dispersion ratio well above 1.0 still using pure Poisson), the finding is explicitly flagged and a Negative Binomial (or appropriate) replacement is validated against the same diagnostics before replacing the live model for that specific market/league
- Possession and pass accuracy are confirmed using Beta regression (or another validated bounded-proportion method), never a count model, per Section 8.4.6's hard rule

### Phase 8 — Genuine Ensemble & Model Agreement `[NEW BUILD, replacing a live component]`

**Build:** Section 8.5 in full — replaces the live pseudo-ensemble blend with the genuine A/B/C combination and agreement metrics. This is one of the more disruptive changes in this plan, since it touches the core blending step currently serving live predictions; it does not go live directly — it runs as part of the challenger stack through Phases 15–17, never swapped into production ahead of a champion/challenger decision.

**Testing gate:**
- Section 27.1's property-based tests confirm `ensemble_mean` and the underlying market probabilities remain valid probabilities after weighting and renormalization
- The worked-example scenarios from Section 8.5 (three models in tight agreement versus three models in wide disagreement) are reconstructed with synthetic inputs and confirmed to produce correspondingly high and low `model_agreement_score` values
- Confirm this phase's ensemble is not live-serving traffic — it exists only within the challenger evaluation path until Phase 17

### Phase 9 — Probability Calibration `[NEW BUILD, trained on real accumulated data]`

**Build:** Section 9 in full. Because the live system has real settlement history, initial calibration models can be fit against genuine out-of-sample settled predictions rather than synthetic data — with the caveat (Section 1.1) that pre-v2 legacy predictions lack `raw_probability`/ensemble fields and are only usable for calibrating markets/stages where the relevant fields exist.

**Testing gate:**
- Section 9.3's training-discipline requirement is verified structurally: the calibration training pipeline is checked to confirm it cannot access the final holdout (Section 24.3) or the same data used to train Models A/B/C
- Section 9.6's hard validation check runs against the fitted calibration models and any market/band failing it is marked ineligible for the recommendation gate rather than silently accepted

### Phase 10 — Confidence & Meta-Confidence Rework `[EXTENSION]`

**Build:** Rework the live confidence scorer's weights per Section 11 — H2H's contribution cut to 0–5%, the extended-H2H bonus removed, new inputs (calibration reliability, stability, OOD penalty) added, model-consensus weight increased now that it reflects genuine agreement.

**Testing gate:**
- Section 27.6's statistical regression test compares the reworked confidence distribution against the live system's current distribution on the fixed benchmark set, with the shift explicitly reviewed and signed off rather than shipped as a surprise
- Confirm Section 11.5's hard rule (confidence and probability never collapse into one number) holds throughout the reworked scorer

### Phase 11 — Recommendation Gate, NO-PREDICTION Codes & Market Selector `[NEW BUILD]`

**Build:** Section 12's formal recommendation-status/reason-code system and Section 13's per-fixture market selector, layered on top of the (by now OOD-aware, calibration-aware, reworked-confidence) gate stack from prior phases.

**Testing gate:**
- Every enumerated reason code in Section 12.3 is exercised by at least one constructed test scenario that should produce it
- Section 13's worked example (Home Win 64% / Double Chance 81% / Over 1.5 79% / Over 2.5 59% / BTTS 52%) is reconstructed and confirmed to select Double Chance or Over 1.5, not defaulting to 1X2

### Phase 12 — Explanation & Structured Rationale Rework `[EXTENSION]`

**Build:** Rework the live explanation generator per Section 16 — structured factors stored first (SHAP for Model C, retained intermediate values for Models A/B/D), text rendered from factors afterward, never generated independently.

**Testing gate:**
- Manual review of a sample of generated explanations against their stored `explanation_factors`, confirming the text's claimed drivers match the actual stored top-magnitude factors
- Confirm banned-phrasing checks (Section 16.3 step 5) are enforced automatically, not just by convention

### Phase 13 — Shadow Lifecycle Settlement `[EXTENSION]`

**Build:** Section 18 in full, hooked into the existing live settlement process (Section 17) via the Phase 2A trigger.

**Testing gate:**
- Shadow settlement writes to `shadow_settlements` correctly for a real, currently-settling fixture without touching the public `prediction_results` ledger at all
- Confirm `accuracy_by_lifecycle_stage` and the other Section 18.1 metrics compute correctly once enough shadow-settled data exists to populate them meaningfully

### Phase 14 — Acca Correlation Support Matrix `[EXTENSION, fixing a live correctness gap]`

**Build:** Section 20.4's correlation support matrix, wired into the live acca engine. **This phase should be treated as a priority correctness fix, not just an enhancement:** if the currently-live acca engine has been combining same-fixture legs outside the goals-derived family (e.g., a 1X2-plus-corners combination) via naive multiplication, that is an active, live pricing inaccuracy this phase directly corrects — worth confirming explicitly, as part of this phase's audit step, whether that's actually been happening in production data to date.

**Testing gate:**
- Audit real, already-generated acca selections for any same-fixture leg pairs outside the `exact_joint` goals-derived family, and confirm none exist going forward once this phase ships (the default-prohibition policy, Section 20.4, blocks them at selection time)
- Confirm goals-derived same-fixture combinations continue pricing via the score matrix exactly as before — this phase must not change already-correct behavior, only add the missing guard for the previously-unhandled case

### Phase 15 — Walk-Forward Backtest `[NEW BUILD, applied to real accumulated history]`

**Build:** Section 24's full methodology, run against the real historical fixture/prediction/settlement data the live system has accumulated, supplemented with the deeper historical backfill data (per the original document's Phase 0 equivalent) where needed for adequate training-window depth.

**Testing gate:**
- Section 24.3's final holdout is confirmed genuinely untouched by every upstream tuning process (hyperparameters, ensemble weights, confidence weights, recommendation thresholds) — a structural code-path check, not a self-report
- Section 24.6's historical-replay fidelity is verified: spot-check several historical predictions the replay generates against what point-in-time data genuinely existed at that historical moment

### Phase 16 — Champion/Challenger Shadow Production `[NEW BUILD, against a real live Champion]`

**Build:** Section 21 in full. The full new stack (Models A/B/C blended, calibrated, confidence-reworked, OOD-gated) runs as Challenger against the real, currently-live v1 engine as Champion (Section 21.2), on real current fixtures, for the minimum evaluation sample.

**Testing gate:**
- Confirm the Challenger's predictions are genuinely shadow-only — never entering the live best-bet pool, never reaching the acca engine, never visible to users, throughout this entire phase
- Section 19.6's statistical significance check is run on the accumulated Champion-vs-Challenger comparison before Phase 17 begins, regardless of how the raw numbers look

### Phase 17 — Production Launch Gate `[Promotion decision + comprehensive validation]`

**Build:** Nothing new — this phase evaluates everything built in Phases 0–16 and, if it passes, executes the actual promotion (Section 21.5) that switches live traffic from the v1 Champion to the new Challenger stack.

**Testing gate — the full report required, none optional:**
- **Classification:** accuracy, precision/recall where relevant
- **Probability quality:** Brier Score, Log Loss, calibration error, reliability diagrams (Section 9.5)
- **Operational/product:** recommendation coverage, predictions per fixture, no-prediction rate broken down by reason code (Section 12.3), latency per fixture, data-completeness distribution
- **Segment analysis:** market, league, confidence tier, lifecycle stage, probability bucket, odds bucket, home/away, early-season vs mature-season, high vs low data completeness
- **Betting/value simulation:** de-vigged market edge, closing-line value, simulated ROI, maximum drawdown, number of bets, average odds, with uncertainty intervals throughout (Section 19.2)
- **Baseline comparison:** Section 25's full comparison against Baselines A–E, with statistical significance
- **Explicit statement, required in the sign-off record:** *the engine does not pass this gate merely because one headline accuracy number exceeds 60%.* Every item above must be reviewed; a single strong topline figure with a weak segment breakdown, poor calibration, or coverage achieved through over-restriction does not clear this gate.
- Only once every item above is reviewed and the Section 21.4 promotion decision is `promote` does `model_registry` update: new Champion `status = "production"`, old Champion `status = "retired"` — the moment live traffic actually shifts, executed exactly once, logged, never silent.

---
## 29. Engine Invocation Schedule (Boundary Note Only)

This document covers the engine itself, not the orchestration system that calls it. The invocation pattern from v1 — horizon sweep, stage-boundary sweep, data-arrival trigger, settlement trigger, all built around Sections 5A–5C — continues unchanged. This revision adds three triggers on top:

- A **drift-check sweep** runs on the configured cadence (Section 4.1), evaluating Section 22's monitored metrics and writing `drift_alerts` where thresholds are crossed.
- A **retraining-candidate sweep** runs on Section 23's configured schedule, independent of drift, checking whether the minimum new-settled-data threshold has been met per market family.
- A **champion/challenger evaluation checkpoint** fires once a challenger (whether from a retraining candidate or a from-scratch new model) reaches its minimum shadow-evaluation sample (Section 21.4), triggering the comparison and promotion-decision workflow.

As with every trigger in this document, the engine itself does not need to know about the schedule internally — each invocation is "evaluate this against the current database state," consistent with Design Principle 6 and Design Principle 9. The orchestration system decides when and how often; the engine's obligation is to be correct, concurrent, idempotent, and — per Section 1.1 — safe to run against a live, already-serving system every time it's invoked.

---

## Appendix A — Summary Table: Market Tier vs. Model vs. Gate Strictness

| Tier | Markets | Model | Gate strictness | Confidence ceiling |
|---|---|---|---|---|
| A | 1X2, double chance, O/U goals, BTTS, correct score, HT/FT, team totals, clean sheet, win to nil, odd/even, Team To Score First / Race to N Corners (where `has_event_timestamps` confirmed) | Models A (Poisson/DC) + B (Elo) + C (ML) via the genuine ensemble (Section 8.5), then calibrated (Section 9) | Base fixture-level gate + OOD gate (Section 5D) | Uncapped except Correct Score (Medium) and structurally noisy outlier lines (Medium) |
| B | Corners, cards, fouls, offsides, red card in match | Model D, distribution chosen per Section 8.4.1's diagnostics (Poisson/Negative Binomial/hurdle/zero-inflated as evidence dictates) | Base + OOD gate + Tier B strictness addition, requires `has_statistics` coverage flag | Uncapped except Red Card In Match (Low) and outlier lines (Medium) |
| C | Shots, shots on target, possession, pass accuracy, GK saves | Model D extension — count models for shots (Negative Binomial where diagnostics indicate), **Beta regression (never Poisson) for possession and pass accuracy** (Section 8.4.6) | Base + OOD gate + Tier C strictness addition (higher than B) | Uncapped by default, revisited after Phase 17's launch gate |
| D | All 1X2/goals/corners/cards combination markets | Goals-derived pairs: exact joint from Model A's matrix. Non-goals-derived pairs: `unsupported` by default, per the correlation support matrix (Section 20.4) | Inherits the stricter of its component markets' gates | Inherits the lower of its component markets' caps; non-`exact_joint` pairs are prohibited from best-bet/acca use by default |

## Appendix B — Explicit List of What Must Never Happen

**Point-in-time correctness and leakage (new category)**
- Never compute a feature or a historical prediction from the database's current state when a specific `as_of_timestamp` is what's actually required (Section 3.4.1)
- Never allow a backtest to query the fully-populated present-day database and treat it as what was available before a historical kickoff (Section 24.6)
- Never compare an EARLY-stage prediction against closing odds — only odds observed at or before that stage's own timestamp (Section 14.1)
- Never let a historical Elo rating reflect the outcome of the very fixture it's being used to predict (Section 8.2)

**Data and modeling**
- Never collapse probability and confidence into one number (Section 11.5)
- Never silently fall back to a default that looks like real data without flagging it (Section 6.4)
- Never infer league coverage from one non-null field — always check the explicit coverage flag (Section 10.1)
- Never let a market be assigned a statistical distribution just because another market used it successfully — run the diagnostics (Section 8.4.1)
- Never model possession or pass accuracy as a count/Poisson process — bounded-proportion methods only (Section 8.4.6)
- Never let any output probability hit exactly 0 or 1 (Section 8.5)
- Never directly blend odds or API-Football's native predictions into a model's probability output — cross-validation only, value analysis is a separate, later step (Sections 10.3, 14.4)
- Never expose the full correct-score matrix — top-N only (Section 10.5)
- Never treat a market's confidence ceiling cap as optional for markets flagged structurally noisy (Section 10.4)
- Never discard Model A's full scoreline matrix after deriving market probabilities (Section 8.1, Section 20.4)
- Never train a calibration model on the same data used to train the underlying models, or anywhere near the final holdout (Section 9.3)
- Never report an accuracy figure without its sample size and confidence interval (Section 19.2)
- Never let a provisional or emerging-sample accuracy figure act like a statistically mature one anywhere downstream (Section 19.3)

**Genuine ensemble and agreement**
- Never treat a second model as independent if it's structurally derived from the first model's own inputs — that's not agreement, it's the same signal counted twice (Section 8.6, Design Principle 11)
- Never average away model disagreement without also recording and surfacing it — `model_agreement_score` must persist alongside `ensemble_mean` on every prediction (Section 8.5)

**Prediction pool, best bets, and market selection**
- Never apply a daily, weekly, or any other count ceiling to the best-bet pool — pool size is determined only by how many predictions clear their market-specific threshold (Section 15.1, Section 12.4)
- Never re-evaluate pool membership at read time by recomputing the ranking score dynamically — compute and store it at write time (Section 15.2)
- Never let the market selector (Section 13) replace or override best-bet pool ranking (Section 15) — they answer different questions and one is not a substitute for the other

**Odds and value**
- Never conflate a high predicted probability with good betting value — `model_edge` against the de-vigged market price is the value signal, not the raw probability (Section 14.3)
- Never fold real market odds into the model's own probability output without it being a deliberate, evidenced, champion/challenger-evaluated experiment (Section 14.4)

**Accumulator engine and correlation**
- Never compute combined acca odds by naively multiplying leg probabilities when two or more legs share a fixture and their market pair is not `exact_joint` or a validated `empirical_estimate` — exclude the pairing or explicitly flag the approximation (Section 20.4)
- Never include an acca leg whose `embargo_active` was true at selection time, even if since lifted without a re-run (Section 20.6)
- Never lower the `is_best_bet` eligibility standard to hit a user's target combined odds (Section 20.6)

**Horizon and embargo**
- Never produce predictions for fixtures outside the 7-day rolling window (Section 5C.1)
- Never begin gate evaluation for a fixture under an active embargo — defer, log, wait for the lift trigger (Section 5C.3)
- Never treat a postponed interleaving match as still blocking an embargo (Section 5C.7)

**Concurrency and correctness**
- Never let one fixture's prediction work read, wait on, or block another fixture's (Design Principle 9, Section 5B.1) — the only shared state permitted is the read-only, pre-computed league-average baselines
- Never overwrite a prediction row in place — always insert a new version and mark the previous one superseded (Section 5A.3)
- Never settle a prediction against an earlier lifecycle-stage version for the public ledger — always grade LOCKED; earlier stages are shadow-settled separately (Section 17.1, Section 18.4)

**Explanations and honesty**
- Never generate explanation text disconnected from stored, structured `explanation_factors` — structured factors first, rendering second, never the reverse, and never a language model inventing rationale after the fact (Section 16.1)
- Never use banned certainty language in explanations (Section 16.3)
- Never drop an unsettleable prediction silently — mark it explicitly with a reason (Section 17.2)

**Model governance**
- Never allow more than one `status = "production"` model per market family — enforced at the database level, not just in application code (Section 21.1)
- Never promote a challenger based on a numerically higher metric alone without Section 19.6's statistical significance check (Section 21.4)
- Never retrain automatically and unconditionally just because a timer or a single drift alert fired (Section 22.3, Section 23.1)
- Never let a model reach production without first passing through the registry, shadow evaluation, and promotion workflow — including a retrained version of an already-production model (Section 23.3)

**Migration and extension discipline (new — specific to this document extending a live system)**
- Never apply a destructive migration to an existing table — every schema change in this revision is additive (Section 1.1, Phase 0)
- Never fabricate a value for a new field on a legacy, pre-v2 prediction row — leave it `NULL` and treat that as a real, expected state downstream (Section 1.1)
- Never swap the live Champion for a new model outside the formal promotion workflow, including during this revision's own rollout (Section 21.5, Phase 17)

**Build process**
- Never proceed to the next build phase with failing tests in the current phase (Section 28, throughout)
- Never claim the 60%+ target has been met without the minimum validation sample, acceptable calibration, reasonable coverage, and significant baseline outperformance all holding simultaneously (Section 26)

## Appendix C — Consolidated Prediction Record Schema Reference

A flat reference of every field on the `predictions` table, grouped as in Section 3.3, for quick lookup without re-reading the full prose contract. This is a reference, not a substitute for Section 3.3 — read there for *why* each field exists.

**Identity & lifecycle:** `id`, `fixture_id`, `market`, `prediction`, `lifecycle_stage`, `gate_tier`, `horizon_eligible`, `embargo_active`, `generated_at`, `superseded_at`, `model_version`

**Probability & calibration:** `raw_probability`, `calibrated_probability`, `calibration_model_version`, `calibration_training_window`, `calibration_method`

**Confidence:** `confidence`, `confidence_tier`, `data_completeness`

**Ensemble:** `poisson_probability`, `elo_probability`, `ml_probability`, `other_model_probabilities`, `ensemble_mean`, `ensemble_variance`, `max_model_spread`, `model_agreement_score`

**Out-of-distribution:** `ood_score`, `feature_distance_score`, `unseen_team_flag`, `unseen_league_flag`, `major_squad_change_flag`

**Lifecycle stability:** `previous_probability`, `probability_delta`, `max_lifecycle_probability_shift`, `prediction_direction_changed`, `lifecycle_stability_score`

**Point-in-time audit:** `prediction_as_of`, `data_cutoff_at`, `latest_source_event_at`, `feature_version`

**Market selection, odds, value:** `ranking_score`, `is_strongest_market_for_fixture`, `bookmaker_implied_probability`, `de_vigged_market_probability`, `model_edge`, `closing_market_probability`, `closing_line_value`

**Recommendation status:** `is_best_bet`, `recommendation_status`, `no_prediction_reason`

**Explanation:** `explanation`, `explanation_factors`

**Audit:** `features_used` (the complete feature snapshot, including Model A's full scoreline matrix)
