# Double chance and draw no bet follow the final 1X2

Date: 2026-11-04

Double chance and draw no bet only restate the 1X2, so they are now derived from the
final 1X2 on every path. A fixture with real odds publishes exactly what it did before.
A fixture without them changes. No totals anchor was built, because the code that writes
odds stores 1X2 prices only.

**Production has not been checked.** See [Production coverage](#production-coverage).

## What the earlier evidence got wrong

[challenger-markets.md](challenger-markets.md) first reported the champion losing to Elo
on both markets. That was a fault in the holdout harness: `run_champion` scored them from
the blend, before the odds anchor, and replaced only the 1X2. Production has always
derived them from the anchored 1X2 when a fixture has real odds.

| Log loss | Fixtures | As first reported | As published | Model B (Elo) | Model C |
| --- | --- | --- | --- | --- | --- |
| Double chance | 48,591 | 0.6029 | **0.5890** | 0.5953 | 0.5986 |
| Draw no bet | 35,836 | 0.6267 | **0.5955** | 0.6081 | 0.6166 |

The champion wins both. The harness now reads the markets as they leave the pipeline's
last step.

## What was actually wrong

A fixture with no real odds skips the anchor, and the derivation lived inside it. Its 1X2
carried the form adjustment while these two stayed raw Model A. When they disagreed on the
favourite, the score consistency gate moved draw no bet to the 1X2's side but kept the raw
probability, publishing a two-way pick below 50%.

## Before and after

Holdout from 2022-07-01. Lower is better, and z is paired with each fixture counted once.
Calibration error is the harness's: the gap between predicted and observed per bin, summed
over ten bins and divided by fixtures. Double chance is the mean of its three selections,
and draw no bet is scored on the home side.

48,558 of the 48,591 fixtures have 1X2 odds, so the corpus barely reaches the changed path.
The second table therefore runs every fixture through that path with its odds removed, as a
fixture no bookmaker covers would run. 1X2 log loss does not change on either: 1.0089 with
odds, 1.0331 without.

| Log loss and calibration, corpus as it is | Fixtures | Log loss before | After | Calibration before | After |
| --- | --- | --- | --- | --- | --- |
| Double chance | 48,591 | 0.5890 | 0.5889 | 0.0120 | 0.0120 |
| Draw no bet | 35,836 | 0.5955 | 0.5954 | 0.0192 | 0.0191 |

| Log loss and calibration, no real odds | Fixtures | Log loss before | After | z | Calibration before | After |
| --- | --- | --- | --- | --- | --- | --- |
| Double chance | 48,591 | 0.6029 | **0.6019** | −8.4 | **0.0060** | 0.0099 |
| Draw no bet | 35,836 | 0.6267 | **0.6245** | −8.8 | **0.0087** | 0.0144 |

| Published rows, no real odds | Before | After |
| --- | --- | --- |
| Draw no bet below 50% | 1,533 of 48,059 (3.19%) | 0 |
| Draw no bet, mean probability / hit rate | 0.6504 / 0.6484 | 0.6573 / 0.6484 |
| Double chance, mean probability / hit rate | 0.7622 / 0.7597 | 0.7682 / 0.7605 |

**The trade.** Log loss improves and the sub-50% picks go, but both markets become
slightly overconfident: the published probability now exceeds the hit rate by 0.8 to 0.9
points, up from about 0.2. Removing the inconsistency passes the 1X2's form adjustment into these
two, and that adds sharpness faster than accuracy.

Both challengers still beat the champion on this path: Model B scores 0.5953 and 0.6081.
With real odds the champion wins, so the gap only matters where odds are missing.

## Share of published sides that change

Rows published by `run_fixture_engine`, after the score consistency gate.

| Path | Fixtures | Double chance | Draw no bet | The other 11 markets |
| --- | --- | --- | --- | --- |
| Real odds | 48,558 | 0 | 0 | 0 |
| No real odds | 48,591 | 4.58% | 0.05% | 0 |

On real odds, every row is identical to the sixth decimal. On no real odds, draw no bet's
side barely moves because the gate had already aligned it, but its probability moves on
91.88% of rows, by up to 0.077. Double chance moves on the same share, by up to 0.058. Its
changed picks hit 1,590 times against 1,551 before, of 2,200.

## Totals

No totals anchor was built, so no weight was tuned and the holdout was not scored for one.

Nothing writes over/under prices. `services/worker/internal/ingestion/odds_job.go` extracts
only "Match Winner" (bet 1), `odds_repository.go` normalises only Home, Draw and Away, the
Football-Data importer writes only `1x2`, and provisional odds are `1x2`. API-Football's
response does carry over/under, and the worker discards it.

## Section 27.6 sign-off

Fixed set: the 48,591 holdout fixtures, through `run_fixture_engine` and
`assign_best_bet_flags`, on `qa` and on this change.

| Check | Real odds | No real odds |
| --- | --- | --- |
| Coverage per market | Unchanged | Unchanged |
| Gate outcome | Unchanged | Unchanged |
| Double chance mean probability | Unchanged (0.7659) | 0.7622 → 0.7682 |
| Draw no bet mean probability | Unchanged (0.6532) | 0.6466 → 0.6532 |
| The other 11 markets | Unchanged | Unchanged |
| Draw no bet at or above the 0.55 best-bet line | Unchanged | 80.34% → 80.94% |

- **Confidence tier: not measured.** The corpus run has no fixture statistics, so every
  double chance and draw no bet row is `none`. For these two markets the score does not read
  the probability, so a tier should not move, but that rests on reading `ConfidenceScorer`.
- **Best bets: not measured.** The flag needs medium confidence, so the run flagged nothing.
  What can move is the 0.55 line: 3.44% of no-odds draw no bet rows cross it upward and 2.84%
  downward. That is an upper bound. Every double chance row stays above it.
- **Tier B and C markets: not run**, for the same reason. This change does not touch them, and
  the score consistency gate does not filter them.
- **Accas: not re-run.** Acca generation can select these markets.

The shift is intended, and the owner has decided:

- Signed off by: the owner, 2026-11-05. The calibration trade above is accepted.
- `model_version`: `python-engine-v2` reached production first, on 2026-11-05, so this ships
  as `python-engine-v3`, together with the market promotions in
  [promoted-markets.md](promoted-markets.md).

## Production coverage

**Not measured.** A read-only query against production was refused by the agent's permission
rules, and that was not worked around. So "no over/under odds" rests on the code above and
not on the rows. The share of fixtures on the no-odds path, which decides how many users see
this change, is unknown. Apply the percentages above to that share.

This query gets both, and changes nothing:

```sql
BEGIN READ ONLY;
SET LOCAL statement_timeout = '120s';

-- Which markets hold prices. Expected: 1x2 only.
SELECT market, count(*) AS rows, count(DISTINCT fixture_id) AS fixtures,
       min(fetched_at)::date AS first, max(fetched_at)::date AS last
FROM odds GROUP BY market ORDER BY rows DESC;

SELECT m.market_key, s.provider, count(*) AS rows, count(DISTINCT s.match_id) AS matches
FROM odds_snapshots s LEFT JOIN odds_markets m ON m.id = s.market_id
GROUP BY 1, 2 ORDER BY rows DESC;

-- How many fixtures take the path this change affects: no real 1X2 price.
WITH recent AS (
  SELECT f.id,
         EXISTS (SELECT 1 FROM odds o WHERE o.fixture_id = f.id AND lower(o.market) IN ('1x2', 'match winner', 'match_winner')
                   AND lower(COALESCE(o.bookmaker, '')) <> 'sokalens-provisional') AS has_real
  FROM fixtures f
  WHERE f.kickoff_at >= now() - interval '90 days' AND f.kickoff_at < now() + interval '7 days'
)
SELECT count(*) AS fixtures,
       count(*) FILTER (WHERE has_real) AS with_real_1x2,
       round(100.0 * count(*) FILTER (WHERE has_real) / NULLIF(count(*), 0), 1) AS real_pct,
       round(100.0 * count(*) FILTER (WHERE NOT has_real) / NULLIF(count(*), 0), 1) AS affected_pct
FROM recent;

ROLLBACK;
```

If the first two results show over/under rows, reopen the totals decision above.

## How far to trust the replay

- **The replay now matches the pipeline.** On the real-odds path its 1X2 and double chance
  equal `run_fixture_engine`'s on all 48,039 published rows of each, to the sixth decimal
  the rows are stored at.
- **It did not before.** The replay shared one `FeatureAssembler`, whose baseline cache is
  keyed by league, season and kick-off and not by history. A fixture could take the baseline
  of whichever match at that kick-off ran first. 83 holdout fixtures differed by run order,
  by up to 0.30. The pipeline builds one per fixture, so published predictions were never
  affected. The fix moved five champion figures in
  [challenger-markets.md](challenger-markets.md) by 0.0002 or less.

## Known limits

- **The no-odds fixtures are a stand-in.** Removing odds from fixtures bookmakers cover is not
  the same as fixtures they do not. The corpus has only 33 of those, too few to score, and no
  cups or continental ties.
- **Calibration has not been done** (section 9). With real odds both markets are
  underconfident: draw no bet publishes 0.6588 and hits 0.6771.
- **The no-odds runs were throwaway.** `python -m model_lab.all_market_eval champion`
  reproduces the published figures. The no-odds and section 27.6 runs took `qa` and this
  change through the steps described above, and those scripts are not kept in the repository.
