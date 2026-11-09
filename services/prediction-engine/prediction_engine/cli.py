from __future__ import annotations

import argparse
from datetime import UTC, datetime, timedelta
import json
import os
import sys
from typing import Callable, TextIO

from prediction_engine.db_repository import (
    DEFAULT_MAX_PENDING_AGE_HOURS,
    DEFAULT_NOT_PLAYED_GRACE_HOURS,
    AccuracySnapshotRepository,
    EnginePredictionRepository,
    PredictionRetentionRepository,
    AccuracyRecountStats,
    InPlaySnapshotRepository,
    TrackerStateRepository,
    PredictionRetentionStats,
    PredictionWriteStats,
    PredictionInputRepository,
    PredictionSettlementRepository,
)
from prediction_engine.inplay_model import DEFAULT_IN_PLAY_CONFIG, RegenerationTier
from prediction_engine.inplay_runner import InPlayRunStats, run_and_save_in_play_fixture
from prediction_engine.pipeline import BackfillDeferred, run_and_save_fixture, run_fixture
from prediction_engine.settlement import STATUS_REVERSED
from prediction_engine.tracker import MatchState
from prediction_engine.config import default_config


# The scope names the in-play path answers to. Nothing else reaches it:
# architecture v2 7.5 requires the in-play branch be selected explicitly, so
# a caller that does not name it gets the pre-match path exactly as before.
IN_PLAY_SCOPE_CHOICES = ("in_play", "inplay")


# Shadow challengers (architecture v2 Models B and C). Read-and-write on their
# own tables only; see prediction_engine/challengers.py.
CHALLENGER_COMMANDS = ("run-challengers", "settle-challengers", "challenger-report")


ConnectFunc = Callable[[str], object]
RunnerFunc = Callable[..., list[object]]


