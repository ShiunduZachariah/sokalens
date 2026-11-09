# Model B: the Elo challenger

Date: 2026-11-04

Architecture v2 section 8.2 asks for a second model that reads results rather
than goal rates, so the ensemble in section 8.5 can measure real agreement.
`services/prediction-engine/prediction_engine/elo.py` is that model. It is
registered at **zero ensemble weight** (`config.v2.ensemble_weights`, the
`model_b` slot). It runs in production only as a shadow challenger, beside
Model C ([challengers.md](challengers.md)), so published output is unchanged.

Do not confuse it with `model_b.py`, which is v1's form adjustment and is
still live in the blend. Section 8.6 reassigns the letter B to Elo.

## What it does

| Part | Behaviour |
| --- | --- |
| Rating | One league-relative rating per team, updated only by matches between two teams of the same league |
| Update | Standard Elo, K = 15 (chosen by the walk-forward tuning window below), scaled by World Football Elo's goal-margin weight; margins above 5 count as 5 so corrupt aggregates cannot swing a rating |
| Home edge | A fixed offset in rating points (50, also tuned) added to the home side's expectation |
| New season | Each rating regresses 20% of the way to its league mean |
| Newcomers (5D.5) | A team entering a league with history starts at that league's measured newcomer level (where earlier arrivals settled after 10 matches), shrunk toward −60 while few arrivals have been seen; a league's first observed season starts everyone at its mean |
| Between leagues | A continental or cross-tier result moves a per-league offset (K = 4), not the team ratings; a league that never plays outside stays neutral |
| Probabilities | Ordered logit from the rating difference: the draw threshold reproduces the league's draw rate for equal teams, and the slope matches the Elo expected-score curve at zero |
| Point in time | `run_elo` replays only finished fixtures that kicked off before both the target and the as-of cutoff |

## Deliberate departures from section 8.2

- **No attack/defence Elo components.** A rating that splits scoring from
  conceding has to be driven by goals, which is Model A's input. It would
  make the two models less independent, the property 8.2 exists for.
- **No over/under or BTTS.** A result-only rating carries no information
  about how many goals a match will have. The model publishes 1X2, double
  chance and draw no bet only.
- **Cross-league fixtures update leagues, not teams.** This matches the split
  `team_strength.py` and `league_strength.py` already make for goal rates.

## Before it can carry weight

1. **League-wide ratings in production.** Done: the shadow path replays every
   finished fixture of the last 1,100 days once per run instead of the 80-row
   history the champion reads (`prediction_engine.challengers`).
2. **Shadow evaluation on production data.** Running: forecasts are settled
   into `shadow_settlements` and compared with the champion daily in
   `champion_challenger_evaluations`. The decision stays `extend` until 500
   paired fixtures have settled.
3. **A weight chosen by walk-forward evidence**, not by hand (section 8.5).
   Still to do, together with calibration (section 9).

## Walk-forward evidence

Run `task model:elo-walk-forward` against the local corpus
(`services/model-lab/README.md` explains how to get it). The harness is
`services/model-lab/model_lab/elo_walk_forward.py`; its docstring lists every
model it scores and why closing-odds models are not a fair opponent for Elo.

### Snapshot, 2026-11-04

Corpus: 301,936 matches across 39 leagues (705 files). Tuning window
2019-07-01 to 2022-07-01, holdout from 2022-07-01: **48,591 fixtures**. Tuning
chose K = 15, home edge 50, season regression 0.20 (log loss 1.0294 on the
tuning window). Lower is better for log loss, Brier and calibration error.

| Model | Log loss | Brier | Accuracy | Calibration error | Mean draw |
| --- | --- | --- | --- | --- | --- |
| Market (de-vigged closing odds) | 0.9988 | 0.5971 | 50.6% | 0.006 | 0.262 |
| **Elo, tuned** | **1.0212** | **0.6119** | **49.0%** | 0.011 | 0.241 |
| Champion (v1 blend + 50% odds anchor) | 1.0223 | 0.6132 | 48.2% | 0.014 | 0.277 |
| Elo, untuned (K 20, home edge 65) | 1.0232 | 0.6134 | 48.9% | 0.023 | 0.234 |
| Model A + Elo, equal mix | 1.0346 | 0.6213 | 47.1% | 0.018 | 0.271 |
| League frequency | 1.0743 | 0.6499 | 43.8% | 0.010 | 0.266 |
| Model A alone | 1.0889 | 0.6574 | 42.9% | 0.064 | 0.302 |
| v1 blend (Model A + form) | 1.0910 | 0.6573 | 43.3% | 0.067 | 0.293 |

Paired log-loss differences per fixture (negative means the first model is
better):

| Comparison | Mean difference | Standard error | z |
| --- | --- | --- | --- |
| Elo vs Model A | −0.0676 | 0.0018 | −36.9 |
| Elo vs v1 blend | −0.0698 | 0.0019 | −37.3 |
| Elo vs league frequency | −0.0531 | 0.0015 | −36.3 |
| Elo vs market | +0.0226 | 0.0009 | +26.5 |
| Champion vs market | +0.0237 | 0.0009 | +26.6 |

Elo beats Model A in all 39 leagues. On the 3,340 fixtures where a side is
new to its league or not yet established, Elo still beats Model A (1.052
against 1.167).

What this does and does not show:

- **Elo, with no market input, matches the published champion**, which takes
  half its 1X2 from closing odds. Neither beats the market.
- **Model A alone scores worse than the league's own historical
  frequencies.** Its expected goals run about 11% high (home 1.67 against
  1.50 actual, away 1.35 against 1.20), and it forecasts too many draws
  (0.30 against about 0.27). That is a finding about Model A, separate from
  this change. Its 1X2 is hidden in production by the 50% odds anchor, but
  totals and BTTS markets get no anchor. Since fixed, which moves the Model A
  and champion rows above: see
  [model-a-expected-goals.md](model-a-expected-goals.md).
- **Elo under-forecasts draws** (0.24). In a trial on the English leagues,
  learning the draw threshold online improved calibration but lost on log
  loss, so it was not kept.
- **The corpus is not production.** It holds 39 mostly European leagues with
  no cups or continental ties, while production covers about 1,200
  competitions. The champion replay uses the production history query and
  code, but sees no cup fixtures or match statistics (1X2 does not use
  statistics). Closing odds are the last price before kick-off; production
  anchors to whatever odds it holds when it runs.
