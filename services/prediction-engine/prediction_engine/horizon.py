from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timedelta


@dataclass(frozen=True)
class FixtureScheduleItem:
    fixture_id: int
    kickoff_at: datetime
    home_team_id: int
    away_team_id: int
    status: str


@dataclass(frozen=True)
class HorizonEmbargoDecision:
    fixture_id: int
    kickoff_at: datetime
    days_to_kickoff: float
    horizon_eligible: bool
    embargo_active: bool = False
    embargo_reason: str | None = None
    embargo_blocking_fixture_id: int | None = None


def horizon_embargo_check(
    fixture: FixtureScheduleItem,
    *,
    now: datetime,
    team_schedule: list[FixtureScheduleItem],
    horizon_days: int = 7,
) -> HorizonEmbargoDecision:
    if horizon_days <= 0:
        raise ValueError("horizon_days must be positive")
    days_to_kickoff = (fixture.kickoff_at - now).total_seconds() / 86400
    if fixture.kickoff_at < now or fixture.kickoff_at >= now + timedelta(days=horizon_days + 1):
        return HorizonEmbargoDecision(
            fixture_id=fixture.fixture_id,
            kickoff_at=fixture.kickoff_at,
            days_to_kickoff=days_to_kickoff,
            horizon_eligible=False,
            embargo_reason="outside_prediction_window",
        )

    blocking = _first_blocking_interleaving_fixture(fixture, team_schedule)
    if blocking is not None:
        team_id = fixture.home_team_id if _team_plays(blocking, fixture.home_team_id) else fixture.away_team_id
        return HorizonEmbargoDecision(
            fixture_id=fixture.fixture_id,
            kickoff_at=fixture.kickoff_at,
            days_to_kickoff=days_to_kickoff,
            horizon_eligible=True,
            embargo_active=True,
            embargo_reason=f"Team {team_id} has unfinished fixture {blocking.fixture_id} before kickoff.",
            embargo_blocking_fixture_id=blocking.fixture_id,
        )

    return HorizonEmbargoDecision(
        fixture_id=fixture.fixture_id,
        kickoff_at=fixture.kickoff_at,
        days_to_kickoff=days_to_kickoff,
        horizon_eligible=True,
    )


def _first_blocking_interleaving_fixture(
    target: FixtureScheduleItem,
    team_schedule: list[FixtureScheduleItem],
) -> FixtureScheduleItem | None:
    candidates = [
        fixture
        for fixture in team_schedule
        if fixture.fixture_id != target.fixture_id
        and target.kickoff_at > fixture.kickoff_at
        and (_team_plays(fixture, target.home_team_id) or _team_plays(fixture, target.away_team_id))
        and not _is_finished(fixture.status)
    ]
    return min(candidates, key=lambda fixture: fixture.kickoff_at) if candidates else None


def _team_plays(fixture: FixtureScheduleItem, team_id: int) -> bool:
    return fixture.home_team_id == team_id or fixture.away_team_id == team_id


def _is_finished(status: str) -> bool:
    return status.strip().upper() in {"FT", "AET", "PEN", "FINISHED"}
