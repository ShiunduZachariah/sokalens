"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { MatchPhase } from "../../lib/matchPhase";
import {
  fetchInPlayChanges,
  fetchInPlayFixture,
  fetchInPlayOverview,
  parseChangeSet,
  formatProbabilityBp,
  marketFamily,
  marketFamilyLabel,
  type InPlayChangeSet,
  type InPlayFixture,
  type InPlaySnapshot,
  type ModelHealth,
} from "../../data/inplay";

/** How often `changes` is polled. The contract advertises 15s, and so does the provider. */
const POLL_MS = 15_000;

/**
 * The live market board for one fixture.
 *
 * Reads `/v1/inplay/overview` for the cold load, streams snapshot changes
 * with polling as a transport fallback, and refreshes this fixture's current
 * market publication every 15 seconds. Changes frames do not carry markets.
 *
 * Three contract rules shape almost everything here:
 *
 * 1. **Stale is not live.** A fixture or snapshot that is stale says so and the
 *    numbers are labelled as last-known, never presented as current. `suspended`
 *    shows no probabilities at all.
 * 2. **Settlement is reversible, with no time bound.** A market can go from won
 *    back to pending after full time. So nothing terminal is cached locally:
 *    every render reads the latest payload, and `applyChanges` replaces state
 *    rather than merging into a remembered verdict.
 * 3. **Sequence gaps are normal.** The cursor comes from the payload, never from
 *    the lists, and a gap is never treated as a lost message.
 */
/**
 * Which of the design's three readings this is.
 *
 * The 2026-10-02 canvas gives the in-play surface a different voice per match
 * phase, and the reason is that the same empty board means three different
 * things. Before kick-off there is nothing to show because nothing has started;
 * during the match an empty board means the model has no defensible rate for this
 * fixture; after full time it means the final read is what it is. Saying "no live
 * markets are published yet" to a reader looking at a fixture two hours out is
 * simply wrong, and that is what this component said before.
 */
export type { MatchPhase };

const PHASE = {
  prematch: {
    badge: "Opening read · live from kick-off",
    title: "In-play predictions · opening read",
    note: "These start from the pre-match model at kick-off, then re-price after every goal, card and on a timer.",
    empty: "Live markets open at kick-off. Until then the pre-match predictions above are the full read on this match.",
  },
  live: {
    badge: "Live · updating",
    title: "In-play predictions",
    note: "Re-priced after every goal, card and on a timer. The pre-match picks above are never changed.",
    empty: "No live markets are published for this match. A market appears when the model has a defensible rate for it, and a statistic this fixture's feed does not report produces no market at all.",
  },
  finished: {
    badge: "Final in-play read · FT",
    title: "In-play predictions · final read",
    note: "How our in-play predictions stood at the final whistle.",
    empty: "No in-play markets were published for this match.",
  },
} as const satisfies Record<MatchPhase, { badge: string; title: string; note: string; empty: string }>;

type Props = { fixtureId: number; phase: MatchPhase };

type BoardState = {
  fixture: InPlayFixture | null;
  snapshot: InPlaySnapshot | null;
  cursor: number;
};

const HEALTH_NOTE: Record<ModelHealth, string | null> = {
  ok: null,
  stale: "These are the last figures we had. The feed has not refreshed them, so they are not current.",
  suspended: "Predictions are suspended for this match. No probabilities are shown while they are.",
};

