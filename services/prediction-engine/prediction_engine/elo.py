"""Elo team-strength ratings: architecture v2 Model B (section 8.2).

Model A reads goals: scoring and conceding rates, rescaled between leagues.
This model reads results. A rating moves only when a team does better or worse
than its rating said it would, so it can genuinely agree or disagree with
Model A instead of restating the same goal rates through another formula. That
independence is the whole reason it exists: section 8.5's ensemble can only
measure agreement between models that are built differently.

It is the v2 meaning of "Model B". `model_b.py` is v1's form adjustment, which
is still live in the blend; v2 relocates that adjustment into Model A's rate
calculation and reassigns the letter to this model (section 8.6). The two are
unrelated, and this one carries zero ensemble weight until a walk-forward
evaluation shows it earns some.

**Ratings are league-relative.** A rating measures a team against its own
league, and only matches between two teams of the same league update it. A
result between leagues (a continental tie, a cup tie across tiers) is evidence
about the leagues, not the teams, so it moves a per-league offset instead; the
offset starts neutral and stays neutral for a league that never plays outside
itself. This is the same split `team_strength.py` and `league_strength.py`
make for goal rates.

**New and promoted teams inherit a measured prior** (section 5D.5). A team
entering a league that already has history starts at that league's newcomer
level: how far below or above the league mean earlier arrivals settled once
they had played a run of matches there, shrunk toward a default while few
arrivals have been seen. A team in a league's first observed season starts at
the league mean. At each new season a rating regresses part of the way back
to its league mean, because squads change over a summer and a rating that
never forgets overstates last season.

**Point-in-time discipline** (section 3.4). A prediction replays only finished
fixtures that kicked off strictly before both the target and the as-of
cutoff, in kick-off order, so a fixture's own result can never reach the
rating used to predict it.

**Probabilities.** The rating difference, plus a home edge, maps to 1X2
through an ordered logit. Its draw threshold reproduces the league's observed
draw rate for two equal teams, and its slope matches the Elo expected-score
curve near zero, so the probabilities agree with the update rule that
produced the ratings. Over/under and BTTS are deliberately absent: a
result-only rating says nothing about how many goals a match will have, and
deriving them would route back through goal rates, the input this model
exists to avoid. The shadow's `challenger_markets` adds them outside this model,
from the rating gap and the league's goal rate only.
"""

from __future__ import annotations

from collections import Counter, defaultdict
from dataclasses import dataclass, field
from datetime import datetime
import math
from typing import Iterable

from prediction_engine.features import (
    FixtureRecord,
    _is_finished,
    is_competitive_league,
    is_continental_competition,
)


MODEL_ID = "elo-v1"


@dataclass(frozen=True)
class EloConfig:
    # Rating points a team is credited with on the scale everyone starts on.
    initial_rating: float = 1500.0
    # Points at stake per match before the margin multiplier. This and the
    # home edge are the values the walk-forward tuning window chose
    # (docs/prediction-engine/elo-challenger.md), not hand-set.
    k_factor: float = 15.0
    # Home edge in rating points, added to the home side's expectation only.
    home_advantage: float = 50.0
    # Share of a team's distance from its league mean removed at a new season.
    season_regression: float = 0.20
    # A newcomer's starting level relative to the league mean, before any
    # arrivals to that league have been observed. Promoted sides usually
    # finish below the middle of the table.
    newcomer_offset: float = -60.0
    # Observed arrivals before a league's measured newcomer level outweighs
    # the default above.
    newcomer_shrinkage: int = 5
    # League matches after which an arrival's rating is recorded as evidence
    # about where newcomers to that league settle.
    newcomer_settle_matches: int = 10
    # Learning rate for the between-league offsets. Small, because a handful
    # of cup ties is weak evidence about two whole leagues.
    league_k_factor: float = 4.0
    max_league_offset: float = 400.0
    # A goal margin beyond this counts as this. Corrupt rows with penalty
    # aggregates in the score columns must not swing a rating by hundreds.
    max_margin: int = 5
    # League draw rate for two equal teams, shrunk toward the prior. Learning
    # the draw threshold online instead calibrated slightly better but scored
    # worse on log loss in the walk-forward test, so the simpler count stays.
    draw_rate_prior: float = 0.26
    draw_rate_prior_matches: int = 200
    min_draw_rate: float = 0.15
    max_draw_rate: float = 0.40
    # Same-league matches a team needs before its rating counts as
    # established rather than a prior.
    established_matches: int = 10
    # Appearances in the current season before they, rather than last
    # season's league, decide which league a team belongs to. Stops an early
    # cup tie from reassigning a side to the cup.
    season_membership_matches: int = 3


