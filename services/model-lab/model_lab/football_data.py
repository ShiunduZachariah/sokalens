from __future__ import annotations

import argparse
import csv
import json
import math
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Iterable


RESULTS = {"H", "D", "A"}
ONE_X_TWO_DECISION_THRESHOLD = 0.05
TEAM_OU_GOALS_HOME_OVER_THRESHOLD = 1.0
TEAM_OU_GOALS_AWAY_OVER_THRESHOLD = 1.0
HT_FT_FULLTIME_THRESHOLD = 0.1
HT_FT_HALFTIME_THRESHOLD = 0.1
ONE_X_TWO_CLASS_ORDER = ["H", "D", "A"]
ODDS_COLUMN_GROUPS = [
    ("PSCH", "PSCD", "PSCA"),
    ("AvgCH", "AvgCD", "AvgCA"),
    ("MaxCH", "MaxCD", "MaxCA"),
]
MARKET_KEYS = [
    "1x2",
    "double_chance",
    "btts",
    "ou_0_5",
    "ou_1_5",
    "ou_2_5",
    "ou_3_5",
    "ou_4_5",
    "ou_5_5",
    "team_ou_goals",
    "ht_ft",
    "corners",
    "cards",
    "fouls",
    "throw_ins",
]


@dataclass(frozen=True)
class FailedFile:
    path: str
    error: str


@dataclass(frozen=True)
class ParserReport:
    parsed_files: int
    failed_files: list[FailedFile]
    row_error_count: int


@dataclass(frozen=True)
class Match:
    source_file: str
    row_number: int
    country: str
    league: str
    season: str
    kickoff: datetime
    home_team: str
    away_team: str
    home_goals: int
    away_goals: int
    result: str
    features: dict[str, str]
    half_time_home_goals: int | None = None
    half_time_away_goals: int | None = None
    home_corners: int | None = None
    away_corners: int | None = None
    home_cards: int | None = None
    away_cards: int | None = None
    home_fouls: int | None = None
    away_fouls: int | None = None
    home_throw_ins: int | None = None
    away_throw_ins: int | None = None
    odds_home: float | None = None
    odds_draw: float | None = None
    odds_away: float | None = None


@dataclass(frozen=True)
class LoadResult:
    matches: list[Match]
    missing_result_rows: int
    duplicate_rows: int
    report: ParserReport


@dataclass(frozen=True)
class Prediction:
    match: Match
    predicted_result: str
    actual_result: str


@dataclass(frozen=True)
class BacktestResult:
    train_size: int
    total_tested: int
    correct: int
    accuracy: float
    predictions: list[Prediction]


@dataclass(frozen=True)
class MarketBenchmark:
    total: int
    correct: int
    accuracy: float
    coverage: float


@dataclass(frozen=True)
class MarketBenchmarkResult:
    train_size: int
    total_tested: int
    markets: dict[str, MarketBenchmark]


@dataclass(frozen=True)
class OneXTwoBenchmarks:
    total_tested: int
    odds_available: int
    actual_draw_rate: float
    predicted_draw_rate_heuristic: float
    always_home_accuracy: float
    odds_favorite_accuracy: float
    logistic_regression_accuracy: float
    logistic_regression_total: int
    logistic_regression_coverage: float


@dataclass
class TeamHistory:
    played: int = 0
    points: int = 0
    goals_for: int = 0
    goals_against: int = 0
    half_time_scored: int = 0
    half_time_allowed: int = 0
    half_time_samples: int = 0
    corners: int = 0
    corners_samples: int = 0
    cards: int = 0
    cards_samples: int = 0
    fouls: int = 0
    fouls_samples: int = 0
    throw_ins: int = 0
    throw_ins_samples: int = 0
    home_played: int = 0
    home_points: int = 0
    away_played: int = 0
    away_points: int = 0
    recent_points: deque[int] = field(default_factory=lambda: deque(maxlen=10))
    recent_goal_diff: deque[int] = field(default_factory=lambda: deque(maxlen=10))
    home_recent_points: deque[int] = field(default_factory=lambda: deque(maxlen=10))
    away_recent_points: deque[int] = field(default_factory=lambda: deque(maxlen=10))


