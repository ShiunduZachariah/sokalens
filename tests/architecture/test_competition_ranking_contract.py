from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[2]
API = ROOT / "services" / "api" / "internal"

# Lists scoped to one competition or one team are correctly ordered by clock:
# a league page showing its own fixtures has nothing to rank. Only lists that
# span the whole catalogue and then truncate can bury a major competition.
CATALOGUE_WIDE = (
    API / "predictions" / "source.go",
    API / "matches" / "source.go",
    API / "search" / "source.go",
)


class CompetitionRankingContractTests(unittest.TestCase):
    def test_catalogue_wide_lists_rank_before_they_sort_by_clock(self) -> None:
        """A truncated day list ordered by clock alone hides the biggest leagues.

        On 2026-09-12 the site published 11,031 predictions for the day and
        served the first hundred by kickoff time: Western Australia NPL, three
        Korean divisions and a Mexican under-21 league. All eight Premier
        League fixtures were present and predicted, and none of them appeared.

        Ordering by `priority_score` first fixes that without imposing a
        ranking on the rest of the catalogue, because the score is 0 for
        everything except the competitions seeded in migration 000062 -- so
        unranked leagues keep exactly the clock order they had.
        """
        offenders = []
        for path in CATALOGUE_WIDE:
            source = path.read_text(encoding="utf-8")
            for match in re.finditer(r"^ORDER BY\s+(.+)$", source, re.M):
                clause = match.group(1)
                if "kickoff_at" not in clause:
                    continue
                # Descending clock order is a "most recent" list (results,
                # recent form), where recency is the point and ranking is not.
                if "kickoff_at DESC" in clause:
                    continue
                if "priority_score" in clause:
                    continue
                line = source[: match.start()].count("\n") + 1
                offenders.append(f"{path.relative_to(ROOT)}:{line}: {clause.strip()[:70]}")

        self.assertEqual(
            offenders,
            [],
            "these catalogue-wide lists sort by clock alone, which buries the "
            "major competitions below the truncation point; order by "
            "COALESCE(l.priority_score, 0) DESC first:\n" + "\n".join(offenders),
        )

    def test_the_major_competitions_are_actually_seeded(self) -> None:
        """The ordering is inert unless something populates the column.

        `priority_score` existed for this from the start and was 0 for 1,239 of
        1,240 leagues, because both ingestion paths insert a literal 0. That is
        why ordering by it had never been worth doing.
        """
        migration = ROOT / "infra" / "db" / "migrations" / "000062_seed_competition_priority.up.sql"
        self.assertTrue(migration.exists(), "the seed migration is missing")
        sql = migration.read_text(encoding="utf-8")

        for competition in ("Premier League", "La Liga", "Serie A", "Bundesliga", "Ligue 1", "UEFA Champions League"):
            self.assertIn(competition, sql, f"{competition} is not seeded")

        # Country-qualified, because league names are not unique: "Premier
        # League" also exists for AM, BB, BD, BH, BM and BT. A bare name match
        # is how Kenya's once outranked England's.
        self.assertIn("'GB'", sql)
        self.assertIn("country_code", sql)


if __name__ == "__main__":
    unittest.main()
