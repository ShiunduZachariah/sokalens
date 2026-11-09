# The possession_winner probability

`possession_winner` publishes the probability that the home side, the away side
or neither has more of the ball. It used to publish a certainty. This note
records what replaced that, the evidence for it, and how to measure it again.

## The model

The home share is a Beta distribution, as architecture v2 section 8.4.6 requires
for a bounded proportion.

- **Mean:** `(home average + (100 - away average)) / 2`, from each team's
  possession in the matches the engine already loads for the fixture. The mean
  stops at 33 and 67, the edges of the evidence.
- **Precision:** 26, from a residual standard deviation of 9.5 points.
- **Outcomes:** the provider reports whole percentages, so `level` is the mass
  that rounds to 50, and `home_more` and `away_more` are the mass either side.

| Expected home share | `home_more` | `level` | `away_more` |
| --- | --- | --- | --- |
| 50 | 0.480 | 0.040 | 0.480 |
| 51 | 0.521 | 0.040 | 0.439 |
| 55 | 0.680 | 0.035 | 0.285 |
| 60 | 0.839 | 0.023 | 0.138 |
| 65 | 0.935 | 0.012 | 0.053 |

## The evidence

Measured on 2026-11-04 with `model_lab.possession_walk_forward` against
`sports_prodqa`, a local database with API-Football statistics up to 2026-08-31.
It is not a measurement of live production, which could not be read at the time.
Every expected share uses only matches that finished before kick-off. The holdout
is the latest 40% of fixtures.

| | 5+ readings per side (the engine's threshold) | 3+ readings per side |
| --- | --- | --- |
| Fixtures | 159, in 30 leagues | 700, in 55 leagues |
| Holdout fixtures | 64 | 280 |
| Residual sd, earlier and holdout | 9.4 and 9.9 | 9.5 and 9.6 |
| Slope of actual on expected | 0.93 | 1.06 |
| Log loss, this model | 0.747 | 0.715 |
| Log loss, base rates | 0.813 | 0.787 |
| Log loss, previous market | 2.737 | 2.446 |
| Gain over base rates, 95% interval | -0.032 to 0.154 | 0.025 to 0.116 |
| Holdout hit rate of the top pick | 64.1% | 66.4% |

Held-out calibration of the top pick, with 3+ readings per side:

| Predicted band | Fixtures | Mean predicted | Observed |
| --- | --- | --- | --- |
| 0.4 to 0.5 | 24 | 0.490 | 0.667 |
| 0.5 to 0.6 | 103 | 0.548 | 0.573 |
| 0.6 to 0.7 | 89 | 0.649 | 0.697 |
| 0.7 to 0.8 | 43 | 0.739 | 0.767 |
| 0.8 to 0.9 | 19 | 0.839 | 0.737 |
| 0.9 to 1.0 | 2 | 0.925 | 1.000 |

The published rows at the clamp say the same thing directly. Of the 589
`possession_winner` rows in that database, 127 have an actual split to compare
against, and 85 of those were right: 67% against a stated 99%.

## What this does not establish

- At the engine's own threshold the holdout is 64 fixtures, and the gain over
  base rates is not distinguishable from zero. The case for the curve rests on
  its single parameter being stable across windows and thresholds, and on the
  larger sample at 3+ readings.
- The top band is overconfident on small numbers: 0.839 predicted against 0.737
  observed, on 19 fixtures.
- The home side holds about 1.5 points more than expected. That is not in the
  mean, because it fell from 2.0 to 0.8 points between the two windows and a
  constant would not have been stable.

## Measuring it again

Re-run this against production once it can be read, and whenever a season of
statistics has accumulated. The session is read-only. Change the precision in
`shot_possession_markets.py` if the implied value has moved, and update this note
and the pinned curve in `tests/prediction-engine/test_possession_market.py`.

```bash
PYTHONPATH="services/prediction-engine:services/model-lab" \
  python -m model_lab.possession_walk_forward --database-url "$DATABASE_URL"
```