DEFAULT_ELO_CONFIG = EloConfig()


@dataclass(frozen=True)
class EloPrediction:
    model_id: str
    home_rating: float
    away_rating: float
    rating_differential: float
    match_result: dict[str, float]
    double_chance: dict[str, float]
    draw_no_bet: dict[str, float]
    home_sample: int
    away_sample: int
    draw_rate: float
    fallback_flags: tuple[str, ...] = field(default_factory=tuple)

    @property
    def markets(self) -> dict[str, dict[str, float]]:
        return {
            "match_result": dict(self.match_result),
            "double_chance": dict(self.double_chance),
            "draw_no_bet": dict(self.draw_no_bet),
        }


class EloRatingBook:
    """Ratings as they stand after the fixtures recorded so far.

    Feed fixtures in kick-off order through `record`, and ask `predict` for a
    fixture before recording it. `run_elo` does exactly that from a history
    list; a walk-forward evaluation can drive one book through a whole corpus
    instead of replaying the history for every fixture, and gets the same
    numbers.
    """

    def __init__(self, config: EloConfig = DEFAULT_ELO_CONFIG) -> None:
        self._config = config
        # League-relative rating per team; the league mean sits near zero.
        self._ratings: dict[int, float] = {}
        self._team_league: dict[int, int] = {}
        self._team_season: dict[int, int] = {}
        self._same_league_matches: Counter[int] = Counter()
        self._season_appearances: dict[tuple[int, int], Counter[int]] = defaultdict(Counter)
        self._league_members: dict[int, set[int]] = defaultdict(set)
        self._league_first_season: dict[int, int] = {}
        self._league_offsets: dict[int, float] = defaultdict(float)
        self._league_matches: Counter[int] = Counter()
        self._league_draws: Counter[int] = Counter()
        self._newcomer_evidence: dict[int, list[float]] = defaultdict(list)
        # Arrivals still settling: team -> (league, matches played there).
        self._settling: dict[int, tuple[int, int]] = {}
        self._prior_used: set[int] = set()

    # --- prediction ---------------------------------------------------------

    def predict(self, fixture: FixtureRecord) -> EloPrediction:
        config = self._config
        home_league = self._league_for(fixture.home_team_id, fixture)
        away_league = self._league_for(fixture.away_team_id, fixture)
        flags: list[str] = []
        home_relative = self._rating_for_prediction(fixture.home_team_id, home_league, fixture.season, "home", flags)
        away_relative = self._rating_for_prediction(fixture.away_team_id, away_league, fixture.season, "away", flags)

        home_rating = config.initial_rating + home_relative
        away_rating = config.initial_rating + away_relative
        if home_league != away_league:
            flags.append("cross_league")
            home_rating += self._league_offsets[home_league]
            away_rating += self._league_offsets[away_league]
            draw_rate = _clamp(config.draw_rate_prior, config.min_draw_rate, config.max_draw_rate)
        else:
            draw_rate = self._draw_rate(home_league)

        differential = home_rating - away_rating + config.home_advantage
        home, draw, away = _ordered_logit(differential, draw_rate)
        decisive = home + away
        return EloPrediction(
            model_id=MODEL_ID,
            home_rating=home_rating,
            away_rating=away_rating,
            rating_differential=differential,
            match_result={"home": home, "draw": draw, "away": away},
            double_chance={
                "home_or_draw": home + draw,
                "draw_or_away": draw + away,
                "home_or_away": decisive,
            },
            draw_no_bet={"home": home / decisive, "away": away / decisive},
            home_sample=self._same_league_matches[fixture.home_team_id],
            away_sample=self._same_league_matches[fixture.away_team_id],
            draw_rate=draw_rate,
            fallback_flags=tuple(flags),
        )

    # --- recording results --------------------------------------------------

    def record(self, fixture: FixtureRecord) -> None:
        """Fold one finished fixture into the ratings. Others are ignored."""
        if not _is_finished(fixture) or not is_competitive_league(fixture.league_name):
            return
        config = self._config
        home_id, away_id = fixture.home_team_id, fixture.away_team_id
        continental = is_continental_competition(fixture.league_name)
        if continental and not (home_id in self._ratings and away_id in self._ratings):
            # A continental tie says how two leagues compare only when both
            # sides already have a domestic level to compare from.
            return
        home_league = self._enter(home_id, fixture)
        away_league = self._enter(away_id, fixture)
        if not continental:
            self._note_appearance(home_id, fixture)
            self._note_appearance(away_id, fixture)

        home_goals = int(fixture.home_goals or 0)
        away_goals = int(fixture.away_goals or 0)
        actual = 1.0 if home_goals > away_goals else 0.5 if home_goals == away_goals else 0.0
        margin_weight = _margin_multiplier(min(abs(home_goals - away_goals), config.max_margin))

        if home_league == away_league:
            differential = self._ratings[home_id] - self._ratings[away_id] + config.home_advantage
            delta = config.k_factor * margin_weight * (actual - _expected_score(differential))
            self._ratings[home_id] += delta
            self._ratings[away_id] -= delta
            self._league_matches[home_league] += 1
            if actual == 0.5:
                self._league_draws[home_league] += 1
            for team_id in (home_id, away_id):
                self._same_league_matches[team_id] += 1
                self._advance_settling(team_id, home_league, fixture.season)
            return

        # Between leagues: the result is evidence about the two leagues.
        expected = _expected_score(
            config.initial_rating + self._ratings[home_id] + self._league_offsets[home_league]
            - config.initial_rating - self._ratings[away_id] - self._league_offsets[away_league]
            + config.home_advantage
        )
        delta = config.league_k_factor * margin_weight * (actual - expected)
        self._league_offsets[home_league] = _clamp(
            self._league_offsets[home_league] + delta, -config.max_league_offset, config.max_league_offset
        )
        self._league_offsets[away_league] = _clamp(
            self._league_offsets[away_league] - delta, -config.max_league_offset, config.max_league_offset
        )

    # --- membership and priors ---------------------------------------------

    def _note_appearance(self, team_id: int, fixture: FixtureRecord) -> None:
        self._season_appearances[(team_id, fixture.season)][fixture.league_id] += 1
        first = self._league_first_season.get(fixture.league_id)
        if first is None or fixture.season < first:
            self._league_first_season[fixture.league_id] = fixture.season

    def _league_for(self, team_id: int, fixture: FixtureRecord) -> int:
        """The league a team belongs to for this fixture, without recording it."""
        counts = self._season_appearances.get((team_id, fixture.season))
        if counts and sum(counts.values()) >= self._config.season_membership_matches:
            # Most appearances this season; ties break on the lower league id.
            return min(counts.items(), key=lambda item: (-item[1], item[0]))[0]
        known = self._team_league.get(team_id)
        if known is None:
            return fixture.league_id
        if known != fixture.league_id and not is_continental_competition(fixture.league_name):
            # A promoted side's first matches of the season are against teams
            # already in its new league. A cup tie is not: the opponent belongs
            # to its own domestic league, not to the cup.
            opponent_id = fixture.away_team_id if team_id == fixture.home_team_id else fixture.home_team_id
            if self._team_league.get(opponent_id) == fixture.league_id:
                return fixture.league_id
        return known

    def _rating_for_prediction(
        self, team_id: int, league_id: int, season: int, side: str, flags: list[str]
    ) -> float:
        """What this team's rating would be at kick-off, without changing it."""
        if team_id not in self._ratings:
            flags.append(f"{side}_unrated")
            return self._starting_rating(league_id, season)
        rating = self._ratings[team_id]
        if self._team_league.get(team_id) != league_id:
            flags.append(f"{side}_newcomer_prior")
            return self._starting_rating(league_id, season)
        if self._team_season.get(team_id, season) < season:
            rating = self._regressed(rating, league_id, season)
        if team_id in self._prior_used or self._same_league_matches[team_id] < self._config.established_matches:
            flags.append(f"{side}_rating_unestablished")
        return rating

    def _enter(self, team_id: int, fixture: FixtureRecord) -> int:
        """Place a team in its league for this fixture, applying any prior."""
        league_id = self._league_for(team_id, fixture)
        current = self._team_league.get(team_id)
        if team_id not in self._ratings or current != league_id:
            if current is not None:
                self._league_members[current].discard(team_id)
            self._ratings[team_id] = self._starting_rating(league_id, fixture.season)
            self._team_league[team_id] = league_id
            self._league_members[league_id].add(team_id)
            self._same_league_matches[team_id] = 0
            if self._is_established_league(league_id, fixture.season):
                self._settling[team_id] = (league_id, 0)
                self._prior_used.add(team_id)
        elif self._team_season.get(team_id, fixture.season) < fixture.season:
            self._ratings[team_id] = self._regressed(self._ratings[team_id], league_id, fixture.season)
        self._team_season[team_id] = fixture.season
        return league_id

    def _starting_rating(self, league_id: int, season: int) -> float:
        if not self._is_established_league(league_id, season):
            return self._league_mean(league_id, season)
        evidence = self._newcomer_evidence.get(league_id, [])
        shrinkage = self._config.newcomer_shrinkage
        offset = (sum(evidence) + self._config.newcomer_offset * shrinkage) / (len(evidence) + shrinkage)
        return self._league_mean(league_id, season) + offset

    def _is_established_league(self, league_id: int, season: int) -> bool:
        first = self._league_first_season.get(league_id)
        return first is not None and first < season

    def _advance_settling(self, team_id: int, league_id: int, season: int) -> None:
        settling = self._settling.get(team_id)
        if settling is None or settling[0] != league_id:
            return
        played = settling[1] + 1
        if played < self._config.newcomer_settle_matches:
            self._settling[team_id] = (league_id, played)
            return
        del self._settling[team_id]
        self._prior_used.discard(team_id)
        self._newcomer_evidence[league_id].append(
            self._ratings[team_id] - self._league_mean(league_id, season)
        )

    def _league_mean(self, league_id: int, season: int) -> float:
        """Mean rating of the league's current sides.

        A team that dropped out of the covered leagues keeps its last rating
        but is no longer part of the league; counting it would drag the mean
        toward every relegated side ever seen.
        """
        members = [
            team_id
            for team_id in self._league_members.get(league_id, ())
            if self._team_season.get(team_id, season) >= season - 1
        ]
        if not members:
            return 0.0
        return sum(self._ratings[team_id] for team_id in members) / len(members)

    def _regressed(self, rating: float, league_id: int, season: int) -> float:
        mean = self._league_mean(league_id, season)
        return mean + ((rating - mean) * (1 - self._config.season_regression))

    def _draw_rate(self, league_id: int) -> float:
        config = self._config
        observed = self._league_draws[league_id]
        matches = self._league_matches[league_id]
        rate = (observed + config.draw_rate_prior * config.draw_rate_prior_matches) / (
            matches + config.draw_rate_prior_matches
        )
        return _clamp(rate, config.min_draw_rate, config.max_draw_rate)


