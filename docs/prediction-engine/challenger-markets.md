# Models B and C across all markets

Date: 2026-11-04

[challengers.md](challengers.md) covers the 1X2 shadow. This extends both challengers
to the other markets the engine publishes. The forecasts here are stored and graded as a
shadow. Since `python-engine-v3`, some published markets read them: see
[promoted-markets.md](promoted-markets.md).

## What each model covers

| Markets | Model B | Model C |
| --- | --- | --- |
| 1X2, double chance, draw no bet | Elo's own | Goal models |
| Totals, BTTS, correct score, odd/even, combinations, half-time result, highest-scoring half | Rating gap mapped to goals | Goal models |
| Corners, cards, fouls, shots, shots on target | No | Count models |
| Possession | No | No: the corpus has no possession data |
| Team to score first | No | No: see below |

- **Model C** has four Poisson tree models for goals (home, away, and each at half
  time) and six for counts. Goal markets come from the score matrix those imply. Counts
  use a negative binomial, because they vary more than Poisson allows.
- **Model B** fits a Poisson regression of goals on the Elo rating gap and the league's
  goal rate. It never reads a team's goal rate, which is the input Elo exists to avoid
  (section 8.6), and results give it no basis for counts.
- **Team to score first** is left out because production results carry no first-goal
  team, so settlement can only grade 0-0 matches.

## Holdout, 2026-11-04

48,591 fixtures from 2022-07-01. Models were trained before that date, with settings
chosen on 2021-07 to 2022-07. Both runs reproduced byte for byte.

"Champion" is the published forecast, replayed through the pipeline's own steps with
the fixes in [model-a-expected-goals.md](model-a-expected-goals.md). "Base rate" always
forecasts the holdout's own frequency, which it could not know in advance. Lower is
better. z is paired against the champion, and each fixture counts once.

| Log loss | Fixtures | Base rate | Champion | Model B | z | Model C | z | Champion + B + C | z |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1X2 | 48,591 | 1.0737 | **1.0089** | 1.0212 | +18.0 | 1.0268 | +24.1 | 1.0144 | +14.2 |
| Double chance | 48,591 | 0.6238 | **0.5889** | 0.5953 | +17.5 | 0.5986 | +24.2 | 0.5919 | +14.1 |
| Draw no bet | 35,836 | 0.6753 | **0.5954** | 0.6081 | +16.0 | 0.6166 | +22.7 | 0.6020 | +13.6 |
| Over/under 1.5 | 48,591 | 0.5631 | 0.5572 | 0.5555 | −2.7 | 0.5546 | −4.6 | **0.5536** | −9.2 |
| Over/under 2.5 | 48,591 | 0.6930 | 0.6849 | 0.6830 | −2.6 | 0.6818 | −4.8 | **0.6803** | −10.1 |
| Over/under 3.5 | 48,591 | 0.5985 | 0.5903 | 0.5886 | −2.4 | 0.5870 | −5.0 | **0.5857** | −10.2 |
| Over/under 4.5 | 48,591 | 0.4040 | 0.3977 | 0.3963 | −2.2 | 0.3952 | −4.3 | **0.3941** | −9.2 |
| BTTS | 48,591 | 0.6913 | 0.6903 | 0.6884 | −3.2 | 0.6873 | −5.8 | **0.6868** | −9.6 |
| BTTS and over/under 2.5 | 48,591 | 1.2217 | 1.2113 | 1.2078 | −4.2 | 1.2070 | −5.8 | **1.2049** | −12.8 |
| BTTS and result | 48,591 | 1.7387 | 1.6980 | 1.6808 | −15.9 | 1.6867 | −10.3 | **1.6806** | −25.1 |
| Result and over/under 2.5 | 48,591 | 1.7186 | 1.6734 | 1.6572 | −14.4 | 1.6627 | −9.4 | **1.6559** | −24.2 |
| Correct score | 48,591 | 2.9775 | 2.9215 | 2.9017 | −14.7 | 2.9072 | −10.6 | **2.8984** | −26.4 |
| Half-time result | 30,387 | 1.0803 | 1.0611 | **1.0503** | −11.3 | 1.0536 | −7.4 | 1.0510 | −16.5 |
| Highest-scoring half | 30,387 | 1.0739 | 1.0885 | 1.0740 | −14.1 | 1.0749 | −13.2 | 1.0755 | −19.3 |
| Odd/even | 48,591 | 0.6931 | 0.6939 | 0.6938 | −2.7 | 0.6939 | −0.8 | 0.6938 | −2.2 |

A later fix to the replay moved five champion figures here by 0.0002 or less: over/under
4.5, BTTS and result, result and over/under 2.5, correct score and half-time result. Their
z-scores and mixes were not recomputed, which that shift is too small to affect. Double
chance and draw no bet were fully rescored.

