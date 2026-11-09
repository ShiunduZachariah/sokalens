"""Walk-forward evaluation of the Elo challenger against the live engine.

Architecture v2 section 21 says a challenger earns ensemble weight by beating
the champion on evidence, and section 24 says that evidence comes from a
walk-forward test, never a random split. This module produces it for Model B
(Elo, `prediction_engine.elo`) on the offline Football-Data corpus:

* Every prediction uses only matches that finished before its kick-off. Elo
  walks the corpus once in kick-off order, predicting each kick-off slot
  before folding its results in.
* Elo's free parameters are chosen on a tuning window that ends where the
  holdout begins, and the holdout is scored once with whatever was chosen.
* The champion is the engine's own code, not a re-implementation. For each
  holdout fixture the harness builds the history the production repository
  would load (the two teams' latest 80 finished fixtures from this season and
  last) and runs the same steps as `run_fixture_engine`: league and team
  strength, feature assembly, Model A, the v1 form blend, and the 50% pull
  toward the bookmaker line.

Models scored on the same fixtures:

``league_frequency``  the league's own home/draw/away rates so far (baseline A)
``model_a``           Model A alone, the Poisson scoreline model (baseline B)
``v1_blend``          Model A plus the v1 form adjustment, before the market
``elo``               the challenger, tuned on the tuning window (baseline C)
``elo_default``       the challenger with its untuned defaults
``model_a_elo_mix``   an equal mix of Model A and Elo, a preview of 8.5
``champion``          what production publishes: v1 blend anchored to the odds
``market``            de-vigged closing odds (baseline D)

Closing odds are the last price before kick-off, so ``market`` and
``champion`` see information Elo and Model A never do. The fair test for a
challenger that will enter the ensemble before the market anchor is Elo
against ``model_a`` and ``v1_blend``.
"""

from __future__ import annotations

import argparse
from bisect import bisect_left
from collections import defaultdict
from dataclasses import dataclass, replace
from datetime import datetime, timezone
import json
import math
from pathlib import Path
import re
import sys
from typing import Iterable

from model_lab.football_data import Match, load_matches

from prediction_engine.config import default_config
from prediction_engine.elo import DEFAULT_ELO_CONFIG, EloConfig, EloRatingBook
from prediction_engine.ensemble import blend_tier_a_markets
from prediction_engine.features import FeatureAssembler, FixtureRecord, OddsSnapshot, team_primary_leagues
from prediction_engine.league_strength import compute_league_strength, measure_home_advantage
from prediction_engine.model_a import ModelAInputs, run_model_a
from prediction_engine.model_b import run_model_b
from prediction_engine.pipeline import (
    _expected_goals,
    _league_strength_scales,
    _model_b_inputs,
    _published_result_markets,
    _team_strength_ratings,
)
from prediction_engine.team_strength import compute_team_strength


OUTCOMES = ("home", "draw", "away")
DEFAULT_HOLDOUT_FROM = "2022-07-01"
DEFAULT_TUNING_YEARS = 3
# The production repository's history query: both teams' fixtures from this
# season and last, newest first, capped at 80 rows.
HISTORY_LIMIT = 80
CALIBRATION_BINS = 10
LEAGUE_FREQUENCY_PRIOR = {"home": 0.45, "draw": 0.26, "away": 0.29}
LEAGUE_FREQUENCY_PRIOR_MATCHES = 50
TUNING_GRID = {
    "k_factor": (10.0, 15.0, 20.0, 30.0),
    "home_advantage": (20.0, 35.0, 50.0, 65.0),
    "season_regression": (0.20, 0.33, 0.50),
}


@dataclass(frozen=True)
class CorpusFixture:
    record: FixtureRecord
    league_key: str
    odds: OddsSnapshot | None
    result: str


# --- corpus ----------------------------------------------------------------