def load_matches(paths: Iterable[str | Path]) -> LoadResult:
    matches: list[Match] = []
    missing_result_rows = 0
    duplicate_rows = 0
    row_error_count = 0
    parsed_files = 0
    failed_files: list[FailedFile] = []
    seen: set[tuple[str, datetime, str, str, int, int, str]] = set()

    for path_like in paths:
        path = Path(path_like)
        try:
            rows = _read_csv_rows(path)
        except Exception as exc:  # noqa: BLE001
            failed_files.append(FailedFile(path=str(path), error=str(exc)))
            continue

        parsed_files += 1
        for row_number, row in rows:
            if _is_empty_row(row):
                continue
            try:
                normalized = _normalize_row(path, row_number, row)
            except ValueError:
                row_error_count += 1
                continue

            if normalized is None:
                missing_result_rows += 1
                continue

            duplicate_key = (
                normalized.league,
                normalized.kickoff,
                normalized.home_team.casefold(),
                normalized.away_team.casefold(),
                normalized.home_goals,
                normalized.away_goals,
                normalized.result,
            )
            if duplicate_key in seen:
                duplicate_rows += 1
                continue
            seen.add(duplicate_key)
            matches.append(normalized)

    matches.sort(key=lambda match: (match.kickoff, match.league, match.home_team, match.away_team))
    return LoadResult(
        matches=matches,
        missing_result_rows=missing_result_rows,
        duplicate_rows=duplicate_rows,
        report=ParserReport(parsed_files=parsed_files, failed_files=failed_files, row_error_count=row_error_count),
    )


def run_backtest(matches: Iterable[Match], train_fraction: float = 0.7) -> BacktestResult:
    chronological = sorted(matches, key=lambda match: (match.kickoff, match.league, match.home_team, match.away_team))
    if not chronological:
        return BacktestResult(train_size=0, total_tested=0, correct=0, accuracy=0.0, predictions=[])

    split = int(len(chronological) * train_fraction)
    split = min(max(split, 1), len(chronological))
    stats: dict[str, TeamHistory] = {}
    for match in chronological[:split]:
        _record_match(stats, match)

    predictions: list[Prediction] = []
    correct = 0
    for match in chronological[split:]:
        predicted = _predict_1x2(stats, match.home_team, match.away_team)
        predictions.append(Prediction(match=match, predicted_result=predicted, actual_result=match.result))
        if predicted == match.result:
            correct += 1
        _record_match(stats, match)

    total = len(predictions)
    accuracy = correct / total if total else 0.0
    return BacktestResult(train_size=split, total_tested=total, correct=correct, accuracy=accuracy, predictions=predictions)


def run_market_benchmarks(matches: Iterable[Match], train_fraction: float = 0.7) -> MarketBenchmarkResult:
    chronological = sorted(matches, key=lambda match: (match.kickoff, match.league, match.home_team, match.away_team))
    if not chronological:
        return MarketBenchmarkResult(train_size=0, total_tested=0, markets={key: MarketBenchmark(0, 0, 0.0, 0.0) for key in MARKET_KEYS})

    split = int(len(chronological) * train_fraction)
    split = min(max(split, 1), len(chronological))
    stats: dict[str, TeamHistory] = {}
    for match in chronological[:split]:
        _record_match(stats, match)

    counters = {key: {"total": 0, "correct": 0} for key in MARKET_KEYS}
    tested = chronological[split:]
    for match in tested:
        predictions = _predict_markets(stats, match)
        actual = _actual_market_outcomes(match)
        for market in MARKET_KEYS:
            predicted = predictions.get(market)
            actual_value = actual.get(market)
            if predicted is None or actual_value is None:
                continue
            counters[market]["total"] += 1
            if predicted == actual_value:
                counters[market]["correct"] += 1
        _record_match(stats, match)

    result: dict[str, MarketBenchmark] = {}
    tested_count = len(tested)
    for market in MARKET_KEYS:
        total = counters[market]["total"]
        correct = counters[market]["correct"]
        result[market] = MarketBenchmark(
            total=total,
            correct=correct,
            accuracy=(correct / total) if total else 0.0,
            coverage=(total / tested_count) if tested_count else 0.0,
        )
    return MarketBenchmarkResult(train_size=split, total_tested=tested_count, markets=result)