The champion's count forecast is the sum of the two teams' rolling averages with
Poisson, on fixtures where it has the five samples per side it needs to publish.
Corner, shot and foul data exist for about 57% of the holdout. The extra-league files
have none.

| Log loss, averaged over published lines | Fixtures | Base rate | Champion | Model C | z |
| --- | --- | --- | --- | --- | --- |
| Total corners | 27,641 | 0.6498 | 0.6729 | **0.6438** | −19.7 |
| Home team corners | 27,904 | 0.6544 | 0.6559 | **0.6272** | −20.0 |
| Away team corners | 27,887 | 0.6567 | 0.6788 | **0.6288** | −29.8 |
| Total cards | 29,819 | 0.6166 | 0.5995 | **0.5908** | −9.5 |
| Total fouls | 27,641 | 0.6027 | 0.5523 | **0.5348** | −14.7 |
| Total shots | 27,641 | 0.5945 | 0.5970 | **0.5453** | −25.5 |
| Shots on target | 27,641 | 0.6533 | 0.6574 | **0.6347** | −15.8 |

What this shows:

- **The champion's odds anchor is what wins 1X2, and the two markets that restate it.**
  No challenger or mix beats it on 1X2, double chance or draw no bet. An earlier version
  of this table showed Elo winning those two, because the harness scored them before the
  anchor: see [champion-dependent-markets.md](champion-dependent-markets.md).
- **Outside those three both challengers beat it nearly everywhere.** C leads on totals
  and BTTS.
- **Most goal-market gains are small in absolute terms.** On over/under 2.5, Model C
  beats the champion by 0.003. The closing market scores 0.6749 on the 30,395 fixtures
  with odds, against 0.6834 for Model C and 0.6872 for the champion. Neither challenger
  is close to the market.
- **Counts are the largest gains.** The champion is worse than the base rate on five
  of the seven: total and home and away corners, shots and shots on target.
- **Nothing beats the base rate on odd/even, and highest-scoring half is level at
  best.** A model has no skill there.

Calibration error fell from 0.024 to 0.013 (B and C) on over/under 2.5, and on BTTS
from 0.027 to 0.011 (B) and 0.005 (C).

## How the shadow runs

No new command: `run-challengers`, `settle-challengers` and `challenger-report` each do
this after their 1X2 work. A fault here appears as `markets.error_type` in the command's
output and affects nothing else. `PREDICTION_ENGINE_CHALLENGERS_ENABLED=false` switches
it off with the rest.

| Table (migration 000092) | Holds |
| --- | --- |
| `challenger_market_predictions` | One row per model, fixture and stage, with a document of market -> selection -> probability. The champion gets a row under its own registry id. |
| `challenger_market_settlements` | Immutable: for each market, what happened and the probability given to it. |

Grading calls `settle_prediction`, so a challenger is graded by the published rules.
A test checks that the holdout scorer and this grading agree. The report never promotes:
each market is `extend` below `minimum_shadow_settled_samples` and `pending` above it,
written to `champion_challenger_evaluations` with the contract
`shadow-markets-v1:<market>`.

`run-challengers --origin replay` scores finished production fixtures. Those rows are
never evaluation evidence, and `challenger-report --origin replay --no-write` reads them.

## Retraining

Needs `services/model-lab/requirements-train.txt`, and writes
`prediction_engine/artifacts/challenger_markets/`. Each export is refused unless the
standard-library evaluator reproduces LightGBM.

```bash
python -m model_lab.all_market_eval champion Model-Training-Data/raw --out champion.jsonl
```

```bash
python -m model_lab.all_market_eval challengers Model-Training-Data/raw --out challengers.jsonl --report-dir . --export-dir services/prediction-engine/prediction_engine/artifacts/challenger_markets
```

```bash
python -m model_lab.all_market_stats Model-Training-Data/raw --report stats.json --export-dir services/prediction-engine/prediction_engine/artifacts/challenger_markets
```

```bash
python -m model_lab.all_market_eval manifest Model-Training-Data/raw --export-dir services/prediction-engine/prediction_engine/artifacts/challenger_markets --code-commit <commit>
```

```bash
python -m model_lab.all_market_eval report Model-Training-Data/raw --champion champion.jsonl --challengers challengers.jsonl
```

## Known limits

- **Not run on production or QA.** No production fixture has been scored, including
  the cups and continental ties the corpus lacks.
- **Production statistics are partial.** Count features are missing for fixtures
  without statistics, and the trees handle that, but their accuracy there is unmeasured.
- **Calibration has not been done** (section 9). It is a promotion requirement.
- **Storage.** A model's document is 2-3 KB, so a fixture at one stage writes about
  8 KB of forecasts plus its settlements, with no retention yet. Production volume has
  not been measured against this.
- **Replay statistics are not point-in-time.** A replay reads statistics as they stand
  today. It does not affect live rows or evidence.
- **Evidence starts at the champion's promotion.** Live rows from before it are not
  counted, as in the 1X2 report.
