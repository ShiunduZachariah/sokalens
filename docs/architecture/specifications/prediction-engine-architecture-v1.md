# Prediction Engine — Core Architecture & Build Specification

**Scope of this document:** the prediction engine only. Not the Go backend, not the worker, not the frontend, not infrastructure. This assumes fixtures, statistics, odds, standings, injuries, and lineups already exist in PostgreSQL, populated by an upstream ingestion worker. The engine's job starts at "read from the database" and ends at "write predictions back to the database."

**Audience:** an autonomous coding agent (Codex) executing this as a build plan. Every phase has a definition of done and a testing gate. Do not proceed to the next phase until the current phase's tests pass.

**Language:** Python 3.11+. Recommended libraries: `numpy`, `scipy`, `pandas`, `psycopg2` or `asyncpg`, `pydantic` (for config/schema validation), `pytest` for testing.

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

---

## 1. What "Core" Means Here — System Boundary

```
                    ╔═══════════════════════════════════════╗
                    ║         PREDICTION ENGINE (THIS DOC)    ║
                    ║                                         ║
  PostgreSQL  ───►  ║  Read → Feature build → Gate → Models  ║  ───►  PostgreSQL
  (read-only         ║  → Ensemble → Confidence → Rank        ║         (write
   to engine)        ║  → Explain → Settle (post-match)       ║          predictions,
                    ║                                         ║          results,
                    ╚═══════════════════════════════════════╝          accuracy)
```

**In scope:**
- Reading raw match/team/league data from PostgreSQL
- Computing derived features
- Deciding whether a fixture is predictable at all (the gate)
- Running statistical models per market group
- Merging model outputs into final probabilities
- Scoring confidence independently of probability
- Refining predictions as later-arriving data (lineups, late injury news) becomes available, across a defined pre-match lifecycle
- Executing fixture predictions concurrently and independently, so high-volume kickoff windows do not bottleneck
- Ranking and exposing predictions as a continuously available pool, sliceable by date range and confidence tier — not a single capped daily list
- Building accumulator (acca) selections on demand from the existing prediction pool, given user-supplied constraints
- Generating natural-language explanations
- Settling predictions against actual results after matches finish
- Maintaining the engine's own accuracy ledger

**Explicitly out of scope (mentioned only where the boundary needs to be drawn):**
- Fetching data from API-Football (that's the ingestion worker's job)
- Serving HTTP requests to a frontend (that's the Go backend's job)
- Database schema definition beyond the tables the engine itself owns (Section 3 defines only what the engine reads and writes)
- Scheduling / cron orchestration beyond stating when the engine should be invoked (Section 5A and the Concurrency & Execution Model section)
- Acting as a bookmaker: the acca engine (Section 13) selects and combines legs from the engine's own modeled probabilities and exposes a fair (model-derived) price; it does not source, hold, or guarantee real bookmaker odds. If real bookmaker odds are reliably ingested for a fixture/market (the `odds` table from Section 3.1), the acca engine may display them alongside the fair price as a reference, but the combined acca price it computes is always derived from the engine's own probabilities unless explicitly told otherwise — this keeps the acca price internally consistent with every other number the engine produces

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