def run_one_x_two_benchmarks(matches: Iterable[Match], train_fraction: float = 0.7) -> OneXTwoBenchmarks:
    chronological = sorted(matches, key=lambda match: (match.kickoff, match.league, match.home_team, match.away_team))
    if not chronological:
        return OneXTwoBenchmarks(
            total_tested=0,
            odds_available=0,
            actual_draw_rate=0.0,
            predicted_draw_rate_heuristic=0.0,
            always_home_accuracy=0.0,
            odds_favorite_accuracy=0.0,
            logistic_regression_accuracy=0.0,
            logistic_regression_total=0,
            logistic_regression_coverage=0.0,
        )

    split = int(len(chronological) * train_fraction)
    split = min(max(split, 1), len(chronological))
    stats: dict[str, TeamHistory] = {}

    train_vectors: list[list[float]] = []
    train_labels: list[int] = []
    test_vectors: list[list[float]] = []
    test_labels: list[int] = []
    odds_available = 0
    odds_favorite_correct = 0
    draw_actual = 0
    draw_predicted_heuristic = 0
    heuristic_correct = 0
    always_home_correct = 0

    for idx, match in enumerate(chronological):
        actual = match.result
        if idx >= split:
            if actual == "D":
                draw_actual += 1
            always_home_correct += int(actual == "H")

        heuristic = _predict_1x2(stats, match.home_team, match.away_team)
        if idx >= split:
            draw_predicted_heuristic += int(heuristic == "D")
            heuristic_correct += int(heuristic == actual)

        feature_vector = _one_x_two_feature_vector(stats, match)
        implied = _implied_probabilities(match)
        if idx >= split and implied:
            odds_available += 1
            odds_favorite = ONE_X_TWO_CLASS_ORDER[_argmax(implied)]
            odds_favorite_correct += int(odds_favorite == actual)

        if feature_vector is not None:
            label = _label_to_index(actual)
            if idx < split:
                train_vectors.append(feature_vector)
                train_labels.append(label)
            else:
                test_vectors.append(feature_vector)
                test_labels.append(label)

        _record_match(stats, match)

    logistic_accuracy = 0.0
    logistic_total = 0
    if train_vectors and test_vectors:
        scaler = _fit_standard_scaler(train_vectors)
        train_scaled = [_transform_features(vector, scaler) for vector in train_vectors]
        test_scaled = [_transform_features(vector, scaler) for vector in test_vectors]
        model = _train_multinomial_logistic_regression(
            train_scaled,
            train_labels,
            class_count=len(ONE_X_TWO_CLASS_ORDER),
            epochs=6,
            learning_rate=0.04,
            l2=1e-4,
        )
        predicted = [_predict_class(model, vector) for vector in test_scaled]
        correct = sum(int(p == y) for p, y in zip(predicted, test_labels))
        logistic_total = len(test_labels)
        logistic_accuracy = correct / logistic_total if logistic_total else 0.0

    total_tested = len(chronological) - split
    return OneXTwoBenchmarks(
        total_tested=total_tested,
        odds_available=odds_available,
        actual_draw_rate=(draw_actual / total_tested) if total_tested else 0.0,
        predicted_draw_rate_heuristic=(draw_predicted_heuristic / total_tested) if total_tested else 0.0,
        always_home_accuracy=(always_home_correct / total_tested) if total_tested else 0.0,
        odds_favorite_accuracy=(odds_favorite_correct / odds_available) if odds_available else 0.0,
        logistic_regression_accuracy=logistic_accuracy,
        logistic_regression_total=logistic_total,
        logistic_regression_coverage=(logistic_total / total_tested) if total_tested else 0.0,
    )


def _read_csv_rows(path: Path) -> list[tuple[int, dict[str, str | None]]]:
    for encoding in ("utf-8-sig", "cp1252"):
        try:
            with path.open(newline="", encoding=encoding) as handle:
                reader = csv.DictReader(handle, restkey="__extra__")
                rows = []
                for row_number, row in enumerate(reader, start=2):
                    if row.get("__extra__"):
                        row["__extra__"] = None
                    rows.append((row_number, row))
                return rows
        except UnicodeDecodeError:
            continue
    raise UnicodeDecodeError("unknown", b"", 0, 1, f"unable to decode {path}")