def main(
    argv: list[str] | None = None,
    *,
    connect: ConnectFunc | None = None,
    runner: RunnerFunc = run_and_save_fixture,
    preview_runner: RunnerFunc = run_fixture,
    settlement_runner: RunnerFunc | None = None,
    accuracy_runner: RunnerFunc | None = None,
    in_play_runner: RunnerFunc | None = None,
    # Injected by tests that need to drive the settlement repository's own
    # branching -- notably the re-settlement sweep, whose per-fixture isolation
    # is only observable when one fixture raises. The existing `settlement_runner`
    # seam cannot express that, because the sweep calls the repository directly
    # rather than through a runner.
    settlement_repository_override: object | None = None,
    stdout: TextIO = sys.stdout,
) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    database_url = args.database_url or os.environ.get("DATABASE_URL", "")
    if not database_url:
        print("database URL is required; pass --database-url or set DATABASE_URL", file=stdout)
        return 2

    generated_at = _parse_generated_at(args.generated_at)
    connection = (connect or _connect)(database_url)
    input_repository = PredictionInputRepository(connection)  # type: ignore[arg-type]
    lifecycle_stability = default_config().v2.lifecycle_stability
    output_repository = EnginePredictionRepository(
        connection,  # type: ignore[arg-type]
        jsonb_adapter=_jsonb_adapter if connect is None else None,
        lifecycle_stability_tolerance=lifecycle_stability.tolerance,
        lifecycle_direction_change_penalty=lifecycle_stability.direction_change_penalty,
        lifecycle_neutral_score=lifecycle_stability.neutral_score,
    )
    settlement_repository = settlement_repository_override or PredictionSettlementRepository(connection)  # type: ignore[arg-type]
    accuracy_repository = AccuracySnapshotRepository(connection)  # type: ignore[arg-type]
    retention_repository = PredictionRetentionRepository(connection)  # type: ignore[arg-type]

    if args.command in CHALLENGER_COMMANDS:
        return _run_challenger_command(
            args,
            connection=connection,
            input_repository=input_repository,
            generated_at=generated_at,
            stdout=stdout,
        )

    if args.command == "preview-btts-corrections":
        report = settlement_repository.preview_btts_corrections(args.fixture_id, limit=args.limit)
        print(json.dumps(report, sort_keys=True), file=stdout)
        return 0

    if args.command == "aggregate-prune-results":
        raw_cutoff = generated_at - timedelta(days=args.raw_retention_days)
        retention_stats = PredictionRetentionStats()
        for _ in range(args.max_batches):
            batch_stats = retention_repository.aggregate_and_prune(
                raw_cutoff=raw_cutoff,
                batch_limit=args.batch_limit,
            )
            retention_stats += batch_stats
            if batch_stats.results_examined < args.batch_limit:
                break
        print(
            json.dumps(
                {
                    "public_detail_hours": args.public_detail_hours,
                    "raw_cutoff": raw_cutoff.isoformat(),
                    "raw_retention_days": args.raw_retention_days,
                    "results_examined": retention_stats.results_examined,
                    "results_pruned": retention_stats.results_pruned,
                    "scope": "retention",
                    "snapshots_written": retention_stats.snapshots_written,
                },
                sort_keys=True,
            ),
            file=stdout,
        )
        return 0

    if args.command == "snapshot-accuracy":
        active_accuracy_runner = accuracy_runner or _snapshot_accuracy
        snapshots = active_accuracy_runner(
            snapshot_date=generated_at.date(),
            accuracy_repository=accuracy_repository,
        )
        print(
            json.dumps(
                {
                    "snapshot_date": generated_at.date().isoformat(),
                    "snapshots_written": len(snapshots),
                    "scope": "accuracy",
                },
                sort_keys=True,
            ),
            file=stdout,
        )
        return 0

    if args.command == "settle-fixture":
        active_settlement_runner = settlement_runner or _settle_fixture
        results = active_settlement_runner(
            args.fixture_id,
            settled_at=generated_at,
            settlement_repository=settlement_repository,
        )
        print(
            json.dumps(
                {
                    "fixture_id": args.fixture_id,
                    "results_written": len(results),
                    "scope": "settlement",
                },
                sort_keys=True,
            ),
            file=stdout,
        )
        return 0

    if args.command == "settle-scope":
        active_settlement_runner = settlement_runner or _settle_fixture
        fixture_ids = settlement_repository.list_settlement_fixture_ids(
            scope=args.scope,
            limit=args.limit,
            now=generated_at,
        )
        # One fixture must not be able to discard the batch.
        #
        # settle_fixture rolls back and re-raises, and this loop used to have no
        # guard, so a single unsettleable fixture abandoned settlement for every
        # fixture queued behind it. The trigger in production was an in-play
        # market with no arm in `_settlement_outcome`: the raise is correct --
        # an unknown market must never be guessed at -- but the blast radius
        # was not.
        #
        # Exception, not BaseException: an operator interrupting the worker is
        # not a settlement failure and must still stop the process.
        results_written = 0
        fixtures_settled = 0
        failures: list[dict[str, object]] = []
        for fixture_id in fixture_ids:
            try:
                results = active_settlement_runner(
                    fixture_id,
                    settled_at=generated_at,
                    settlement_repository=settlement_repository,
                )
            except Exception as error:  # noqa: BLE001 - per-fixture isolation is the point
                failures.append({"fixture_id": fixture_id, "error": f"{type(error).__name__}: {error}"})
                continue
            results_written += len(results)
            fixtures_settled += 1
        if failures:
            # Capped the way the model_registry_id skip message is capped: a bad
            # batch must not emit an unbounded log line.
            shown = ", ".join(str(failure["fixture_id"]) for failure in failures[:10])
            print(
                f"settlement failed on {len(failures)} of {len(fixture_ids)} fixture(s): {shown}"
                f"{' ...' if len(failures) > 10 else ''}",
                flush=True,
            )
        print(
            json.dumps(
                {
                    "fixture_ids": fixture_ids,
                    "fixtures_processed": len(fixture_ids),
                    "fixtures_settled": fixtures_settled,
                    "fixtures_failed": len(failures),
                    "failures": failures,
                    "results_written": results_written,
                    "scope": "settlement",
                    "settlement_scope": args.scope,
                },
                sort_keys=True,
            ),
            file=stdout,
        )
        # Non-zero on any failure, after attempting every fixture. Exiting zero
        # on a partial batch is how this defect stayed invisible for a month.
        # Settlement is idempotent -- `_CURRENT_UNSETTLED_PREDICTIONS_SQL`
        # excludes rows that already have a `prediction_results` entry -- so the
        # progress this run made is kept and a retry only picks up what is left.
        # Jobs carry bounded `max_attempts`, so a permanently failing fixture
        # stops rather than spinning.
        return 1 if failures else 0

    if args.command == "resettle-retracted":
        # The provider is the source of truth. A withdrawn goal must withdraw the
        # grades it decided, however long after the whistle it arrives.
        #
        # Per-fixture isolation for the same reason settle-scope has it: one
        # fixture that cannot be re-graded must not abandon the rest, and a
        # partial run must still exit non-zero so the failure is not silent.
        fixture_ids = settlement_repository.list_retracted_settlement_fixture_ids(limit=args.limit)
        examined = 0
        changed = 0
        reversed_count = 0
        failures: list[dict[str, object]] = []
        for fixture_id in fixture_ids:
            try:
                outcomes = settlement_repository.resettle_fixture(fixture_id, recorded_at=generated_at)
            except Exception as error:  # noqa: BLE001 - per-fixture isolation is the point
                failures.append({"fixture_id": fixture_id, "error": f"{type(error).__name__}: {error}"})
                continue
            examined += len(outcomes)
            changed += sum(1 for outcome in outcomes if outcome.changed)
            reversed_count += sum(
                1 for outcome in outcomes if outcome.changed and outcome.result.status == STATUS_REVERSED
            )
        if failures:
            shown = ", ".join(str(failure["fixture_id"]) for failure in failures[:10])
            print(
                f"re-settlement failed on {len(failures)} of {len(fixture_ids)} fixture(s): {shown}"
                f"{' ...' if len(failures) > 10 else ''}",
                flush=True,
            )
        print(
            json.dumps(
                {
                    "fixture_ids": fixture_ids,
                    "fixtures_processed": len(fixture_ids),
                    "fixtures_failed": len(failures),
                    "failures": failures,
                    "predictions_examined": examined,
                    "predictions_changed": changed,
                    "predictions_reversed": reversed_count,
                    "scope": "resettlement",
                },
                sort_keys=True,
            ),
            file=stdout,
        )
        return 1 if failures else 0

    if args.command == "refresh-trackers":
        # Runs over the live scope, so every fixture with a match in progress has
        # its pre-match picks re-judged. Picks are read with the kickoff guard: a
        # tracker is a public surface and an in-play row must never be served as
        # a pre-match pick.
        tracker_repository = TrackerStateRepository(connection)  # type: ignore[arg-type]
        fixture_ids = input_repository.list_candidate_fixture_ids(
            scope=IN_PLAY_SCOPE_CHOICES[0],
            limit=args.limit,
            as_of_timestamp=generated_at,
        )
        totals: dict[str, int] = {}
        failures: list[dict[str, object]] = []
        for fixture_id in fixture_ids:
            live = input_repository.load_in_play_state(fixture_id, as_of_timestamp=generated_at)
            if live is None:
                continue
            try:
                counts = tracker_repository.refresh_tracker_state(
                    fixture_id,
                    state=MatchState(
                        home_goals=live.snapshot.home_goals or 0,
                        away_goals=live.snapshot.away_goals or 0,
                        finished=False,
                    ),
                    match_minute=live.snapshot.elapsed_minute,
                    computed_at=generated_at,
                )
            except Exception as error:  # noqa: BLE001 - one fixture must not end the sweep
                connection.rollback()  # type: ignore[attr-defined]
                failures.append({"fixture_id": fixture_id, "error": f"{type(error).__name__}: {error}"})
                continue
            for state_name, count in counts.items():
                totals[state_name] = totals.get(state_name, 0) + count
        print(
            json.dumps(
                {
                    "failures": failures,
                    "fixtures_failed": len(failures),
                    "fixtures_processed": len(fixture_ids),
                    "scope": "tracker_state",
                    "states": totals,
                },
                sort_keys=True,
            ),
            file=stdout,
        )
        return 1 if failures else 0

    if args.command == "finalise-snapshots":
        # The live poll drops a fixture the moment it ends, so no live pass ever
        # sees a match at the final whistle. Without this sweep the permanent
        # full-time record would never be written.
        examined, frozen = InPlaySnapshotRepository(connection).finalise_snapshots(  # type: ignore[arg-type]
            limit=args.limit
        )
        print(
            json.dumps(
                {
                    "fixtures_examined": examined,
                    "scope": "snapshot_finalisation",
                    "snapshots_frozen": frozen,
                },
                sort_keys=True,
            ),
            file=stdout,
        )
        return 0

    if args.command == "recount-reversed":
        # The other half of reversible settlement: resettle-retracted marks
        # needs_recount, this applies it. Without this pass a withdrawn result
        # stays in published accuracy for ever.
        recount_stats = AccuracyRecountStats()
        for _ in range(args.max_batches):
            batch = retention_repository.recount_reversed_results(limit=args.limit)
            recount_stats += batch
            if batch.flagged_examined == 0:
                break
        print(
            json.dumps(
                {
                    "buckets_adjusted": recount_stats.buckets_adjusted,
                    "flagged_examined": recount_stats.flagged_examined,
                    "flags_cleared": recount_stats.flags_cleared,
                    "predictions_recounted": recount_stats.predictions_recounted,
                    "scope": "accuracy_recount",
                    "unrecountable": recount_stats.unrecountable,
                },
                sort_keys=True,
            ),
            file=stdout,
        )
        # Unrecountable rows are a reported debt, not a run failure: they predate
        # the ledger recording a bucket and no later pass can place them.
        return 0

    if args.command == "void-expired":
        summary = settlement_repository.void_expired_predictions(
            now=generated_at,
            limit=args.limit,
            max_pending_age_hours=args.max_pending_age_hours,
            not_played_grace_hours=args.not_played_grace_hours,
        )
        print(
            json.dumps(
                {
                    "scope": "settlement_expiry",
                    "fixtures_processed": summary.fixtures_touched,
                    "results_examined": summary.examined,
                    "results_written": summary.voided,
                    "results_voided": summary.voided,
                    "evaluation_facts_written": summary.facts_written,
                    "unevaluable_prediction_ids": list(summary.unevaluable_prediction_ids),
                    "void_reasons": summary.reasons,
                    "max_pending_age_hours": args.max_pending_age_hours,
                },
                sort_keys=True,
            ),
            file=stdout,
        )
        return 0

    if args.command == "run-scope" and args.scope in IN_PLAY_SCOPE_CHOICES:
        return _run_in_play_scope(
            args,
            generated_at=generated_at,
            input_repository=input_repository,
            output_repository=output_repository,
            snapshot_repository=InPlaySnapshotRepository(
                connection,  # type: ignore[arg-type]
                jsonb_adapter=_jsonb_adapter if connect is None else None,
            ),
            connection=connection,
            in_play_runner=in_play_runner,
            stdout=stdout,
        )

    if args.command == "run-fixture":
        fixture_ids = [args.fixture_id]
        scope = "fixture"
    else:
        scope = args.scope
        fixture_ids = input_repository.list_candidate_fixture_ids(
            scope=args.scope,
            limit=args.limit,
            as_of_timestamp=generated_at,
        )

    write_stats = PredictionWriteStats()
    predictions_generated = 0
    failures: list[dict[str, object]] = []
    skipped_fixture_ids: list[int] = []
    deferred_fixture_ids: list[int] = []
    for fixture_id in fixture_ids:
        before_stats = output_repository.total_write_stats
        persist_predictions = not (
            args.command == "run-scope"
            and scope in {"historical", "replay", "finished"}
            and not args.persist_replay
            and runner is run_and_save_fixture
        )
        try:
            if persist_predictions:
                predictions = runner(
                    fixture_id,
                    as_of_timestamp=generated_at,
                    input_repository=input_repository,
                    output_repository=output_repository,
                )
            else:
                predictions = preview_runner(
                    fixture_id,
                    as_of_timestamp=generated_at,
                    input_repository=input_repository,
                )
        except BackfillDeferred:
            deferred_fixture_ids.append(fixture_id)
            continue
        except Exception as error:
            if args.command != "run-scope":
                raise
            connection.rollback()  # type: ignore[attr-defined]
            failures.append(
                {
                    "fixture_id": fixture_id,
                    "error_type": type(error).__name__,
                    "message": str(error),
                }
            )
            continue
        predictions_generated += len(predictions)
        fixture_stats = output_repository.total_write_stats - before_stats
        if not persist_predictions:
            pass
        elif fixture_stats.evaluated == len(predictions):
            write_stats += fixture_stats
        else:
            # Injected test/custom runners may not use the concrete repository.
            write_stats += PredictionWriteStats(inserted=len(predictions))
        if not predictions:
            skipped_fixture_ids.append(fixture_id)

    print(
        json.dumps(
            {
                "failures": failures,
                "fixture_ids": fixture_ids,
                "fixtures_failed": len(failures),
                "fixtures_processed": len(fixture_ids),
                "fixtures_skipped": len(skipped_fixture_ids),
                "predictions_generated": predictions_generated,
                "predictions_inserted": write_stats.inserted,
                "predictions_unchanged": write_stats.unchanged,
                "predictions_stale": write_stats.stale,
                "predictions_versioned": write_stats.versioned,
                "predictions_written": write_stats.written,
                "scope": scope,
                "skipped_fixture_ids": skipped_fixture_ids,
                **(
                    {
                        "deferred_fixture_ids": deferred_fixture_ids,
                        "fixtures_deferred": len(deferred_fixture_ids),
                    }
                    if deferred_fixture_ids
                    else {}
                ),
            },
            sort_keys=True,
        ),
        file=stdout,
    )
    return 0


