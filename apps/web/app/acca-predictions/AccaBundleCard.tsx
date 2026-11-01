"use client";

import { useState } from "react";
import Link from "next/link";
import { ShareButton } from "../components/ShareButton";
import { PlusIcon, CheckIcon } from "../components/match/icons";
import { TeamCrest } from "../components/TeamCrest";
import { LocalTime } from "../components/LocalTime";
import { marketLabel, marketSelectionLabel } from "../data/livePredictions";
import { fixtureHref } from "../lib/fixtureHref";
import { formatOdds, type OddsFormat } from "../lib/odds";
import type { AccaBundle } from "./accaBundles";
import { localePath, type Locale } from "../i18n/config";

/**
 * How many legs a card shows. Not "before it expands" -- this is all it shows.
 *
 * The design is explicit: "max legs per card -- the full ticket lives on the
 * ACCA detail page". A card that expanded in place defeated the grid, because
 * `.ac-grid` has `grid-auto-rows: 1fr`: open a twenty-leg ticket and its
 * five-leg neighbour stretches to match it, which is the column of empty space
 * the catalogue kept showing.
 *
 * So every card is two legs tall, always, and the rest of the ticket is one
 * click away on its own page. The only exception is a slip composed live for
 * the current filters: it has no durable id and therefore no page, so for that
 * one case the card still expands in place -- otherwise its remaining legs
 * would be unreachable.
 */
export const VISIBLE_LEGS = 2;

type Props = {
  added: boolean;
  bundle: AccaBundle;
  locale: Locale;
  oddsFormat: OddsFormat;
  onAdd: (bundle: AccaBundle) => void;
  sharePath: string;
  shareText: string;
};

function impliedChance(probability: number) {
  if (!Number.isFinite(probability) || probability <= 0) return null;
  const percent = probability * 100;
  // Below 1% two decimals still say something; above it they are noise.
  return `${percent < 1 ? percent.toFixed(2) : percent.toFixed(1)}%`;
}

/** "Sat 27 Sep" style day label, from the same instant LocalTime renders. */
function dayLabel(iso: string) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short", timeZone: "UTC" });
}

