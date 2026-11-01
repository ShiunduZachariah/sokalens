# In-play analytic model

Implements architecture v2 §7.1, §7.2, §7.3 and §7.6.

- `services/prediction-engine/prediction_engine/inplay_features.py`
- `services/prediction-engine/prediction_engine/inplay_model.py`
- Tests: `tests/prediction-engine/test_inplay_features.py`,
  `test_inplay_model.py`, `test_inplay_candidate_scope.py`

## What this is, and what it deliberately is not

This is the analytic model §7.1 asks for: Model A's Poisson/Dixon-Coles
machinery conditioned on match state. **Nothing here is trained.** Production
began writing `fixture_live_snapshots` on 2026-09-21; at the time of writing
the table holds a few hundred rows across four fixtures and `fixture_events`
holds fifteen. There is no dataset to fit against and no attempt is made to
pretend otherwise. Every constant in the model is a midpoint of a range the
architecture document states, not an estimate from data.

The whole model is:

```
f            = max(0, (90 - m) / 90)
lambda'_home = lambda_home * f * rho_red(r_h, r_a) * rho_state(g_h - g_a)
lambda'_away = lambda_away * f * rho_red(r_a, r_h) * rho_state(g_a - g_h)
```

remaining-goals matrix = Poisson x Poisson with the existing Dixon-Coles
correction (`rho = -0.08`), shifted by the current score.

`rho_intensity` is **not applied**. §15 decision 5 leaves it disabled until
snapshot data supports fitting it. `intensity_multiplier()` exists as the seam,
returns `1.0`, and raises if `INTENSITY_CALIBRATION_ENABLED` is ever flipped
without a calibration table behind it. A test asserts it is off.

## §7.3 launch markets — coverage

| Market | Status | Key | Notes |
|---|---|---|---|
| 1X2 | **Shipped** | `match_result_in_play` | 90-minute result from the score-shifted remaining matrix |
| Next goal | **Shipped** | `next_goal` | Competing-Poisson closed form; selections `home` / `away` / `none` |
| Over/under remaining goals | **Shipped** | `total_goals_remaining` | Lines 0.5–3.5 on goals still to come |
| Over/under match goals | **Shipped (bonus)** | `total_goals_match` | Same distribution shifted by the current score; lines already settled by the score are withheld, not published at 100% |
| BTTS from current state | **Shipped** | `btts_in_play` | Withheld as determined once both teams have scored |

Held back, exactly as §7.3 says: correct score, HT/FT, and every exotic. The
full final-score distribution *is* computed and returned as
`InPlayModelResult.final_score_distribution` for audit and for the other
markets to be derived from, but it is not published as a market — it needs
calibration data to be honest, and there is none.

## What the model cannot do yet

These are limitations of having no snapshot history, not bugs:

1. **`rho_intensity` is off.** Shot rate against pre-match expectation is the
   single biggest piece of in-play signal the model is not using. It stays off
   until there are enough snapshots to fit it (§15.5). Until then a match that
   is 0-0 but wildly one-sided reads identically to a quiet 0-0.
2. **No stoppage time.** `f = max(0, (90 - m) / 90)` is the document's formula
   and is implemented literally. At minute 90 it is zero, so every market
   collapses onto the current score and `publishable_markets()` withholds all
   of them. Real matches play 90+4. Correcting this needs measured added time
   per competition, which is a snapshot-history question.
3. **No extra time or penalties.** Every launch market settles on the
   90-minute result, which is already decided by the time a match reaches
   extra time. Those periods are suppressed with
   `EXTRA_TIME_NOT_MODELLED` rather than guessed at.
4. **The `rho_red` and `rho_state` constants are asserted, not measured.**
   0.275 / 0.125 and the 5–15% game-state band are midpoints of the ranges in
   §7.2. They are structurally right — early reds matter more than late ones,
   trailing teams push — but the magnitudes are unvalidated. They are
   configurable on `InPlayModelConfig` so a fit can replace them without a
   refactor.
5. **Dixon-Coles on a partial match is an approximation.** The correction was
   fitted on full-match low scores, not on the tail of one. Kept because §7.2
   asks for it and because uncorrected independent Poisson is known to be
   worse on 0-0 and 1-1.