export function InPlayMarkets({ fixtureId, phase }: Props) {
  const copy = PHASE[phase];
  const [state, setState] = useState<BoardState | null>(null);
  const [status, setStatus] = useState<"loading" | "ready" | "unavailable">("loading");
  // Held in a ref as well as in state: the poll reads it without being
  // re-created on every tick, which would reset the interval each time.
  const cursor = useRef(0);

  const load = useCallback(async () => {
    const overview = await fetchInPlayOverview();
    if (!overview) {
      setStatus("unavailable");
      return;
    }
    const fixture = overview.fixtures.find((candidate) => candidate.fixtureId === fixtureId) ?? null;
    // The latest snapshot for this fixture, by sequence. The list is shared
    // across every live fixture, so it has to be filtered before being reduced.
    const snapshot = overview.snapshots
      .filter((candidate) => candidate.fixtureId === fixtureId)
      .reduce<InPlaySnapshot | null>((latest, candidate) => (!latest || candidate.sequence > latest.sequence ? candidate : latest), null);
    cursor.current = overview.cursor;
    setState({
      cursor: overview.cursor,
      fixture,
      snapshot,
    });
    setStatus("ready");
  }, [fixtureId]);

  useEffect(() => {
    // A fixture that has not kicked off is not in the live scope, so `overview`
    // would return nothing for it. Skipping the request avoids a call per
    // pre-match page view that can only ever come back empty.
    if (phase === "prematch") {
      setStatus("ready");
      return;
    }
    void load();
  }, [load, phase]);

  /*
   * Applies one changeset, whichever transport delivered it.
   *
   * The stream and the polling fallback share one server-side query, so they
   * cannot disagree -- which is why they share one reducer here too. Writing the
   * stream its own would be the obvious place for the two paths to drift.
   */
  const apply = useCallback((changes: InPlayChangeSet) => {
    if (!Number.isSafeInteger(changes.cursor) || changes.cursor <= cursor.current) return;
    const next = changes.snapshots
      .filter((candidate) => candidate.fixtureId === fixtureId)
      .reduce<InPlaySnapshot | null>((latest, candidate) => (!latest || candidate.sequence > latest.sequence ? candidate : latest), null);
    setState((current) => (current ? {
      ...current,
      cursor: changes.cursor,
      // A changeset with no snapshot for this fixture means "unchanged", not
      // "withdrawn". Market membership is refreshed separately below.
      snapshot: next && (!current.snapshot || next.sequence > current.snapshot.sequence) ? next : current.snapshot,
    } : current));
    // A cursor acknowledges applied data, never merely a received frame.
    cursor.current = changes.cursor;
  }, [fixtureId]);

  /*
   * The stream, with polling as the fallback that always works.
   *
   * Not a freshness optimisation -- the provider refreshes every 15 seconds, so
   * polling sees everything the stream sees. It is request volume: a poll is four
   * requests a minute per open match page.
   *
   * `transport` starts at "stream" and moves to "poll" on failure, never back. An
   * intermediary that buffers or strips SSE is a real possibility we do not
   * control, and a client that keeps retrying a stream it cannot have would sit
   * there showing a stale board. Once it falls back it stays fallen back for the
   * life of the component, which is the life of the page.
   */
  const [transport, setTransport] = useState<"stream" | "poll">("stream");

  useEffect(() => {
    // Nothing to stream before kick-off, and nothing changes after full time.
    if (status !== "ready" || phase !== "live" || transport !== "stream") return;
    if (typeof window.EventSource === "undefined") {
      setTransport("poll");
      return;
    }

    const source = new EventSource(`/api/inplay/stream?since=${encodeURIComponent(String(cursor.current))}`);
    let delivered = false;
    let closed = false;
    const close = () => { closed = true; source.close(); };
    const fallback = () => {
      if (closed) return;
      close();
      setTransport("poll");
    };

    // The server names its frames: `event: changes`, with the cursor as the SSE
    // id. A named event is NOT delivered to a `message` listener -- `message`
    // only receives frames sent without an event name -- so listening for it
    // meant every frame was dropped on the floor.
    //
    // That was invisible in production for a second reason: the stream never
    // connected at all. /v1/inplay/stream answered 501 because it was wrapped in
    // http.TimeoutHandler, EventSource raised `error` before any frame, and the
    // client fell back to polling, which works. Fixing only the 501 would have
    // been worse than leaving it broken: frames would start arriving, this
    // listener would still ignore them, and because the connection is healthy no
    // `error` fires either -- so the fallback below would never trigger and the
    // board would sit silently stale. The two fixes belong in one change.
    source.addEventListener("changes", (event) => {
      if (closed) return;
      try {
        const payload = JSON.parse(event.data);
        // The lenient API parser is useful for display, but a stream cursor
        // must never acknowledge rows it silently discarded. Go may encode an
        // empty events slice as null; an absent field is not an empty slice.
        if (!payload || !Number.isSafeInteger(payload.cursor) || payload.cursor < 0 ||
            !(Array.isArray(payload.snapshots) || payload.snapshots === null) ||
            !(Array.isArray(payload.events) || payload.events === null) ||
            typeof payload.more !== "boolean") throw new Error("Invalid changes frame");
        const changes = parseChangeSet(payload);
        if (changes.snapshots.length !== (payload.snapshots?.length ?? 0) ||
            changes.events.length !== (payload.events?.length ?? 0) ||
            [...changes.snapshots, ...changes.events].some((row) =>
              !Number.isSafeInteger(row.sequence) || row.sequence <= 0 || row.sequence > changes.cursor ||
              !Number.isSafeInteger(row.fixtureId) || row.fixtureId <= 0)) throw new Error("Invalid changes rows");
        if (event.lastEventId && event.lastEventId !== String(changes.cursor)) throw new Error("Invalid changes cursor");
        apply(changes);
        delivered = true;
      } catch {
        // EventSource has already remembered this frame's id. Letting it
        // reconnect would skip data we did not apply, so resume via polling.
        fallback();
      }
    });

    // A close announcement is not applied data. Resume from the last successful
    // changeset instead of allowing its id/payload to advance the browser cursor.
    source.addEventListener("reconnect", fallback);

    source.addEventListener("error", () => {
      // EventSource reconnects by itself and resumes with `Last-Event-ID`, so a
      // transient drop needs nothing from us. A failure BEFORE any frame arrived
      // is the one that means this connection is never going to work here.
      if (!delivered) fallback();
    });

    return close;
  }, [apply, phase, status, transport]);

  useEffect(() => {
    if (status !== "ready" || phase !== "live") return;
    let cancelled = false;
    let inFlight = false;
    // Changes frames contain snapshots, not markets. Refresh the one-fixture
    // publication independently so an open page does not retain old lines.
    const refresh = async () => {
      if (inFlight) return;
      inFlight = true;
      try {
        const fixture = await fetchInPlayFixture(fixtureId);
        if (!cancelled && fixture) setState((current) => current ? { ...current, fixture } : current);
      } finally {
        inFlight = false;
      }
    };
    const timer = window.setInterval(() => { void refresh(); }, POLL_MS);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, [fixtureId, phase, status]);

  useEffect(() => {
    if (status !== "ready" || phase !== "live" || transport !== "poll") return;
    let cancelled = false;

    const tick = async () => {
      const since = cursor.current;
      const changes = await fetchInPlayChanges(cursor.current);
      if (cancelled || !changes) return;
      apply(changes);
      // `more` means the limit truncated the set, so ask again at once rather
      // than waiting out the interval and falling further behind.
      if (changes.more && cursor.current > since && !cancelled) void tick();
    };

    void tick();
    const timer = window.setInterval(() => { void tick(); }, POLL_MS);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, [apply, phase, status, transport]);

  /*
   * Markets grouped into families, because a ladder arrives as one row per line
   * -- `total_goals_match_8.5`, `total_goals_match_9.5` -- and listing twelve
   * sibling rows flat tells a reader nothing about which line is the interesting
   * one. Grouped on the key prefix, as the contract instructs.
   */
  const families = useMemo(() => {
    const markets = state?.fixture?.markets ?? [];
    const grouped = new Map<string, typeof markets>();
    for (const market of markets) {
      const family = marketFamily(market.key);
      grouped.set(family, [...(grouped.get(family) ?? []), market]);
    }
    return [...grouped.entries()].map(([family, items]) => ({
      family,
      label: marketFamilyLabel(family, items[0]?.label),
      items: [...items].sort((left, right) => left.key.localeCompare(right.key, undefined, { numeric: true })),
    }));
  }, [state?.fixture?.markets]);

  const header = (
    <div className="iv-head">
      <h4>{copy.title}</h4>
      <span className="iv-head__badge">{copy.badge}</span>
      <p>{copy.note}</p>
    </div>
  );

  if (status === "loading") {
    return <>{header}<p className="micro" role="status">Loading live markets…</p></>;
  }

  if (status === "unavailable") {
    return (
      <>
      {header}
      <p className="ip-note" role="status">
        <span>
          Live markets could not be loaded. The pre-match predictions above are unaffected — they
          were generated before kick-off and are never re-modelled from the live score.
        </span>
      </p>
      </>
    );
  }

  const fixture = state?.fixture;
  const health = state?.snapshot?.modelHealth ?? "ok";
  const healthNote = HEALTH_NOTE[health];
  // Suspended withholds every figure, which the contract requires and which this
  // enforces at the one place that draws them.
  const showFigures = health !== "suspended";

  if (!fixture || families.length === 0) {
    return (
      <>
        {header}
        <p className="ip-note" role="status"><span>{copy.empty}</span></p>
      </>
    );
  }

  return (
    <div className="iv-board">
      {header}
      {fixture.stale || healthNote ? (
        <p className="iv-health" role="status">
          {healthNote ?? "These figures are the last we had; the feed has not refreshed them."}
        </p>
      ) : null}

      {families.map((group) => (
        <section className="iv-family" key={group.family}>
          <h4>{group.label}</h4>
          <ul>
            {group.items.map((market) => (
              <li className={market.locked ? "iv-line iv-line--locked" : "iv-line"} key={market.key}>
                <span className="iv-line__name">
                  {/*
                    The line, read off the key. A locked market still shows which
                    line exists. If an older upstream still withholds a figure,
                    preserve that missing-data state without a purchase offer.
                  */}
                  {market.prediction?.selection ?? "—"}
                  {market.key !== group.family ? (
                    <span className="iv-line__value"> {market.key.slice(group.family.length + 1)}</span>
                  ) : null}
                </span>
                {market.locked || !market.prediction || !showFigures ? (
                  <span className="iv-line__locked">{market.locked ? "Unavailable" : "—"}</span>
                ) : (
                  <span className="iv-line__prob">{formatProbabilityBp(market.prediction.probabilityBp)}</span>
                )}
              </li>
            ))}
          </ul>
        </section>
      ))}

      <p className="iv-foot">
        {phase === "live"
          ? "Updated from the provider every 15 seconds. A market settles the moment its outcome cannot change, and a settlement can be reversed if the provider withdraws the evidence."
          : "A settlement can still be reversed if the provider withdraws the evidence, which can happen after full time."}
      </p>
    </div>
  );
}
