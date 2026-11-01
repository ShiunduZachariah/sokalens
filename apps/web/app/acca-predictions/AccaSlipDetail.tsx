"use client";

import { useState } from "react";
import Link from "next/link";
import { LocalTime } from "../components/LocalTime";
import { ShareButton } from "../components/ShareButton";
import { CheckIcon, PlusIcon } from "../components/match/icons";
import { marketLabel, marketSelectionLabel } from "../data/livePredictions";
import { fixtureHref } from "../lib/fixtureHref";
import { formatOdds, oddsFormatLabels, type OddsFormat } from "../lib/odds";
import { useOddsFormat } from "../components/OddsFormatControl";
import { addBuilderLegs } from "../multi-match-builder/builder-state";
import { bundleLegs } from "./AccaExplorer";
import { legStateLabels, type AccaSlipLeg, type AccaSlipView } from "./accaSlip";
import type { AccaBundle } from "./accaBundles";
import type { Locale } from "../i18n/config";

/**
 * One ACCA's detail page, as two client islands either side of the shell's grid.
 *
 * ## Why two islands rather than one component spanning both columns
 *
 * `ContentPageShell` owns the 8/4 grid and puts the rail's ad slot inside the
 * rail, so the page hands it `children` and `rail` separately. A single
 * component wrapping both columns would have to re-create that grid and would
 * either lose the ad slot or duplicate it.
 *
 * The two islands both read the odds format from `useOddsFormat`, which is
 * backed by `localStorage` plus a window event — so changing the format in the
 * summary re-renders the legs, without either island knowing the other exists.
 * That event is the reason the preference is not component state.
 *
 * ## Why the prices need a client island at all
 *
 * Every figure on this page is drawn in the reader's chosen format, which lives
 * in their browser. Client components still server-render, so the legs, the
 * picks and the result banner are all in the HTML a crawler receives; only the
 * format of the numbers settles on the client.
 *
 * ## The view is computed on the server and passed in
 *
 * `accaSlipView` needs the clock to tell a leg being played from one still to
 * come, and reading the clock during render is a hydration mismatch waiting to
 * happen. The page reads it once, server-side, and these components render what
 * they were handed. The page is `force-dynamic`, so a reload re-reads it —
 * which is also the only thing that can change the leg grades, since those come
 * from the API.
 */

function impliedChance(probability: number) {
  if (!Number.isFinite(probability) || probability <= 0) return null;
  const percent = probability * 100;
  // Below 1% two decimals still say something; above it they are noise.
  return `${percent < 1 ? percent.toFixed(2) : percent.toFixed(1)}%`;
}

/** The leg's own chance, as a whole percent, for the inline bar. */
function legChance(leg: AccaSlipLeg) {
  if (Number.isFinite(leg.probability) && leg.probability > 0) return Math.round(leg.probability * 100);
  if (Number.isFinite(leg.fairOdds) && leg.fairOdds > 0) return Math.round(100 / leg.fairOdds);
  return null;
}

function dayLabel(key: string) {
  // The grouping key is already the UTC date, so parsing it as midnight UTC
  // names the same day the legs were grouped into.
  const date = new Date(`${key}T00:00:00Z`);
  if (Number.isNaN(date.getTime())) return key;
  return date.toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short", timeZone: "UTC" });
}

