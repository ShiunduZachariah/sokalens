# Model A's expected goals: why they overshot, and the fix

Date: 2026-11-04

Model A's expected goals ran about 11% high on average, and it forecast too many
draws. The average hid a worse problem: the expected goals barely tracked reality
at all. This records the cause and the evidence for the fix.

## The cause

**Team ratings were fitted on the wrong sample.** The history query returns only
the two teams' own fixtures, so each opponent was seen only against those two
teams. An opponent's defence rating then absorbs the goals the target team scored
against it, which leaves the target's attack rating almost unidentified.

The clearest evidence uses twenty identical teams, where every true rating is 1.0
and any spread is pure artefact:

| Attack rating, with the settings of the time | 5th percentile | 95th percentile |
| --- | --- | --- |
| Fitted on the two teams' fixtures | 0.48 | 1.78 |
| Fitted on the whole league | 0.72 | 1.29 |
| Real production-style ratings on the corpus | 0.48 | 1.97 |

So nearly all of the real spread was artefact. Multiplying two of these ratings
put a home side's expected goals anywhere from 0.37 to 4.82. Because the product is
skewed, the mean came out high while the median was far too low (1.25 home against
1.50 actual). The excess draws follow from the same spread: fixtures with two low
expected-goals figures got draw forecasts above 45%, and those fixtures drew 28% of
the time.

Three things were not the cause. The home advantage ratio is right, league
strength scales are neutral on this corpus, and Dixon-Coles rho is fine: removing
it made log loss worse on the tuning window.

## The fix

1. **Fit on the whole league.** `load_fixture_inputs` also reads every finished
   fixture of the two teams' own leagues, this season and last, under the same
   point-in-time rules (`_LEAGUE_HISTORY_FIXTURES_SQL`). Only the team-strength fit
   uses it; form, gating and everything else still read the two-team history.
2. **Divide by the rate the ratings were fitted against.** The competition baseline
   came from the two teams' fixtures this season, a handful of matches early on.
3. **Rate on more matches, and shrink by what they are worth.** `DECAY` moved from
   0.90 to 0.98, because at 0.90 a rating rested on about ten matches. Shrinkage now
   uses the decay-weighted match count rather than the raw count, with
   `SHRINKAGE_MATCHES` at 12.

The settings in 2 and 3 were chosen on the tuning window (2019-07 to 2022-07) by the
sum of 1X2, over/under 2.5 and BTTS log loss. Decay 0.97 and 0.98 tied.

## Holdout, 2026-11-04

48,591 fixtures across 39 leagues from 2022-07-01, the same set as the Elo and Model
C comparisons, scored once with these settings. "Published" includes the form
adjustment and, for 1X2, the odds anchor. An early sample of this holdout was looked
at while diagnosing the cause, before tuning.

| Expected goals | Before | After | Actual |
| --- | --- | --- | --- |
| Mean, home / away | 1.67 / 1.35 | 1.50 / 1.19 | 1.50 / 1.20 |
| Median, home / away | 1.25 / 1.00 | 1.45 / 1.15 | |
| 5th to 95th percentile, home | 0.37 to 4.82 | 0.96 to 2.22 | |
| Slope of goals scored on expected goals (1 is ideal) | 0.14 | 0.88 | |
| Model A mean draw probability | 0.302 | 0.267 | 0.262 |

Lower is better. "Base rate" always forecasts the holdout's own frequency, which it
could not know in advance, so it is a generous bar.

| Log loss | Before | After | Paired z | Base rate |
| --- | --- | --- | --- | --- |
| Over/under 1.5, published | 0.8009 | 0.5572 | -66.6 | 0.5631 |
| Over/under 2.5, published | 0.9579 | 0.6849 | -70.6 | 0.6930 |
| Over/under 3.5, published | 0.9465 | 0.5903 | -74.6 | 0.5985 |
| BTTS, published | 0.8538 | 0.6903 | -57.2 | 0.6913 |
| 1X2, Model A alone | 1.0889 | 1.0348 | -34.4 | |
| 1X2, published | 1.0223 | 1.0089 | -17.7 | |

- **Totals and BTTS were worse than knowing nothing, and are now slightly better.**
  They have no odds anchor, so this reached published predictions directly.
  Calibration error fell from 0.25 to 0.024 on over/under 2.5 and from 0.21 to 0.027
  on BTTS. All 39 leagues improved on over/under 2.5.
- **Predicted-side hit rate** rose from 52.2% to 55.7% on over/under 2.5 and from
  51.4% to 53.9% on BTTS.
- **The published 1X2 now beats Elo alone** (1.0212), still behind the closing market
  (0.9988).

## What changes for users

Published probabilities become much less extreme, because the extremes were false.
Over/under 2.5 picks at 80% or above fall from 48% of fixtures to 0.2%; those picks
claimed 90% on average and hit 54% of the time. On the holdout the published side changes
on 18% of 1X2, 36% of over/under 2.5 and 38% of BTTS fixtures.

## Version

This ships as `python-engine-v2`. The name counts champion versions; it is not the
architecture v2 specification. Migration 000091 retires `python-engine-v1` and
registers v2 as the production model for every market family.

- **What splits.** `model_evaluation_facts` and `prediction_accuracy_daily` keep the
  two versions apart, and settled v1 rows are not rewritten.
- **Shadow evidence.** The challenger report counts live rows only from the current
  champion's `promoted_at`. A forecast the old image makes between the migration and
  the new containers starting can still be counted, because it postdates the promotion.
- **Rollback.** A database trigger links a row with no model to the exact version
  that wrote it, because the old image cannot find a retired row and settlement skips
  a prediction with no model.

## Known limits

- **The corpus has no cups or continental ties.** Those fixtures rate each team in
  its own league, as before, but the corpus cannot score them. Dropping the
  competition baseline also removes its effect on them; it had the wrong sign, since
  a low-scoring competition raised the expected goals.
- **Published hit rates still pool the versions.** The public report
  (`services/api/internal/predictions/source.go`) and the engine's accuracy snapshots
  sum `prediction_accuracy_daily` across `model_version`, so they blend v1 and v2.