def _run_in_play_scope(
    args: argparse.Namespace,
    *,
    generated_at: datetime,
    input_repository: PredictionInputRepository,
    output_repository: EnginePredictionRepository,
    snapshot_repository: object,
    connection: object,
    in_play_runner: RunnerFunc | None,
    stdout: TextIO,
) -> int:
    """Run the in-play scope and report why each fixture did or did not publish.

    The summary is deliberately richer than the pre-match one. 14 says
    suppress rather than extrapolate, and a suppression that leaves no trace
    is indistinguishable from the engine never having run: `suppressed_by_reason`
    is how an operator tells "nothing published because every snapshot went
    stale" apart from "nothing published because the job is not firing".
    """
    runner = in_play_runner or run_and_save_in_play_fixture
    tier = RegenerationTier.A if args.tier == "A" else RegenerationTier.B
    fixture_ids = input_repository.list_candidate_fixture_ids(
        scope=args.scope,
        limit=args.limit,
        as_of_timestamp=generated_at,
    )
    stats = InPlayRunStats()
    outcomes: list[dict[str, object]] = []
    failures: list[dict[str, object]] = []
    for fixture_id in fixture_ids:
        try:
            outcome = runner(
                fixture_id,
                as_of_timestamp=generated_at,
                input_repository=input_repository,
                output_repository=output_repository,
                tier=tier,
                snapshot_repository=snapshot_repository,
            )
        except Exception as error:  # noqa: BLE001 - one bad fixture must not end the scope
            connection.rollback()  # type: ignore[attr-defined]
            failures.append(
                {
                    "fixture_id": fixture_id,
                    "error_type": type(error).__name__,
                    "message": str(error),
                }
            )
            continue
        stats.record(outcome)
        outcomes.append(outcome.summary())

    print(
        json.dumps(
            {
                "failures": failures,
                "fixture_ids": fixture_ids,
                "fixtures_failed": len(failures),
                "fixtures_processed": stats.fixtures_processed,
                "fixtures_published": stats.fixtures_published,
                "fixtures_suppressed": stats.fixtures_processed - stats.fixtures_published,
                "freshness_limit_seconds": DEFAULT_IN_PLAY_CONFIG.freshness_limit_seconds,
                "outcomes": outcomes,
                "predictions_generated": stats.predictions_generated,
                "predictions_inserted": output_repository.total_write_stats.inserted,
                "predictions_unchanged": output_repository.total_write_stats.unchanged,
                "predictions_versioned": output_repository.total_write_stats.versioned,
                "predictions_written": output_repository.total_write_stats.written,
                "scope": "in_play",
                "suppressed_by_reason": stats.suppressed_by_reason,
                "tier": tier.value,
            },
            sort_keys=True,
        ),
        file=stdout,
    )
    return 0


