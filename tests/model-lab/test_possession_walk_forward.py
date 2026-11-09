"""The possession evidence harness: as-of discipline and the report's shape."""

from __future__ import annotations

import random
import unittest

from model_lab.possession_walk_forward import Fixture, Observation, evaluate, share_fit, walk_forward_observations


def round_robin(teams: int = 8, rounds: int = 10, season: int = 2026) -> list[Fixture]:
    fixtures: list[Fixture] = []
    next_id = 1
    for round_number in range(rounds):
        for home in range(1, teams + 1):
            away = (home + round_number) % teams + 1
            if home == away:
                continue
            fixtures.append(Fixture(next_id, 1_000 + round_number * 100, season, 39, home, away))
            next_id += 1
    return fixtures


class WalkForwardTests(unittest.TestCase):
    def test_a_fixture_never_reads_its_own_match_or_a_later_one(self) -> None:
        # Team 1's only readings are after kick-off, apart from `min_samples`
        # earlier ones holding 60. A leak would move the expectation off 60.
        fixtures = [Fixture(index, index * 100, 2026, 39, 1, 2) for index in range(1, 9)]
        possession = {index: {1: 60.0, 2: 40.0} for index in range(1, 6)}
        possession[6] = {1: 90.0, 2: 10.0}
        possession[7] = {1: 90.0, 2: 10.0}
        possession[8] = {1: 90.0, 2: 10.0}

        rows = walk_forward_observations(fixtures, possession, min_samples=5)

        self.assertEqual(rows[0].kickoff, 600)
        self.assertEqual(rows[0].expected, 60.0)

    def test_too_few_readings_are_not_an_observation(self) -> None:
        fixtures = [Fixture(index, index * 100, 2026, 39, 1, 2) for index in range(1, 5)]
        possession = {index: {1: 55.0, 2: 45.0} for index in range(1, 5)}

        self.assertEqual(walk_forward_observations(fixtures, possession, min_samples=5), [])
        self.assertEqual(len(walk_forward_observations(fixtures, possession, min_samples=3)), 1)

    def test_a_split_that_is_not_a_match_is_not_a_result(self) -> None:
        fixtures = [Fixture(index, index * 100, 2026, 39, 1, 2) for index in range(1, 8)]
        possession = {index: {1: 55.0, 2: 45.0} for index in range(1, 7)}
        possession[7] = {1: 0.0, 2: 0.0}

        rows = walk_forward_observations(fixtures, possession, min_samples=5)

        self.assertNotIn(700, [row.kickoff for row in rows])

    def test_seasons_older_than_last_are_not_read(self) -> None:
        fixtures = [Fixture(index, index * 100, 2024, 39, 1, 2) for index in range(1, 7)]
        fixtures.append(Fixture(7, 700, 2026, 39, 1, 2))
        possession = {index: {1: 55.0, 2: 45.0} for index in range(1, 8)}

        rows = walk_forward_observations(fixtures, possession, min_samples=5)

        self.assertNotIn(700, [row.kickoff for row in rows])

    def test_the_outcome_follows_the_split(self) -> None:
        self.assertEqual(Observation(0, 39, 50.0, 55.0, 45.0).outcome, "home_more")
        self.assertEqual(Observation(0, 39, 50.0, 45.0, 55.0).outcome, "away_more")
        self.assertEqual(Observation(0, 39, 50.0, 50.0, 50.0).outcome, "level")


class EvaluationTests(unittest.TestCase):
    def synthetic(self, sd: float, size: int = 800) -> list[Observation]:
        rng = random.Random(11)
        rows: list[Observation] = []
        for index in range(size):
            expected = rng.uniform(38.0, 62.0)
            share = round(min(max(rng.gauss(expected, sd), 5.0), 95.0))
            rows.append(Observation(index, 39, expected, float(share), float(100 - share)))
        return rows

    def test_the_fit_recovers_the_noise_it_was_given(self) -> None:
        fit = share_fit(self.synthetic(9.5))

        self.assertAlmostEqual(fit["residual_sd_points"], 9.5, delta=0.6)
        self.assertAlmostEqual(fit["slope"], 1.0, delta=0.1)
        self.assertAlmostEqual(fit["implied_precision"], 26.0, delta=4.0)

    def test_the_shipped_curve_beats_both_references_when_its_assumptions_hold(self) -> None:
        report = evaluate(self.synthetic(9.5))

        losses = report["holdout_log_loss"]
        self.assertLess(losses["shipped"], losses["base_rate"])
        self.assertLess(losses["base_rate"], losses["previous"])
        self.assertGreater(report["shipped_gain_over_base_rate_95ci"][0], 0.0)

    def test_the_report_exposes_its_holdout_and_calibration(self) -> None:
        report = evaluate(self.synthetic(9.5), holdout_fraction=0.25)

        self.assertEqual(report["fixtures"], 800)
        self.assertEqual(report["holdout_fixtures"], 200)
        self.assertEqual(sum(cell["n"] for cell in report["holdout_top_pick"]["bins"].values()), 200)


if __name__ == "__main__":
    unittest.main()