def corpus_fixtures(matches: Iterable[Match]) -> list[CorpusFixture]:
    """Football-Data rows as engine fixture records with stable integer ids.

    The main-league files carry no country or season column, so both come from
    the corpus path: raw/main/<Country>/<season>/<Div>.csv. Teams are keyed
    by country as well as name, so two clubs that share a name in different
    countries stay two teams.
    """
    team_ids: dict[tuple[str, str], int] = {}
    league_ids: dict[str, int] = {}
    fixtures: list[CorpusFixture] = []
    for index, match in enumerate(matches, start=1):
        country = match.country or _path_country(match.source_file)
        league_key = f"{country} {match.league}".strip()
        season = _season_year(match.season) or _season_year(_path_season(match.source_file)) or _season_from_kickoff(match.kickoff)
        league_id = league_ids.setdefault(league_key, len(league_ids) + 1)
        home_id = team_ids.setdefault((country, match.home_team.casefold()), len(team_ids) + 1)
        away_id = team_ids.setdefault((country, match.away_team.casefold()), len(team_ids) + 1)
        record = FixtureRecord(
            id=index,
            kickoff_at=match.kickoff.replace(tzinfo=timezone.utc),
            league_id=league_id,
            league_name=league_key,
            season=season,
            home_team_id=home_id,
            away_team_id=away_id,
            home_goals=match.home_goals,
            away_goals=match.away_goals,
            status="FT",
            source="football-data",
        )
        odds = (
            OddsSnapshot(fixture_id=index, home_win=match.odds_home, draw=match.odds_draw, away_win=match.odds_away)
            if match.odds_home and match.odds_draw and match.odds_away
            else None
        )
        result = {"H": "home", "D": "draw", "A": "away"}[match.result]
        fixtures.append(CorpusFixture(record=record, league_key=league_key, odds=odds, result=result))
    fixtures.sort(key=lambda fixture: (fixture.record.kickoff_at, fixture.record.id))
    return fixtures


def _path_country(source_file: str) -> str:
    parts = Path(source_file).parts
    for marker in ("main", "extra"):
        if marker in parts:
            position = parts.index(marker)
            if position + 1 < len(parts):
                return parts[position + 1]
    return ""


def _path_season(source_file: str) -> str:
    parts = Path(source_file).parts
    return parts[-2] if len(parts) >= 2 else ""


def _season_year(value: str) -> int | None:
    found = re.search(r"(19|20)\d{2}", value or "")
    return int(found.group(0)) if found else None


def _season_from_kickoff(kickoff: datetime) -> int:
    return kickoff.year if kickoff.month >= 7 else kickoff.year - 1


# --- Elo and the frequency baseline -----------------------------------------


def elo_predictions(
    fixtures: list[CorpusFixture],
    config: EloConfig,
    *,
    score_from: datetime,
    score_until: datetime | None = None,
) -> dict[int, dict[str, float]]:
    """1X2 per fixture id from one chronological pass over the corpus.

    Every fixture in a kick-off slot is predicted before any of that slot's
    results is recorded, so simultaneous matches never see each other.
    """
    book = EloRatingBook(config)
    predictions: dict[int, dict[str, float]] = {}
    for slot in _kickoff_slots(fixtures):
        kickoff = slot[0].record.kickoff_at
        if score_until is not None and kickoff >= score_until:
            break
        if kickoff >= score_from:
            for fixture in slot:
                predictions[fixture.record.id] = book.predict(fixture.record).match_result
        for fixture in slot:
            book.record(fixture.record)
    return predictions


def elo_flags(fixtures: list[CorpusFixture], config: EloConfig, *, score_from: datetime) -> dict[int, tuple[str, ...]]:
    book = EloRatingBook(config)
    flags: dict[int, tuple[str, ...]] = {}
    for slot in _kickoff_slots(fixtures):
        if slot[0].record.kickoff_at >= score_from:
            for fixture in slot:
                flags[fixture.record.id] = book.predict(fixture.record).fallback_flags
        for fixture in slot:
            book.record(fixture.record)
    return flags


def league_frequency_predictions(fixtures: list[CorpusFixture], *, score_from: datetime) -> dict[int, dict[str, float]]:
    counts: dict[int, dict[str, int]] = defaultdict(lambda: {outcome: 0 for outcome in OUTCOMES})
    predictions: dict[int, dict[str, float]] = {}
    for slot in _kickoff_slots(fixtures):
        if slot[0].record.kickoff_at >= score_from:
            for fixture in slot:
                seen = counts[fixture.record.league_id]
                total = sum(seen.values()) + LEAGUE_FREQUENCY_PRIOR_MATCHES
                predictions[fixture.record.id] = {
                    outcome: (seen[outcome] + LEAGUE_FREQUENCY_PRIOR[outcome] * LEAGUE_FREQUENCY_PRIOR_MATCHES) / total
                    for outcome in OUTCOMES
                }
        for fixture in slot:
            counts[fixture.record.league_id][fixture.result] += 1
    return predictions