def _run_market_step(
    args: argparse.Namespace,
    *,
    connection: object,
    input_repository: PredictionInputRepository,
    generated_at: datetime,
) -> dict[str, object]:
    """The all-market shadow for this command, run after the 1X2 shadow.

    A fault here is reported in the command's output and goes no further: the 1X2
    shadow and the job that owns this command must not depend on it.
    """
    try:
        from prediction_engine import challenger_markets as markets
        from prediction_engine.pipeline import champion_market_document

        repository = markets.MarketRepository(connection)
        if args.command == "settle-challengers":
            return markets.run_market_settlement(repository, settled_at=generated_at, limit=args.limit)
        if args.command == "challenger-report":
            return markets.run_market_report(
                repository,
                now=generated_at,
                origin=args.origin,
                minimum_samples=default_config().v2.promotion.minimum_shadow_settled_samples,
                write=not args.no_write,
            )

        def champion(fixture: object, as_of: datetime) -> dict[str, dict[str, float]] | None:
            try:
                return champion_market_document(input_repository.load_fixture_inputs(fixture.id, as_of_timestamp=as_of))  # type: ignore[attr-defined]
            except Exception:
                connection.rollback()  # type: ignore[attr-defined]
                return None

        return markets.run_market_forecasts(
            repository,
            as_of=generated_at,
            origin=args.origin,
            limit=args.limit,
            history_days=args.history_days,
            min_interval=timedelta(minutes=args.min_interval_minutes if args.origin == markets.LIVE else 0),
            champion=champion,
            models=markets.load_market_models(),
            code_commit=os.environ.get("PREDICTION_ENGINE_CODE_COMMIT", "").strip(),
        )
    except Exception as error:  # noqa: BLE001 - see the docstring
        connection.rollback()  # type: ignore[attr-defined]
        return {"error_type": type(error).__name__, "message": str(error)}