6. **No in-play calibration or confidence tiering.** The existing
   `ConfidenceScorer` is built on pre-match feature completeness. In-play
   confidence needs settled in-play predictions to score against, which is
   Phase 5.

## Staleness (§14, §6.4)

A prediction on stale data is **suppressed, never extrapolated**. The model
does not project the match clock forward from wall time — §6.2's projected
minute is a *display* concern for the delivery layer. `run_in_play_model`
refuses to produce markets when the newest snapshot is 90 seconds or older
(`STALE_AFTER_SECONDS`, the §6.4 first tier) and returns
`SuppressionReason.STALE_DATA`.

A suppressed result still carries its audit envelope — `data_cutoff_at`,
`fixture_live_snapshot_id`, `match_minute`, `period`, `feature_version` — so
the caller can record *why* nothing was published.

## The two guards (§7.5) — extended, not deleted

Both pre-match guards are untouched:

- `WHERE f.kickoff_at >= %(as_of_timestamp)s` in
  `db_repository.py::_UPCOMING_FIXTURE_IDS_SQL` — still there, asserted by
  `test_inplay_candidate_scope.py`.
- `AND ep.generated_at <= f.kickoff_at` in
  `services/api/internal/{matches,predictions}/source.go` — not touched from
  here; its in-play read path belongs to the API surface.

Added alongside the first: `_IN_PLAY_FIXTURE_IDS_SQL`, reached only through an
explicit scope (`in_play` / `inplay` / `in-play`), with the mirrored condition
`f.kickoff_at < %(as_of_timestamp)s`, bounded by the 210-minute reaper hard
bound and requiring a snapshot within the freshness window. `live` remains an
unsupported scope on purpose.

## Cadence (§7.6)

`should_regenerate()` is a pure function: regenerate on any goal, any red
card, or a period change including half time; otherwise every two minutes
(Tier A) or five (Tier B). Event-driven first, timer as the backstop.

## Production schema notes

The live tables do not match the schema printed in the architecture document.
Read off the Contabo host on 2026-09-21:

- `fixture_live_snapshots.home_goals` / `away_goals` are **nullable** (the
  document says `NOT NULL`). A null score is flagged and suppresses.
- `fixture_live_snapshots.home_red_cards` / `away_red_cards` are nullable and
  are **NULL in every row** — ingestion is not populating them. Red cards are
  therefore counted from `fixture_events`, with the snapshot columns used as a
  monotone floor for when ingestion starts filling them. *This is worth an
  ingestion ticket; it is outside this change's scope.*
- `fixture_events` uses `observed_at` (not `captured_at`), `identity_key` (not
  `provider_event_key`) and `assist_id` (not `assist_player_id`), and has an
  extra `retracted_at`.
- `event_type` / `event_detail` carry raw provider casing (`Goal` /
  `Normal Goal`, `subst` / `Substitution 1`). All matching is
  case-insensitive, and a second yellow is detected explicitly because
  `Second Yellow card` contains no "red".
- The same real-world event can arrive under two different `identity_key`s
  when the provider revises a minute or a scorer — fixture 3981689's
  20th-minute goal did exactly this. Goals are therefore read from the
  snapshot score, which is authoritative; events are used only for the card
  timeline, where a structural de-duplication key is applied.

## Not wired up here

These modules are pure and self-contained. Deliberately **not** done, because
they belong to other in-flight workstreams:

- Writing `InPlayModelResult` into `engine_predictions` — needs the §7.4
  lifecycle/audit migration (`IN_PLAY_1H`, `IN_PLAY_2H`, `data_cutoff_at`,
  `fixture_live_snapshot_id`, `match_minute`, `period`, `feature_version`).
- Gating in-play predictions behind login — §8.
- Pushing regenerated predictions to clients — §9.
- The worker loop that calls `should_regenerate()` and the `in_play` candidate
  scope.

When wiring up: clamp probabilities into `[0.01, 0.99]` before building an
`EnginePrediction`, and call `publishable_markets()` rather than iterating
`result.markets` directly — a determined market is a fact, not a prediction,
and `EnginePrediction.validate()` will reject it.