def tune_elo(
    fixtures: list[CorpusFixture],
    *,
    tuning_from: datetime,
    holdout_from: datetime,
) -> tuple[EloConfig, list[dict[str, float]]]:
    """Pick Elo parameters by log loss on the tuning window alone."""
    labels = {fixture.record.id: fixture.result for fixture in fixtures}
    trials: list[dict[str, float]] = []
    best: tuple[float, EloConfig] | None = None
    for k_factor in TUNING_GRID["k_factor"]:
        for home_advantage in TUNING_GRID["home_advantage"]:
            for season_regression in TUNING_GRID["season_regression"]:
                config = replace(
                    DEFAULT_ELO_CONFIG,
                    k_factor=k_factor,
                    home_advantage=home_advantage,
                    season_regression=season_regression,
                )
                predictions = elo_predictions(fixtures, config, score_from=tuning_from, score_until=holdout_from)
                loss = _mean(_log_loss(predictions[fixture_id], labels[fixture_id]) for fixture_id in predictions)
                trials.append(
                    {
                        "k_factor": k_factor,
                        "home_advantage": home_advantage,
                        "season_regression": season_regression,
                        "log_loss": round(loss, 6),
                        "fixtures": len(predictions),
                    }
                )
                if best is None or loss < best[0]:
                    best = (loss, config)
    assert best is not None
    return best[1], trials


# --- the champion, through the engine's own code -----------------------------


class ChampionReplay:
    """Model A, the v1 blend and the published 1X2, the way production makes them."""

    def __init__(self, fixtures: list[CorpusFixture]) -> None:
        self._by_team: dict[int, list[FixtureRecord]] = defaultdict(list)
        for fixture in fixtures:
            self._by_team[fixture.record.home_team_id].append(fixture.record)
            self._by_team[fixture.record.away_team_id].append(fixture.record)
        self._kickoffs = {
            team_id: [record.kickoff_at for record in records] for team_id, records in self._by_team.items()
        }
        self._by_league: dict[int, list[FixtureRecord]] = defaultdict(list)
        for fixture in fixtures:
            self._by_league[fixture.record.league_id].append(fixture.record)
        self._league_kickoffs = {
            league_id: [record.kickoff_at for record in records] for league_id, records in self._by_league.items()
        }
        self._config = default_config()
        self.expected_goals: dict[int, tuple[float, float]] = {}

    def new_assembler(self) -> FeatureAssembler:
        """One per fixture, as the pipeline does.

        Its baseline cache is keyed by league and kick-off, not by history. Sharing one
        hands a fixture the baseline of whichever match at that kick-off ran first.
        """
        return FeatureAssembler(
            form_windows=self._config.form.lookback_windows,
            decay_rate=self._config.form.decay_rate,
        )

    def history(self, target: FixtureRecord) -> list[FixtureRecord]:
        rows: dict[int, FixtureRecord] = {}
        for team_id in (target.home_team_id, target.away_team_id):
            records = self._by_team[team_id]
            end = bisect_left(self._kickoffs[team_id], target.kickoff_at)
            for record in records[:end]:
                if record.id != target.id and record.season in (target.season, target.season - 1):
                    rows[record.id] = record
        newest_first = sorted(rows.values(), key=lambda record: (record.kickoff_at, record.id), reverse=True)
        return newest_first[:HISTORY_LIMIT]

    def league_history(self, target: FixtureRecord, history: list[FixtureRecord]) -> list[FixtureRecord]:
        """The team-strength fit's fixtures, as _load_league_history reads them."""
        primary = team_primary_leagues(history)
        league_ids = {primary[team] for team in (target.home_team_id, target.away_team_id) if team in primary}
        rows: list[FixtureRecord] = []
        for league_id in league_ids:
            end = bisect_left(self._league_kickoffs[league_id], target.kickoff_at)
            rows.extend(
                record
                for record in self._by_league[league_id][:end]
                if record.season in (target.season, target.season - 1)
            )
        return rows

    def predict(self, fixture: CorpusFixture) -> dict[str, dict[str, float]]:
        target = fixture.record
        history = self.history(target)
        strength_table = compute_league_strength(history)
        home_advantage = measure_home_advantage(history, target.league_id)
        team_table = compute_team_strength(self.league_history(target, history), home_advantage=home_advantage)
        features = self.new_assembler().assemble(
            target,
            history,
            {},
            as_of_timestamp=target.kickoff_at,
            odds=fixture.odds,
            league_strength=_league_strength_scales(strength_table, target),
            home_advantage=home_advantage,
            team_strength=_team_strength_ratings(team_table, target),
        )
        home_lambda, away_lambda = _expected_goals(features)
        model_a = run_model_a(ModelAInputs(home_lambda=home_lambda, away_lambda=away_lambda))
        blended = blend_tier_a_markets(
            model_a.markets,
            run_model_b(_model_b_inputs(features, model_a.markets)).market_deltas,
            self._config,
        )
        anchored = _published_result_markets(blended, features.odds_implied_1x2, self._config.probability_clamp)
        self.expected_goals[target.id] = (home_lambda, away_lambda)
        return {
            "model_a": _one_x_two(model_a.markets["match_result"]),
            "v1_blend": _one_x_two(blended["match_result"]),
            "champion": _one_x_two(anchored["match_result"]),
            "market": dict(features.odds_implied_1x2),
        }