def _normalize_row(path: Path, row_number: int, row: dict[str, str | None]) -> Match | None:
    home_goals_raw = _first(row, "FTHG", "HG")
    away_goals_raw = _first(row, "FTAG", "AG")
    result = _first(row, "FTR", "Res").upper()
    if not home_goals_raw or not away_goals_raw or result not in RESULTS:
        return None

    home_goals = int(home_goals_raw)
    away_goals = int(away_goals_raw)
    home_team = _first(row, "HomeTeam", "Home")
    away_team = _first(row, "AwayTeam", "Away")
    kickoff = _parse_kickoff(_first(row, "Date"), _first(row, "Time"))
    country = _first(row, "Country")
    league = _first(row, "League", "Div")
    season = _first(row, "Season")
    odds_home, odds_draw, odds_away = _extract_closing_odds(row)

    features = {
        "country": country,
        "league": league,
        "season": season,
        "kickoff_date": kickoff.date().isoformat(),
        "kickoff_time": kickoff.strftime("%H:%M") if _first(row, "Time") else "",
        "home_team": home_team,
        "away_team": away_team,
    }
    return Match(
        source_file=str(path),
        row_number=row_number,
        country=country,
        league=league,
        season=season,
        kickoff=kickoff,
        home_team=home_team,
        away_team=away_team,
        home_goals=home_goals,
        away_goals=away_goals,
        result=result,
        features=features,
        half_time_home_goals=_optional_int(_first(row, "HTHG")),
        half_time_away_goals=_optional_int(_first(row, "HTAG")),
        home_corners=_optional_int(_first(row, "HC")),
        away_corners=_optional_int(_first(row, "AC")),
        home_cards=_optional_int(_first(row, "HY"), _first(row, "HR")),
        away_cards=_optional_int(_first(row, "AY"), _first(row, "AR")),
        home_fouls=_optional_int(_first(row, "HF")),
        away_fouls=_optional_int(_first(row, "AF")),
        odds_home=odds_home,
        odds_draw=odds_draw,
        odds_away=odds_away,
    )


def _first(row: dict[str, str | None], *names: str) -> str:
    for name in names:
        value = row.get(name)
        if value is not None and value.strip():
            return value.replace("\xa0", " ").strip()
    return ""


def _optional_int(*values: str) -> int | None:
    for value in values:
        if value:
            return int(value)
    return None


def _optional_float(*values: str) -> float | None:
    for value in values:
        if not value:
            continue
        try:
            parsed = float(value)
        except ValueError:
            continue
        if parsed > 1.0:
            return parsed
    return None


def _extract_closing_odds(row: dict[str, str | None]) -> tuple[float | None, float | None, float | None]:
    for home_col, draw_col, away_col in ODDS_COLUMN_GROUPS:
        home = _optional_float(_first(row, home_col))
        draw = _optional_float(_first(row, draw_col))
        away = _optional_float(_first(row, away_col))
        if home and draw and away:
            return home, draw, away
    return None, None, None


def _is_empty_row(row: dict[str, str | None]) -> bool:
    return all((value is None or not str(value).strip()) for value in row.values())


def _parse_kickoff(date_value: str, time_value: str) -> datetime:
    value = f"{date_value} {time_value or '00:00'}"
    for fmt in ("%d/%m/%Y %H:%M", "%d/%m/%y %H:%M"):
        try:
            return datetime.strptime(value, fmt)
        except ValueError:
            continue
    raise ValueError(f"unsupported Football-Data date/time: {value!r}")


def _record_match(stats: dict[str, TeamHistory], match: Match) -> None:
    home = stats.setdefault(match.home_team, TeamHistory())
    away = stats.setdefault(match.away_team, TeamHistory())
    home.played += 1
    away.played += 1
    home.goals_for += match.home_goals
    home.goals_against += match.away_goals
    away.goals_for += match.away_goals
    away.goals_against += match.home_goals

    if match.result == "H":
        home.points += 3
        home_points_earned = 3
        away_points_earned = 0
    elif match.result == "A":
        away.points += 3
        home_points_earned = 0
        away_points_earned = 3
    else:
        home.points += 1
        away.points += 1
        home_points_earned = 1
        away_points_earned = 1

    home.home_played += 1
    home.home_points += home_points_earned
    away.away_played += 1
    away.away_points += away_points_earned
    home.recent_points.append(home_points_earned)
    away.recent_points.append(away_points_earned)
    home.home_recent_points.append(home_points_earned)
    away.away_recent_points.append(away_points_earned)
    home.recent_goal_diff.append(match.home_goals - match.away_goals)
    away.recent_goal_diff.append(match.away_goals - match.home_goals)

    if match.half_time_home_goals is not None and match.half_time_away_goals is not None:
        home.half_time_scored += match.half_time_home_goals
        home.half_time_allowed += match.half_time_away_goals
        home.half_time_samples += 1
        away.half_time_scored += match.half_time_away_goals
        away.half_time_allowed += match.half_time_home_goals
        away.half_time_samples += 1

    _record_optional_stat(home, away, match.home_corners, match.away_corners, "corners")
    _record_optional_stat(home, away, match.home_cards, match.away_cards, "cards")
    _record_optional_stat(home, away, match.home_fouls, match.away_fouls, "fouls")
    _record_optional_stat(home, away, match.home_throw_ins, match.away_throw_ins, "throw_ins")


