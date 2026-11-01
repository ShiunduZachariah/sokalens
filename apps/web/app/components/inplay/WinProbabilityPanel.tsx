"use client";

import { useEffect, useState } from "react";
import { fetchInPlaySeries, type InPlaySeriesPoint } from "../../data/inplay";
import { WinProbabilityChart } from "./WinProbabilityChart";

/**
 * Loads a fixture's probability graph and draws it.
 *
 * Fetched once rather than polled. The graph is a record of a match, and the
 * live edge of it already arrives through the stream that drives the market
 * cards above; re-reading the whole series every fifteen seconds to append one
 * point would be the N+1 shape the in-play board was written to avoid.
 *
 * Renders nothing at all until the series answers. A chart frame that appears
 * empty and then fills reads as a broken panel for as long as it is empty, and
 * this sits below content that is already useful.
 */
export function WinProbabilityPanel({
  awayTeam,
  fixtureId,
  homeTeam,
}: {
  awayTeam: string;
  fixtureId: number;
  homeTeam: string;
}) {
  const [points, setPoints] = useState<InPlaySeriesPoint[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    void fetchInPlaySeries(fixtureId).then((series) => {
      if (!cancelled) setPoints(series);
    });
    return () => { cancelled = true; };
  }, [fixtureId]);

  if (points === null || points.length === 0) return null;
  return <WinProbabilityChart awayTeam={awayTeam} homeTeam={homeTeam} points={points} />;
}