def _one_x_two(values: object) -> dict[str, float]:
    assert isinstance(values, dict)
    probabilities = {outcome: float(values[outcome]) for outcome in OUTCOMES}
    total = sum(probabilities.values())
    return {outcome: value / total for outcome, value in probabilities.items()}


# --- scoring -----------------------------------------------------------------


def score(predictions: dict[int, dict[str, float]], labels: dict[int, str]) -> dict[str, object]:
    fixture_ids = sorted(predictions)
    losses = [_log_loss(predictions[fixture_id], labels[fixture_id]) for fixture_id in fixture_ids]
    briers = [_brier(predictions[fixture_id], labels[fixture_id]) for fixture_id in fixture_ids]
    hits = [
        max(OUTCOMES, key=lambda outcome: predictions[fixture_id][outcome]) == labels[fixture_id]
        for fixture_id in fixture_ids
    ]
    return {
        "fixtures": len(fixture_ids),
        "log_loss": round(_mean(losses), 5),
        "brier": round(_mean(briers), 5),
        "accuracy": round(_mean(1.0 if hit else 0.0 for hit in hits), 5),
        "calibration_error": round(_calibration_error(predictions, labels), 5),
        "mean_probability": {
            outcome: round(_mean(predictions[fixture_id][outcome] for fixture_id in fixture_ids), 5)
            for outcome in OUTCOMES
        },
    }


def paired_log_loss(
    challenger: dict[int, dict[str, float]],
    reference: dict[int, dict[str, float]],
    labels: dict[int, str],
) -> dict[str, float]:
    """Mean per-fixture log-loss difference (negative = challenger better)."""
    fixture_ids = sorted(set(challenger) & set(reference))
    differences = [
        _log_loss(challenger[fixture_id], labels[fixture_id]) - _log_loss(reference[fixture_id], labels[fixture_id])
        for fixture_id in fixture_ids
    ]
    if len(differences) < 2:
        return {"fixtures": len(differences), "mean_difference": 0.0, "standard_error": 0.0, "z": 0.0}
    mean = _mean(differences)
    variance = sum((value - mean) ** 2 for value in differences) / (len(differences) - 1)
    standard_error = math.sqrt(variance / len(differences))
    return {
        "fixtures": len(differences),
        "mean_difference": round(mean, 6),
        "standard_error": round(standard_error, 6),
        "z": round(mean / standard_error, 2) if standard_error > 0 else 0.0,
    }


def _log_loss(probabilities: dict[str, float], outcome: str) -> float:
    return -math.log(max(probabilities[outcome], 1e-12))


def _brier(probabilities: dict[str, float], outcome: str) -> float:
    return sum((probabilities[key] - (1.0 if key == outcome else 0.0)) ** 2 for key in OUTCOMES)


def _calibration_error(predictions: dict[int, dict[str, float]], labels: dict[int, str]) -> float:
    """Expected calibration error over every (outcome, probability) pair."""
    bins: list[list[tuple[float, float]]] = [[] for _ in range(CALIBRATION_BINS)]
    for fixture_id, probabilities in predictions.items():
        for outcome in OUTCOMES:
            probability = probabilities[outcome]
            index = min(int(probability * CALIBRATION_BINS), CALIBRATION_BINS - 1)
            bins[index].append((probability, 1.0 if labels[fixture_id] == outcome else 0.0))
    total = sum(len(entries) for entries in bins)
    if total == 0:
        return 0.0
    return sum(
        len(entries) / total * abs(_mean(p for p, _ in entries) - _mean(y for _, y in entries))
        for entries in bins
        if entries
    )