def _run_challenger_command(
    args: argparse.Namespace,
    *,
    connection: object,
    input_repository: PredictionInputRepository,
    generated_at: datetime,
    stdout: TextIO,
) -> int:
    from prediction_engine import challengers
    from prediction_engine.model_c import load_model
    from prediction_engine.pipeline import champion_match_result

    repository = challengers.ChallengerRepository(connection)
    if args.command == "settle-challengers":
        summary = challengers.run_settlement(repository, settled_at=generated_at, limit=args.limit)
    elif args.command == "challenger-report":
        summary = challengers.run_report(
            repository,
            now=generated_at,
            origin=args.origin,
            minimum_samples=default_config().v2.promotion.minimum_shadow_settled_samples,
            write=not args.no_write,
        )
    else:

        def champion(fixture: object, as_of: datetime) -> dict[str, float] | None:
            # The champion's own pipeline at the challenger's as-of time. A
            # fixture it cannot score is recorded unpaired, never guessed.
            try:
                return champion_match_result(input_repository.load_fixture_inputs(fixture.id, as_of_timestamp=as_of))  # type: ignore[attr-defined]
            except Exception:
                connection.rollback()  # type: ignore[attr-defined]
                return None

        summary = challengers.run_forecasts(
            repository,
            as_of=generated_at,
            origin=args.origin,
            limit=args.limit,
            history_days=args.history_days,
            min_interval=timedelta(minutes=args.min_interval_minutes if args.origin == challengers.LIVE else 0),
            champion=champion,
            model_c=load_model(),
        )
    summary["markets"] = _run_market_step(
        args, connection=connection, input_repository=input_repository, generated_at=generated_at
    )
    print(json.dumps(summary, sort_keys=True, default=str), file=stdout)
    return 0


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="prediction-engine")
    subparsers = parser.add_subparsers(dest="command", required=True)

    fixture = subparsers.add_parser("run-fixture")
    _add_common_options(fixture)
    fixture.add_argument("--fixture-id", type=int, required=True)

    scope = subparsers.add_parser("run-scope")
    _add_common_options(scope)
    scope.add_argument(
        "--scope",
        choices=("upcoming", "historical", "replay", "finished", *IN_PLAY_SCOPE_CHOICES),
        default="upcoming",
    )
    scope.add_argument("--limit", type=int, default=100)
    scope.add_argument(
        "--tier",
        choices=("A", "B"),
        default="A",
        help="in-play regeneration tier (architecture v2 7.6): A is every 2 minutes, B every 5",
    )
    scope.add_argument(
        "--persist-replay",
        action="store_true",
        help="explicitly persist historical/replay output into the canonical engine table",
    )

    settlement = subparsers.add_parser("settle-fixture")
    _add_common_options(settlement)
    settlement.add_argument("--fixture-id", type=int, required=True)

    correction_preview = subparsers.add_parser(
        "preview-btts-corrections", help="read-only combined BTTS/totals correction report; never applies changes"
    )
    _add_common_options(correction_preview)
    correction_preview.add_argument("--fixture-id", type=_positive_int, required=True)
    correction_preview.add_argument("--limit", type=_positive_int, default=500)

    settlement_scope = subparsers.add_parser("settle-scope")
    _add_common_options(settlement_scope)
    settlement_scope.add_argument("--scope", choices=("finished", "historical", "replay"), default="finished")
    settlement_scope.add_argument("--limit", type=int, default=100)

    trackers = subparsers.add_parser("refresh-trackers")
    _add_common_options(trackers)
    trackers.add_argument("--limit", type=_positive_int, default=200)

    finalise = subparsers.add_parser("finalise-snapshots")
    _add_common_options(finalise)
    finalise.add_argument("--limit", type=_positive_int, default=500)

    recount = subparsers.add_parser("recount-reversed")
    _add_common_options(recount)
    recount.add_argument("--limit", type=_positive_int, default=500)
    recount.add_argument("--max-batches", type=_positive_int, default=20)

    resettle = subparsers.add_parser("resettle-retracted")
    _add_common_options(resettle)
    resettle.add_argument("--limit", type=_positive_int, default=100)

    expiry = subparsers.add_parser("void-expired")
    _add_common_options(expiry)
    expiry.add_argument("--limit", type=_positive_int, default=500)
    expiry.add_argument(
        "--max-pending-age-hours",
        type=_positive_int,
        default=DEFAULT_MAX_PENDING_AGE_HOURS,
    )
    expiry.add_argument(
        "--not-played-grace-hours",
        type=_positive_int,
        default=DEFAULT_NOT_PLAYED_GRACE_HOURS,
    )

    shadow = subparsers.add_parser("run-challengers", help="forecast with the zero-weight challengers (Models B and C)")
    _add_common_options(shadow)
    shadow.add_argument("--origin", choices=("live", "replay"), default="live")
    # These forecasts are published now, so a fixture waits on this backlog to get them.
    shadow.add_argument("--limit", type=_positive_int, default=500)
    shadow.add_argument("--history-days", type=_positive_int, default=1100)
    shadow.add_argument("--min-interval-minutes", type=int, default=30)

    shadow_settle = subparsers.add_parser("settle-challengers")
    _add_common_options(shadow_settle)
    shadow_settle.add_argument("--limit", type=_positive_int, default=2000)

    shadow_report = subparsers.add_parser("challenger-report")
    _add_common_options(shadow_report)
    shadow_report.add_argument("--origin", choices=("live", "replay"), default="live")
    shadow_report.add_argument("--no-write", action="store_true", help="print the comparison without recording it")

    snapshot = subparsers.add_parser("snapshot-accuracy")
    _add_common_options(snapshot)

    retention = subparsers.add_parser("aggregate-prune-results")
    _add_common_options(retention)
    retention.add_argument("--public-detail-hours", type=_positive_int, default=24)
    retention.add_argument("--raw-retention-days", type=_positive_int, default=2)
    retention.add_argument("--batch-limit", type=_positive_int, default=1000)
    retention.add_argument("--max-batches", type=_positive_int, default=20)
    return parser


