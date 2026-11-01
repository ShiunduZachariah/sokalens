"use client";

import type { LiveOddsPrice } from "../data/liveMatches";
import { formatOdds } from "../lib/odds";
import { useOddsFormat } from "./OddsFormatControl";

type OddsStripProps = {
  odds: LiveOddsPrice[];
};

const trendLabels: Record<LiveOddsPrice["trend"], string> = {
  down: "Shortened",
  flat: "Stable",
  up: "Drifted",
};

export function OddsStrip({ odds }: OddsStripProps) {
  const [oddsFormat] = useOddsFormat();
  if (odds.length === 0) {
    return <p className="micro">Odds context unavailable for this fixture.</p>;
  }

  return (
    <div className="odds-strip" aria-label="Odds context">
      {odds.map((price) => (
        <div className="odds-strip__item" key={`${price.label}-${price.value}`}>
          <span className="odds-strip__label">{price.label}</span>
          <span className="odds-strip__value">{formatOdds(Number(price.value), oddsFormat)}</span>
          <span className="odds-strip__trend">{trendLabels[price.trend]}</span>
        </div>
      ))}
    </div>
  );
}