def run_elo(
    target: FixtureRecord,
    history: Iterable[FixtureRecord],
    *,
    as_of_timestamp: datetime,
    config: EloConfig = DEFAULT_ELO_CONFIG,
) -> EloPrediction:
    """Elo 1X2 for one fixture from the history knowable at `as_of_timestamp`."""
    if as_of_timestamp.tzinfo is None or as_of_timestamp.utcoffset() is None:
        raise ValueError("as_of_timestamp must be timezone-aware")
    prior = sorted(
        (
            record
            for record in history
            if record.id != target.id
            and record.kickoff_at < target.kickoff_at
            and record.kickoff_at < as_of_timestamp
            and _is_finished(record)
        ),
        key=lambda record: (record.kickoff_at, record.id),
    )
    book = EloRatingBook(config)
    for record in prior:
        book.record(record)
    return book.predict(target)


def _expected_score(differential: float) -> float:
    return 1.0 / (1.0 + 10 ** (-differential / 400.0))


def _margin_multiplier(margin: int) -> float:
    """World Football Elo's goal-difference weight: a rout says more than a 1-0."""
    if margin <= 1:
        return 1.0
    if margin == 2:
        return 1.5
    return (11 + margin) / 8


def _ordered_logit(differential: float, draw_rate: float) -> tuple[float, float, float]:
    """Home/draw/away probabilities from a rating difference.

    The threshold c gives P(draw) = draw_rate when the difference is zero. The
    slope is chosen so the expected score P(home) + P(draw)/2 has the same
    gradient at zero as the Elo curve, which keeps these probabilities on the
    scale the update rule assumes.
    """
    threshold = math.log((1 + draw_rate) / (1 - draw_rate))
    slope = (math.log(10) / 400.0) / (1 - draw_rate**2)
    home = _sigmoid(slope * differential - threshold)
    away = _sigmoid(-slope * differential - threshold)
    draw = max(1.0 - home - away, 0.0)
    total = home + draw + away
    return home / total, draw / total, away / total


def _sigmoid(value: float) -> float:
    if value >= 0:
        return 1.0 / (1.0 + math.exp(-value))
    exponent = math.exp(value)
    return exponent / (1.0 + exponent)


def _clamp(value: float, minimum: float, maximum: float) -> float:
    return min(max(value, minimum), maximum)