**Instruction to the build agent:** implement Tier A fully in Phase 3, Tier B in Phase 4, Tier C in Phase 5, Tier D in Phase 6. Do not skip tiers. Do not merge them into one phase — they have different data reliability profiles and need separate test suites.

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
- **league_coverage** — league_id, season_id, has_statistics, has_xg, has_injuries, has_odds, has_lineups (this mirrors API-Football's own per-league `coverage` flags — the engine must check this before trusting any field from a given league, since lower-tier leagues frequently return null for advanced stats)
- **api_predictions** (optional but recommended) — fixture_id, predicted_winner, win_or_draw, under_over, percent_home, percent_draw, percent_away, advice — API-Football's own native `/predictions` endpoint output, stored as a cross-validation signal (see Section 9.3)

### 3.2 What the engine must NOT assume

- That every field is populated. Null-handling is mandatory everywhere, not optional defensive code. A league with `has_xg = false` must never have xg-dependent logic silently fall through to a default value that looks like real data — it must explicitly degrade (see Section 5.3).
- That `team_season_stats` has a full season of history early in a season. Early-season fixtures (first 5-6 gameweeks) have structurally thinner data and the gate must treat them more cautiously regardless of league tier.
- That referee data exists for the assigned referee. Many fixtures will have `referee_stats = null`. The set-piece model must have a defined fallback (league-average card/foul rate) rather than crashing or skipping the whole fixture.
- That a fixture within the 7-day horizon is automatically eligible for prediction. A fixture may be within the window but blocked by the interleaved-match embargo (Section 5C). The engine must check both conditions before entering prediction work.

### 3.3 Tables the engine writes (write contract)

These are the tables the engine owns and writes to. The ingestion worker must never write to these tables, and the Go backend must treat all of them as read-only. Schema details below are fields the engine is responsible for populating — not the full table DDL, which is the infrastructure layer's concern.

**predictions** — the engine's primary output table. One row per fixture × market × lifecycle stage version. Required fields the engine must write on every insert:
- `id` (UUID, engine-generated)
- `fixture_id` (foreign key to fixtures)
- `market` (e.g. "1X2", "over_2.5", "corners_over_9.5")
- `prediction` (e.g. "Home", "Over", "Yes")
- `probability` (float 0.01–0.99, never null)
- `confidence` (float 0.00–1.00, independently computed, never null)
- `confidence_tier` ("strong", "medium", "low", or "none")
- `is_best_bet` (boolean — set by Section 11's pool logic, not by the model layer)
- `lifecycle_stage` ("EARLY", "PRE_MATCH", or "LINEUP_CONFIRMED" — which stage produced this version)
- `data_completeness` (float 0.0–1.0, the Section 5.3 score at time of prediction)
- `features_used` (structured JSON snapshot of the complete feature vector from Section 7 — must be the actual values used, not a reference or pointer)
- `explanation` (text, generated by Section 11.4)
- `generated_at` (timestamp — when this version was produced)
- `superseded_at` (timestamp, nullable — null means this is the current version; set when a later lifecycle stage produces a replacement, per 5A.3)
- `gate_tier` ("SKIP", "BASIC", or "FULL" — the fixture-level gate outcome that governed this prediction)
- `horizon_eligible` (boolean — whether this fixture was within the 7-day rolling window at the time of prediction, per Section 5C)
- `embargo_active` (boolean — whether any interleaved-match embargo was in effect for a team in this fixture at the time of prediction, per Section 5C)
- `model_version` (string — version identifier of the prediction engine code that produced this row, set from a configured constant updated with each deployment)

**prediction_results** — written by the settlement process (Section 12) after each fixture completes. One row per prediction row that was the LOCKED-stage version for its fixture. Required fields: `prediction_id`, `fixture_id`, `market`, `predicted`, `actual`, `is_correct`, `probability`, `confidence`, `confidence_tier`, `lifecycle_stage_at_settlement`, `league_id`, `settled_at`.

**accuracy_snapshots** — written by the accuracy aggregation process (Section 12.3) on its scheduled cadence, not on every individual settlement. One row per snapshot_date × scope. Required fields: `snapshot_date`, `scope` (e.g. "overall", "market:1X2", "league:39", "tier:strong"), `scope_id`, `total_predictions`, `correct`, `accuracy`, `roi_simulated`, `computed_at`.

**acca_selections** — written by the acca engine (Section 13) when it builds an accumulator selection. One row per acca. Required fields: `id` (UUID), `requested_at`, `legs_requested` (integer), `target_odds` (float, nullable — the user's combined odds target, if any), `markets_filter` (JSON array of market strings the user requested), `start_datetime`, `end_datetime`, `legs` (JSON array of {fixture_id, market, prediction, probability, confidence, fair_odds}), `combined_fair_odds` (float), `combined_fair_probability` (float), `leg_count` (integer), `any_leg_embargoed` (boolean — true if any leg had an active embargo at selection time, which must be shown as a caveat), `generated_at`.

**fixture_prediction_horizon** — written by the engine's pre-run eligibility check (Section 5C) to record, for each fixture, its horizon status and embargo status at the time of each engine sweep. Required fields: `fixture_id`, `checked_at`, `kickoff_at`, `days_to_kickoff`, `horizon_eligible` (boolean), `embargo_reason` (text, nullable — populated when `horizon_eligible` is false due to embargo, explaining which interleaving match is blocking prediction and why), `embargo_blocking_fixture_id` (integer, nullable — the specific midweek-fixture ID that is blocking this weekend fixture).

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
- Per market-group, the blend ratio between the statistical model and the form-adjustment model (e.g., 1X2: 55/45, Total Goals: 70/30 — exact defaults defined in Section 8.4)

**Form calculation**
- Decay rate per game (default 0.85)
- Lookback window sizes (default last 5 and last 10 matches, both computed)

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
- Maximum legs sourced from the same fixture in a single acca (default 2 — same joint-probability reason as the per-fixture cap in best-bet presentation; see Section 13.4)

**Worker pool and execution**
- Maximum concurrent fixture-processing workers (default: number of available CPUs minus 2, reserving headroom for PostgreSQL and the Go backend; minimum 1; set explicitly, never derived dynamically at runtime)
- Lifecycle stage priority ordering weights for the worker pool queue (5B.4)

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

The base gate above governs whether a fixture gets predictions at all. But Tier B and Tier C markets need an **additional, stricter check on top of fixture-level gating**, because a fixture can have perfectly good goal-history data while having patchy or absent corner/card/shot data (this happens constantly in lower-tier leagues). This second-level check happens at Section 9 (per-market eligibility), not here — the fixture-level gate in this section only ever blocks Tier A. A fixture passing FULL tier here is a necessary but not sufficient condition for, say, a corners market to be shown.

---

## 5A. The Incremental Prediction Lifecycle (Late-Arriving Data)

This section exists because of a specific, real constraint: confirmed lineups typically don't arrive until roughly an hour before kickoff, injury news can change in the hours before a match, and a prediction engine that only ever runs once per fixture, far in advance, is either stale by kickoff or has to ignore signal that genuinely changes the picture (a key striker confirmed out, a backup goalkeeper starting). The fix is not "wait until the last possible moment to predict" (that breaks every downstream use case that needs predictions hours or days ahead — best-bet listings, weekend preview pages, acca building) and it is not "predict once and never touch it again" (that throws away real signal). The fix is treating every fixture's prediction as something that passes through defined stages, each producing a complete, fully valid, separately confidence-scored prediction set — never a partial or broken one.

### 5A.1 Lifecycle stages (named windows relative to kickoff)

- **Stage EARLY (kickoff minus several days down to roughly kickoff minus 6 hours)** — the first stage at which a fixture can produce a FULL-tier prediction, once it's passed the Section 5 gate. Lineups are never available here. Confidence scoring (Section 10.1) naturally scores the lineup-confirmation factor at zero for every prediction produced in this stage, which is expected and correct, not a defect.
- **Stage PRE-MATCH (roughly kickoff minus 6 hours down to roughly kickoff minus 90 minutes)** — injury/availability data (`player_availability`) is most likely to have firmed up by this point even though lineups themselves haven't been announced. A re-run here picks up any injury news that's emerged since the EARLY stage and reflects it in the injury-impact features (Section 6.1, Group 5) and therefore in the model outputs and confidence.
- **Stage LINEUP-CONFIRMED (triggered by data arrival, not a fixed time offset — typically lands somewhere around kickoff minus 60 to 75 minutes for competitions that publish lineups early, but the trigger is the data itself, never a clock)** — this is the highest-confidence stage achievable pre-match. Once `lineups_confirmed` flips true in the upstream data for a fixture, that fixture becomes eligible for a lineup-aware re-run regardless of what wall-clock time it happens to be. The lineup-confirmation confidence factor (10.1) now scores full points where it previously scored zero, and if the confirmed lineup shows an unexpected absence not already reflected in `player_availability` (a key player simply left out of the squad without a formally logged injury), the injury-impact feature must be recomputed using the actual named starting XI versus each team's established regular-starter baseline, not just the formally logged injuries table — a quietly rested star striker affects the match exactly as much as a publicly announced injury does, and the engine must treat it the same way.
- **Stage LOCKED (kickoff itself onward)** — no further pre-match re-runs are issued for this fixture. The last LINEUP-CONFIRMED (or, if lineups never arrived for this competition/league, the last PRE-MATCH) prediction set is what stands and is what gets settled post-match per Section 12. This stage exists so that "the prediction" has an unambiguous, final, citable version once the match has actually started — there is no value in re-predicting a match that's already kicked off, and doing so would also violate Design Principle 6 (the engine predicts outcomes, it does not chase a moving target mid-event, which is explicitly out of scope per Section 1's exclusion of in-play markets).

### 5A.2 What "re-running" actually means

A re-run for a fixture already in EARLY or PRE-MATCH stage is not a patch, a diff, or a partial update applied to the previous prediction set. It is the entire pipeline (gate re-evaluation, Section 6's full feature re-assembly, Section 8's full model run, Section 10's full confidence scoring, Section 11's re-ranking) executed again from scratch against the database's current state, consistent with Design Principle 6 (pure function of current state). The only thing that changes between two runs for the same fixture is that the database now contains more or better information than it did before. This matters for a concrete reason: if re-runs were partial patches, subtle bugs could leave a prediction set in an inconsistent state (e.g., the 1X2 market reflecting confirmed lineups while the BTTS market for the same fixture still reflects the pre-lineup estimate). A full re-run guarantees every market in a fixture's prediction set is always internally consistent with the same snapshot of knowledge.

### 5A.3 Versioning requirement

Every prediction row (Section 3's schema, written by the engine) must carry a `lifecycle_stage` value (one of EARLY, PRE_MATCH, LINEUP_CONFIRMED) and a `generated_at` timestamp. When a re-run produces a new prediction set for a fixture, the new rows do not overwrite the old ones in place — they are inserted as a new version, and the previous version is marked superseded (a `superseded_at` timestamp set, row retained, never deleted). Two things depend on this: first, the accuracy ledger (Section 12) needs to be able to state, honestly, which lifecycle stage's prediction is the one being graded for accuracy (this document's default rule: **the LOCKED-stage prediction — i.e., the last version produced before kickoff — is the one settlement always grades**, since that's the most-informed prediction the engine ever produced and grading an early, deliberately-less-confident version would understate the value of the lineup-aware refinement); second, any API surface built on top of this engine (out of scope for this document, but the contract must support it) can choose to show "what we said this morning" versus "what we say now" if that's ever a desired product feature, because the history is genuinely preserved rather than overwritten.

### 5A.4 Confidence must reflect lifecycle stage honestly, not just data completeness in the abstract

Section 10.1 already scores a lineup-confirmation factor, and Section 5.3's data completeness score already includes a lineup-confirmation sub-weight. This section adds one explicit rule on top: a prediction produced in EARLY stage must never be displayed or treated identically to a prediction produced in LINEUP_CONFIRMED stage for the same fixture, even if every other feature happens to be identical, because the two predictions carry genuinely different epistemic weight. This is already mechanically true given how the scoring works (the lineup factor differs), but it's stated here explicitly because it's the kind of distinction a build agent could accidentally erode over time (for instance, by caching an EARLY-stage confidence score and reusing it across stages as a shortcut) — that must never happen.

### 5A.5 What does NOT trigger a re-run

To avoid re-running fixtures needlessly (which directly feeds into the concurrency/load concerns addressed next), a re-run is only ever triggered by one of: the stage-boundary schedule itself (Section 15 defines the invocation pattern), or an explicit data-arrival event for that specific fixture (lineups newly confirmed, an injury status newly logged). Cosmetic or unrelated database writes (e.g., another fixture's data being ingested, a standings refresh for a different matchday) must never cascade into unnecessary re-runs of fixtures whose own relevant data hasn't changed. This is an orchestration-layer responsibility to enforce (the engine itself, per Design Principle 6, simply does the right thing whenever it's invoked) but it's stated here so the build agent designing the invocation triggers understands why over-triggering is a real cost, not just an inefficiency — see 5B.4 below for the resource impact specifically.

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
| Accumulators (Section 13) | User-specified start/end within the 7-day window | Embargoed fixtures are excluded from acca leg candidates entirely — an acca must never include a leg whose prediction was built on incomplete information relative to what will be available before the match |

---

## 6. Feature Engineering

Every model in Section 8 consumes the same feature vector — there is one feature-building pass per fixture, not one per market. This avoids redundant computation and, more importantly, keeps every model looking at a consistent picture of the match.

### 6.1 Feature groups and their exact contents

**Group 1 — Recent form (computed separately for last-5 and last-10 windows, for each team)**
- Points-per-game, exponentially decay-weighted (most recent match weighted highest, weight = decay_rate^(games_ago))
- Goals scored average, decay-weighted
- Goals conceded average, decay-weighted
- Expected-goals-for average, decay-weighted, falling back to actual goals scored when xG is null for that league (and flagging that fallback occurred — see 6.4)
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
- Injury impact score per team (0.0–1.0; weight missing key players — defined as players who started 60%+ of available matches this season — more heavily than fringe squad players)
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
- Bookmaker implied probabilities for 1X2, over/under 2.5, BTTS where odds data exists for this fixture
- API-Football's own native prediction percentages, if the `api_predictions` table has a row for this fixture (used purely as a cross-validation signal in confidence scoring — never as a direct input to the engine's own probability output, to avoid the engine just parroting someone else's model)

**Group 9 — Data quality meta-features**
- The full data completeness score from Section 5.3
- Per-group completeness sub-scores (e.g., "Group 6 completeness" specifically, since a fixture might have excellent goals data but poor corners data) — these sub-scores are what drive the Tier B/C per-market gating in Section 9

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

These flags feed two places: the confidence scorer (each fallback used reduces confidence) and the explanation generator (the explanation should be honest about caveats — see Section 11.4).

### 6.5 Derby/rivalry list maintenance

This is a maintained reference list, not a computed feature — rivalries are geographic/historical facts, not statistical patterns. The build agent should create this as a simple configuration table (league_id, team_id_a, team_id_b, rivalry_name) seeded with well-known rivalries for the leagues in initial scope, with a documented process for adding more as league coverage expands. Do not attempt to infer rivalries from data (e.g., "teams that play extra-aggressively against each other") — that's a fragile proxy for what is actually a known, listable fact.

---

## 7. Feature Vector Assembly — Output Contract

The output of Section 6 is a single structured object per fixture containing every feature above, fully computed, with all fallback flags set. This object is what gets passed into both the gate (Section 5, though gate completeness is actually computed first and is a prerequisite input to this same object) and every model in Section 8. Treat this as a strict schema — every model declares which fields it consumes, and the assembly step must guarantee all declared fields are present (using explicit defaults/nulls where genuinely absent, never omitting the key).

This is also the object that gets persisted (as a snapshot, e.g. structured JSON) alongside each prediction, so that any historical prediction can be fully reconstructed and audited later — this is what "features_used" means operationally, and it is non-negotiable for the accuracy-tracking and future ML phases.

**What is persisted as `features_used` is broader than just this section's input vector.** It is the input feature vector from Section 6 *plus* any model-internal computed artifact that a later section of this document explicitly declares as a required output. Section 6's feature vector is the assembly step's own output; a model in Section 8 may compute additional structured artifacts during its own run (not inputs, but intermediate or full computational results) that must also be captured at persistence time if any downstream component depends on reading them back later. Do not treat "features_used" as scoped only to Section 6 — treat it as the single, complete, append-as-needed audit record for everything that went into and came out of a fixture's prediction run. Section 8.1 specifies one concrete required artifact of this kind; any future model added to this engine that produces an intermediate structure another component depends on must declare that requirement the same explicit way.

---

## 8. The Model Layer

Three independent statistical models run per fixture, each responsible for a different family of markets, followed by an ensemble step that blends overlapping outputs.

### 8.1 Model A — Poisson / Dixon-Coles Goals Model (drives all of Tier A)

**Purpose:** produce a full probability matrix over scorelines (home goals × away goals, 0 through the configured max, default 8 each side), from which every Tier A market is derived.

**Method:**
1. Compute attack strength for each team: their goals-scored rate (home or away split as appropriate) divided by the league's average goals-scored rate for that venue context.
2. Compute defence strength for each team: their goals-conceded rate divided by the league's average goals-conceded rate for that venue context.
3. Compute the expected goals (lambda) for each team as: their attack strength × the opponent's defence strength × the league's average goals rate for that venue context.
4. Apply situational adjustments to the lambdas: reduce home lambda proportionally to home injury impact, reduce away lambda proportionally to away injury impact, apply a smaller reduction for fixture congestion on each side, apply a small variance widening (not a lambda shift, a matrix-shape adjustment) when match importance is high to reflect upset risk.
5. Build the full scoreline probability matrix using independent Poisson distributions for home and away goals.
6. Apply the Dixon-Coles correction to the low-scoring cells specifically (0-0, 1-0, 0-1, 1-1), which corrects a well-documented bias where naive independent Poisson underestimates draws and tight low-scoring outcomes. Use the configured rho parameter.
7. Normalize the full matrix so all cells sum to exactly 1.0.

**Markets derived from this single matrix (all of Tier A, computed by summing the relevant cells):**
- 1X2: sum below diagonal (home win), diagonal (draw), above diagonal (away win)
- Double chance, draw no bet: arithmetic combinations of the above
- Over/under at every configured goal line: sum cells where home+away goals crosses the threshold
- BTTS yes/no: 1 minus (row 0 sum + column 0 sum − cell [0,0]), and its complement
- Correct score: the matrix cells directly, sorted descending, top-N exposed (never all 81 cells — see 9.5 on correct score handling specifically)
- Half-time/full-time, highest scoring half: requires a secondary, smaller Poisson model for half-time goals specifically (compute half-time lambdas using the same attack/defence method but against historical half-time-goals rates rather than full-time rates) — this is a parallel calculation, not derivable from the full-time matrix alone, since the two halves aren't independent in a way that lets you just split the full-time lambda in half
- Team total goals (per-team O/U): marginal distribution of each team's goals from the matrix (row sums for home, column sums for away)
- Win to nil: home win cells where away column is 0; away win cells where home row is 0
- Odd/even total goals: sum cells by total-goals parity
- Clean sheet: row 0 sum (away clean sheet... note the direction carefully) and column 0 sum (home clean sheet)

**Mandatory output requirement — the matrix itself, not just its derived markets, must be preserved:** the final, normalized, Dixon-Coles-corrected scoreline matrix produced by step 7 above is a required output of Model A, in addition to every derived market probability listed above. This full matrix (all cells, not just the top-N correct-score cells shown to users) must be included in the `features_used` JSON snapshot persisted with the prediction (per Section 7's rule that model-internal artifacts declared as required by a downstream section must be captured at persistence time). This is not optional and not merely useful for debugging: the acca engine's correlation handling (Section 13.4) depends on being able to read this exact matrix back from storage to correctly compute joint probabilities for multi-leg accas drawn from the same fixture. If the matrix is discarded after market derivation and only the final probabilities are kept, Section 13.4's method cannot function and there is no way to reconstruct the matrix later that would be guaranteed consistent with the original prediction (re-running the model against current data would use whatever data exists at reconstruction time, not the data that was actually used when the original prediction was made — which is exactly the historical-consistency problem Section 7's audit-record principle exists to prevent).

### 8.2 Model B — Form Adjustment Model (modifier, not a standalone model)

**Purpose:** this model does not produce independent probabilities. It produces *adjustment deltas* that shift Model A's output toward what recent form suggests, capturing momentum/trend signal that a season-aggregate Poisson rate alone would miss (a team's season average might be solid, but if they've lost their last 4 matches, that matters).

**Method:**
1. Compute a normalized "current strength" score for each team from the decay-weighted recent-form points-per-game (Section 6.1, Group 1), scaled to a 0–1 range.
2. Compute the strength differential (home current strength − away current strength).
3. Map this differential to bounded probability shift amounts for 1X2 outcomes — the magnitude of the shift must be capped (a hard ceiling on how much form alone can move the final probability, configured, default no single market shift exceeding roughly 15-16 percentage points) so that a short hot/cold streak can't overwhelm a full season of underlying data.
4. Compute separate, smaller adjustment deltas for BTTS and Over/Under 2.5 based on the form-rates from Section 6.1 (the BTTS rate and Over-2.5 rate already computed as part of form features) compared against the baseline Model A would otherwise imply.
5. These deltas are consumed exclusively by the ensemble step (8.4) — Model B has no other output path and is never shown or stored as a standalone prediction.

### 8.3 Model C — Set-Piece & Discipline Model (drives Tier B, structurally separate from goals modeling)

**Purpose:** corners, cards, fouls, offsides are not goals — they follow different distributions, different team-style drivers (a possession-heavy team generates more corners regardless of how many goals they score), and need their own rate-based Poisson treatment rather than being derived from the goals matrix.

**Method (run once per statistic family — corners, cards, fouls, offsides — using the same structural pattern each time):**
1. Compute each team's for/against rate for the statistic, split home/away, from season averages.
2. Compute the combined expected total as the sum of (home team's for-rate + away team's against-rate-adjusted-component), normalized against the league average for that statistic — mirroring the attack/defence strength logic from Model A but applied to the new statistic instead of goals.
3. Apply the referee modifier specifically for the cards and fouls families (not corners or offsides, which aren't referee-driven the way discipline stats are): multiply the combined expected total by the ratio of this referee's average cards-or-fouls-per-match to the league average. Use the league-average fallback when referee-specific data is absent, as established in 6.4.
4. Build a single Poisson distribution (not a 2D matrix — corners/cards/fouls are typically modeled as a single combined-total rate, since correlating exact home/away splits for set-pieces has much weaker statistical grounding than it does for goals) using the resulting lambda.
5. Derive over/under probabilities at each configured line directly from this Poisson distribution's cumulative probabilities.
6. For per-team splits (Team Corners O/U, Team Cards O/U, etc.), build a second, separate Poisson distribution using only that team's for-rate/against-rate-adjusted lambda rather than attempting to split the combined-total distribution proportionally (proportional splitting of a Poisson sum does not preserve correct marginal probabilities — model the team total directly).

**Special handling for rare-event markets (Red Card In Match):** standard Poisson modeling is statistically shaky for genuinely rare events with small counts (most matches have zero red cards). Use the team and league red-card base rates directly as a probability (not lambda-then-derive), and treat this market's confidence ceiling as structurally capped lower than other markets regardless of how good the surrounding data is — flag this explicitly in the per-market eligibility rules (Section 9.4).

### 8.4 The Ensemble Step — Merging Model A, B, and C Outputs

This step exists because Model A and Model B both produce signal for the same Tier A markets, and they need to be combined into one final number per market, not shown as two competing predictions.

**Method, per market:**
1. Look up the configured blend ratio for this specific market from Section 4.1 (e.g., 1X2 defaults to 55% weight on Model A's raw output, 45% weight on the form-adjusted version; Total Goals defaults to a heavier 70/30 lean toward Model A, since season-long goal-scoring rates are more stable than short-term form for total-goals specifically; these exact defaults should be treated as a documented starting point, not fixed forever — they are exactly the kind of thing Phase 12's backtest validation (Section 14) should refine).
2. Compute the blended probability as: (Model A weight × Model A's raw probability) + (Model B weight × (Model A's raw probability + Model B's adjustment delta for this market)).
3. For 1X2 specifically, after blending all three outcomes independently, renormalize the three so they sum to exactly 1.0 (independent blending can introduce small rounding drift that must be corrected before storage).
4. Tier B markets (corners, cards, fouls) pass through from Model C directly with no blending — Model B's form-adjustment logic does not apply to set-piece markets in this version, since the relationship between "recent form" (points/results-based) and "recent corner-taking behavior" is a separate, weaker signal that would need its own dedicated study before being safely blended in; treat this as a deliberate v1 simplification, not an oversight.
5. Tier C markets (shots, possession) similarly pass through from a shots/possession-specific extension of Model C's pattern (same Poisson-rate method, applied to shots-on-target and possession statistics) with no form blending in v1.
6. Every output probability, after blending and renormalization, must be clipped to a configured safe range (default 0.01 to 0.99) — never output an absolute 0% or 100%, which would be both statistically indefensible from a finite historical sample and a product-trust problem if ever shown to a user.

---

## 9. Per-Market Eligibility — The Second Gate

Section 5's gate decides whether a fixture gets predictions at all. This section governs which *specific markets within that fixture* are actually eligible to be shown, given that data reliability varies by market tier even within a single fixture that otherwise passed the fixture-level gate.

### 9.1 Eligibility check sequence (run once per candidate market, after the fixture has already passed the Section 5 gate)

1. Confirm `league_coverage` explicitly supports the statistic family this market depends on (e.g., a Tier B corners market requires `league_coverage.has_statistics = true` for this league-season — do not infer support from "the field happened to be non-null for one fixture," since that can be a data entry fluke rather than systematic coverage).
2. Confirm the market-tier-specific data completeness sub-score (from Section 6.1 Group 9's per-group completeness) meets the elevated floor for that tier (base floor + the tier's configured strictness addition from Section 4.1).
3. Confirm the underlying team/league sample size for this specific statistic family is sufficient — a league might have excellent goals history but have only just started reporting corner statistics this season, which a pure "is the field non-null" check would miss but a "how many matches of corner data exist" check catches.
4. If any check fails, this specific market is excluded from this fixture's output — the fixture's gate tier (BASIC/FULL) and other markets are unaffected.

### 9.2 Why this two-layer gate structure matters

Without this distinction, you get one of two bad outcomes: either the engine is overly conservative and skips entire fixtures because corner data happens to be thin (even though the goals predictions would have been perfectly sound), or it's overly permissive and shows a confident-looking corners prediction built on three matches of patchy data just because the fixture overall looked fine. Two layers solve both failure modes at once.

### 9.3 Cross-validation against API-Football's native predictions and bookmaker odds

Where the `api_predictions` table has a row for this fixture, and where odds data exists, both are used exclusively as confidence-scoring inputs (Section 10), never as direct probability inputs into Model A/B/C. The reasoning: if the engine's own models are allowed to directly average in someone else's model's output, the engine stops being its own model and starts being a wrapper around a competitor's predictions — which is both a product differentiation problem and makes the engine's stated explanations (Section 11) dishonest, since the explanation would claim "based on form and historical data" while actually partially reflecting an opaque third-party model. Cross-validation strictly means: "does our independently-computed probability roughly agree with this external reference," which is a confidence signal, not a probability input.

### 9.4 Special eligibility rule for rare-event and structurally-low-confidence markets

Certain markets (Red Card In Match, exact Correct Score, Win To Nil for a heavy underdog) have a hard confidence ceiling configured regardless of how complete the underlying data is — meaning even with perfect data completeness, these markets should never be promoted to "Strong" confidence tier, because the underlying statistical uncertainty is irreducible at the sample sizes football provides. This is a configuration flag per market (`max_confidence_tier`), not a hack — it's an honest acknowledgment that some markets are inherently noisier than others, and the engine should never claim false precision on them. Apply this explicitly to: Correct Score (cap at Medium), Red Card In Match (cap at Low), any individual Over/Under line more than two standard deviations from the league mean for that statistic (cap at Medium — e.g., a corners line of 14.5 in a league averaging 9 corners per match is a long-tail bet regardless of how good the data otherwise looks).

### 9.5 Correct Score handling — specific rule

Never expose the full scoreline matrix as "predictions." Always expose only the top-N highest-probability scorelines (configured, default top 8), and always attach a caveat in the explanation noting that correct score is inherently the hardest market to call accurately (this should also be reflected honestly later in the accuracy-tracking dashboard — Correct Score's historical hit rate will be visibly, structurally lower than 1X2's, and that's expected and should be shown as such, not hidden).

---

## 10. Confidence Scoring

This is the component that answers "how much should this probability be trusted," fully independent of the probability value itself.

### 10.1 Confidence factors and their weights (sum to 100, default weights — tunable per Section 4)

- **Data completeness (25 points)** — the fixture-level score from Section 5.3, scaled directly
- **Sample size sufficiency (15 points)** — scaled from the minimum of home/away team match counts, capped at a configured ceiling (default: full points at 15+ matches, linear scaling below that)
- **Model agreement (20 points)** — how closely Model A's raw output and the form-adjusted (Model B-influenced) output agree for this specific market; high agreement means both signals point the same direction, which is itself informative — if season-long rates and recent form sharply disagree, that's genuine uncertainty about which signal to trust, and confidence should reflect that disagreement, not paper over it
- **Head-to-head consistency (10 points)** — only scored when sufficient H2H history exists (4+ meetings per 6.1); measures whether the H2H pattern aligns with what the blended model output suggests; zero-weighted (not partially weighted) when H2H is insufficient, consistent with the flag set in 6.4
- **Market cross-validation alignment (15 points)** — only scored when odds data and/or API-Football native predictions exist for this fixture and market; measures how closely the engine's independently-computed probability sits relative to these external references; this is the only place external predictions/odds enter the confidence calculation at all, per 9.3
- **Lineup confirmation (10 points)** — full points if lineups are confirmed for this fixture at the time of scoring, zero otherwise; this is naturally time-dependent (a fixture's EARLY-stage prediction will legitimately score lower here than its later LINEUP_CONFIRMED-stage re-run, per the lifecycle defined in Section 5A)
- **Extended H2H sample bonus (5 points)** — small bonus when 6+ H2H meetings exist, on top of the base H2H consistency score, rewarding genuinely deep historical matchup data

### 10.2 Per-market-tier confidence ceiling enforcement

After computing the raw weighted confidence score, apply the `max_confidence_tier` cap from Section 9.4 if this market has one configured. This is a hard ceiling applied after scoring, not a weight adjustment during scoring — the distinction matters because it keeps the underlying score honest (you can see what the score "would have been") while still preventing the system from ever claiming Strong confidence on a market that's structurally too noisy to deserve it.

### 10.3 Confidence tier assignment

Apply the thresholds from Section 4.1 directly to the final (post-cap) score: Strong at or above the configured Strong floor, Medium at or above the Medium floor, Low at or above the Low floor, otherwise no recommendation for this market on this fixture (the prediction can still be computed and stored for transparency/tracking purposes — see Section 12 — but is never surfaced as a recommendation and never eligible for best-bet selection).

### 10.4 Why confidence and probability must never be combined into one displayed number

A 58% probability at Strong confidence and a 58% probability at Low confidence are genuinely different pieces of information, and collapsing them (for example, into some single "score out of 100" that blends both) destroys exactly the distinction that makes this engine more trustworthy than a naive one. Both numbers must always be computed, stored, and exposed separately. This is restated here because it's the single most common implementation shortcut a build agent might be tempted to take under time pressure, and it directly undermines Design Principle 1.

---

## 11. Recommendation Ranking & The Best Bet Pool

### 11.1 Architectural shift from this version: no daily cap, no single daily list

There is no hardcoded maximum number of best bets per day, per week, per weekend, or per any other time period. The engine's goal is to produce as many high-quality, confidence-backed recommendations as the data supports across the entire 7-day horizon window (Section 5C), covering as many leagues, markets, and fixtures as possible. Every prediction that clears the confidence floor (Section 4's configured minimum tier — default: Medium and above) is a best bet. The pool is as large or as small as the day's fixtures and the data behind them honestly support.

The previous design (cap of 10 per day) was a display-oriented constraint, not a quality constraint. That constraint now belongs entirely in the downstream API and frontend layer, not in the engine. The engine produces the complete pool of valid recommendations; downstream services slice it however they need (by day, by confidence tier, by league, by market, by date range) for whatever product surface they're serving. This means a Saturday with 40 fixtures across 10 leagues, all with strong data, can legitimately produce 60 or 80 best-bet recommendations across multiple markets and the engine neither truncates that pool nor treats it as a sign something is wrong. A quiet Tuesday midweek with 8 fixtures and thin data might produce 4 recommendations. Both are correct outputs.

### 11.2 Composite ranking score — how predictions are ordered within the pool

Every prediction that has cleared the per-market eligibility gate (Section 9) and been confidence-scored (Section 10) gets a composite ranking score that governs where it sits within any slice of the pool. This score is used for ordering within a response, not for filtering — filtering is entirely by confidence tier (Section 4.1) and by any date/league/market parameters in the specific slice request.

Compute the ranking score as a weighted sum of:
- Probability value — the model's estimated likelihood of the predicted outcome occurring
- Confidence score — the independently computed confidence level (Section 10.1), post-cap
- Data completeness — the Section 5.3 score, reflecting how much of the ideal input data was available
- Market-specific historical accuracy — the engine's own track record on this market from the accuracy ledger (Section 12); defaults to a neutral baseline early in the engine's life, as configured in Section 4.1; this weight grows more informative over time as the ledger accumulates data
- League-specific historical accuracy — same principle applied per league
- Edge versus market odds — a small additional weight when the engine's probability meaningfully exceeds the bookmaker-implied probability for the same market on the same fixture, suggesting the model has found something the market has not; only scored when odds data is available (Section 6.1 Group 8), zero otherwise

All weights are in Section 4.1's ranking weights group. The ranking score is stored on the prediction row alongside the prediction itself, so that sliced responses can be sorted purely by reading stored values without recomputing rankings on every API call.

### 11.3 The best-bet pool — structure and what it contains

The best-bet pool is the complete set of predictions in the database that meet all of the following conditions simultaneously:
- `superseded_at` is null (this is the current, active version of the prediction for this fixture × market combination)
- `confidence_tier` is "strong" or "medium" (configurable minimum tier from Section 4.1)
- `probability` is at or above the configured probability floor (default 0.55)
- `gate_tier` is "FULL" (only FULL-tier fixtures are best-bet eligible, per Section 5.1)
- `horizon_eligible` is true (fixture was within the 7-day window when predicted)
- `embargo_active` is false (no interleaved-match embargo was active at prediction time)
- `kickoff_at` is in the future (past matches are not best bets, they are settled outcomes)
- Market is not in the configured list of best-bet-ineligible markets (the engine never promotes Correct Score to best-bet status regardless of confidence tier, per 9.4; this exclusion is configuration-driven, not hardcoded)

This pool is continuously maintained: when a new prediction is produced (any lifecycle stage), it is immediately inserted and the previous version for that fixture × market superseded. When a fixture kicks off, its predictions transition out of the active best-bet pool automatically (via the `kickoff_at` filter above — they remain in the database for settlement and accuracy tracking). There is no batch job that "refreshes the daily best bets" — the pool is always current.

### 11.4 Slice patterns — how the pool is queried for specific product surfaces

The engine exposes the pool via the `predictions` table with sufficient fields to support all of the following query patterns without any additional engine logic. These patterns are described here so the engine's write contract (Section 3.3) ensures the right fields are always populated to support them:

**Today's best bets:** filter pool by `kickoff_at >= today midnight UTC AND kickoff_at < tomorrow midnight UTC`, sort by ranking score descending. Result may contain any number of predictions across all leagues and markets.

**Tomorrow's best bets:** same pattern, shifted one day forward.

**Weekend best bets:** filter by `kickoff_at >= nearest Saturday midnight UTC AND kickoff_at < Monday midnight UTC`. This is the highest-volume response since most European leagues concentrate fixtures on Saturday and Sunday. On a typical weekend this may yield hundreds of predictions across all markets.

**Saturday picks / Sunday picks:** single-day filter within the weekend range.

**This week's best bets:** filter by `kickoff_at >= today midnight UTC AND kickoff_at < (today + 7 days) midnight UTC` — the full horizon window.

**By league:** add `AND league_id = ?` to any of the above patterns.

**By market:** add `AND market = ?` to any of the above patterns (e.g., all today's Over 2.5 goals picks across all leagues).

**By confidence tier only:** filter by `confidence_tier = 'strong'` for users who only want the engine's most confident picks, regardless of date.

The engine does not enforce or care which of these patterns is used — it populates the pool and the downstream service queries it. The only engine responsibility is ensuring every field needed by every filter above is indexed appropriately in PostgreSQL.

### 11.5 Per-fixture presentation cap — display variety only, not a pool filter

The configured maximum-per-fixture cap from Section 4.1 (default 2) applies only when the downstream service is assembling a display response — it is NOT a filter applied to what enters the pool. A fixture with 6 valid best-bet predictions across 6 different markets has all 6 in the pool. When a downstream service assembles a "today's best bets" display response, it may optionally apply the per-fixture cap to ensure no single match dominates the displayed list. This is a presentation concern, not a quality concern, and it must be implemented in the API layer, not by the engine omitting valid predictions from the pool.

### 11.6 General (non-best-bet) predictions

Every prediction that cleared the per-market eligibility gate (Section 9) but does not meet the best-bet threshold — because confidence is Low or below, or because the market is structurally ineligible for best-bet status — is still stored and exposed as a general prediction, with its full probability and confidence tier visible. The distinction between "best bet" and "general prediction" is solely the `confidence_tier` and the `is_best_bet` boolean flag on the prediction row; every other field is identical. General predictions are used by the "match preview" and "all markets" views in the product, which show the complete picture for a fixture regardless of how confident each individual market's prediction is.

### 11.7 Explanation generation — method, not decoration

Every prediction (best bet or general) must carry a natural-language explanation generated directly from the specific feature values that drove that prediction — not a generic templated sentence with team names swapped in.

**Method:**
1. For each market, define which feature groups are the primary drivers (e.g., for Over/Under goals: recent form scoring rates, H2H average goals, combined xG; for Corners: team corner averages, possession tendency; for 1X2: form differential, standings rank gap, H2H record, injury impact if active).
2. At generation time, pull the actual computed values for those specific drivers from this fixture's feature vector (Section 7) — not placeholder language.
3. Select the 2–3 strongest contributing factors (the ones with the most extreme or notable values relative to league baseline) and phrase them as the explanation using actual numbers: "averages 1.8 goals per home match," "7 of their last 10 combined meetings exceeded 2.5 goals" — not vague qualifiers like "often" or "frequently."
4. **Mandatory caveat inclusion:** if any fallback flags were set during feature assembly (Section 6.4) for the factors actually used in this explanation, the explanation must say so plainly. If the market carries a `max_confidence_tier` cap (Section 9.4), note that this market is inherently harder to call precisely. If an interleaved-match embargo was recently lifted for this fixture, note that the prediction incorporates the result of the intervening midweek match.
5. The explanation generator never claims certainty. Banned phrasing: "guaranteed," "certain," "sure thing," "can't lose." Required framing: probabilistic language throughout — "the data suggests," "is favored to," "the model estimates."

---

## 12. Result Settlement & Accuracy Ledger

This is what turns the engine from a one-shot predictor into a system that gets measurably better and more trustworthy over time, and it must be built with the same rigor as the prediction logic itself — an accuracy system that's wrong or inconsistent is worse than no accuracy system at all, because it actively misleads.

### 12.1 Settlement trigger

Once a fixture's status updates to full-time (sourced from the same `fixtures` table the engine reads, populated by upstream ingestion — this is a read of a status the engine itself does not write), settlement runs for that fixture.

### 12.2 Settlement method, per stored prediction for that fixture

1. Compute the actual outcome for this specific market from the fixture's final statistics (e.g., for 1X2: compare final goal counts; for Over/Under 2.5: sum final goals against the threshold; for BTTS: check both teams' final goal counts are non-zero; for Corners Over/Under: compare final corner statistics against the threshold).
2. Some markets may be unsettleable even after full-time if the underlying statistic was never populated for this fixture (e.g., a lower-tier league fixture where corner statistics never arrived even after the match concluded) — in this case, mark the prediction as "unsettled — data unavailable," never silently drop it and never guess.
3. Compare the predicted outcome against the actual outcome and record a binary correct/incorrect flag.
4. Store this result permanently, linked back to the original prediction (including its confidence tier and the full feature snapshot from Section 7), never overwritten or deleted.

### 12.3 Accuracy aggregation (the ledger the rest of the system, and the ranking formula in 11.1, depends on)

Maintain running accuracy figures, recomputed on a schedule (not on every single settlement, to avoid redundant computation) at minimum across these dimensions: overall accuracy, accuracy per market, accuracy per market per confidence tier (this is the single most important breakdown — it directly validates or invalidates whether the confidence scoring system in Section 10 is actually meaningful; if Strong-confidence predictions aren't meaningfully outperforming Low-confidence ones over a large enough sample, that's a signal the confidence model itself needs revisiting, not just the prediction models), accuracy per league, and accuracy trend over recent time windows (e.g., last 30 days vs all-time, to surface whether the engine is currently in good or poor form itself).

### 12.4 Feeding back into ranking

The market-level and league-level accuracy figures computed here are exactly what Section 11.1's ranking formula consumes. This creates a deliberate feedback loop: markets and leagues where the engine has a proven track record get a ranking boost over time, while markets/leagues with a poor track record get gently deprioritized in the best-bet selection, without ever being fully excluded (general predictions remain unaffected by this — it only influences best-bet ranking).

---

## 13. Accumulator (Acca) Engine

The acca engine is a selection and combination layer that operates entirely on top of the existing prediction pool (Section 11.3). It does not run its own models, it does not re-score confidence, and it does not modify any existing prediction. Its sole job is: given user-supplied constraints, find the best set of prediction legs from the current pool that together form a coherent accumulator, compute a combined fair price for that selection, generate an explanation, and write the result to the `acca_selections` table (Section 3.3).

### 13.1 User inputs — the acca engine's interface contract

The acca engine accepts the following inputs. All are optional except the date/time range — at minimum, a start and end point must be provided for the acca engine to have a defined fixture universe to select from:

- **Number of legs** — integer, the desired number of match-market selections in the acca. If not provided, the engine selects up to the configured maximum (Section 4.1). If provided but fewer qualifying legs exist in the requested window, the engine returns as many legs as are available and clearly flags that the requested leg count could not be met.
- **Target combined odds** — float, the user's target minimum combined odds for the accumulator (e.g., 5.00 meaning the user wants a selection that, at fair price, is at least 5-to-1). This constrains which legs are eligible: legs with fair odds below a threshold implied by the target, if the target is very high, will be excluded in favour of more speculative picks. If not provided, the engine optimises for the highest combined confidence-weighted score rather than for a specific odds target.
- **Markets filter** — list of market strings. If provided, only predictions in those markets are eligible as legs (e.g., a user who only wants Over/Under goals picks in their acca specifies `["over_1.5", "over_2.5", "over_3.5", "btts_yes"]`). If not provided, all best-bet-eligible markets are considered.
- **Start datetime** — the earliest kickoff time any leg in the acca may have (UTC). Defaults to the current time if not provided, since legs for matches already kicked off are ineligible.
- **End datetime** — the latest kickoff time any leg may have (UTC). Must be within the 7-day horizon window (Section 5C.1). If a provided end datetime extends beyond the current horizon window, the engine trims it to the window boundary and flags this in the response.

### 13.2 Leg eligibility — which predictions may enter an acca

A prediction in the pool is eligible to be an acca leg if all of the following are true:
- `is_best_bet = true` (this is not lowered for accas — the acca engine builds on the engine's strongest recommendations, never on general predictions)
- `embargo_active = false` (Section 5C.8 — embargoed predictions are excluded from accas explicitly, because an acca's combined odds are meaningless if one or more legs is based on pre-embargo data that may not reflect the midweek result)
- `kickoff_at` is after the current time (the match must not yet have kicked off)
- `kickoff_at` is within the user's requested `[start_datetime, end_datetime]` range
- The prediction's market is in the user's requested markets list (or the filter is absent and all markets are eligible)
- The current lifecycle stage of the prediction is the most recent non-superseded version (verified via `superseded_at IS NULL`)

### 13.3 Leg selection algorithm — step by step

1. **Query the eligible pool** — apply all conditions from 13.2 against the `predictions` table. The result is a candidate set of individual predictions.

2. **Group by fixture** — the candidate set will contain multiple predictions from the same fixtures (e.g., a single Arsenal vs Chelsea match might contribute a 1X2 pick, a corners pick, and a BTTS pick). These must be handled as a group for correlation reasons (see 13.4).

3. **Apply per-fixture leg cap** — from each fixture, select at most the configured maximum number of legs (Section 4.1, default 2). When more than the maximum are eligible from a single fixture, rank them by composite ranking score (Section 11.2) and take the top N. This cap is a statistical necessity, not an arbitrary limit: beyond 2 legs from the same fixture, the correlated outcomes of the same match begin to dominate the acca's combined probability in a way that is not accurately reflected by independent multiplication — the math changes qualitatively, not just quantitatively.

4. **Compute fair odds per leg** — for each eligible prediction, compute the fair (model-derived) decimal odds as 1 ÷ probability. Round to 2 decimal places for display. If real bookmaker odds are available for the same fixture and market in the `odds` table, store these alongside but do not replace the fair odds with them — the acca price must be computed from the engine's own probabilities for internal consistency.

5. **Check target odds feasibility** — if the user provided a target combined odds value, determine whether the candidate set can plausibly meet it. The combined fair odds of any N-leg selection is the product of each leg's fair odds, adjusted for within-fixture correlation (see 13.4). If even the highest-odds combination of N legs from the candidate set falls below the target, the engine must respond honestly: return the highest-confidence N-leg selection available, clearly state the achievable combined odds, and flag that the requested odds target could not be met with available predictions rather than degrading to lower-confidence legs to hit the target.

6. **Build the final leg set** — if no specific leg count is requested, select the set of N legs (up to the configured maximum, subject to fixture caps) that maximises the composite ranking score summed across legs, subject to the target odds constraint if present. If a specific leg count is requested, select exactly N legs using the same criterion, or as many as exist if fewer than N qualify.

7. **Compute combined price** — compute the combined fair probability as described in 13.4. Derive combined fair odds as 1 ÷ combined probability.

8. **Generate acca explanation** — write a brief plain-language summary referencing each leg's specific reasoning (drawing from each leg's individual `explanation` field), the overall confidence profile of the selection (average confidence tier across legs, number of Strong vs Medium legs), and any caveats (embargo-recently-lifted legs, any fallback flags on individual legs).

9. **Write to `acca_selections`** — persist the full acca as a single row per Section 3.3's write contract, including all legs, the combined price, and all flags.

### 13.4 Correlation handling — why naive probability multiplication is wrong for multi-leg accas

This is the most statistically important rule in the acca engine and the one most likely to be misimplemented without explicit documentation.

**For legs from different fixtures:** treating them as independent is a reasonable approximation. The 1X2 outcome of Arsenal vs Chelsea is not meaningfully correlated with the corners count in Real Madrid vs Barcelona, and the independence assumption holds well enough that multiplying their individual probabilities together to get the joint probability is correct.

**For two legs from the same fixture:** independence does not hold. A prediction of "Home Win" and a prediction of "Under 2.5 Goals" from the same match are not independent events — the scorelines where the home team wins 1-0, 2-0, 2-1 contribute to both, while a 3-2 home win contributes to the first but not the second. Naive multiplication of P(Home Win) × P(Under 2.5) would understate the correlation and produce an incorrect combined probability.

**The correct method for within-fixture multi-leg combinations:** this situation is exactly what Tier D (Section 2.4) and the score matrix from Model A (Section 8.1) already solve. When two legs from the same fixture appear together in an acca, compute their joint probability by summing the relevant cells of the scoreline probability matrix directly — the same matrix used to derive all Tier A market probabilities. This matrix is already computed and stored (as part of `features_used` in the JSON snapshot on each prediction row — the matrix must be stored there precisely to support this case). Read the matrix from the stored features of either prediction row for that fixture, then sum the cells corresponding to the intersection of the two predicted outcomes. Use this joint probability instead of the product of the individual probabilities for that fixture-pair's contribution to the combined acca price.

**For set-piece multi-leg combinations from the same fixture** (e.g., corners over AND cards over from the same match): there is no cross-market matrix equivalent to the goals score matrix for these markets in the current engine. Treat these as approximately independent with a documented caveat that the independence assumption is weaker here than for goals-derived markets, and that the displayed combined odds may slightly understate or overstate the true joint probability. Do not silently compute them as independent without noting this caveat.

**The practical consequence:** the acca engine must, before building the combined price, first group legs by fixture and handle each fixture-group's joint probability via matrix cell summation (for goals-based legs) or documented approximation (for set-piece legs), then multiply across fixture-groups (which are genuinely independent of each other) to produce the final combined fair probability.

### 13.5 Named acca products and the date ranges they use

These map to the product surfaces the engine must support. The acca engine itself has no awareness of product names — it simply executes against a start/end datetime range. The product names below describe how the downstream API should frame its requests to the acca engine:

| Acca product name | Start datetime | End datetime | Notes |
|---|---|---|---|
| Today's acca | Today midnight UTC | Tomorrow midnight UTC | Includes all today's fixtures |
| Tomorrow's acca | Tomorrow midnight UTC | Day-after-tomorrow midnight UTC | Useful for planning |
| Weekend acca | Nearest Saturday midnight UTC | Monday midnight UTC | The canonical weekly product |
| Saturday acca | Nearest Saturday midnight UTC | Nearest Saturday 23:59 UTC | Saturday fixtures only |
| Sunday acca | Nearest Sunday midnight UTC | Nearest Sunday 23:59 UTC | Sunday fixtures only |
| Midweek acca | Monday midnight UTC | Saturday midnight UTC | Tuesday/Wednesday/Thursday fixtures |
| Full-week acca | Today midnight UTC | 7 days from today midnight UTC | Entire horizon window |
| Custom acca | User-supplied | User-supplied | Must be within 7-day horizon; trimmed if outside |

### 13.6 What the acca engine must never do

- Never include a leg whose `embargo_active` was true at the time of selection, even if the embargo has since lifted but the prediction has not been re-run incorporating the interleaving match's result
- Never compute combined odds by naive probability multiplication for legs from the same fixture (see 13.4)
- Never lower the leg eligibility standard (`is_best_bet = true` minimum) to hit a user's target combined odds — return an honest, shorter or lower-odds selection instead
- Never include legs from fixtures outside the 7-day horizon window (Section 5C.1)
- Never present a combined odds figure without noting it is a fair/model-derived price unless the downstream API explicitly chooses to display real bookmaker odds alongside it

---

## 14. Phased Build Plan With Testing Gates

Each phase below has a definition of done and a mandatory testing gate. Do not begin the next phase until the current phase's tests pass against representative real data (not just synthetic/mocked data — synthetic data alone cannot catch the null-handling and coverage-flag edge cases this document repeatedly emphasizes).

### Phase 0 — Data Contract Verification

**Build:** nothing yet. Connect to the existing PostgreSQL instance and verify every table/field listed in Section 3.1 actually exists and is populated for a representative sample of fixtures across at least 3 different leagues of varying tier (one major league, one mid-tier league, one minor league) and at least 50 fixtures total.

**Testing gate:**
- Confirm field presence and approximate null-rate per league tier (expect higher null rates in minor leagues — document the actual percentages observed, since these become the basis for tuning Section 4.1's strictness multipliers later)
- Confirm `league_coverage` flags (or equivalent) exist and are trustworthy — spot-check at least 5 fixtures where a coverage flag says a stat is unavailable, and confirm the underlying field is indeed null/absent for those fixtures
- If any required table/field genuinely does not exist yet, stop and report this before writing any engine code — the engine cannot be built against a contract that isn't real

### Phase 1 — Configuration & Validation Layer

**Build:** Section 4 in full — every configuration group, with sensible defaults as specified throughout this document, plus the startup validation logic from 4.2.

**Testing gate:**
- Unit tests confirming validation correctly rejects: blend ratios that don't sum to 1.0, non-descending confidence thresholds, negative weights, decay rates outside (0,1)
- Unit tests confirming validation passes with the documented defaults
- Confirm the engine genuinely refuses to start (not just logs a warning) on invalid configuration

### Phase 2 — Feature Engineering Layer

**Build:** Section 6 in full, plus the assembly/output contract from Section 7. This is the largest single phase — build and test each feature group (6.1 through 6.7) independently before integrating them into the full assembly step.

**Testing gate, per feature group:**
- Test against fixtures with complete data (every group should compute cleanly with realistic, sensible-looking values — spot check several by hand against the raw underlying stats)
- Test against fixtures with deliberately sparse data (a newly-promoted team, an early-season fixture, a minor league fixture with null xG) — confirm fallback logic triggers correctly and the corresponding flag (6.4) is set
- Test the league-average baseline computation (6.2) separately — confirm it's computed once per league-season per engine run, not recomputed per-fixture (this should be verifiable by checking it's cached/computed in a single pass, not by counting database calls, which is an implementation detail — the test should assert on the actual baseline values being identical across multiple fixtures in the same league-season within one run)
- Test match importance scoring (6.3) against a few known hand-verified scenarios (e.g., construct a synthetic standings table where you know by hand that team X is mathematically still in title contention, and confirm the score reflects that)
- **Integration test:** assemble a full feature vector for at least 20 real fixtures spanning the 3 league tiers from Phase 0, and manually review the complete output for each — this is the single most valuable test in the entire build, since every downstream component depends entirely on this layer being correct

### Phase 2A — Lifecycle Stages, Versioning & Concurrency

**Build:** Sections 5A and 5B in full. This phase is placed immediately after feature engineering and before the gate because every subsequent phase must run as a concurrent, versioned, lifecycle-aware system from the start — retrofitting concurrency onto a serial system consistently produces subtle shared-state bugs that are hard to detect in later phases.

**Testing gate:**
- Unit test lifecycle stage assignment (5A.1): construct four synthetic fixtures at precisely EARLY, PRE_MATCH, LINEUP_CONFIRMED, and LOCKED time offsets. Confirm the engine assigns the correct stage to each, derived from current time and database data state, never from hardcoded time arithmetic
- Unit test versioning (5A.2 and 5A.3): run the engine twice for the same fixture with two different database states (simulating pre-lineup and post-lineup). Confirm the second run inserts a new prediction row with `superseded_at = null` and sets `superseded_at` on the first run's rows. Confirm no in-place overwrites occur anywhere in the predictions table
- Unit test the lineup-from-actual-XI path (5A.1, LINEUP_CONFIRMED stage): construct a fixture where a regular starter is absent from the confirmed lineup but not listed in `player_availability`. Confirm injury impact features are recomputed from the actual named XI
- Concurrency test: submit 20 fixtures simultaneously to the bounded worker pool (5B.3), simulating a Saturday 15:00 cluster. Confirm all 20 complete successfully, none block or wait on each other, wall-clock time is meaningfully less than 20× single-fixture time, and no prediction data from one fixture appears on any other fixture's rows
- Priority queue test: queue a mix of EARLY-stage and LINEUP_CONFIRMED-stage fixtures. Confirm LINEUP_CONFIRMED fixtures are consistently dequeued and processed first per 5B.4
- Idempotency test: invoke the engine for the same fixture twice in rapid succession. Confirm exactly two prediction version rows exist in the database after both complete, the later one is current (superseded_at null), the earlier one is marked superseded, and no corrupted partial state is visible at any point during or after both runs
- Failure isolation test: inject a deliberate error in one fixture's feature assembly (e.g., a null value where a non-null is expected). Submit that fixture alongside 5 valid fixtures in the same worker pool batch. Confirm the 5 valid fixtures produce complete predictions, the failing fixture produces a logged error with its fixture ID, and the batch does not halt or corrupt the valid results

### Phase 3 — Match Quality Gate

**Build:** Section 5 in full. Implement step 0 of 5.2 (horizon and embargo pre-check) as a stub that always passes — the full implementation comes in Phase 10A after the pool and ranking system exist to test against.

**Testing gate:**
- Construct test fixtures that should hit each of steps 1 through 5 in 5.2 and confirm each correctly produces SKIP/BASIC/FULL as expected, in the correct evaluation order — a fixture failing step 2 must never reach step 4 (confirm short-circuit, not just final-outcome)
- Confirm the data completeness score (5.3) computes consistently and sub-weights sum to exactly 1.0
- Run the gate against all real Phase 0 fixtures and review the SKIP/BASIC/FULL distribution — expect major-league fixtures to overwhelmingly produce FULL, minor-league fixtures to produce meaningfully more BASIC/SKIP. If major-league fixtures are frequently getting SKIP, the bug is in Phase 2's feature engineering, not the gate — debug backward

### Phase 4 — Model A (Poisson/Dixon-Coles) — Tier A Markets

**Build:** Section 8.1 in full, plus the market-derivation logic for every Tier A market listed in Section 2.1.

**Testing gate:**
- Unit test the raw Poisson matrix construction against hand-computed expected values for a simple, controlled lambda pair (e.g., lambda_home=1.5, lambda_away=1.2 — compute by hand what P(1-1) should be, confirm the code matches)
- Unit test the Dixon-Coles correction specifically — confirm it only affects the four specified low-score cells and confirm the matrix still sums to 1.0 after correction and normalization
- Unit test every single Tier A market derivation against the same fixed test matrix — for a known matrix, manually compute what 1X2, BTTS, each O/U line, etc. should equal, and confirm the code's derivation matches exactly. This must cover every market in Section 2.1's table, not a sample of them
- Run Model A against the real Phase 0 fixtures (post-feature-engineering, post-gate) and spot-check outputs for plausibility (e.g., a historically dominant home team against a struggling away team should show a sensible home-win-favored distribution, not something perverse)
- Specifically test the half-time/full-time market's parallel half-time lambda calculation (8.1) since this is structurally different from the rest of Tier A — confirm it's genuinely using half-time-specific historical rates, not just halving the full-time lambda

### Phase 5 — Model B (Form Adjustment) and Ensemble Integration

**Build:** Section 8.2 and Section 8.4.

**Testing gate:**
- Unit test the strength-differential-to-probability-shift mapping (8.2) against hand-computed examples, confirming the configured cap is genuinely enforced (construct an extreme synthetic case — one team in red-hot form, one in a terrible slump — and confirm the resulting shift does not exceed the configured ceiling)
- Unit test the ensemble blend (8.4) against fixed inputs: given a known Model A output and a known Model B delta, confirm the blended result matches hand-calculated expectations for the configured blend ratio
- Unit test renormalization specifically — construct a case where independent blending would produce three 1X2 probabilities summing to something like 1.003, confirm renormalization corrects this to exactly 1.0
- Unit test the output clipping (8.4 step 6) — construct an extreme synthetic input designed to push a probability toward 0 or 1, confirm it's clipped to the configured safe range
- Run the full Model A + B + ensemble pipeline against real fixtures and compare against the Model-A-only outputs from Phase 4 for the same fixtures — confirm the form adjustment is having a sensible, bounded effect, not wildly swinging predictions

### Phase 6 — Model C (Set-Piece & Discipline) — Tier B Markets

**Build:** Section 8.3 in full, plus the Tier B per-market eligibility logic from Section 9.1-9.2.

**Testing gate:**
- Unit test the combined-total Poisson construction for corners specifically against hand-computed values, same method as Phase 4's matrix testing
- Unit test the referee modifier specifically — construct a synthetic referee with a known above-average card rate, confirm the resulting lambda is correctly scaled up, and confirm the league-average fallback produces a neutral (1.0×) modifier when referee data is absent
- Unit test the per-team split distributions (built as separate distributions per 8.3 step 6, not proportionally split) — confirm a team-specific corners O/U line produces sensible, independently-computed probabilities, not a naive fraction of the combined total
- Specifically test the rare-event handling for Red Card In Match (8.3's special handling paragraph) — confirm it does not use the standard Poisson-then-derive method and confirm its confidence ceiling cap is enforced regardless of how complete the surrounding data looks
- Test the Tier B eligibility gate (9.1-9.2) against fixtures with strong goals data but deliberately thin corner-history — confirm the fixture's overall gate tier (BASIC/FULL from Phase 3) is unaffected while the corners market specifically gets excluded
- Run Model C against real fixtures from a major league (expect mostly-eligible Tier B markets) and a minor league (expect a meaningfully higher exclusion rate) and manually review for plausibility

### Phase 7 — Tier C Markets (Shots, Possession)

**Build:** the shots/possession extension of Model C's pattern referenced in Section 8.4 step 5, plus Tier C eligibility (the elevated strictness floor from 4.1 and 9.1).

**Testing gate:**
- Same structural tests as Phase 6 (hand-computed Poisson verification, eligibility gate behavior) applied to shots-on-target and possession statistics specifically
- Confirm Tier C's elevated eligibility floor genuinely excludes more fixtures/markets than Tier B's floor did for the same underlying fixtures — if Tier C and Tier B produce near-identical exclusion rates, the strictness multiplier configuration likely isn't actually differentiated and needs revisiting

### Phase 8 — Tier D (Combination Markets)

**Build:** Section 2.4's combination markets, computed as joint probabilities from already-computed Tier A/B independent outputs.

**Testing gate:**
- Unit test joint probability computation against hand-computed examples (e.g., given a known P(Away Win) and a known P(Under 2.5), confirm the combination market's joint probability matches expectations, accounting for the fact these aren't fully independent events and the computation must derive the joint correctly from the underlying matrix/distributions rather than naively multiplying two marginal probabilities together, which would be statistically incorrect for correlated outcomes like score and total goals)
- Confirm combination markets correctly inherit the stricter of their two component markets' eligibility status (a 1X2-and-Corners combo market should never be eligible if the Corners component alone failed Tier B eligibility for this fixture)

### Phase 9 — Confidence Scoring

**Build:** Section 10 in full.

**Testing gate:**
- Unit test each of the 7 confidence factors (10.1) independently against constructed scenarios with known expected sub-scores
- Unit test the `max_confidence_tier` cap enforcement (10.2, 9.4) — construct a scenario with otherwise-perfect data completeness for a capped market (e.g., Correct Score) and confirm the final tier is still capped at Medium despite a high raw score
- Unit test tier assignment (10.3) against the configured thresholds, confirming boundary behavior exactly at threshold values (e.g., a score of exactly 0.75 should land in Strong if the floor is 0.75-inclusive — confirm this matches the documented intent and is consistent, since off-by-one boundary bugs here directly affect product trust)
- Run confidence scoring against the full real-fixture test set accumulated from Phases 4-8 and review the distribution of confidence tiers — confirm Strong-tier predictions are visibly rarer than Medium, which should be rarer than Low (this is the expected shape if the scoring is well-calibrated; an inverted or flat distribution suggests a weighting bug)

### Phase 10 — Ranking, Best Bet Pool, and Explanation Generation

**Build:** Section 11 in full — the unbounded confidence-thresholded prediction pool, composite ranking score, `is_best_bet` flag assignment, per-fixture presentation cap (stored on the prediction row but not enforced by the engine as a pool filter), and explanation generation.

**Testing gate:**
- Unit test the composite ranking formula (11.2) against hand-computed examples using known probability, confidence, accuracy, and edge-vs-odds inputs
- Confirm the best-bet pool population logic (11.3): construct a set of predictions spanning multiple confidence tiers, gate tiers, and lifecycle versions (current and superseded). Confirm the pool contains exactly the predictions that meet all six conditions listed in 11.3 — not one more, not one fewer. Specifically: confirm superseded predictions are not in the pool, confirm BASIC-tier fixture predictions are not in the pool, confirm embargoed-at-prediction-time predictions are not in the pool even if the embargo has since lifted
- Confirm there is NO daily cap or total cap applied anywhere in the engine's pool logic: construct a hypothetical day with 80 qualifying predictions across 40 fixtures and confirm all 80 appear in the pool with no truncation
- Confirm the `is_best_bet` flag is set correctly on every prediction row, accurately reflecting pool membership, and that this flag is computed and stored at write time, not dynamically evaluated on every read
- Test each of the six slice filter patterns from 11.4 against a constructed pool spanning multiple days and leagues — confirm each filter produces exactly the right subset of the pool
- Test explanation generation (11.7) against at least 10 real fixtures across different markets and leagues, manually reviewing each generated explanation for: factual accuracy against the actual feature values used, presence of mandatory caveats where any fallback flags were set, presence of the recently-lifted-embargo caveat where applicable, absence of banned certainty language
- Explanation testing is necessarily more manual and qualitative than earlier phases — budget real review time here, do not treat it as a quick pass

### Phase 10A — Prediction Horizon Window & Interleaved-Match Embargo

**Build:** Section 5C in full, replacing the stub from Phase 3 with the complete implementation. This phase is placed after Phase 10 (the pool and ranking system are working) because the embargo logic needs to correctly mark predictions in the pool and the `fixture_prediction_horizon` write path (Section 3.3) needs the pool infrastructure to already exist so that embargo-lift re-triggers can be confirmed to produce correct pool updates.

**Testing gate:**
- Unit test the 7-day window boundary logic (5C.1): construct fixtures exactly at the window boundary (kickoff at T+7d 23:59, T+7d 00:00, and T+8d 00:00 UTC) and confirm only the first two are within the window and the third is not
- Unit test embargo detection (5C.3): construct a team schedule with a Wednesday 20:00 match and a Saturday 15:00 match (gap: ~67 hours, below the 72-hour threshold); confirm the Saturday fixture is embargoed and `embargo_blocking_fixture_id` points to the Wednesday fixture. Construct a Tuesday 20:00 and Saturday 15:00 (gap: ~91 hours, above threshold); confirm no embargo
- Unit test the embargo lift trigger (5C.5): simulate the Wednesday match reaching FT status with statistics ingested, and confirm the Saturday fixture's embargo status flips to inactive and the fixture is queued for a re-run
- Edge case — postponed interleaving match (5C.7): construct a scenario where the blocking Wednesday match is postponed after initially being scheduled. Confirm the embargo on Saturday's fixture lifts immediately when the postponement is confirmed, with appropriate logging
- Edge case — both teams have interleaving matches: confirm embargo applies and lifts correctly based on the last of the two interleaving matches to complete, not the first
- Edge case — three matches within the window (5C.7): confirm the third fixture is embargoed pending the second, independently of whether the first has completed
- Confirm the `fixture_prediction_horizon` table is correctly written for every fixture evaluated, whether eligible, in-window-but-embargoed, or outside the window, with all required fields populated
- Run the complete pipeline (Phase 0 through Phase 10A) against a real historical weekend that included a midweek programme — manually confirm that the Saturday/Sunday predictions, when run in simulation after the midweek matches complete, incorporate those midweek results in their form features

### Phase 11 — Result Settlement & Accuracy Ledger

**Build:** Section 12 in full.

**Testing gate:**
- Unit test settlement logic (12.2) per market against constructed fixture results with known correct answers, covering every market from the catalogue, not a sample
- Specifically test the "unsettleable — data unavailable" path (12.2 step 2) — construct a fixture where a Tier B statistic predicted pre-match never arrived post-match, confirm it's marked unsettled rather than dropped or guessed
- Confirm settlement always grades the LOCKED-stage prediction (the last non-superseded version produced before kickoff) and never grades an earlier EARLY or PRE_MATCH version
- Test accuracy aggregation (12.3) against a constructed set of settled predictions with known outcomes, confirming the computed accuracy percentages match hand calculation across every required dimension (overall, per-market, per-market-per-tier, per-league, time-windowed)
- Confirm accuracy snapshots are not recomputed on every individual settlement but only on the configured schedule — verify by checking that settling 50 predictions triggers a limited number of snapshot writes, not 50
- **Integration test against ground truth:** select a real historical window (a completed set of fixtures with known results), run the entire pipeline from Phase 2 through Phase 11 against that window as if today were the start of that period, and compare predictions against actual outcomes. The goal is confirming the pipeline runs end-to-end correctly and settlement math is sound — not validating prediction quality, which requires a much larger sample

### Phase 11A — Acca Engine

**Build:** Section 13 in full — inputs, leg eligibility, leg selection algorithm, correlation handling (13.4), combined fair price computation, explanation generation, and the `acca_selections` write path (Section 3.3).

**Testing gate:**
- Unit test leg eligibility filtering (13.2): construct a pool containing predictions that are: best-bet eligible, general-prediction-only, embargoed at prediction time, from outside the horizon window, superseded, and past kickoff. Confirm only the best-bet-eligible, embargo-free, current-version, future-kickoff predictions pass the filter.
- Unit test the per-fixture leg cap (13.3 step 3): construct a fixture with 5 eligible predictions across 5 markets. Confirm only the top 2 by ranking score are selected into the acca when the cap is 2.
- **Critical — correlation handling test (13.4):** construct a known scoreline probability matrix (use the same hand-computed test matrix from Phase 4). Manually compute the joint probability of "Home Win AND Under 2.5 Goals" by summing matrix cells directly. Invoke the acca engine with two legs from this fixture targeting those two markets. Confirm the engine's computed joint probability matches the hand-computed matrix-cell-sum result — NOT the naive product of the two individual probabilities. This is the single most important correctness test for the acca engine.
- Unit test the target-odds feasibility check (13.3 step 5): construct a pool where the highest achievable 3-leg combined odds is 4.2. Request a 3-leg acca with target combined odds of 6.0. Confirm the engine returns the best 3-leg selection available (at ~4.2 combined odds) and explicitly flags that the target could not be met, rather than degrading to lower-confidence legs or returning an error.
- Test each named acca product's date range computation (13.5) — specifically test that Saturday acca selects only Saturday fixtures, and weekend acca includes both Saturday and Sunday
- Test the `any_leg_embargoed` flag: construct an acca where one leg had an active embargo at prediction time (even if lifted since). Confirm the flag is set on the `acca_selections` row
- Confirm combined fair probability is computed correctly: for a 3-leg acca with two legs from different fixtures (independent) and two legs from the same fixture (joint probability via matrix), verify the final combined probability equals (joint probability of same-fixture legs) × (independent leg probability), not a naive triple product

### Phase 12 — Backtest Validation (Pre-Launch Gate)

**Build:** nothing new — this is a dedicated validation phase using everything built in Phases 1 through 11A.

**Testing gate:**
- Run the complete engine against the largest available historical window of real, completed fixtures (ideally a minimum of one full season per league tier covered, where data permits) as a backtest, generating predictions exactly as if running live, then settling every one against actual results
- Verify the core hypothesis of the confidence-tier system: **Strong-confidence predictions must show a measurably higher hit rate than Medium, which must show a measurably higher hit rate than Low, across a large enough sample to be statistically meaningful.** If this ordering does not hold, the confidence scoring weights (Section 10.1) need revisiting before this engine is considered ready — a confidence system that does not actually correlate with accuracy is actively misleading rather than merely unhelpful
- Verify per-market accuracy specifically for Tier C and rare-event/capped markets (9.4) — confirm their accuracy, while expected to be noisier, is not catastrophically below what the displayed probabilities would imply
- Verify the embargo system produces measurably better predictions for post-embargo runs than for embargo-bypassed (pre-interleaving-match) runs, where the historical data contains enough midweek-then-weekend fixture sequences to compare — this validates the core hypothesis behind Section 5C
- Run the acca engine against the historical pool produced by the backtest, generating historical accas for multiple date windows, and compare combined leg accuracy against what independent naive probability multiplication would have predicted — confirm the correlation-adjusted combined probability from 13.4 is more accurate than the naive product
- Document the actual observed accuracy figures from this backtest as the engine's baseline — this becomes the reference for all future tuning (blend weights, decay rates, embargo thresholds) and the reference against which the live engine's early performance is compared

---

## 15. Engine Invocation Schedule (Boundary Note Only)

This document covers the engine itself, not the orchestration system that calls it — but the engine's design assumes the following invocation pattern, built around Section 5A's lifecycle stages, Section 5B's concurrency model, and Section 5C's horizon/embargo rules:

- A **horizon sweep** runs on a regular schedule (at minimum, once per day; more frequently during heavy fixture periods), scanning all fixtures within the 7-day window for their current horizon eligibility and embargo status per Section 5C, writing results to `fixture_prediction_horizon`, and submitting eligible fixtures to the worker pool if they need an initial EARLY-stage prediction run.
- A **stage-boundary sweep** identifies fixtures that have newly crossed into PRE_MATCH stage and submits them for a re-run, picking up any injury news that has solidified since their EARLY-stage prediction.
- A **data-arrival trigger** fires per-fixture when lineup data is confirmed, when an embargo-blocking match reaches FT (triggering re-evaluation and queuing of the previously-embargoed fixture), or when any other relevant per-fixture data changes that would meaningfully update features — submitting just that fixture to the worker pool with appropriate priority per 5B.4.
- A **settlement trigger** fires per-fixture once that fixture's status reaches full-time, invoking Section 12's settlement logic against the LOCKED-stage prediction set.
- The engine itself does not need to know about this schedule internally. Each invocation is simply "evaluate this fixture against the current database state" — consistent with Design Principle 6 (pure function of current state) and Design Principle 9 (per-fixture independence). The orchestration system decides when and how many fixtures to submit to the worker pool; the engine's obligation is to be correct, concurrent, and idempotent every time it is called.

---

## Appendix A — Summary Table: Market Tier vs. Model vs. Gate Strictness

| Tier | Markets | Model | Gate strictness | Confidence ceiling |
|---|---|---|---|---|
| A | 1X2, double chance, O/U goals, BTTS, correct score, HT/FT, team totals, clean sheet, win to nil, odd/even | Model A (Poisson/DC) + Model B (form) blended | Base fixture-level gate only | Uncapped, except Correct Score (capped Medium) |
| B | Corners, cards, fouls, offsides, red card in match | Model C (set-piece/discipline) | Base + Tier B strictness addition, requires `has_statistics` coverage flag | Uncapped, except Red Card In Match (capped Low) and outlier lines (capped Medium) |
| C | Shots, shots on target, possession, pass accuracy, GK saves | Model C extension (shots/possession variant) | Base + Tier C strictness addition (higher than B) | Uncapped by default, revisit after Phase 12 backtest |
| D | All 1X2/goals/corners combination markets | Derived jointly from A/B/C outputs, no independent model | Inherits the stricter of its component markets' gates | Inherits the lower of its component markets' caps |

## Appendix B — Explicit List of What Must Never Happen (Pulled From Throughout This Document, Collected for Quick Reference)

**Data and modeling**
- Never collapse probability and confidence into one number (Section 10.4)
- Never silently fall back to a default that looks like real data without flagging it (Section 6.4)
- Never infer league coverage from one non-null field — always check the explicit coverage flag (Section 9.1)
- Never let Model B's adjustment exceed its configured cap (Section 8.2)
- Never let any output probability hit exactly 0 or 1 (Section 8.4)
- Never directly blend in odds or API-Football's native predictions as a probability input — cross-validation only (Section 9.3)
- Never expose the full correct-score matrix — top-N only (Section 9.5)
- Never treat a market's confidence ceiling cap as optional for markets explicitly flagged as structurally noisy (Section 9.4)
- Never discard Model A's full scoreline matrix after deriving market probabilities — it is a required part of `features_used` and the acca engine's correlation handling cannot function without reading it back (Section 8.1, Section 13.4)

**Prediction pool and best bets**
- Never apply a daily cap, weekly cap, or any other count ceiling to the best-bet pool (Section 11.1) — pool size is determined only by how many predictions clear the confidence floor
- Never re-evaluate pool membership at read time by recomputing the ranking score dynamically — compute and store it at write time
- Never include a prediction in the acca leg pool if its `embargo_active` was true at the time it was produced, even if the embargo has since lifted and the prediction has not been re-run with the updated data (Section 13.6)

**Horizon and embargo**
- Never produce predictions for fixtures outside the 7-day rolling window (Section 5C.1)
- Never begin gate evaluation or feature computation for a fixture that is within the window but under an active embargo (Section 5C.3) — defer, log the embargo, and wait for the lift trigger
- Never treat a postponed interleaving match as still blocking an embargo — a postponed match that will not now occur before the subsequent fixture lifts the embargo immediately (Section 5C.7)
- Never compute combined acca odds by naively multiplying individual leg probabilities when two or more legs come from the same fixture — use the joint probability from the scoreline matrix (Section 13.4)

**Concurrency and correctness**
- Never let one fixture's prediction work read, wait on, or block another fixture's prediction work (Section 5B.1 and Design Principle 9) — the only shared state permitted is the read-only pre-computed league-average baselines
- Never overwrite a prediction row in place — always insert a new version and mark the previous one superseded (Section 5A.3)
- Never settle a prediction against an earlier lifecycle stage version — always grade the LOCKED-stage prediction (Section 12.2 and 5A.3)

**Explanations and honesty**
- Never generate an explanation with templated language disconnected from actual feature values (Section 11.7)
- Never use banned certainty language in explanations: "guaranteed," "certain," "sure thing," "can't lose" (Section 11.7)
- Never drop an unsettleable prediction silently — mark it explicitly as unsettled with reason (Section 12.2)

**Build process**
- Never proceed to the next build phase with failing tests in the current phase (Section 14, throughout)
