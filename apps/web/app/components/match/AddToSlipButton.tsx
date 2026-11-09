"use client";

import { useEffect, useMemo, useState } from "react";
import { PlusIcon, CheckIcon } from "./icons";
import {
  BUILDER_EVENT_NAME,
  addBuilderLegs,
  builderLegId,
  fairOdds,
  hasBuilderLegStarted,
  readBuilderSelection,
  writeBuilderSelection,
  type BuilderLeg,
} from "../../multi-match-builder/builder-state";

export type AddToSlipTarget = {
  kickoffAt: string;
  marketKey: string;
  matchId: number;
  matchStatus: string;
  matchTitle: string;
  /**
   * The two sides and their crests, carried onto the leg so the Builder ticket
   * can draw its inline crest pair (review 2.3). Optional, and the ticket falls
   * back to `matchTitle` without them -- but a leg added here would then be the
   * one uncrested row in an otherwise crested ticket, so every call site passes
   * them.
   */
  homeTeam?: string;
  awayTeam?: string;
  homeTeamLogo?: string;
  awayTeamLogo?: string;
  predictionId: number;
  /** The engine's probability, 0 to 1, used to derive the fair price. */
  probability: number;
  selection: string;
};

type Props = {
  className?: string;
  /** Hide the label and show the icon alone, for the market cards' icon row. */
  iconOnly?: boolean;
  label?: string;
  target: AddToSlipTarget;
};

function toBuilderLeg(target: AddToSlipTarget): BuilderLeg {
  const odds = fairOdds(target.probability);
  return {
    id: builderLegId(target.predictionId, target.matchId, target.marketKey, target.selection),
    predictionId: target.predictionId,
    matchId: target.matchId,
    matchTitle: target.matchTitle,
    homeTeam: target.homeTeam,
    awayTeam: target.awayTeam,
    homeTeamLogo: target.homeTeamLogo,
    awayTeamLogo: target.awayTeamLogo,
    marketKey: target.marketKey,
    selection: target.selection,
    odds,
    oddsType: odds === null ? "unavailable" : "fair",
    kickoffAt: target.kickoffAt,
    matchStatus: target.matchStatus,
    source: "manual",
  };
}

/**
 * The slip toggle, extracted so the hero's headline pick and every market card
 * share one implementation.
 *
 * It was previously inlined in MatchPredictionCard, which meant the refreshed
 * hero would have needed a second copy of the same subscribe-and-poll logic --
 * and two copies of a toggle reading the same store is how they drift.
 *
 * The 1s poll alongside the event listener is deliberate and inherited: the
 * custom event covers changes this tab makes, while the interval catches a leg
 * whose kickoff passes while the page sits open, which no event announces.
 */
export function AddToSlipButton({ className, iconOnly = false, label, target }: Props) {
  const leg = useMemo(() => toBuilderLeg(target), [target]);
  const [selected, setSelected] = useState(false);
  const [started, setStarted] = useState(() => hasBuilderLegStarted(leg));

  useEffect(() => {
    const update = () => {
      setSelected(readBuilderSelection().some((item) => item.id === leg.id));
      setStarted(hasBuilderLegStarted(leg));
    };
    update();
    const interval = window.setInterval(update, 1_000);
    window.addEventListener(BUILDER_EVENT_NAME, update);
    return () => {
      window.clearInterval(interval);
      window.removeEventListener(BUILDER_EVENT_NAME, update);
    };
  }, [leg]);

  const toggle = () => {
    if (started) return;
    if (selected) {
      writeBuilderSelection(readBuilderSelection().filter((item) => item.id !== leg.id));
    } else {
      addBuilderLegs([leg]);
    }
  };

  const text = started ? "Match started" : selected ? "In slip" : label ?? "Add to slip";
  // An icon-only control still needs a name, and "In slip" alone would not say
  // which market it refers to on a page carrying eight of them.
  const accessibleName = iconOnly
    ? `${text}: ${target.matchTitle} — ${target.selection}`
    : undefined;

  return (
    <button
      aria-label={accessibleName}
      aria-pressed={selected}
      className={className}
      disabled={started}
      onClick={toggle}
      title={iconOnly ? text : undefined}
      type="button"
    >
      {selected && !started ? <CheckIcon /> : <PlusIcon />}
      {iconOnly ? null : text}
    </button>
  );
}