def _add_common_options(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--database-url", default="")
    parser.add_argument("--as-of-timestamp", "--generated-at", dest="generated_at", default="")


def _parse_generated_at(raw: str) -> datetime:
    if not raw:
        return datetime.now(tz=UTC)
    parsed = datetime.fromisoformat(raw.replace("Z", "+00:00"))
    if parsed.tzinfo is None or parsed.utcoffset() is None:
        raise ValueError("as-of timestamp must be timezone-aware")
    return parsed.astimezone(UTC)


def _positive_int(raw: str) -> int:
    value = int(raw)
    if value <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return value


def _connect(database_url: str) -> object:
    import psycopg
    from psycopg.rows import dict_row

    return psycopg.connect(database_url, row_factory=dict_row)


def _settle_fixture(fixture_id: int, *, settled_at: datetime, settlement_repository: PredictionSettlementRepository) -> list[object]:
    return list(settlement_repository.settle_fixture(fixture_id, settled_at=settled_at))


def _snapshot_accuracy(*, snapshot_date: object, accuracy_repository: AccuracySnapshotRepository) -> list[object]:
    return list(accuracy_repository.compute_and_save_snapshots(snapshot_date=snapshot_date))  # type: ignore[arg-type]


def _jsonb_adapter(value: dict[str, object]) -> object:
    from psycopg.types.json import Jsonb

    return Jsonb(value)


if __name__ == "__main__":
    raise SystemExit(main())