def _record_optional_stat(home: TeamHistory, away: TeamHistory, home_value: int | None, away_value: int | None, key: str) -> None:
    if home_value is None or away_value is None:
        return
    if key == "corners":
        home.corners += home_value
        away.corners += away_value
        home.corners_samples += 1
        away.corners_samples += 1
        return
    if key == "cards":
        home.cards += home_value
        away.cards += away_value
        home.cards_samples += 1
        away.cards_samples += 1
        return
    if key == "fouls":
        home.fouls += home_value
        away.fouls += away_value
        home.fouls_samples += 1
        away.fouls_samples += 1
        return
    if key == "throw_ins":
        home.throw_ins += home_value
        away.throw_ins += away_value
        home.throw_ins_samples += 1
        away.throw_ins_samples += 1


def _predict_markets(stats: dict[str, TeamHistory], match: Match) -> dict[str, str | None]:
    home = stats.get(match.home_team, TeamHistory())
    away = stats.get(match.away_team, TeamHistory())

    home_ppm = _points_per_match(home)
    away_ppm = _points_per_match(away)
    diff = home_ppm - away_ppm
    predicted_1x2 = _predict_1x2(stats, match.home_team, match.away_team)

    expected_home_goals = _expected_goals(home, away)
    expected_away_goals = _expected_goals(away, home)
    expected_total = expected_home_goals + expected_away_goals
    predicted_btts = "yes" if min(expected_home_goals, expected_away_goals) > 0.85 else "no"

    predictions: dict[str, str | None] = {
        "1x2": predicted_1x2,
        "double_chance": "home_draw" if diff >= 0 else "draw_away",
        "btts": predicted_btts,
        "ou_0_5": "over" if expected_total > 0.5 else "under",
        "ou_1_5": "over" if expected_total > 1.5 else "under",
        "ou_2_5": "over" if expected_total > 2.5 else "under",
        "ou_3_5": "over" if expected_total > 3.5 else "under",
        "ou_4_5": "over" if expected_total > 4.5 else "under",
        "ou_5_5": "over" if expected_total > 5.5 else "under",
        "team_ou_goals": _predict_team_over_under(expected_home_goals, expected_away_goals),
        "ht_ft": _predict_ht_ft(home, away),
        "corners": _predict_stat_over_under(home, away, "corners", 9.5),
        "cards": _predict_stat_over_under(home, away, "cards", 3.5),
        "fouls": _predict_stat_over_under(home, away, "fouls", 21.5),
        "throw_ins": _predict_stat_over_under(home, away, "throw_ins", 24.5),
    }
    return predictions


def _actual_market_outcomes(match: Match) -> dict[str, str | None]:
    outcomes: dict[str, str | None] = {
        "1x2": match.result,
        "double_chance": "home_draw" if match.result == "H" else "draw_away" if match.result == "A" else "home_draw",
        "btts": "yes" if match.home_goals > 0 and match.away_goals > 0 else "no",
        "ou_0_5": "over" if (match.home_goals + match.away_goals) > 0 else "under",
        "ou_1_5": "over" if (match.home_goals + match.away_goals) > 1 else "under",
        "ou_2_5": "over" if (match.home_goals + match.away_goals) > 2 else "under",
        "ou_3_5": "over" if (match.home_goals + match.away_goals) > 3 else "under",
        "ou_4_5": "over" if (match.home_goals + match.away_goals) > 4 else "under",
        "ou_5_5": "over" if (match.home_goals + match.away_goals) > 5 else "under",
        "team_ou_goals": _actual_team_over_under(match),
        "ht_ft": _actual_ht_ft(match),
        "corners": _actual_over_under_from_optionals(match.home_corners, match.away_corners, 9.5),
        "cards": _actual_over_under_from_optionals(match.home_cards, match.away_cards, 3.5),
        "fouls": _actual_over_under_from_optionals(match.home_fouls, match.away_fouls, 21.5),
        "throw_ins": _actual_over_under_from_optionals(match.home_throw_ins, match.away_throw_ins, 24.5),
    }
    return outcomes


