# Models B and C in published predictions

Date: 2026-11-05

`python-engine-v3` publishes Models B and C on the markets where they beat the champion. The
owner took this decision on 2026-11-05, ahead of the section 21.4 shadow minimum. The code is
`prediction_engine/promoted_markets.py`, and every published row names its source in
`features_used.probability_source`.

## What each market uses

Log loss on the holdout: 48,591 fixtures from 2022-07-01, lower is better. The figures and the
method are in [challenger-markets.md](challenger-markets.md) and
[champion-dependent-markets.md](champion-dependent-markets.md).

| Market | Published from | Champion | Published | z |
| --- | --- | --- | --- | --- |
| 1X2 | Champion | **1.0089** | 1.0089 | |
| Double chance | Champion | **0.5889** | 0.5889 | |
| Draw no bet | Champion | **0.5954** | 0.5954 | |
| Over/under 2.5 | Champion + B + C | 0.6849 | **0.6803** | −10.1 |
| BTTS | Champion + B + C | 0.6903 | **0.6868** | −9.6 |
| BTTS and over/under 2.5 | Champion + B + C | 1.2113 | **1.2049** | −12.8 |
| BTTS and result | Champion + B + C | 1.6979 | **1.6806** | −25.1 |
| Result and over/under 2.5 | Champion + B + C | 1.6733 | **1.6559** | −24.2 |
| Correct score | Champion + B + C | 2.9214 | **2.8984** | −26.4 |
| Half-time result | Model B | 1.0609 | **1.0503** | −11.3 |
| Highest-scoring half | Model B | 1.0885 | **1.0740** | −14.1 |
| Total corners | Model C | 0.6729 | **0.6438** | −19.7 |
| Home team corners | Model C | 0.6559 | **0.6272** | −20.0 |
| Away team corners | Model C | 0.6788 | **0.6288** | −29.8 |
| Total cards | Model C | 0.5995 | **0.5908** | −9.5 |
| Total fouls | Model C | 0.5523 | **0.5348** | −14.7 |
| Total shots | Model C | 0.5970 | **0.5453** | −25.5 |
| Shots on target | Model C | 0.6574 | **0.6347** | −15.8 |

Odd/even, team to score first and possession stay with the champion, because no model has
evidence there. Double chance, draw no bet and the other 1X2-derived figures keep their
odds-anchored value, so none of them can contradict the published 1X2.

## How it works

1. `run-challengers` stores a forecast per model, fixture and lifecycle stage. It rides each
   upcoming-prediction run, at most every 30 minutes and up to 500 targets, nearest kick-off first.
2. `load_fixture_inputs` reads the newest live forecast of each model made by the as-of time.
   Replay rows are never read, so a replay is not fed a forecast made for the real match.
3. `run_fixture_engine` replaces the probabilities and then picks the selection as before. The
   gate, data requirements and the score consistency check are unchanged.

A forecast targets a fixture that already has a prediction, so a fixture first publishes the
champion alone. The upcoming scope normally skips a fixture with current predictions, so a forecast
made after a row was last evaluated also brings its fixture back, once. That adds one published
version per forecast, and existing `python-engine-v2` rows move to v3 the same way.

**The score-derived markets share one grid.** Correct score and BTTS with totals come from the
mean of the three score grids. A forecast stores scores up to 5-5 plus "other", and "other" is
spread over larger scores in the champion's proportions. Result with totals reads the same grid.

**This adds no coverage.** A market is published only if the champion would publish it.

## Switching it off

Set `PREDICTION_ENGINE_PROMOTED_MARKETS_ENABLED=false` on the worker and restart it. Rows then
publish the champion's numbers, still labelled `python-engine-v3`, with
`probability_source: champion`. A missing or malformed forecast does the same for that market alone.

## What the decision accepted

- **No live evidence yet.** Section 21.4 asks for 500 settled shadow forecasts, and there were
  none at the decision. The evidence is the holdout. The registry row records this in
  `audit_metadata`.
- **The holdout is not production.** It has no cups or continental ties, and count data for about
  57% of fixtures. Model C's count accuracy where production statistics are partial is unmeasured.
- **Calibration has not been done** (section 9). The models are better calibrated than the
  champion on the markets measured, but no calibration step runs.
- **Pick gains are modest on goal markets.** Share of the mix's more likely side that won, against
  the champion's, on the holdout: over/under 2.5 56.35% against 55.75%, BTTS 54.42% against 53.94%,
  over/under 3.5 71.46% against 71.29%. Multi-way and count markets were not measured this way.
- **Public percentages barely move.** Pages show the composite confidence score, not the
  probability, so the visible change is mainly in which picks are made. See `ConfidenceScorer`.

## On production's own fixtures

`production-copy-test.sh`, 2026-11-05: 1,868 recently finished production fixtures replayed, including
cups. Log loss against the champion, each model alone, since this replay does not score the mix.

| Market | Fixtures | Champion | Model B | z | Model C | z |
| --- | --- | --- | --- | --- | --- | --- |
| 1X2 | 1,868 | 1.0132 | 1.0238 | +1.5 | 1.0045 | −1.4 |
| Over/under 2.5 | 1,868 | 0.6723 | 0.6732 | +0.1 | 0.6674 | −0.8 |
| BTTS | 1,868 | 0.7032 | 0.6985 | −0.9 | 0.6871 | −3.2 |
| Correct score | 1,868 | 3.0516 | 3.0452 | −0.5 | 3.0171 | −2.7 |
| Half-time result | 1,799 | 1.0595 | 1.0572 | −0.4 | 1.0472 | −2.6 |
| Total corners, over/under 9.5 | 162 | 0.8122 | | | 0.6778 | −3.2 |
| Total shots, over/under 24.5 | 161 | 0.7723 | | | 0.5793 | −3.7 |

These agree with the holdout in direction, and Model B is weaker here than Model C on half-time
result, which is worth rechecking against live results. Replay statistics are as they stand today,
not as they stood before kick-off, so the count rows flatter Model C.

## Watching it

- **Live comparison.** `challenger-report` still compares B and C with the plain champion: the
  row stored under the champion's registry id is `champion_market_document`, which does not read
  these forecasts. Rows published by the promoted markets are graded in the published ledger.
- **On production's data before release.** `scripts/qa/production-copy-test.sh` compares v2 with
  v3 market by market, with each source.