def _mean(values: Iterable[float]) -> float:
    items = list(values)
    return sum(items) / len(items) if items else 0.0


def _kickoff_slots(fixtures: list[CorpusFixture]) -> Iterable[list[CorpusFixture]]:
    slot: list[CorpusFixture] = []
    for fixture in fixtures:
        if slot and fixture.record.kickoff_at != slot[0].record.kickoff_at:
            yield slot
            slot = []
        slot.append(fixture)
    if slot:
        yield slot


# --- the report --------------------------------------------------------------


def run_walk_forward(
    fixtures: list[CorpusFixture],
    *,
    holdout_from: datetime,
    tuning_years: int = DEFAULT_TUNING_YEARS,
    tune: bool = True,
    champion: bool = True,
    progress: bool = False,
) -> dict[str, object]:
    labels = {fixture.record.id: fixture.result for fixture in fixtures}
    tuning_from = holdout_from.replace(year=holdout_from.year - tuning_years)
    holdout = [fixture for fixture in fixtures if fixture.record.kickoff_at >= holdout_from]

    tuned, trials = tune_elo(fixtures, tuning_from=tuning_from, holdout_from=holdout_from) if tune else (DEFAULT_ELO_CONFIG, [])
    models: dict[str, dict[int, dict[str, float]]] = {
        "league_frequency": league_frequency_predictions(fixtures, score_from=holdout_from),
        "elo": elo_predictions(fixtures, tuned, score_from=holdout_from),
        "elo_default": elo_predictions(fixtures, DEFAULT_ELO_CONFIG, score_from=holdout_from),
    }
    flags = elo_flags(fixtures, tuned, score_from=holdout_from)

    if champion:
        replay = ChampionReplay(fixtures)
        for name in ("model_a", "v1_blend", "champion", "market"):
            models[name] = {}
        for position, fixture in enumerate(holdout, start=1):
            result = replay.predict(fixture)
            for name, probabilities in result.items():
                if probabilities:
                    models[name][fixture.record.id] = probabilities
            if progress and position % 5000 == 0:
                print(f"champion replay: {position}/{len(holdout)}", file=sys.stderr)
        models["model_a_elo_mix"] = {
            fixture_id: {
                outcome: (probabilities[outcome] + models["elo"][fixture_id][outcome]) / 2 for outcome in OUTCOMES
            }
            for fixture_id, probabilities in models["model_a"].items()
        }

    all_ids = set(models["elo"])
    odds_ids = all_ids & set(models.get("market", {}))
    newcomer_ids = {
        fixture_id
        for fixture_id in all_ids
        if any(flag.endswith(("_newcomer_prior", "_unrated", "_rating_unestablished")) for flag in flags[fixture_id])
    }

    def scope(ids: set[int]) -> dict[str, object]:
        return {
            name: score({fixture_id: predictions[fixture_id] for fixture_id in ids if fixture_id in predictions}, labels)
            for name, predictions in models.items()
            if any(fixture_id in predictions for fixture_id in ids)
        }

    def restrict(name: str, ids: set[int]) -> dict[int, dict[str, float]]:
        return {fixture_id: models[name][fixture_id] for fixture_id in ids if fixture_id in models[name]}

    comparisons: dict[str, object] = {}
    for challenger, reference, ids in (
        ("elo", "league_frequency", all_ids),
        ("elo", "model_a", all_ids),
        ("elo", "v1_blend", all_ids),
        ("model_a_elo_mix", "model_a", all_ids),
        ("model_a_elo_mix", "v1_blend", all_ids),
        ("elo", "market", odds_ids),
        ("champion", "market", odds_ids),
    ):
        if challenger in models and reference in models:
            comparisons[f"{challenger}_vs_{reference}"] = paired_log_loss(
                restrict(challenger, ids), restrict(reference, ids), labels
            )

    expected_goals: dict[str, float] = {}
    if champion and replay.expected_goals:
        by_id = {fixture.record.id: fixture.record for fixture in holdout}
        pairs = [(lambdas, by_id[fixture_id]) for fixture_id, lambdas in sorted(replay.expected_goals.items())]
        expected_goals = {
            "fixtures": len(pairs),
            "model_a_mean_home_lambda": round(_mean(lambdas[0] for lambdas, _ in pairs), 4),
            "model_a_mean_away_lambda": round(_mean(lambdas[1] for lambdas, _ in pairs), 4),
            "actual_mean_home_goals": round(_mean(float(record.home_goals or 0) for _, record in pairs), 4),
            "actual_mean_away_goals": round(_mean(float(record.away_goals or 0) for _, record in pairs), 4),
        }

    agreement: dict[str, float] = {}
    if "model_a" in models:
        shared = sorted(set(models["model_a"]) & all_ids)
        spreads = [
            max(abs(models["elo"][fixture_id][outcome] - models["model_a"][fixture_id][outcome]) for outcome in OUTCOMES)
            for fixture_id in shared
        ]
        agreement = {
            "fixtures": len(spreads),
            "mean_max_spread": round(_mean(spreads), 5),
            "share_spread_over_0_10": round(_mean(1.0 if value > 0.10 else 0.0 for value in spreads), 5),
            "share_spread_over_0_20": round(_mean(1.0 if value > 0.20 else 0.0 for value in spreads), 5),
        }

    by_league: dict[str, object] = {}
    league_of = {fixture.record.id: fixture.league_key for fixture in holdout}
    league_ids: dict[str, set[int]] = defaultdict(set)
    for fixture_id in all_ids:
        league_ids[league_of[fixture_id]].add(fixture_id)
    for league_key in sorted(league_ids):
        ids = league_ids[league_key]
        entry = {"fixtures": len(ids), "elo_log_loss": score(restrict("elo", ids), labels)["log_loss"]}
        if "model_a" in models:
            entry["model_a_log_loss"] = score(restrict("model_a", ids), labels)["log_loss"]
            entry["v1_blend_log_loss"] = score(restrict("v1_blend", ids), labels)["log_loss"]
        by_league[league_key] = entry

    return {
        "holdout_from": holdout_from.date().isoformat(),
        "tuning_from": tuning_from.date().isoformat(),
        "corpus_fixtures": len(fixtures),
        "holdout_fixtures": len(holdout),
        "tuned_elo_config": {
            "k_factor": tuned.k_factor,
            "home_advantage": tuned.home_advantage,
            "season_regression": tuned.season_regression,
        },
        "tuning_trials": trials,
        "all_holdout": scope(all_ids),
        "holdout_with_odds": scope(odds_ids),
        "holdout_with_newcomer_or_unestablished_side": scope(newcomer_ids),
        "paired_log_loss": comparisons,
        "elo_model_a_agreement": agreement,
        "model_a_expected_goals_check": expected_goals,
        "by_league": by_league,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("paths", nargs="+", type=Path, help="Football-Data CSV files or directories")
    parser.add_argument("--holdout-from", default=DEFAULT_HOLDOUT_FROM, help="first holdout kick-off date, YYYY-MM-DD")
    parser.add_argument("--tuning-years", type=int, default=DEFAULT_TUNING_YEARS)
    parser.add_argument("--no-tune", action="store_true", help="score Elo with its defaults only")
    parser.add_argument("--no-champion", action="store_true", help="skip the engine replay (Elo and baselines only)")
    parser.add_argument("--output", type=Path, help="write the JSON report here as well as to stdout")
    args = parser.parse_args(argv)

    files = _csv_paths(args.paths)
    loaded = load_matches(files)
    fixtures = corpus_fixtures(loaded.matches)
    holdout_from = datetime.strptime(args.holdout_from, "%Y-%m-%d").replace(tzinfo=timezone.utc)
    report = run_walk_forward(
        fixtures,
        holdout_from=holdout_from,
        tuning_years=args.tuning_years,
        tune=not args.no_tune,
        champion=not args.no_champion,
        progress=True,
    )
    report = {
        "files": len(files),
        "duplicate_rows_skipped": loaded.duplicate_rows,
        "failed_files": len(loaded.report.failed_files),
        **report,
    }
    text = json.dumps(report, indent=2, sort_keys=True)
    if args.output:
        args.output.write_text(text + "\n", encoding="utf-8")
    print(text)
    return 0


def _csv_paths(values: Iterable[Path]) -> list[Path]:
    """CSV files under the given paths, skipping the corpus's combined copies.

    `_combined_main` and `_combined_extra` repeat every row of the per-league
    files; reading both would score each match twice.
    """
    files: list[Path] = []
    for value in values:
        candidates = sorted(value.rglob("*.csv")) if value.is_dir() else [value]
        files.extend(path for path in candidates if not any(part.startswith("_combined") for part in path.parts))
    return files


if __name__ == "__main__":
    raise SystemExit(main())