def _one_x_two_feature_vector(stats: dict[str, TeamHistory], match: Match) -> list[float] | None:
    implied = _implied_probabilities(match)
    if implied is None:
        return None

    home = stats.get(match.home_team, TeamHistory())
    away = stats.get(match.away_team, TeamHistory())
    home_ppm = _points_per_match(home)
    away_ppm = _points_per_match(away)
    home_home_ppm = _home_points_per_match(home)
    away_away_ppm = _away_points_per_match(away)
    home_recent5 = _average_points(home.recent_points, 5) / 3
    away_recent5 = _average_points(away.recent_points, 5) / 3
    home_recent10 = _average_points(home.recent_points, 10) / 3
    away_recent10 = _average_points(away.recent_points, 10) / 3
    home_gd5 = _average(home.recent_goal_diff, 5)
    away_gd5 = _average(away.recent_goal_diff, 5)
    home_expected = _expected_goals(home, away)
    away_expected = _expected_goals(away, home)

    return [
        1.0,
        implied[0],
        implied[1],
        implied[2],
        home_ppm,
        away_ppm,
        home_ppm - away_ppm,
        home_home_ppm,
        away_away_ppm,
        home_home_ppm - away_away_ppm,
        home_recent5,
        away_recent5,
        home_recent5 - away_recent5,
        home_recent10,
        away_recent10,
        home_recent10 - away_recent10,
        home_gd5,
        away_gd5,
        home_gd5 - away_gd5,
        home_expected,
        away_expected,
        home_expected - away_expected,
    ]


def _implied_probabilities(match: Match) -> tuple[float, float, float] | None:
    if not match.odds_home or not match.odds_draw or not match.odds_away:
        return None
    inv_home = 1 / match.odds_home
    inv_draw = 1 / match.odds_draw
    inv_away = 1 / match.odds_away
    total = inv_home + inv_draw + inv_away
    if total <= 0:
        return None
    return inv_home / total, inv_draw / total, inv_away / total


def _home_points_per_match(team_stats: TeamHistory | None) -> float:
    if not team_stats or team_stats.home_played == 0:
        return 0.0
    return team_stats.home_points / team_stats.home_played


def _away_points_per_match(team_stats: TeamHistory | None) -> float:
    if not team_stats or team_stats.away_played == 0:
        return 0.0
    return team_stats.away_points / team_stats.away_played


def _average_points(values: deque[int], limit: int) -> float:
    if not values:
        return 0.0
    selected = list(values)[-limit:]
    return sum(selected) / len(selected)


def _average(values: deque[int], limit: int) -> float:
    if not values:
        return 0.0
    selected = list(values)[-limit:]
    return sum(selected) / len(selected)


def _label_to_index(label: str) -> int:
    return ONE_X_TWO_CLASS_ORDER.index(label)


def _argmax(values: tuple[float, ...] | list[float]) -> int:
    best_index = 0
    best_value = values[0]
    for index, value in enumerate(values[1:], start=1):
        if value > best_value:
            best_index = index
            best_value = value
    return best_index


def _fit_standard_scaler(samples: list[list[float]]) -> tuple[list[float], list[float]]:
    feature_count = len(samples[0])
    means = [0.0] * feature_count
    for row in samples:
        for index, value in enumerate(row):
            means[index] += value
    means = [value / len(samples) for value in means]
    variances = [0.0] * feature_count
    for row in samples:
        for index, value in enumerate(row):
            diff = value - means[index]
            variances[index] += diff * diff
    stds = [math.sqrt(value / len(samples)) if value > 0 else 1.0 for value in variances]
    stds = [std if std > 1e-9 else 1.0 for std in stds]
    return means, stds


def _transform_features(sample: list[float], scaler: tuple[list[float], list[float]]) -> list[float]:
    means, stds = scaler
    transformed: list[float] = []
    for index, value in enumerate(sample):
        if index == 0:
            transformed.append(1.0)
            continue
        transformed.append((value - means[index]) / stds[index])
    return transformed