export function AccaSlipLegs({ locale, view }: { locale: Locale; view: AccaSlipView }) {
  const [format] = useOddsFormat();
  // No pills while the whole slip is still ahead: a column of "Not started"
  // repeats what the kick-off times already say.
  const showStatuses = view.result !== null;

  return (
    <>
      <div className="as-legsHead">
        <h2>Every leg</h2>
        <span>Sorted by kick-off · leg chance = 1 ÷ fair odds</span>
      </div>

      {view.days.map((day) => (
        <section className="as-day" key={day.key}>
          <div className="as-day__head">
            <h3>{dayLabel(day.key)}</h3>
            <span>{day.legs.length === 1 ? "1 leg" : `${day.legs.length} legs`}</span>
          </div>
          <ol className="as-legs">
            {day.legs.map((leg) => {
              const chance = legChance(leg);
              return (
                <li className="as-leg" key={`${leg.matchId}:${leg.marketKey}:${leg.selection}`}>
                  <span className="as-leg__time"><LocalTime timeOnly value={leg.kickoffAt} /></span>
                  <Link className="as-leg__fixture" href={fixtureHref(leg)}>
                    <span className="as-leg__teams">
                      {leg.homeTeam} <i>v</i> {leg.awayTeam}
                    </span>
                    <span className="as-leg__league">{leg.leagueName}</span>
                  </Link>
                  <span className="as-leg__pick">
                    <span className="as-leg__market">{marketLabel(leg.marketKey, locale)}</span>
                    <span className="as-leg__selection">
                      {marketSelectionLabel(leg.marketKey, leg.selection, locale)}
                    </span>
                  </span>
                  <span className="as-leg__chance">
                    <span className="as-leg__chanceValue">{chance === null ? "—" : `≈ ${chance}%`}</span>
                    <span aria-hidden="true" className="as-leg__bar">
                      <i style={{ width: `${Math.max(0, Math.min(100, chance ?? 0))}%` }} />
                    </span>
                  </span>
                  <span className="as-leg__price">
                    <span className="as-leg__odds">
                      {leg.fairOdds > 0 && Number.isFinite(leg.fairOdds) ? formatOdds(leg.fairOdds, format) : "—"}
                    </span>
                    {showStatuses ? (
                      <span className={`as-pill as-pill--${leg.state}`}>{legStateLabels[leg.state]}</span>
                    ) : null}
                  </span>
                </li>
              );
            })}
          </ol>
        </section>
      ))}
    </>
  );
}

const ODDS_FORMATS = Object.keys(oddsFormatLabels) as OddsFormat[];

export function AccaSlipSummary({
  bundle,
  shareLabel,
  sharePath,
  shareText,
  view,
}: {
  bundle: AccaBundle;
  shareLabel: string;
  sharePath: string;
  shareText: string;
  view: AccaSlipView;
}) {
  const [format, setFormat] = useOddsFormat();
  const [added, setAdded] = useState(false);
  const implied = impliedChance(bundle.combinedFairProbability);

  return (
    <div className="as-summary">
      <div className="as-summary__price">
        <span className="as-summary__label">Combined fair odds</span>
        <span className="as-summary__odds">{formatOdds(bundle.combinedFairOdds, format)}</span>
      </div>

      <dl className="as-summary__facts">
        <div>
          <dt>Implied chance</dt>
          <dd>{implied ? `≈ ${implied}` : "—"}</dd>
        </div>
        <div>
          <dt>Legs</dt>
          <dd>{bundle.legs.length}</dd>
        </div>
        <div>
          <dt>First kick-off</dt>
          <dd>{view.firstKickoffAt ? <LocalTime value={view.firstKickoffAt} /> : "—"}</dd>
        </div>
        <div>
          <dt>Last kick-off</dt>
          <dd>{view.lastKickoffAt ? <LocalTime value={view.lastKickoffAt} /> : "—"}</dd>
        </div>
      </dl>

      <div className="as-summary__formats">
        <span className="as-summary__label">Odds format</span>
        {/*
          A segmented group of buttons rather than the `<select>` the rest of the
          site uses, because the design draws one and all three options fit. It
          sets the same preference through the same hook, so the select elsewhere
          and this group never disagree.
        */}
        <div aria-label="Odds format" className="as-formats" role="group">
          {ODDS_FORMATS.map((key) => (
            <button
              aria-pressed={format === key}
              className="as-formats__btn"
              key={key}
              onClick={() => setFormat(key)}
              type="button"
            >
              {oddsFormatLabels[key]}
            </button>
          ))}
        </div>
      </div>

      <div className="as-summary__actions">
        <button
          className="mp-btn mp-btn--primary"
          onClick={() => {
            addBuilderLegs(bundleLegs(bundle));
            setAdded(true);
            window.setTimeout(() => setAdded(false), 2200);
          }}
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
          title={shareLabel}
        />
      </div>

      <p className="as-summary__note">
        More legs add uncertainty, so every bundle is framed as analysis — never a guaranteed
        outcome. Copying to the Builder lets you edit legs, one selection per fixture.
      </p>
    </div>
  );
}