export function AccaBundleCard({
  added, bundle, locale, oddsFormat, onAdd, sharePath, shareText,
}: Props) {
  const [expanded, setExpanded] = useState(false);
  const hidden = Math.max(0, bundle.legs.length - VISIBLE_LEGS);
  const implied = impliedChance(bundle.combinedFairProbability);
  const legCount = bundle.legs.length;
  // Every "view all" opens the slip page, as the design has it. The condition
  // is the id, not the origin: the catalogue serves UUIDs for every slip it
  // returns today, and a card with no id at all is the only thing that cannot
  // navigate -- it would have nowhere to go.
  const slipHref = bundle.id
    ? localePath(`/acca-predictions/slip/${encodeURIComponent(bundle.id)}`, locale)
    : null;

  return (
    <article className="ac-card" id={`acca-${bundle.id}`}>
      <div className="ac-card__head">
        <div className="ac-card__title">
          <div className="ac-card__titleText">
            <span className="ac-card__meta">
              <span>{bundle.legs.length === 1 ? "1 leg" : `${bundle.legs.length} legs`}</span>
              <span aria-hidden="true">·</span>
              <strong>{bundle.label}</strong>
            </span>
            {/*
              A stored slip has a durable id and therefore a page of its own. A
              slip composed for these filters does not -- it is never written
              down, so `/acca-predictions/slip/{id}` would 404 on it -- and that
              is the whole reason this is conditional rather than always a link.
            */}
            <h3 className="ac-card__market" title={bundle.label}>
              {bundle.origin === "on_demand" || !bundle.id ? bundle.label : (
                <Link href={localePath(`/acca-predictions/slip/${encodeURIComponent(bundle.id)}`, locale)}>
                  {bundle.label}
                </Link>
              )}
            </h3>
            {bundle.origin === "on_demand" ? (
              <span className="ac-origin">
                Composed live for these filters — not part of the stored catalogue
              </span>
            ) : null}
          </div>
          <div className="ac-card__price">
            <span className="ac-card__priceLabel">Combined fair odds</span>
            <span className="ac-card__odds">{formatOdds(bundle.combinedFairOdds, oddsFormat)}</span>
            {implied ? <span className="ac-card__implied">≈ {implied} implied chance</span> : null}
          </div>
        </div>
        {/* One segment per leg, its opacity that leg's probability. Decorative:
            every leg's own price is listed directly below. */}
        <div aria-hidden="true" className="ac-segments">
          {bundle.legs.map((leg) => (
            <i
              key={`${leg.matchId}:${leg.marketKey}:${leg.selection}`}
              style={{ opacity: Math.max(0.25, Math.min(1, leg.probability || 0.5)) }}
            />
          ))}
        </div>
      </div>

      {/*
        Every leg stays in the DOM whether or not the card is expanded. The
        collapse is a visual cap, not a content gate -- legs are the substance a
        crawler reads this page for, and hiding them behind a click would put
        them behind an interaction no crawler performs.
      */}
      <ol className="ac-legs">
        {bundle.legs.map((leg, index) => (
          <li
            className="ac-leg"
            hidden={!expanded && index >= VISIBLE_LEGS}
            key={`${leg.matchId}:${leg.marketKey}:${leg.selection}`}
          >
            <span className="ac-leg__when">
              <span className="ac-leg__day">{dayLabel(leg.kickoffAt)}</span>
              <span className="ac-leg__time"><LocalTime timeOnly value={leg.kickoffAt} /></span>
            </span>
            <span className="ac-leg__fixture">
              {/*
                Stacked with crests, as the prediction rows now are (review
                UI-04/UI-05, and 3 lists ACCA legs among the surfaces that get
                a crest). The legs already carried logo URLs -- acca.Leg has
                emitted them since the slip query was written -- so this is a
                render change only.
              */}
              <Link className="ac-leg__teams" href={fixtureHref(leg)}>
                <span className="ac-leg__team">
                  <TeamCrest logoUrl={leg.homeTeamLogo} name={leg.homeTeam} size={20} />
                  <span className="ac-leg__teamname">{leg.homeTeam}</span>
                </span>
                <span className="ac-leg__team">
                  <TeamCrest logoUrl={leg.awayTeamLogo} name={leg.awayTeam} size={20} />
                  <span className="ac-leg__teamname">{leg.awayTeam}</span>
                </span>
              </Link>
              <span className="ac-leg__league">{leg.leagueName}</span>
            </span>
            <span className="ac-leg__pick">
              <span className="ac-leg__market">{marketLabel(leg.marketKey, locale)}</span>
              <span className="ac-leg__selection">
                {marketSelectionLabel(leg.marketKey, leg.selection, locale)}
              </span>
            </span>
            <span className="ac-leg__odds">
              {leg.fairOdds > 0 && Number.isFinite(leg.fairOdds) ? formatOdds(leg.fairOdds, oddsFormat) : "—"}
            </span>
          </li>
        ))}
        {/* A one-leg slip would otherwise sit a row shorter than the rest. */}
        {!expanded && hidden === 0 && bundle.legs.length < VISIBLE_LEGS + 1 ? (
          <li aria-hidden="true" className="ac-legs__filler" />
        ) : null}
      </ol>

      <div className="ac-card__spacer" />

      {hidden > 0 ? (
        slipHref ? (
          <Link className="ac-card__more" href={slipHref}>
            {`+${hidden} more · View all ${legCount} legs`}
          </Link>
        ) : (
          <button
            aria-expanded={expanded}
            className="ac-card__more"
            onClick={() => setExpanded((value) => !value)}
            type="button"
          >
            {expanded ? "Show fewer legs" : `+${hidden} more · View all ${legCount} legs`}
          </button>
        )
      ) : (
        <span className="ac-card__allShown">{`All ${legCount} legs shown`}</span>
      )}

      <div className="ac-card__foot">
        <button
          className="mp-btn mp-btn--primary"
          onClick={() => onAdd(bundle)}
          type="button"
        >
          {added ? <CheckIcon /> : <PlusIcon />}
          {added ? "Added to Builder" : "Add ACCA to Builder"}
        </button>
        <ShareButton
          compact
          entityId={bundle.id}
          entityType="acca"
          path={sharePath}
          text={shareText}
          title={bundle.label}
        />
      </div>
    </article>
  );
}