def _train_multinomial_logistic_regression(
    samples: list[list[float]],
    labels: list[int],
    class_count: int,
    epochs: int,
    learning_rate: float,
    l2: float,
) -> list[list[float]]:
    feature_count = len(samples[0])
    weights = [[0.0] * feature_count for _ in range(class_count)]
    for epoch in range(epochs):
        rate = learning_rate / (1 + epoch * 0.15)
        for sample, label in zip(samples, labels):
            probabilities = _softmax_logits(weights, sample)
            for class_index in range(class_count):
                target = 1.0 if class_index == label else 0.0
                gradient = probabilities[class_index] - target
                for feature_index, value in enumerate(sample):
                    regularization = l2 * weights[class_index][feature_index]
                    weights[class_index][feature_index] -= rate * (gradient * value + regularization)
    return weights


def _softmax_logits(weights: list[list[float]], sample: list[float]) -> list[float]:
    logits = []
    for class_weights in weights:
        logits.append(sum(weight * value for weight, value in zip(class_weights, sample)))
    maximum = max(logits)
    exp_values = [math.exp(value - maximum) for value in logits]
    total = sum(exp_values)
    return [value / total for value in exp_values]


def _predict_class(weights: list[list[float]], sample: list[float]) -> int:
    probabilities = _softmax_logits(weights, sample)
    return _argmax(probabilities)


def _predict_1x2(stats: dict[str, TeamHistory], home_team: str, away_team: str) -> str:
    home_rating = _points_per_match(stats.get(home_team))
    away_rating = _points_per_match(stats.get(away_team))
    difference = home_rating - away_rating
    if difference > ONE_X_TWO_DECISION_THRESHOLD:
        return "H"
    if difference < -ONE_X_TWO_DECISION_THRESHOLD:
        return "A"
    return "D"


def _points_per_match(team_stats: TeamHistory | None) -> float:
    if not team_stats or team_stats.played == 0:
        return 0.0
    return team_stats.points / team_stats.played


def _expected_goals(attack_team: TeamHistory, defense_team: TeamHistory) -> float:
    attack = (attack_team.goals_for / attack_team.played) if attack_team.played else 1.0
    concession = (defense_team.goals_against / defense_team.played) if defense_team.played else 1.0
    return (attack * 0.6) + (concession * 0.4)


def _predict_team_over_under(expected_home_goals: float, expected_away_goals: float) -> str:
    if expected_home_goals > TEAM_OU_GOALS_HOME_OVER_THRESHOLD:
        return "home_over_1_5"
    if expected_away_goals > TEAM_OU_GOALS_AWAY_OVER_THRESHOLD:
        return "away_over_1_5"
    if expected_home_goals >= expected_away_goals:
        return "away_under_1_5"
    return "home_under_1_5"


def _actual_team_over_under(match: Match) -> str:
    if match.home_goals > 1:
        return "home_over_1_5"
    if match.away_goals > 1:
        return "away_over_1_5"
    if match.home_goals >= match.away_goals:
        return "away_under_1_5"
    return "home_under_1_5"


def _predict_ht_ft(home: TeamHistory, away: TeamHistory) -> str | None:
    if home.half_time_samples == 0 or away.half_time_samples == 0:
        return None

    full_diff = _points_per_match(home) - _points_per_match(away)
    predicted_full_time = "H" if full_diff > HT_FT_FULLTIME_THRESHOLD else "A" if full_diff < -HT_FT_FULLTIME_THRESHOLD else "D"
    home_half = home.half_time_scored / home.half_time_samples
    away_half = away.half_time_scored / away.half_time_samples
    half = "H" if (home_half - away_half) > HT_FT_HALFTIME_THRESHOLD else "A" if (away_half - home_half) > HT_FT_HALFTIME_THRESHOLD else "D"
    return _ht_ft_selection(half, predicted_full_time)


def _actual_ht_ft(match: Match) -> str | None:
    if match.half_time_home_goals is None or match.half_time_away_goals is None:
        return None
    half = "H" if match.half_time_home_goals > match.half_time_away_goals else "A" if match.half_time_home_goals < match.half_time_away_goals else "D"
    return _ht_ft_selection(half, match.result)


def _ht_ft_selection(half: str, full: str) -> str:
    return {
        ("H", "H"): "ht_home_ft_home",
        ("H", "D"): "ht_home_ft_draw",
        ("H", "A"): "ht_home_ft_away",
        ("D", "H"): "ht_draw_ft_home",
        ("D", "D"): "ht_draw_ft_draw",
        ("D", "A"): "ht_draw_ft_away",
        ("A", "H"): "ht_away_ft_home",
        ("A", "D"): "ht_away_ft_draw",
        ("A", "A"): "ht_away_ft_away",
    }[(half, full)]


def _predict_stat_over_under(home: TeamHistory, away: TeamHistory, key: str, threshold: float) -> str | None:
    home_total, home_samples = _stat_values(home, key)
    away_total, away_samples = _stat_values(away, key)
    if home_samples == 0 or away_samples == 0:
        return None
    expected = (home_total / home_samples) + (away_total / away_samples)
    return "over" if expected > threshold else "under"


def _actual_over_under_from_optionals(home: int | None, away: int | None, threshold: float) -> str | None:
    if home is None or away is None:
        return None
    return "over" if (home + away) > threshold else "under"


def _stat_values(team: TeamHistory, key: str) -> tuple[int, int]:
    if key == "corners":
        return team.corners, team.corners_samples
    if key == "cards":
        return team.cards, team.cards_samples
    if key == "fouls":
        return team.fouls, team.fouls_samples
    return team.throw_ins, team.throw_ins_samples


def main() -> int:
    parser = argparse.ArgumentParser(description="Run an offline Football-Data chronological baseline backtest.")
    parser.add_argument("csv", nargs="+", type=Path, help="Football-Data CSV file(s) to read directly.")
    parser.add_argument("--train-fraction", type=float, default=0.7)
    args = parser.parse_args()

    csv_paths = _expand_csv_paths(args.csv)
    loaded = load_matches(csv_paths)
    backtest = run_backtest(loaded.matches, train_fraction=args.train_fraction)
    market_benchmarks = run_market_benchmarks(loaded.matches, train_fraction=args.train_fraction)
    one_x_two_benchmarks = run_one_x_two_benchmarks(loaded.matches, train_fraction=args.train_fraction)
    print(
        json.dumps(
            {
                "matches": len(loaded.matches),
                "missing_result_rows": loaded.missing_result_rows,
                "duplicate_rows": loaded.duplicate_rows,
                "train_size": backtest.train_size,
                "total_tested": backtest.total_tested,
                "tested_matches": backtest.total_tested,
                "correct": backtest.correct,
                "successful_predictions": backtest.correct,
                "failed_predictions": backtest.total_tested - backtest.correct,
                "accuracy": round(backtest.accuracy, 4),
                "hit_rate": round(backtest.accuracy, 4),
                "parser_report": {
                    "parsed_files": loaded.report.parsed_files,
                    "failed_files": [{"path": item.path, "error": item.error} for item in loaded.report.failed_files],
                    "row_error_count": loaded.report.row_error_count,
                },
                "market_benchmarks": {
                    "train_size": market_benchmarks.train_size,
                    "total_tested": market_benchmarks.total_tested,
                    "markets": {
                        key: {
                            "total": item.total,
                            "tested_matches": item.total,
                            "correct": item.correct,
                            "successful_predictions": item.correct,
                            "failed_predictions": item.total - item.correct,
                            "accuracy": round(item.accuracy, 4),
                            "hit_rate": round(item.accuracy, 4),
                            "coverage": round(item.coverage, 4),
                        }
                        for key, item in market_benchmarks.markets.items()
                    },
                },
                "one_x_two_benchmarks": {
                    "total_tested": one_x_two_benchmarks.total_tested,
                    "odds_available": one_x_two_benchmarks.odds_available,
                    "actual_draw_rate": round(one_x_two_benchmarks.actual_draw_rate, 4),
                    "predicted_draw_rate_heuristic": round(one_x_two_benchmarks.predicted_draw_rate_heuristic, 4),
                    "always_home_accuracy": round(one_x_two_benchmarks.always_home_accuracy, 4),
                    "odds_favorite_accuracy": round(one_x_two_benchmarks.odds_favorite_accuracy, 4),
                    "logistic_regression_accuracy": round(one_x_two_benchmarks.logistic_regression_accuracy, 4),
                    "logistic_regression_total": one_x_two_benchmarks.logistic_regression_total,
                    "logistic_regression_coverage": round(one_x_two_benchmarks.logistic_regression_coverage, 4),
                },
            },
            sort_keys=True,
        )
    )
    return 0


def _expand_csv_paths(values: Iterable[Path]) -> list[Path]:
    expanded: list[Path] = []
    for value in values:
        if value.is_dir():
            expanded.extend(sorted(value.rglob("*.csv")))
        else:
            expanded.append(value)
    return expanded


if __name__ == "__main__":
    raise SystemExit(main())
